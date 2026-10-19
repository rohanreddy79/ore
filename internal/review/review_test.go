// SPDX-License-Identifier: AGPL-3.0-only

package review

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

func TestOpenAndServerSideMerge(t *testing.T) {
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
	au := gitx.Author{Name: "t", Email: "t@t"}
	c0, _ := gitx.CommitFiles(ctx, rp, "", []gitx.FileEdit{{Path: "a.txt", Content: []byte("a\n")}, {Path: "b.txt", Content: []byte("b\n")}}, "init", au)
	gitx.UpdateRef(ctx, rp, "refs/heads/main", c0, gitx.ZeroOID)
	// feature changes a.txt; main concurrently changes b.txt (clean merge)
	f1, _ := gitx.CommitFiles(ctx, rp, c0, []gitx.FileEdit{{Path: "a.txt", Content: []byte("a2\n")}}, "feature", au)
	gitx.UpdateRef(ctx, rp, "refs/heads/feature", f1, gitx.ZeroOID)
	m1, _ := gitx.CommitFiles(ctx, rp, c0, []gitx.FileEdit{{Path: "b.txt", Content: []byte("b2\n")}}, "main moves", au)
	gitx.UpdateRef(ctx, rp, "refs/heads/main", m1, c0)

	svc := &Service{DB: db, ReposDir: reposDir}
	ch, err := svc.Open(ctx, repo, OpenRequest{SourceRef: "feature", Title: "Change a"}, ledger.Actor{Kind: "agent", ID: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Number != 1 || !strings.HasPrefix(ch.ChangeID, "I") || ch.BaseOID != c0 || ch.HeadOID != f1 {
		t.Fatalf("change: %+v", ch)
	}
	diff, _ := svc.Diff(ctx, repo, ch)
	if !strings.Contains(diff, "+a2") {
		t.Fatalf("diff: %s", diff)
	}
	// reopening the same branch returns the same change
	again, err := svc.Open(ctx, repo, OpenRequest{SourceRef: "feature"}, ledger.System)
	if err != nil || again.ID != ch.ID {
		t.Fatalf("reopen: %v %+v", err, again)
	}
	mergeOID, err := svc.Merge(ctx, repo, ch, ledger.Actor{Kind: "human", ID: "rohan"})
	if err != nil {
		t.Fatal(err)
	}
	tip, _ := gitx.RevParse(ctx, rp, "refs/heads/main")
	if tip != mergeOID {
		t.Fatalf("main not advanced: %s vs %s", tip, mergeOID)
	}
	a, _ := gitx.ReadFile(ctx, rp, tip, "a.txt")
	b, _ := gitx.ReadFile(ctx, rp, tip, "b.txt")
	if string(a) != "a2\n" || string(b) != "b2\n" {
		t.Fatalf("merge result wrong: %q %q", a, b)
	}
	got, _ := db.Change(ctx, repo, 1)
	if got.State != "merged" || got.MergeOID == nil || *got.MergeOID != mergeOID {
		t.Fatalf("ledger: %+v", got)
	}
	evs, _ := db.Events(ctx, ledger.EventsOptions{Type: "io.ore.change."})
	if len(evs) != 2 || evs[0].Type != "io.ore.change.opened" || evs[1].Type != "io.ore.change.merged" {
		t.Fatalf("events: %+v", evs)
	}
	// a second merge of the same change is refused
	if _, err := svc.Merge(ctx, repo, got, ledger.System); err == nil {
		t.Fatal("expected merged change to be refused")
	}

	// conflict: both sides edit a.txt differently
	f2, _ := gitx.CommitFiles(ctx, rp, tip, []gitx.FileEdit{{Path: "a.txt", Content: []byte("left\n")}}, "left", au)
	gitx.UpdateRef(ctx, rp, "refs/heads/left", f2, gitx.ZeroOID)
	m2, _ := gitx.CommitFiles(ctx, rp, tip, []gitx.FileEdit{{Path: "a.txt", Content: []byte("right\n")}}, "right", au)
	gitx.UpdateRef(ctx, rp, "refs/heads/main", m2, tip)
	ch2, err := svc.Open(ctx, repo, OpenRequest{SourceRef: "left"}, ledger.System)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Merge(ctx, repo, ch2, ledger.System)
	var ce *ConflictError
	if !errors.As(err, &ce) || len(ce.Paths) != 1 || ce.Paths[0] != "a.txt" {
		t.Fatalf("expected conflict on a.txt, got %v", err)
	}
}
