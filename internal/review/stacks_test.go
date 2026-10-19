// SPDX-License-Identifier: AGPL-3.0-only

package review

import (
	"errors"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/gitx"
)

// stack3 builds b1 <- b2 <- b3 on top of c0 editing a, b and c respectively.
func stack3(e *env) (b1, b2, b3 string) {
	b1 = e.branch("b1", e.c0, edit("a.txt", "A\n"), "edit a", e.human)
	b2 = e.branch("b2", b1, edit("b.txt", "B\n"), "edit b", e.human)
	b3 = e.branch("b3", b2, edit("c.txt", "C\n"), "edit c", e.human)
	return
}

func TestStackOrdering(t *testing.T) {
	e := newEnv(t)
	stack3(e)
	st, err := e.svc.OpenStack(e.ctx, e.repo, StackRequest{Branches: []string{"b1", "b2", "b3"}}, e.human)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "open" || len(st.Changes) != 3 || st.TargetRef != "refs/heads/main" {
		t.Fatalf("stack: %+v", st)
	}
	wantTargets := []string{"refs/heads/main", "refs/heads/b1", "refs/heads/b2"}
	for i, c := range st.Changes {
		if c.SourceRef != "refs/heads/b"+string(rune('1'+i)) || c.TargetRef != wantTargets[i] || c.Number != i+1 || c.State != "open" {
			t.Fatalf("change %d: %+v", i, c)
		}
	}
	got, err := e.svc.Stack(e.ctx, e.repo, st.ID)
	if err != nil || len(got.ChangeIDs) != 3 || got.ChangeIDs[0] != st.Changes[0].ID || got.ChangeIDs[2] != st.Changes[2].ID {
		t.Fatalf("get: %v %+v", err, got)
	}
	list, _ := e.svc.Stacks(e.ctx, e.repo)
	if len(list) != 1 || list[0].ID != st.ID {
		t.Fatalf("list: %+v", list)
	}
	// wrong order, unknown branch, empty list
	for _, bad := range [][]string{{"b2", "b1"}, {"b1", "nope"}, {}} {
		if _, err := e.svc.OpenStack(e.ctx, e.repo, StackRequest{Branches: bad}, e.human); !errors.Is(err, ErrBadRequest) {
			t.Fatalf("branches %v: expected bad request, got %v", bad, err)
		}
	}
	// reopening adopts the existing changes rather than duplicating them
	again, err := e.svc.OpenStack(e.ctx, e.repo, StackRequest{Branches: []string{"b1", "b2"}}, e.human)
	if err != nil || again.ChangeIDs[0] != st.ChangeIDs[0] || again.ChangeIDs[1] != st.ChangeIDs[1] {
		t.Fatalf("adopt: %v %+v", err, again)
	}
	if evs := e.events("io.ore.stack.opened"); len(evs) != 2 {
		t.Fatalf("stack events: %d", len(evs))
	}
}

func TestRebaseCommitsReplaysOntoNewTip(t *testing.T) {
	e := newEnv(t)
	f1 := e.commit(e.c0, edit("a.txt", "A\n"), "edit a")
	f2 := e.commit(f1, edit("b.txt", "B\n"), "edit b")
	m1 := e.commit(e.c0, edit("z.txt", "z\n"), "main adds z")
	commits, err := commitsToReplay(e.ctx, e.rp, m1, f2)
	if err != nil || len(commits) != 2 || commits[0] != f1 || commits[1] != f2 {
		t.Fatalf("commits: %v %v", err, commits)
	}
	res, err := rebaseCommits(e.ctx, e.rp, commits, m1, gitx.Author{Name: "Ore", Email: "ore@acme.invalid"})
	if err != nil || len(res.Replayed) != 2 || res.NewHead == f2 {
		t.Fatalf("rebase: %v %+v", err, res)
	}
	if e.file(res.NewHead, "a.txt") != "A\n" || e.file(res.NewHead, "b.txt") != "B\n" || e.file(res.NewHead, "z.txt") != "z\n" {
		t.Fatal("rebased tree wrong")
	}
	ci, _ := readCommit(e.ctx, e.rp, res.NewHead)
	if ci.Author.Name != "t" || !strings.HasPrefix(ci.Message, "edit b") || ci.Parents[0] != res.Replayed[0] {
		t.Fatalf("metadata not preserved: %+v", ci)
	}
	// commits already upstream by patch id are skipped
	m2 := e.commit(m1, edit("a.txt", "A\n"), "cherry-picked edit a")
	commits, _ = commitsToReplay(e.ctx, e.rp, m2, f2)
	if len(commits) != 1 || commits[0] != f2 {
		t.Fatalf("expected only f2 to replay, got %v", commits)
	}
	// conflicts stop at the offending commit
	m3 := e.commit(e.c0, edit("b.txt", "other\n"), "main edits b")
	res, err = rebaseCommits(e.ctx, e.rp, []string{f1, f2}, m3, gitx.Author{Name: "Ore", Email: "o@o"})
	if !errors.Is(err, gitx.ErrConflict) || res.Conflict != f2 || len(res.Conflicts) != 1 || res.Conflicts[0] != "b.txt" || len(res.Replayed) != 1 {
		t.Fatalf("conflict: %v %+v", err, res)
	}
}

