// SPDX-License-Identifier: AGPL-3.0-only

package pipelines

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

func TestLoadSpecAndInfer(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644)
	s, explicit, err := LoadSpec(dir)
	if err != nil || explicit || len(s.Tasks) != 1 || !strings.Contains(s.Tasks[0].Run, "go test") {
		t.Fatalf("infer: %v %v %+v", err, explicit, s)
	}
	os.MkdirAll(filepath.Join(dir, ".ore"), 0o755)
	os.WriteFile(filepath.Join(dir, SpecPath), []byte("version: 0\ntasks:\n  - id: lint\n    image: alpine:3\n    run: echo lint\n  - run: echo test\n"), 0o644)
	s, explicit, err = LoadSpec(dir)
	if err != nil || !explicit || len(s.Tasks) != 2 || s.Tasks[1].ID != "task-2" {
		t.Fatalf("explicit: %v %v %+v", err, explicit, s)
	}
}

func TestRunOnHost(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := ledger.Open(ctx, filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	org, _ := db.CreateOrg(ctx, "acme", ledger.System)
	repo, _ := db.CreateRepo(ctx, org, "app", "main", 2, 8080, ledger.System)
	reposDir := filepath.Join(dir, "repos")
	rp := filepath.Join(reposDir, "acme", "app.git")
	if err := gitx.InitBare(ctx, rp, "main"); err != nil {
		t.Fatal(err)
	}
	oid, err := gitx.CommitFiles(ctx, rp, "", []gitx.FileEdit{
		{Path: ".ore/pipeline.yaml", Content: []byte("version: 0\ntasks:\n  - id: ok\n    run: echo hello from $PWD; test -f README\n  - id: bad\n    run: exit 3\n  - id: never\n    run: echo no\n")},
		{Path: "README", Content: []byte("x")},
	}, "init", gitx.Author{Name: "t", Email: "t@t"})
	if err != nil {
		t.Fatal(err)
	}
	if err := gitx.UpdateRef(ctx, rp, "refs/heads/main", oid, gitx.ZeroOID); err != nil {
		t.Fatal(err)
	}
	rn := &Runner{DB: db, ReposDir: reposDir, LogsDir: filepath.Join(dir, "logs"), Executor: "host"}
	run, err := rn.Run(ctx, repo, oid, nil, ledger.System)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" {
		t.Fatalf("expected failed, got %s", run.Status)
	}
	if !strings.Contains(run.Tasks, `"id":"ok","image":"","status":"succeeded"`) || !strings.Contains(run.Tasks, `"id":"never","image":"","status":"skipped"`) {
		t.Fatalf("tasks: %s", run.Tasks)
	}
	log, _ := os.ReadFile(run.LogPath)
	if !strings.Contains(string(log), "hello from") || !strings.Contains(string(log), "[bad] finished: failed (exit 3") {
		t.Fatalf("log: %s", log)
	}
	evs, _ := db.Events(ctx, ledger.EventsOptions{Type: "io.ore.pipeline.run."})
	if len(evs) != 3 {
		t.Fatalf("expected queued/started/finished, got %d", len(evs))
	}
}

func TestSparseMaterialization(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := ledger.Open(ctx, filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	org, _ := db.CreateOrg(ctx, "acme", ledger.System)
	repo, _ := db.CreateRepo(ctx, org, "mono", "main", 2, 8080, ledger.System)
	reposDir := filepath.Join(dir, "repos")
	rp := filepath.Join(reposDir, "acme", "mono.git")
	if err := gitx.InitBare(ctx, rp, "main"); err != nil {
		t.Fatal(err)
	}
	oid, err := gitx.CommitFiles(ctx, rp, "", []gitx.FileEdit{
		{Path: ".ore/pipeline.yaml", Content: []byte("version: 0\npaths: [svc/a]\ntasks:\n  - id: check\n    run: test -f svc/a/main.txt && test ! -e svc/b && test ! -e huge.bin\n")},
		{Path: "svc/a/main.txt", Content: []byte("a")},
		{Path: "svc/b/main.txt", Content: []byte("b")},
		{Path: "huge.bin", Content: []byte(strings.Repeat("x", 1<<20))},
	}, "mono", gitx.Author{Name: "t", Email: "t@t"})
	if err != nil {
		t.Fatal(err)
	}
	gitx.UpdateRef(ctx, rp, "refs/heads/main", oid, gitx.ZeroOID)
	rn := &Runner{DB: db, ReposDir: reposDir, LogsDir: filepath.Join(dir, "logs"), Executor: "host"}
	run, err := rn.Run(ctx, repo, oid, nil, ledger.System)
	if err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(run.LogPath)
	if run.Status != "succeeded" || !strings.Contains(string(log), "(sparse)") {
		t.Fatalf("sparse run: %s\n%s", run.Status, log)
	}
}
