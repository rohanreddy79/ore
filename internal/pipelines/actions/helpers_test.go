// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
)

// fixtureResolver answers for the actions the corpus uses, so compilation
// is deterministic and needs no network.
var fixtureResolver = &StaticResolver{Refs: map[string]map[string]string{
	"actions/checkout":              {"v4": "11bd71901bbe5b1630ceea73d27597364c9af683", "v4.2.2": "11bd71901bbe5b1630ceea73d27597364c9af683", "v3": "f43a0e5ff2bd294095638e18286ca9a3d1956744", "main": "85e6279cec87321a52edac9c87bce653a07cf6c2"},
	"actions/setup-go":              {"v5": "0a12ed9d6a96ab950c8f026ed9f722fe0da7ef32", "v4": "93397bea11091df50f3d7e59dc26a789fa2e2b9d"},
	"actions/setup-node":            {"v4": "39370e3970a6d050fe5d0e2d3f6f5f4d9c8e3b3c", "v3": "1a4442cacd436585916779262731d5b162bc6ec7"},
	"actions/setup-python":          {"v5": "0b93645e9fea7318ecaed2b359559ac225c90a2b", "v4": "65d7f2d534ac1bc67fcd62888c5f4f3d2cb2b236"},
	"actions/cache":                 {"v4": "1bd1e32a3bdc45362d1e726936510720a7c30a57", "v3": "88522ab9f39a2ea568f7027eddc7d8d8bc9d59c8"},
	"actions/upload-artifact":       {"v4": "b4b15b8c7c6ac21ea08fcf65892d2ee8f75cf882", "v3": "a8a3f3ad30e3422c9c7b888a15615d19a852ae32"},
	"actions/download-artifact":     {"v4": "fa0a91b85d4f404e444e00e005971372dc801d16", "v3": "9bc31d5ccc31df68ecc42ccf4149144866c47d8a"},
	"actions/github-script":         {"v7": "60a0d83039c74a4aee543508d2ffcb1c3799cdea"},
	"codecov/codecov-action":        {"v4": "e28ff129e5465c2c0dcc6f003fc735cb6ae0c673"},
	"docker/setup-buildx-action":    {"v3": "c47758b77c9736f4b2ef4073d4d51994fabfe501"},
	"docker/build-push-action":      {"v6": "4f58ea79222b3b9dc2c8bbdd6debcef730109a75"},
	"docker/login-action":           {"v3": "9780b0c442fbb1117ed29e0efdff1e18412f7567"},
	"softprops/action-gh-release":   {"v2": "c062e08bd532815e2082a85e87e3ef29c3e6d191"},
	"golangci/golangci-lint-action": {"v6": "971e284b6050e8a5849b72094c50ab08da042db8"},
	"pnpm/action-setup":             {"v4": "a3252b78c470c02df07e9d59298aecedc3ccdd6d"},
}}

type testRun struct {
	t      *testing.T
	ctx    context.Context
	db     *ledger.DB
	repo   ledger.Repo
	rp     string
	dir    string
	runner *pipelines.Runner
	stores *pipelines.Stores
}

func newTestRun(t *testing.T) *testRun {
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
	repo, err := db.CreateRepo(ctx, org, "app", "main", 2, 8080, ledger.System)
	if err != nil {
		t.Fatal(err)
	}
	reposDir := filepath.Join(dir, "repos")
	rp := filepath.Join(reposDir, "acme", "app.git")
	if err := gitx.InitBare(ctx, rp, "main"); err != nil {
		t.Fatal(err)
	}
	stores := pipelines.NewStores(db, filepath.Join(dir, "data"))
	rn := &pipelines.Runner{DB: db, ReposDir: reposDir, LogsDir: filepath.Join(dir, "data", "logs"), DataDir: filepath.Join(dir, "data"), Executor: "host",
		Stores: stores, Frontend: &Frontend{Compiler: &Compiler{Resolver: fixtureResolver, Executor: "host"}}}
	return &testRun{t: t, ctx: ctx, db: db, repo: repo, rp: rp, dir: dir, runner: rn, stores: stores}
}

// commit writes files on branch (default main) and returns the commit id.
func (tr *testRun) commit(branch string, files map[string]string) string {
	tr.t.Helper()
	if branch == "" {
		branch = "main"
	}
	ref := "refs/heads/" + branch
	parent, _ := gitx.RevParse(tr.ctx, tr.rp, ref)
	old := gitx.ZeroOID
	if parent != "" {
		old = parent
	}
	var edits []gitx.FileEdit
	for p, c := range files {
		edits = append(edits, gitx.FileEdit{Path: p, Content: []byte(c)})
	}
	oid, err := gitx.CommitFiles(tr.ctx, tr.rp, parent, edits, "commit", gitx.Author{Name: "t", Email: "t@t"})
	if err != nil {
		tr.t.Fatal(err)
	}
	if err := gitx.UpdateRef(tr.ctx, tr.rp, ref, oid, old); err != nil {
		tr.t.Fatal(err)
	}
	return oid
}

type runOutcome struct {
	run     ledger.PipelineRun
	tasks   []pipelines.TaskRunResult
	log     string
	plans   []StoredPlan
	byID    map[string]pipelines.TaskRunResult
	taskIDs []string
}

func (tr *testRun) run(ctx context.Context, oid string, changeID *string) runOutcome {
	tr.t.Helper()
	run, err := tr.runner.Run(ctx, tr.repo, oid, changeID, ledger.Actor{Kind: "human", ID: "tester"})
	if err != nil {
		tr.t.Fatal(err)
	}
	out := runOutcome{run: run, byID: map[string]pipelines.TaskRunResult{}}
	logb, _ := os.ReadFile(run.LogPath)
	out.log = string(logb)
	if run.Tasks != "" && run.Tasks != "null" {
		if err := json.Unmarshal([]byte(run.Tasks), &out.tasks); err != nil {
			tr.t.Fatalf("tasks json: %v: %s", err, run.Tasks)
		}
	}
	for _, task := range out.tasks {
		out.byID[task.ID] = task
		out.taskIDs = append(out.taskIDs, task.ID)
	}
	out.plans, _ = PlansForRun(ctx, tr.db, run.ID)
	return out
}

func (o runOutcome) task(t *testing.T, id string) pipelines.TaskRunResult {
	t.Helper()
	task, ok := o.byID[id]
	if !ok {
		t.Fatalf("no task %s in %v\n%s", id, o.taskIDs, o.log)
	}
	return task
}

func (o runOutcome) step(t *testing.T, taskID string, index int) pipelines.StepRunResult {
	t.Helper()
	task := o.task(t, taskID)
	for _, s := range task.Steps {
		if s.Index == index {
			return s
		}
	}
	t.Fatalf("task %s has no step %d: %+v", taskID, index, task.Steps)
	return pipelines.StepRunResult{}
}

func (o runOutcome) divergenceCodes() []string {
	set := map[string]bool{}
	for _, p := range o.plans {
		for _, d := range p.Divergences {
			set[d.Code] = true
		}
	}
	return sortedKeys(set)
}

func hasCode(codes []string, code string) bool {
	for _, c := range codes {
		if c == code {
			return true
		}
	}
	return false
}

func dockerAvailable() bool {
	if os.Getenv("ORE_TEST_NO_DOCKER") != "" {
		return false
	}
	return exec.Command("docker", "info").Run() == nil
}

func mustContain(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Fatalf("expected %q in:\n%s", sub, s)
	}
}

func mustNotContain(t *testing.T, s, sub string) {
	t.Helper()
	if strings.Contains(s, sub) {
		t.Fatalf("did not expect %q in:\n%s", sub, s)
	}
}
