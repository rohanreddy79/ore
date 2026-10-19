// SPDX-License-Identifier: AGPL-3.0-only

package pipelines

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

// Repeat runs on one commit clone the cached tree instead of extracting it
// again; a new commit gets its own entry; the cache never leaks a run's edits.
func TestTreeCacheClonesRepeatRuns(t *testing.T) {
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
	spec := "version: 0\ntasks:\n  - id: check\n    run: test \"$(cat v.txt)\" = \"$EXPECT\" && echo scratch > v.txt\n"
	c1, err := gitx.CommitFiles(ctx, rp, "", []gitx.FileEdit{{Path: ".ore/pipeline.yaml", Content: []byte(spec)}, {Path: "v.txt", Content: []byte("one")}}, "one", gitx.Author{Name: "t", Email: "t@t"})
	if err != nil {
		t.Fatal(err)
	}
	gitx.UpdateRef(ctx, rp, "refs/heads/main", c1, gitx.ZeroOID)
	rn := &Runner{DB: db, ReposDir: reposDir, LogsDir: filepath.Join(dir, "logs"), Executor: "host"}
	t.Setenv("EXPECT", "one")
	t.Setenv("ORE_TREE_CACHE", "")
	for i, want := range []string{"extracted into the tree cache", "cloned from the tree cache", "cloned from the tree cache"} {
		run, err := rn.Run(ctx, repo, c1, nil, ledger.System)
		if err != nil {
			t.Fatal(err)
		}
		log, _ := os.ReadFile(run.LogPath)
		if run.Status != "succeeded" || !strings.Contains(string(log), want) {
			t.Fatalf("run %d: %s, want %q\n%s", i, run.Status, want, log)
		}
	}
	cacheDir := filepath.Join(dir, "cache", "trees", repo.ID)
	entries, _ := os.ReadDir(cacheDir)
	if len(entries) != 1 {
		t.Fatalf("cache entries after three runs on one commit: %d", len(entries))
	}
	if b, _ := os.ReadFile(filepath.Join(cacheDir, entries[0].Name(), "v.txt")); string(b) != "one" {
		t.Fatalf("cached tree modified by a run: %q", b)
	}
	c2, _ := gitx.CommitFiles(ctx, rp, c1, []gitx.FileEdit{{Path: "v.txt", Content: []byte("two")}}, "two", gitx.Author{Name: "t", Email: "t@t"})
	gitx.UpdateRef(ctx, rp, "refs/heads/main", c2, c1)
	t.Setenv("EXPECT", "two")
	run, err := rn.Run(ctx, repo, c2, nil, ledger.System)
	if err != nil || run.Status != "succeeded" {
		log, _ := os.ReadFile(run.LogPath)
		t.Fatalf("new commit: %v %s\n%s", err, run.Status, log)
	}
	if entries, _ = os.ReadDir(cacheDir); len(entries) != 2 {
		t.Fatalf("cache entries after a second commit: %d", len(entries))
	}
	// concurrent first runs on a fresh commit build the entry once
	c3, _ := gitx.CommitFiles(ctx, rp, c2, []gitx.FileEdit{{Path: "v.txt", Content: []byte("three")}}, "three", gitx.Author{Name: "t", Email: "t@t"})
	gitx.UpdateRef(ctx, rp, "refs/heads/main", c3, c2)
	t.Setenv("EXPECT", "three")
	var wg sync.WaitGroup
	built := int32(0)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run, err := rn.Run(ctx, repo, c3, nil, ledger.System)
			log, _ := os.ReadFile(run.LogPath)
			if err != nil || run.Status != "succeeded" {
				t.Errorf("concurrent run: %v %s\n%s", err, run.Status, log)
			}
			if strings.Contains(string(log), "built from the previous tree and the diff") || strings.Contains(string(log), "extracted into") {
				atomic.AddInt32(&built, 1)
			}
		}()
	}
	wg.Wait()
	if built != 1 {
		t.Fatalf("tree built %d times by six concurrent runs, want 1", built)
	}
}

// A fresh commit's tree is built from the nearest cached tree plus the diff:
// deletions, content changes, a file turning into a directory and back, a
// new file and a mode change all land; the task in the tree checks itself.
func TestTreeCacheBuildsAFreshCommitFromTheDiff(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := ledger.Open(ctx, filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	org, _ := db.CreateOrg(ctx, "acme", ledger.System)
	repo, _ := db.CreateRepo(ctx, org, "inc", "main", 2, 8080, ledger.System)
	reposDir := filepath.Join(dir, "repos")
	rp := filepath.Join(reposDir, "acme", "inc.git")
	if err := gitx.InitBare(ctx, rp, "main"); err != nil {
		t.Fatal(err)
	}
	spec := "version: 0\ntasks:\n  - id: check\n    run: sh check.sh\n"
	au := gitx.Author{Name: "t", Email: "t@t"}
	c1, err := gitx.CommitFiles(ctx, rp, "", []gitx.FileEdit{
		{Path: ".ore/pipeline.yaml", Content: []byte(spec)},
		{Path: "check.sh", Content: []byte("test \"$(cat a/x)\" = one && test -f b && test -f d/e && test -f del.txt && test ! -x run.sh\n")},
		{Path: "a/x", Content: []byte("one")}, {Path: "b", Content: []byte("file b")}, {Path: "d/e", Content: []byte("e")},
		{Path: "del.txt", Content: []byte("gone")}, {Path: "run.sh", Content: []byte("echo hi\n")},
	}, "one", au)
	if err != nil {
		t.Fatal(err)
	}
	gitx.UpdateRef(ctx, rp, "refs/heads/main", c1, gitx.ZeroOID)
	c2, err := gitx.CommitFiles(ctx, rp, c1, []gitx.FileEdit{
		{Path: "check.sh", Content: []byte("test \"$(cat a/x)\" = two && test -d b && test -f b/inner && test -f d && test ! -e del.txt && test -f new.txt && test -x run.sh\n")},
		{Path: "a/x", Content: []byte("two")}, {Path: "b", Content: nil}, {Path: "b/inner", Content: []byte("inner")},
		{Path: "d/e", Content: nil}, {Path: "d", Content: []byte("now a file")},
		{Path: "del.txt", Content: nil}, {Path: "new.txt", Content: []byte("new")},
		{Path: "run.sh", Content: []byte("echo hi\n"), Mode: "100755"},
	}, "two", au)
	if err != nil {
		t.Fatal(err)
	}
	gitx.UpdateRef(ctx, rp, "refs/heads/main", c2, c1)
	rn := &Runner{DB: db, ReposDir: reposDir, LogsDir: filepath.Join(dir, "logs"), Executor: "host"}
	t.Setenv("ORE_TREE_CACHE", "")
	for i, c := range []struct{ oid, how string }{{c1, "extracted into the tree cache"}, {c2, "built from the previous tree and the diff"}, {c2, "cloned from the tree cache"}} {
		run, err := rn.Run(ctx, repo, c.oid, nil, ledger.System)
		if err != nil {
			t.Fatal(err)
		}
		log, _ := os.ReadFile(run.LogPath)
		if run.Status != "succeeded" || !strings.Contains(string(log), c.how) {
			t.Fatalf("run %d: %s, want %q\n%s", i, run.Status, c.how, log)
		}
	}
}
