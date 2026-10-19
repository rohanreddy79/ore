// SPDX-License-Identifier: AGPL-3.0-only

package review

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
)

// env is a repository with a green main: STATUS says ok and the pipeline
// (host executor) checks it. A change that writes "broken" to STATUS is red.
type env struct {
	t     *testing.T
	ctx   context.Context
	db    *ledger.DB
	repo  ledger.Repo
	rp    string
	svc   *Service
	c0    string
	au    gitx.Author
	human ledger.Actor
	agent ledger.Actor
}

const pipelineYAML = "version: 0\ntasks:\n  - id: check\n    run: grep -qx ok STATUS\n"

var baseFiles = []gitx.FileEdit{
	{Path: "STATUS", Content: []byte("ok\n")},
	{Path: "a.txt", Content: []byte("1\n2\n3\n4\n5\n")},
	{Path: "b.txt", Content: []byte("b\n")},
	{Path: "c.txt", Content: []byte("c\n")},
	{Path: ".ore/pipeline.yaml", Content: []byte(pipelineYAML)},
}

func newEnv(t *testing.T) *env {
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
	e := &env{t: t, ctx: ctx, db: db, repo: repo, rp: rp, au: gitx.Author{Name: "t", Email: "t@t"},
		human: ledger.Actor{Kind: "human", ID: "alice"}, agent: ledger.Actor{Kind: "agent", ID: "claude-code"}}
	e.c0 = e.commit("", baseFiles, "init")
	e.setRef("refs/heads/main", e.c0, gitx.ZeroOID, ledger.System)
	e.svc = &Service{DB: db, ReposDir: reposDir, Verifier: &pipelines.Runner{DB: db, ReposDir: reposDir, LogsDir: filepath.Join(dir, "logs"), Executor: "host"}}
	return e
}

func (e *env) commit(parent string, edits []gitx.FileEdit, msg string) string {
	e.t.Helper()
	oid, err := gitx.CommitFiles(e.ctx, e.rp, parent, edits, msg+"\n", e.au)
	if err != nil {
		e.t.Fatal(err)
	}
	return oid
}

// setRef moves a ref with compare-and-swap and records the push on the
// ledger as actor, like a push through githttp would.
func (e *env) setRef(ref, newOID, oldOID string, actor ledger.Actor) {
	e.t.Helper()
	if err := gitx.UpdateRef(e.ctx, e.rp, ref, newOID, oldOID); err != nil {
		e.t.Fatal(err)
	}
	if err := e.db.RecordRefUpdate(e.ctx, e.repo, ref, oldOID, newOID, "push", actor); err != nil {
		e.t.Fatal(err)
	}
}

// branch creates a branch with one commit on top of from, pushed by actor.
func (e *env) branch(name, from string, edits []gitx.FileEdit, msg string, actor ledger.Actor) string {
	e.t.Helper()
	oid := e.commit(from, edits, msg)
	e.setRef("refs/heads/"+name, oid, gitx.ZeroOID, actor)
	return oid
}

// push moves an existing branch to a new commit.
func (e *env) push(name, from string, edits []gitx.FileEdit, msg string, actor ledger.Actor) string {
	e.t.Helper()
	old, err := gitx.RevParse(e.ctx, e.rp, "refs/heads/"+name)
	if err != nil {
		e.t.Fatal(err)
	}
	oid := e.commit(from, edits, msg)
	e.setRef("refs/heads/"+name, oid, old, actor)
	return oid
}

func (e *env) open(source string) ledger.Change {
	e.t.Helper()
	ch, err := e.svc.Open(e.ctx, e.repo, OpenRequest{SourceRef: source}, e.human)
	if err != nil {
		e.t.Fatalf("open %s: %v", source, err)
	}
	return ch
}

func (e *env) tip(ref string) string {
	e.t.Helper()
	oid, err := gitx.RevParse(e.ctx, e.rp, ref)
	if err != nil {
		e.t.Fatalf("rev-parse %s: %v", ref, err)
	}
	return oid
}

func (e *env) file(rev, path string) string {
	e.t.Helper()
	b, err := gitx.ReadFile(e.ctx, e.rp, rev, path)
	if err != nil {
		e.t.Fatalf("read %s:%s: %v", rev, path, err)
	}
	return string(b)
}

func (e *env) change(n int) ledger.Change {
	e.t.Helper()
	c, err := e.db.Change(e.ctx, e.repo, n)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *env) events(typ string) []ledger.Event {
	e.t.Helper()
	evs, err := e.db.Events(e.ctx, ledger.EventsOptions{Type: typ, Limit: 1000})
	if err != nil {
		e.t.Fatal(err)
	}
	return evs
}

func edit(path, content string) []gitx.FileEdit {
	return []gitx.FileEdit{{Path: path, Content: []byte(content)}}
}
