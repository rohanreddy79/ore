// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
	"github.com/orevcs/ore/internal/review"
)

func setup(t *testing.T, level int) (*ledger.DB, ledger.Repo, *Loop, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := ledger.Open(ctx, filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	org, _ := db.CreateOrg(ctx, "acme", ledger.System)
	repo, _ := db.CreateRepo(ctx, org, "app", "main", level, 8080, ledger.System)
	reposDir := filepath.Join(dir, "repos")
	rp := filepath.Join(reposDir, "acme", "app.git")
	if err := gitx.InitBare(ctx, rp, "main"); err != nil {
		t.Fatal(err)
	}
	c0, err := gitx.CommitFiles(ctx, rp, "", []gitx.FileEdit{
		{Path: "greeting.txt", Content: []byte("Hello, World!\n")},
		{Path: ".ore/pipeline.yaml", Content: []byte("version: 0\ntasks:\n  - id: test\n    run: grep -q 'Hello, Ore!' greeting.txt\n")},
	}, "init", gitx.Author{Name: "t", Email: "t@t"})
	if err != nil {
		t.Fatal(err)
	}
	gitx.UpdateRef(ctx, rp, "refs/heads/main", c0, gitx.ZeroOID)
	logs := filepath.Join(dir, "logs")
	loop := &Loop{
		DB: db, ReposDir: reposDir, LogsDir: logs,
		Runner:      &pipelines.Runner{DB: db, ReposDir: reposDir, LogsDir: logs, Executor: "host"},
		Deployer:    &delivery.Deployer{DB: db, ReposDir: reposDir, Executor: "none"},
		Review:      &review.Service{DB: db, ReposDir: reposDir},
		AdapterName: "demo-script",
	}
	return db, repo, loop, rp
}

func TestLoopLandsAtLevel3(t *testing.T) {
	ctx := context.Background()
	db, repo, loop, rp := setup(t, 3)
	loop.AdapterCmd = `sed -i.bak 's/World/Ore/' greeting.txt && rm greeting.txt.bak`
	issue, _ := db.CreateIssue(ctx, repo, "Greeting should say Hello, Ore!", "The test expects Ore.", ledger.Actor{Kind: "human", ID: "rohan"})
	run, err := loop.RunSync(ctx, repo, Request{Issue: &issue}, ledger.Actor{Kind: "human", ID: "rohan"})
	if err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(run.LogPath)
	if run.Status != "completed" {
		t.Fatalf("status %s error %q\n%s", run.Status, run.Error, log)
	}
	if run.ChangeID == nil || run.PipelineRunID == nil || run.Step != "observe" {
		t.Fatalf("run: %+v", run)
	}
	tip, _ := gitx.RevParse(ctx, rp, "refs/heads/main")
	content, _ := gitx.ReadFile(ctx, rp, tip, "greeting.txt")
	if string(content) != "Hello, Ore!\n" {
		t.Fatalf("main not fixed: %q", content)
	}
	iss, _ := db.Issue(ctx, repo, 1)
	if iss.State != "closed" {
		t.Fatalf("issue not closed: %+v", iss)
	}
	ch, _ := db.Change(ctx, repo, 1)
	if ch.State != "merged" || !strings.Contains(ch.Provenance, `"author_kind":"agent"`) {
		t.Fatalf("change: %+v", ch)
	}
	evs, _ := db.Events(ctx, ledger.EventsOptions{})
	var types []string
	for _, e := range evs {
		types = append(types, e.Type)
	}
	joined := strings.Join(types, " ")
	for _, want := range []string{"io.ore.issue.opened", "io.ore.agent.session.started", "io.ore.ref.updated", "io.ore.change.opened", "io.ore.pipeline.run.finished", "io.ore.change.merged", "io.ore.issue.closed", "io.ore.agent.session.completed"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %v", want, types)
		}
	}
}

func TestLoopBlocksAtLevel2AndFailsOnRedPipeline(t *testing.T) {
	ctx := context.Background()
	db, repo, loop, _ := setup(t, 2)
	loop.AdapterCmd = `sed -i.bak 's/World/Ore/' greeting.txt && rm greeting.txt.bak`
	run, err := loop.RunSync(ctx, repo, Request{Prompt: "fix the greeting"}, ledger.System)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "blocked" || !strings.Contains(run.Error, "human must land") {
		t.Fatalf("expected blocked at L2, got %s %q", run.Status, run.Error)
	}
	ch, _ := db.Change(ctx, repo, 1)
	if ch.State != "open" {
		t.Fatalf("change should stay open: %+v", ch)
	}

	loop.AdapterCmd = `echo wrong > greeting.txt`
	run, _ = loop.RunSync(ctx, repo, Request{Prompt: "break it"}, ledger.System)
	if run.Status != "failed" || !strings.Contains(run.Error, "verification failed") {
		t.Fatalf("expected verification failure, got %s %q", run.Status, run.Error)
	}

	loop.AdapterCmd = `true`
	run, _ = loop.RunSync(ctx, repo, Request{Prompt: "do nothing"}, ledger.System)
	if run.Status != "failed" || !strings.Contains(run.Error, "no changes") {
		t.Fatalf("expected no-changes failure, got %s %q", run.Status, run.Error)
	}
}

