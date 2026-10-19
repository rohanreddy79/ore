// SPDX-License-Identifier: AGPL-3.0-only

package review

import (
	"testing"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

func TestLineMapper(t *testing.T) {
	cases := []struct {
		name string
		diff string
		in   int
		want int
		ok   bool
	}{
		{"before a deletion", "@@ -3 +2,0 @@\n-3\n", 2, 2, true},
		{"the deleted line", "@@ -3 +2,0 @@\n-3\n", 3, 0, false},
		{"after a deletion", "@@ -3 +2,0 @@\n-3\n", 4, 3, true},
		{"insertion at top shifts", "@@ -0,0 +1 @@\n+0\n", 1, 2, true},
		{"insertion after line 2, line 2 stays", "@@ -2,0 +3 @@\n+x\n", 2, 2, true},
		{"insertion after line 2, line 3 shifts", "@@ -2,0 +3 @@\n+x\n", 3, 4, true},
		{"replaced range is unmapped", "@@ -2,2 +2,3 @@\n-2\n-3\n+a\n+b\n+c\n", 3, 0, false},
		{"after a replacement", "@@ -2,2 +2,3 @@\n-2\n-3\n+a\n+b\n+c\n", 4, 5, true},
		{"two hunks accumulate", "@@ -1 +0,0 @@\n-1\n@@ -4 +3,0 @@\n-4\n", 5, 3, true},
		{"no hunks", "", 7, 7, true},
	}
	for _, c := range cases {
		got, ok := mapLine(parseHunks(c.diff), c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%s: mapLine(%d) = %d,%v want %d,%v", c.name, c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestCommentReanchoredWhenLinesStillExist(t *testing.T) {
	e := newEnv(t)
	e.branch("feature", e.c0, edit("a.txt", "1\n2\nX\n4\n5\n"), "feat", e.human)
	ch := e.open("feature")
	cm, err := e.svc.PostComment(e.ctx, e.repo, ch, CommentRequest{Path: "a.txt", StartLine: 3, Body: "why X?"}, e.human)
	if err != nil || cm.PatchSet != 1 || cm.Lane != LaneHuman || cm.Anchor == nil || cm.Anchor.StartLine != 3 {
		t.Fatalf("post: %v %+v", err, cm)
	}
	// patch set 2 inserts a line at the top: the comment follows X to line 4
	e.push("feature", e.c0, edit("a.txt", "0\n1\n2\nX\n4\n5\n"), "feat v2", e.human)
	list, err := e.svc.Comments(e.ctx, e.repo, ch)
	if err != nil || len(list) != 1 {
		t.Fatal(err, list)
	}
	if got := list[0]; got.Outdated || got.Anchor == nil || got.Anchor.PatchSet != 2 || got.Anchor.StartLine != 4 || got.Anchor.EndLine != 4 || got.PatchSet != 1 {
		t.Fatalf("re-anchor: %+v anchor=%+v", got, got.Anchor)
	}
	// a change-level comment has no anchor and is never outdated
	if cl, err := e.svc.PostComment(e.ctx, e.repo, ch, CommentRequest{Body: "overall fine"}, e.human); err != nil || cl.Anchor != nil || cl.Outdated {
		t.Fatalf("change-level: %v %+v", err, cl)
	}
	// resolve
	res, err := e.svc.ResolveComment(e.ctx, e.repo, ch, cm.ID, true, e.human)
	if err != nil || !res.Resolved {
		t.Fatalf("resolve: %v %+v", err, res)
	}
	if _, err := e.svc.ResolveComment(e.ctx, e.repo, ch, "cmt_nope", true, e.human); err == nil {
		t.Fatal("expected not found")
	}
}

func TestCommentOnDeletedLineBecomesOutdated(t *testing.T) {
	e := newEnv(t)
	e.branch("feature", e.c0, edit("a.txt", "1\n2\nX\n4\n5\n"), "feat", e.human)
	ch := e.open("feature")
	onX, _ := e.svc.PostComment(e.ctx, e.repo, ch, CommentRequest{Path: "a.txt", StartLine: 3, EndLine: 3, Body: "on X"}, e.human)
	on5, _ := e.svc.PostComment(e.ctx, e.repo, ch, CommentRequest{Path: "a.txt", StartLine: 5, Body: "on 5"}, e.agent)
	// patch set 2 deletes the X line
	e.push("feature", e.c0, edit("a.txt", "1\n2\n4\n5\n"), "feat v2", e.human)
	list, err := e.svc.Comments(e.ctx, e.repo, ch)
	if err != nil || len(list) != 2 {
		t.Fatal(err, list)
	}
	byID := map[string]Comment{}
	for _, c := range list {
		byID[c.ID] = c
	}
	if got := byID[onX.ID]; !got.Outdated || got.Anchor != nil {
		t.Fatalf("comment on deleted line should be outdated: %+v", got)
	}
	if got := byID[on5.ID]; got.Outdated || got.Anchor == nil || got.Anchor.StartLine != 4 || got.Lane != LaneAgent {
		t.Fatalf("comment on surviving line should move to 4 in the agent lane: %+v %+v", got, got.Anchor)
	}
	// patch set 3 deletes the file: everything anchored to it is outdated
	e.push("feature", e.c0, []gitx.FileEdit{{Path: "a.txt", Content: nil}}, "drop a.txt", e.human)
	list, _ = e.svc.Comments(e.ctx, e.repo, ch)
	for _, c := range list {
		if !c.Outdated {
			t.Fatalf("expected outdated after file deletion: %+v", c)
		}
	}
	// a non-human actor cannot claim the human lane
	if _, err := e.svc.PostComment(e.ctx, e.repo, ch, CommentRequest{Body: "x", Lane: LaneHuman}, e.agent); err == nil {
		t.Fatal("agent claimed the human lane")
	}
	// a bad patch set is refused
	if _, err := e.svc.PostComment(e.ctx, e.repo, ch, CommentRequest{Body: "x", PatchSet: 9}, e.human); err == nil {
		t.Fatal("expected bad request for patch set 9")
	}
}

func TestLateCommentOnMergedChange(t *testing.T) {
	e := newEnv(t)
	e.branch("feature", e.c0, edit("a.txt", "1\n2\nX\n4\n5\n"), "feat", e.human)
	ch := e.open("feature")
	if _, err := e.svc.Merge(e.ctx, e.repo, ch, e.human); err != nil {
		t.Fatal(err)
	}
	merged := e.change(ch.Number)
	if merged.State != "merged" {
		t.Fatal(merged.State)
	}
	bot := ledger.Actor{Kind: "app", ID: "linter"}
	cm, err := e.svc.PostComment(e.ctx, e.repo, merged, CommentRequest{Path: "a.txt", StartLine: 3, Body: "post-merge nit"}, bot)
	if err != nil {
		t.Fatalf("late comment refused: %v", err)
	}
	if cm.Lane != LaneBot || cm.PatchSet != 1 || cm.Anchor == nil {
		t.Fatalf("late comment: %+v", cm)
	}
	list, _ := e.svc.Comments(e.ctx, e.repo, merged)
	if len(list) != 1 || list[0].Lane != LaneBot {
		t.Fatalf("list: %+v", list)
	}
	evs := e.events("io.ore.comment.posted")
	if len(evs) != 1 || evs[0].OreActor != bot {
		t.Fatalf("event: %+v", evs)
	}
}
