// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orevcs/ore/internal/agent"
	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
	"github.com/orevcs/ore/internal/review"
)

// The fixture: a repository whose pipeline (host executor) is green when
// greeting.txt says "Hello, Ore!" and no BROKEN file exists. The default
// adapter fixes the greeting. Tests swap adapters to misbehave on purpose.
const (
	pipelineYAML = "version: 0\ntasks:\n  - id: test\n    run: grep -q 'Hello, Ore!' greeting.txt && test ! -f BROKEN\n"
	fixAdapter   = `sed -i.bak 's/World/Ore/' greeting.txt && rm -f greeting.txt.bak`
	// fixAndUnbreak also removes BROKEN when the issue is a revert signal.
	fixAndUnbreak = `if printf '%s' "$ORE_ISSUE_TITLE" | grep -q reverted; then rm -f BROKEN; fi; ` + fixAdapter
	// fixmeAdapter leaves a FIXME the first time and removes it when the
	// reviewer's findings come back.
	fixmeAdapter = `if [ -n "$ORE_FINDINGS" ]; then sed -i.bak '/FIXME/d' greeting.txt && rm -f greeting.txt.bak; else printf 'Hello, Ore!\nFIXME remove me\n' > greeting.txt; fi`
)

type env struct {
	t        *testing.T
	ctx      context.Context
	dir      string
	db       *ledger.DB
	org      ledger.Org
	repo     ledger.Repo
	reposDir string
	rp       string
	loop     *agent.Loop
	rev      *review.Service
	svc      *Service
	deployer *delivery.Deployer
	c0       string
	human    ledger.Actor
}

func newEnv(t *testing.T, level int) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := ledger.Open(ctx, filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	org, err := db.CreateOrg(ctx, "acme", ledger.System)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := db.CreateRepo(ctx, org, "app", "main", level, 8080, ledger.System)
	if err != nil {
		t.Fatal(err)
	}
	reposDir := filepath.Join(dir, "repos")
	rp := filepath.Join(reposDir, "acme", "app.git")
	if err := gitx.InitBare(ctx, rp, "main"); err != nil {
		t.Fatal(err)
	}
	c0, err := gitx.CommitFiles(ctx, rp, "", []gitx.FileEdit{
		{Path: "greeting.txt", Content: []byte("Hello, World!\n")},
		{Path: ".ore/pipeline.yaml", Content: []byte(pipelineYAML)},
	}, "init", gitx.Author{Name: "t", Email: "t@t"})
	if err != nil {
		t.Fatal(err)
	}
	if err := gitx.UpdateRef(ctx, rp, "refs/heads/main", c0, gitx.ZeroOID); err != nil {
		t.Fatal(err)
	}
	logs := filepath.Join(dir, "logs")
	runner := &pipelines.Runner{DB: db, ReposDir: reposDir, LogsDir: logs, Executor: "host"}
	deployer := &delivery.Deployer{DB: db, ReposDir: reposDir, Executor: "none"}
	rev := &review.Service{DB: db, ReposDir: reposDir, Verifier: runner}
	loop := &agent.Loop{DB: db, ReposDir: reposDir, LogsDir: logs, Runner: runner, Deployer: deployer, Review: rev, AdapterCmd: fixAdapter, AdapterName: "demo-script"}
	svc := &Service{DB: db, ReposDir: reposDir, LogsDir: logs, Loop: loop, Reviews: rev, RunSync: true}
	loop.Governor = svc
	return &env{t: t, ctx: ctx, dir: dir, db: db, org: org, repo: repo, reposDir: reposDir, rp: rp, loop: loop, rev: rev, svc: svc, deployer: deployer, c0: c0, human: ledger.Actor{Kind: "human", ID: "rohan"}}
}

