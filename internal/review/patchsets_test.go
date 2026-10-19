// SPDX-License-Identifier: AGPL-3.0-only

package review

import (
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/ledger"
)

func TestPatchSetDetection(t *testing.T) {
	e := newEnv(t)
	bob := ledger.Actor{Kind: "human", ID: "bob"}
	f1 := e.branch("feature", e.c0, edit("a.txt", "1\n2\nX\n4\n5\n"), "feat 1", e.human)
	ch := e.open("feature")

	sets, err := e.svc.PatchSets(e.ctx, e.repo, ch)
	if err != nil || len(sets) != 1 || sets[0].Number != 1 || sets[0].BaseOID != e.c0 || sets[0].HeadOID != f1 || sets[0].PushedBy != e.human {
		t.Fatalf("after open: %v %+v", err, sets)
	}
	// a plain push (no API call) is detected on the next read and attributed to the pusher
	f2 := e.push("feature", f1, edit("a.txt", "1\n2\nX\n4\n5\n6\n"), "feat 2", bob)
	sets, err = e.svc.PatchSets(e.ctx, e.repo, ch)
	if err != nil || len(sets) != 2 || sets[1].Number != 2 || sets[1].HeadOID != f2 || sets[1].BaseOID != e.c0 || sets[1].PushedBy != bob {
		t.Fatalf("after push: %v %+v", err, sets)
	}
	if got := e.change(ch.Number); got.HeadOID != f2 {
		t.Fatalf("change head not updated: %s", got.HeadOID[:8])
	}
	// reopening the branch records nothing new
	again := e.open("feature")
	if again.ID != ch.ID {
		t.Fatal("open created a second change")
	}
	if sets, _ = e.svc.PatchSets(e.ctx, e.repo, ch); len(sets) != 2 {
		t.Fatalf("reopen recorded a patch set: %+v", sets)
	}
	// a rebase onto a moved main records a patch set with the new base
	m1 := e.commit(e.c0, edit("d.txt", "d\n"), "main moves")
	e.setRef("refs/heads/main", m1, e.c0, e.human)
	f3 := e.push("feature", m1, edit("a.txt", "1\n2\nX\n4\n5\n6\n"), "feat rebased", e.human)
	sets, _ = e.svc.PatchSets(e.ctx, e.repo, ch)
	if len(sets) != 3 || sets[2].BaseOID != m1 || sets[2].HeadOID != f3 {
		t.Fatalf("after rebase: %+v", sets)
	}
	if evs := e.events("io.ore.patchset.recorded"); len(evs) != 3 {
		t.Fatalf("expected 3 patchset events, got %d", len(evs))
	}
	// Open through the service on a moved head records the patch set too (the Open hook)
	f4 := e.commit(m1, edit("a.txt", "1\n2\nX\n4\n5\n6\n7\n"), "feat 4")
	e.setRef("refs/heads/feature", f4, f3, e.human)
	if _, err := e.svc.Open(e.ctx, e.repo, OpenRequest{SourceRef: "feature"}, e.human); err != nil {
		t.Fatal(err)
	}
	if sets, _ = e.svc.loadPatchSets(e.ctx, ch); len(sets) != 4 || sets[3].HeadOID != f4 {
		t.Fatalf("open hook: %+v", sets)
	}
}

func TestInterdiffIdenticalPatchSetsIsEmpty(t *testing.T) {
	e := newEnv(t)
	f1 := e.branch("feature", e.c0, edit("a.txt", "1\n2\nX\n4\n5\n"), "feat", e.human)
	ch := e.open("feature")
	// an amended commit message: a new patch set with an identical tree
	e.push("feature", e.c0, edit("a.txt", "1\n2\nX\n4\n5\n"), "feat (reworded)", e.human)
	id, err := e.svc.Interdiff(e.ctx, e.repo, ch, 1, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	if !id.Identical || id.Diff != "" || id.Rebased {
		t.Fatalf("expected empty interdiff, got %+v", id)
	}
	if !strings.Contains(id.RangeDiff, "=") && !strings.Contains(id.RangeDiff, "!") {
		t.Fatalf("range-diff missing: %q", id.RangeDiff)
	}
	same, err := e.svc.Interdiff(e.ctx, e.repo, ch, 1, 1, false)
	if err != nil || !same.Identical || same.Diff != "" || same.From.HeadOID != f1 {
		t.Fatalf("1..1: %v %+v", err, same)
	}
	if _, err := e.svc.Interdiff(e.ctx, e.repo, ch, 1, 7, false); err == nil {
		t.Fatal("expected not found for patch set 7")
	}
}

func TestInterdiffRebasedShowsOnlyThePatchDelta(t *testing.T) {
	e := newEnv(t)
	e.branch("feature", e.c0, edit("a.txt", "1\n2\nX\n4\n5\n"), "feat", e.human)
	ch := e.open("feature")
	m1 := e.commit(e.c0, edit("b.txt", "main\n"), "main moves")
	e.setRef("refs/heads/main", m1, e.c0, e.human)
	// patch set 2: rebased onto m1 and the patch itself changed X -> Y
	e.push("feature", m1, edit("a.txt", "1\n2\nY\n4\n5\n"), "feat v2", e.human)
	id, err := e.svc.Interdiff(e.ctx, e.repo, ch, 1, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if !id.Rebased || id.Identical {
		t.Fatalf("expected rebased non-identical: %+v", id)
	}
	if !strings.Contains(id.Diff, "-X") || !strings.Contains(id.Diff, "+Y") {
		t.Fatalf("interdiff should show X->Y:\n%s", id.Diff)
	}
	if strings.Contains(id.Diff, "b.txt") {
		t.Fatalf("interdiff leaked the base change (b.txt):\n%s", id.Diff)
	}
	// patch set 3: rebase only, no patch change -> identical
	e.push("feature", m1, edit("a.txt", "1\n2\nY\n4\n5\n"), "feat v2 again", e.human)
	id, err = e.svc.Interdiff(e.ctx, e.repo, ch, 2, 3, false)
	if err != nil || !id.Identical {
		t.Fatalf("2..3: %v %+v", err, id)
	}
	// a pure rebase from patch set 1 shows only X->Y as well (base factored out)
	id, _ = e.svc.Interdiff(e.ctx, e.repo, ch, 1, 3, false)
	if !strings.Contains(id.Diff, "+Y") || strings.Contains(id.Diff, "main") {
		t.Fatalf("1..3:\n%s", id.Diff)
	}
}