func TestLoopUsesSparseCheckoutWhenPathsDeclared(t *testing.T) {
	ctx := context.Background()
	_, repo, loop, rp := setup(t, 3)
	// declare an active area; the agent must only see svc/a plus the pipeline dir
	c0, _ := gitx.RevParse(ctx, rp, "refs/heads/main")
	c1, err := gitx.CommitFiles(ctx, rp, c0, []gitx.FileEdit{
		{Path: ".ore/pipeline.yaml", Content: []byte("version: 0\npaths: [svc/a]\ntasks:\n  - id: test\n    run: grep -q 'Hello, Ore!' svc/a/greeting.txt\n")},
		{Path: "svc/a/greeting.txt", Content: []byte("Hello, World!\n")},
		{Path: "svc/b/other.txt", Content: []byte("b\n")},
		{Path: "greeting.txt", Content: nil},
	}, "monorepo layout", gitx.Author{Name: "t", Email: "t@t"})
	if err != nil {
		t.Fatal(err)
	}
	if err := gitx.UpdateRef(ctx, rp, "refs/heads/main", c1, c0); err != nil {
		t.Fatal(err)
	}
	// the agent edits inside the cone and also creates a file outside it
	loop.AdapterCmd = `test ! -e svc/b && test ! -e greeting.txt && sed -i.bak 's/World/Ore/' svc/a/greeting.txt && rm svc/a/greeting.txt.bak && mkdir -p notes && echo why > notes/CHANGE.md`
	run, err := loop.RunSync(ctx, repo, Request{Prompt: "fix greeting in svc/a"}, ledger.System)
	if err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(run.LogPath)
	if run.Status != "completed" || !strings.Contains(string(log), "sparse checkout of 2 path(s)") {
		t.Fatalf("status %s error %q\n%s", run.Status, run.Error, log)
	}
	content, _ := gitx.ReadFile(ctx, rp, "refs/heads/main", "svc/a/greeting.txt")
	if string(content) != "Hello, Ore!\n" {
		t.Fatalf("not landed: %q", content)
	}
	other, _ := gitx.ReadFile(ctx, rp, "refs/heads/main", "svc/b/other.txt")
	if string(other) != "b\n" {
		t.Fatalf("sparse checkout must not drop untouched paths: %q", other)
	}
	note, err := gitx.ReadFile(ctx, rp, "refs/heads/main", "notes/CHANGE.md")
	if err != nil || string(note) != "why\n" {
		t.Fatalf("a file created outside the sparse cone must be committed: %v %q", err, note)
	}
}

// When the server authenticates Claude Code with ANTHROPIC_API_KEY, the
// adapter gets a configuration directory of its own, outside the workspace,
// so a stale interactive login on the host cannot override the key; an
// explicit CLAUDE_CONFIG_DIR is left alone, and without a key nothing changes.
func TestAdapterGetsAnIsolatedClaudeConfigDirWithAnAPIKey(t *testing.T) {
	ctx := context.Background()
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	db, repo, loop, _ := setup(t, 3)
	loop.AdapterCmd = `test -n "$CLAUDE_CONFIG_DIR" && test -d "$CLAUDE_CONFIG_DIR" && case "$CLAUDE_CONFIG_DIR" in "$PWD"/*) exit 1;; esac && sed -i.bak 's/World/Ore/' greeting.txt && rm greeting.txt.bak`
	issue, _ := db.CreateIssue(ctx, repo, "Greeting should say Hello, Ore!", "", ledger.Actor{Kind: "human", ID: "rohan"})
	run, err := loop.RunSync(ctx, repo, Request{Issue: &issue}, ledger.Actor{Kind: "human", ID: "rohan"})
	if err != nil || run.Status != "completed" {
		log, _ := os.ReadFile(run.LogPath)
		t.Fatalf("status %s err %v error %q\n%s", run.Status, err, run.Error, log)
	}
	if want := filepath.Join(filepath.Dir(loop.LogsDir), "claude-config"); loop.claudeConfigDir() != want {
		t.Fatalf("config dir %q, want %q", loop.claudeConfigDir(), want)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "/elsewhere")
	if got := loop.claudeConfigDir(); got != "" {
		t.Fatalf("explicit CLAUDE_CONFIG_DIR overridden with %q", got)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	if got := loop.claudeConfigDir(); got != "" {
		t.Fatalf("no key but config dir %q", got)
	}
}
