// SPDX-License-Identifier: AGPL-3.0-only

package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerSideCommitMergeAndCAS(t *testing.T) {
	ctx := context.Background()
	rp := filepath.Join(t.TempDir(), "r.git")
	if err := InitBare(ctx, rp, "main"); err != nil {
		t.Fatal(err)
	}
	au := Author{Name: "t", Email: "t@t"}
	c0, err := CommitFiles(ctx, rp, "", []FileEdit{{Path: "a", Content: []byte("1\n")}, {Path: "dir/b", Content: []byte("b\n")}}, "init", au)
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateRef(ctx, rp, "refs/heads/main", c0, ZeroOID); err != nil {
		t.Fatal(err)
	}
	files, _ := ListTree(ctx, rp, "refs/heads/main")
	if strings.Join(files, ",") != "a,dir/b" {
		t.Fatalf("tree: %v", files)
	}
	c1, _ := CommitFiles(ctx, rp, c0, []FileEdit{{Path: "a", Content: []byte("2\n")}, {Path: "dir/b", Content: nil}}, "edit+delete", au)
	files, _ = ListTree(ctx, rp, c1)
	if strings.Join(files, ",") != "a" {
		t.Fatalf("delete failed: %v", files)
	}
	// CAS: wrong old value is refused
	if err := UpdateRef(ctx, rp, "refs/heads/main", c1, c1); !errors.Is(err, ErrCASFailed) {
		t.Fatalf("expected CAS failure, got %v", err)
	}
	if err := UpdateRef(ctx, rp, "refs/heads/main", c1, c0); err != nil {
		t.Fatal(err)
	}
	// clean merge: side edits a new file
	s1, _ := CommitFiles(ctx, rp, c0, []FileEdit{{Path: "c", Content: []byte("c\n")}}, "side", au)
	tree, conflicts, err := MergeTree(ctx, rp, c1, s1)
	if err != nil || conflicts != "" || tree == "" {
		t.Fatalf("merge-tree: %v %q %q", err, tree, conflicts)
	}
	m, err := CommitTree(ctx, rp, tree, []string{c1, s1}, "merge", au)
	if err != nil {
		t.Fatal(err)
	}
	files, _ = ListTree(ctx, rp, m)
	if strings.Join(files, ",") != "a,c" {
		t.Fatalf("merged tree: %v", files)
	}
	// conflicting merge
	x, _ := CommitFiles(ctx, rp, c0, []FileEdit{{Path: "a", Content: []byte("x\n")}}, "x", au)
	_, conflicts, err = MergeTree(ctx, rp, c1, x)
	if !errors.Is(err, ErrConflict) || !strings.Contains(conflicts, "a") {
		t.Fatalf("expected conflict on a, got %v %q", err, conflicts)
	}
	// refs snapshot/diff
	before, _ := Refs(ctx, rp)
	UpdateRef(ctx, rp, "refs/heads/x", x, ZeroOID)
	after, _ := Refs(ctx, rp)
	d := DiffRefs(before, after)
	if len(d) != 1 || d[0].Ref != "refs/heads/x" || d[0].Old != ZeroOID || d[0].New != x {
		t.Fatalf("diff refs: %+v", d)
	}
	if !IsAncestor(ctx, rp, c0, m) || IsAncestor(ctx, rp, x, m) {
		t.Fatal("ancestry wrong")
	}
	content, _ := ReadFile(ctx, rp, m, "a")
	if string(content) != "2\n" {
		t.Fatalf("read file: %q", content)
	}
	log, _ := Log(ctx, rp, m, 10)
	if len(log) != 4 || log[0].Subject != "merge" {
		t.Fatalf("log: %+v", log)
	}
}

func TestGateBoundsConcurrentGitProcesses(t *testing.T) {
	ctx := context.Background()
	rp := filepath.Join(t.TempDir(), "r.git")
	if err := InitBare(ctx, rp, "main"); err != nil {
		t.Fatal(err)
	}
	// 200 concurrent reads must all succeed; the gate serializes them in batches.
	errs := make(chan error, 200)
	for i := 0; i < 200; i++ {
		go func() {
			_, err := Refs(ctx, rp)
			errs <- err
		}()
	}
	for i := 0; i < 200; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if Concurrency() < 8 {
		t.Fatalf("gate too small: %d", Concurrency())
	}
	// a canceled context does not wait for a slot
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Run(cctx, rp, nil, "rev-parse", "HEAD"); err == nil {
		t.Fatal("expected a canceled context to fail fast")
	}
}