func TestStackLandsBottomUpWithServerSideRebase(t *testing.T) {
	e := newEnv(t)
	_, b2, b3 := stack3(e)
	st, err := e.svc.OpenStack(e.ctx, e.repo, StackRequest{Branches: []string{"b1", "b2", "b3"}}, e.human)
	if err != nil {
		t.Fatal(err)
	}
	// main moves underneath with an unrelated file
	m1 := e.commit(e.c0, edit("d.txt", "d\n"), "main moves")
	e.setRef("refs/heads/main", m1, e.c0, e.human)

	landed, landings, err := e.svc.LandStack(e.ctx, e.repo, st, LandRequest{}, e.human)
	if err != nil {
		t.Fatalf("land stack: %v (%+v)", err, landed)
	}
	if landed.State != "landed" || len(landings) != 3 {
		t.Fatalf("stack: %+v landings=%d", landed, len(landings))
	}
	tip := e.tip("refs/heads/main")
	if e.file(tip, "a.txt") != "A\n" || e.file(tip, "b.txt") != "B\n" || e.file(tip, "c.txt") != "C\n" || e.file(tip, "d.txt") != "d\n" {
		t.Fatal("main is missing part of the stack")
	}
	if *landings[2].MergeOID != tip {
		t.Fatal("last landing is not the tip")
	}
	for i, c := range landed.Changes {
		if c.State != "merged" || c.TargetRef != "refs/heads/main" {
			t.Fatalf("change %d: %+v", i, c)
		}
		sets, _ := e.svc.loadPatchSets(e.ctx, c)
		want := 1
		if i > 0 {
			want = 2 // the rebase recorded a patch set
		}
		if len(sets) != want {
			t.Fatalf("change %d patch sets: %+v", i, sets)
		}
	}
	// the upper branches were moved to their rebased commits, by compare-and-swap
	if e.tip("refs/heads/b2") == b2 || e.tip("refs/heads/b3") == b3 {
		t.Fatal("upper branches were not rebased")
	}
	if !gitx.IsAncestor(e.ctx, e.rp, *landings[0].MergeOID, e.tip("refs/heads/b2")) {
		t.Fatal("b2 was not rebased onto the first merge")
	}
	if evs := e.events("io.ore.change.rebased"); len(evs) != 2 {
		t.Fatalf("rebased events: %d", len(evs))
	}
	if evs := e.events("io.ore.stack.landed"); len(evs) != 1 {
		t.Fatal("no stack.landed event")
	}
	if _, _, err := e.svc.LandStack(e.ctx, e.repo, landed, LandRequest{}, e.human); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("relanding a landed stack: %v", err)
	}
}

func TestStackConflictingMiddleChangeAborts(t *testing.T) {
	e := newEnv(t)
	stack3(e)
	st, err := e.svc.OpenStack(e.ctx, e.repo, StackRequest{Branches: []string{"b1", "b2", "b3"}}, e.human)
	if err != nil {
		t.Fatal(err)
	}
	// main edits b.txt differently: change #2 will conflict on rebase
	m1 := e.commit(e.c0, edit("b.txt", "main's b\n"), "main edits b")
	e.setRef("refs/heads/main", m1, e.c0, e.human)

	aborted, landings, err := e.svc.LandStack(e.ctx, e.repo, st, LandRequest{}, e.human)
	var se *StackError
	var ce *ConflictError
	if !errors.As(err, &se) || !errors.As(err, &ce) || len(ce.Paths) != 1 || ce.Paths[0] != "b.txt" {
		t.Fatalf("expected a stack conflict on b.txt, got %v", err)
	}
	if aborted.State != "aborted" || aborted.FailedChangeNumber != 2 || !strings.Contains(aborted.Error, "change #2") || !strings.Contains(aborted.Error, "b.txt") {
		t.Fatalf("stack state: %+v", aborted)
	}
	if len(landings) != 1 || landings[0].Status != LandingLanded {
		t.Fatalf("landings: %+v", landings)
	}
	states := []string{}
	for _, c := range aborted.Changes {
		states = append(states, c.State)
	}
	if strings.Join(states, ",") != "merged,open,open" {
		t.Fatalf("expected lower merged and upper open, got %v", states)
	}
	tip := e.tip("refs/heads/main")
	if e.file(tip, "a.txt") != "A\n" || e.file(tip, "b.txt") != "main's b\n" {
		t.Fatal("main should contain change #1 and nothing above it")
	}
	evs := e.events("io.ore.stack.aborted")
	if len(evs) != 1 || !strings.Contains(string(evs[0].Data), `"failed_change_number":2`) {
		t.Fatalf("aborted event: %+v", evs)
	}
	// the author restacks: b2 rewritten on main, b3 rewritten on the new b2; landing again resumes from #2
	nb2 := e.push("b2", tip, edit("b.txt", "main's b\nB\n"), "edit b (fixed)", e.human)
	e.push("b3", nb2, edit("c.txt", "C\n"), "edit c", e.human)
	again, landings, err := e.svc.LandStack(e.ctx, e.repo, aborted, LandRequest{}, e.human)
	if err != nil || again.State != "landed" || len(landings) != 2 {
		t.Fatalf("resume: %v %+v landings=%d", err, again, len(landings))
	}
	tip = e.tip("refs/heads/main")
	if e.file(tip, "b.txt") != "main's b\nB\n" || e.file(tip, "c.txt") != "C\n" {
		t.Fatal("resumed stack did not land")
	}
}
