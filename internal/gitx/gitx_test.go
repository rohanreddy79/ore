// SPDX-License-Identifier: AGPL-3.0-only

package gitx

import (
	"context"
	"errors"
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