func TestResolveRefReadsBranchesFromDiskAndFallsBackToGit(t *testing.T) {
	ctx := context.Background()
	rp := filepath.Join(t.TempDir(), "r.git")
	if err := InitBare(ctx, rp, "main"); err != nil {
		t.Fatal(err)
	}
	au := Author{Name: "t", Email: "t@t"}
	c0, err := CommitFiles(ctx, rp, "", []FileEdit{{Path: "a", Content: []byte("1\n")}}, "init", au)
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateRef(ctx, rp, "refs/heads/main", c0, ZeroOID); err != nil {
		t.Fatal(err)
	}
	// loose ref file
	if got, err := ResolveRef(ctx, rp, "refs/heads/main"); err != nil || got != c0 {
		t.Fatalf("loose: %q %v", got, err)
	}
	// packed-refs only
	if _, err := Run(ctx, rp, nil, "pack-refs", "--all"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(rp, "refs", "heads", "main")); err == nil {
		t.Fatal("loose ref still present after pack-refs")
	}
	if got, err := ResolveRef(ctx, rp, "refs/heads/main"); err != nil || got != c0 {
		t.Fatalf("packed: %q %v", got, err)
	}
	// a newer loose ref wins over the stale packed entry
	c1, err := CommitFiles(ctx, rp, c0, []FileEdit{{Path: "a", Content: []byte("2\n")}}, "second", au)
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateRef(ctx, rp, "refs/heads/main", c1, c0); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveRef(ctx, rp, "refs/heads/main"); err != nil || got != c1 {
		t.Fatalf("after update: %q %v (want %s)", got, err, c1)
	}
	// annotated tags, HEAD and abbreviations go through git and peel to the commit
	if _, err := Run(ctx, rp, nil, "tag", "-a", "v1", "-m", "v1", c1); err != nil {
		t.Fatal(err)
	}
	for _, rev := range []string{"refs/tags/v1", "HEAD", "main", c1[:10]} {
		if got, err := ResolveRef(ctx, rp, rev); err != nil || got != c1 {
			t.Fatalf("%s: %q %v", rev, got, err)
		}
	}
	if _, err := ResolveRef(ctx, rp, "refs/heads/nope"); err == nil {
		t.Fatal("missing branch resolved")
	}
	if isHexOID("ref: refs/heads/main") || !isHexOID(c1) {
		t.Fatal("isHexOID")
	}
}

// Refs read from packed-refs and loose files must match for-each-ref exactly:
// loose over packed, annotated tags by tag object, symbolic refs left out.
func TestRefsFromDiskMatchForEachRef(t *testing.T) {
	ctx := context.Background()
	rp := filepath.Join(t.TempDir(), "r.git")
	if err := InitBare(ctx, rp, "main"); err != nil {
		t.Fatal(err)
	}
	au := Author{Name: "t", Email: "t@t"}
	c0, _ := CommitFiles(ctx, rp, "", []FileEdit{{Path: "a", Content: []byte("1\n")}}, "init", au)
	UpdateRef(ctx, rp, "refs/heads/main", c0, ZeroOID)
	UpdateRef(ctx, rp, "refs/heads/feature/x", c0, ZeroOID)
	Run(ctx, rp, nil, "tag", "-a", "v1", "-m", "v1", c0)
	Run(ctx, rp, nil, "tag", "light", c0)
	Run(ctx, rp, nil, "pack-refs", "--all")
	c1, _ := CommitFiles(ctx, rp, c0, []FileEdit{{Path: "a", Content: []byte("2\n")}}, "second", au)
	UpdateRef(ctx, rp, "refs/heads/main", c1, c0) // loose, shadows the packed entry
	UpdateRef(ctx, rp, "refs/ore/changes/1", c1, ZeroOID)
	Run(ctx, rp, nil, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/heads/main")
	want, err := refsFromGit(ctx, rp)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := readRefsFromDisk(rp)
	if !ok {
		t.Fatal("disk read declined")
	}
	if len(got) != len(want) || len(want) < 5 {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %q want %q", k, got[k], v)
		}
	}
	if got["refs/heads/main"] != c1 {
		t.Fatalf("loose ref did not win: %v", got)
	}
	if _, ok := readRefsFromDisk(filepath.Join(rp, "nope")); ok {
		t.Fatal("missing repository should decline")
	}
}