// reload refreshes the repository record (its level may have changed).
func (e *env) reload() ledger.Repo {
	e.t.Helper()
	r, err := e.db.RepoByID(e.ctx, e.repo.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	e.repo = r
	return r
}

// run executes one loop pass on a prompt or issue and returns the run.
func (e *env) run(req agent.Request) (ledger.AgentRun, error) {
	e.t.Helper()
	return e.loop.RunSync(e.ctx, e.reload(), req, e.human)
}

func (e *env) runLog(run ledger.AgentRun) string {
	b, _ := os.ReadFile(run.LogPath)
	return string(b)
}

func (e *env) mustComplete(run ledger.AgentRun, err error) ledger.AgentRun {
	e.t.Helper()
	if err != nil {
		e.t.Fatalf("run: %v", err)
	}
	if run.Status != "completed" {
		e.t.Fatalf("run %s %q (step %s)\n%s", run.Status, run.Error, run.Step, e.runLog(run))
	}
	return run
}

// scriptFile writes a shell script into the test dir and returns a command for it.
func (e *env) scriptFile(name, body string) string {
	e.t.Helper()
	p := filepath.Join(e.dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		e.t.Fatal(err)
	}
	return "sh " + p
}

// enableReviewer configures the deterministic script reviewer.
func (e *env) enableReviewer() {
	e.t.Helper()
	cmd := e.scriptFile("reviewer.sh", ScriptReviewer)
	if _, err := e.svc.SetRole(e.ctx, e.repo, RoleReviewer, RoleInput{AdapterCmd: &cmd}, e.human); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) tip() string {
	e.t.Helper()
	oid, err := gitx.RevParse(e.ctx, e.rp, "refs/heads/main")
	if err != nil {
		e.t.Fatal(err)
	}
	return oid
}

func (e *env) file(rev, path string) string {
	b, err := gitx.ReadFile(e.ctx, e.rp, rev, path)
	if err != nil {
		return ""
	}
	return string(b)
}

// breakMain pushes a BROKEN file to main so the next merge commit is red.
func (e *env) breakMain() {
	e.t.Helper()
	tip := e.tip()
	oid, err := gitx.CommitFiles(e.ctx, e.rp, tip, []gitx.FileEdit{{Path: "BROKEN", Content: []byte("x\n")}}, "break main\n", gitx.Author{Name: "t", Email: "t@t"})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := gitx.UpdateRef(e.ctx, e.rp, "refs/heads/main", oid, tip); err != nil {
		e.t.Fatal(err)
	}
	_ = e.db.RecordRefUpdate(e.ctx, e.repo, "refs/heads/main", tip, oid, "push", e.human)
}

// unbreakMain removes BROKEN from main so the next clone is green.
func (e *env) unbreakMain() {
	e.t.Helper()
	tip := e.tip()
	if e.file(tip, "BROKEN") == "" {
		return
	}
	oid, err := gitx.CommitFiles(e.ctx, e.rp, tip, []gitx.FileEdit{{Path: "BROKEN", Content: nil}}, "repair main\n", gitx.Author{Name: "t", Email: "t@t"})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := gitx.UpdateRef(e.ctx, e.rp, "refs/heads/main", oid, tip); err != nil {
		e.t.Fatal(err)
	}
	_ = e.db.RecordRefUpdate(e.ctx, e.repo, "refs/heads/main", tip, oid, "push", e.human)
}

// alwaysRed is a gate hook that keeps every head green and every merge red:
// main is repaired before a run plans and broken again before it lands.
func (e *env) alwaysRed() func(step string) {
	return func(step string) {
		switch step {
		case "plan":
			e.unbreakMain()
		case "land":
			if e.file(e.tip(), "BROKEN") == "" {
				e.breakMain()
			}
		}
	}
}

func (e *env) events(typ string) []ledger.Event {
	e.t.Helper()
	evs, err := e.db.Events(e.ctx, ledger.EventsOptions{Type: typ, Limit: 1000})
	if err != nil {
		e.t.Fatal(err)
	}
	return evs
}

func (e *env) runs() []ledger.AgentRun {
	e.t.Helper()
	runs, err := e.db.AgentRuns(e.ctx, e.repo, 100)
	if err != nil {
		e.t.Fatal(err)
	}
	return runs
}

// tick runs the watcher until it has nothing new, returning the decisions made.
func (e *env) tick() []Signal {
	e.t.Helper()
	for i := 0; i < 20; i++ {
		n, err := e.svc.Tick(e.ctx)
		if err != nil {
			e.t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	sigs, err := e.svc.Signals(e.ctx, e.repo, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return sigs
}

// hookGov wraps the fleet so a test can act between steps of a run.
type hookGov struct {
	*Service
	onGate func(step string)
}

func (h hookGov) Gate(ctx context.Context, repo ledger.Repo, run *ledger.AgentRun, principal ledger.Actor, step string) error {
	if h.onGate != nil {
		h.onGate(step)
	}
	return h.Service.Gate(ctx, repo, run, principal, step)
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func f64(v float64) *float64 { return &v }

var _ = time.Second

func agent_Request(prompt string) agent.Request { return agent.Request{Prompt: prompt} }
