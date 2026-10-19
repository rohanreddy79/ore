// SPDX-License-Identifier: AGPL-3.0-only

package review

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

func TestLandQueueStrategy(t *testing.T) {
	e := newEnv(t)
	e.branch("feature", e.c0, edit("a.txt", "A\n"), "feat", e.human)
	ch := e.open("feature")
	l, err := e.svc.Land(e.ctx, e.repo, ch, LandRequest{Strategy: StrategyQueue}, e.human)
	if err != nil || l.Status != LandingLanded || l.MergeOID == nil || *l.MergeOID != e.tip("refs/heads/main") || l.VerificationRunID != nil {
		t.Fatalf("queue: %v %+v", err, l)
	}
	list, _ := e.svc.Landings(e.ctx, e.repo, ch)
	if len(list) != 1 || list[0].ID != l.ID || list[0].ChangeNumber != ch.Number {
		t.Fatalf("landings: %+v", list)
	}
	if _, err := e.svc.Land(e.ctx, e.repo, ch, LandRequest{}, e.human); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("relanding a merged change: %v", err)
	}
	if _, err := e.svc.Land(e.ctx, e.repo, ch, LandRequest{Strategy: "yolo"}, e.human); !errors.Is(err, ErrBadRequest) {
		t.Fatal("bad strategy accepted")
	}
	for _, typ := range []string{"io.ore.landing.requested", "io.ore.landing.landed", "io.ore.change.merged"} {
		if len(e.events(typ)) != 1 {
			t.Fatalf("missing event %s", typ)
		}
	}
}

func TestLandWhenGreenMergesOnlyWhenGreen(t *testing.T) {
	e := newEnv(t)
	e.branch("bad", e.c0, edit("STATUS", "broken\n"), "break it", e.human)
	bad := e.open("bad")
	l, err := e.svc.Land(e.ctx, e.repo, bad, LandRequest{Strategy: StrategyLandWhenGreen}, e.human)
	var le *LandingError
	if !errors.As(err, &le) || !errors.Is(err, ErrVerificationFailed) || l.Status != LandingFailed || l.VerificationRunID == nil || l.MergeOID != nil {
		t.Fatalf("red head should not land: %v %+v", err, l)
	}
	if e.tip("refs/heads/main") != e.c0 || e.change(bad.Number).State != "open" {
		t.Fatal("red change was merged")
	}
	run, _ := e.db.PipelineRun(e.ctx, *l.VerificationRunID)
	if run.Status != "failed" || run.CommitOID != bad.HeadOID {
		t.Fatalf("run: %+v", run)
	}
	e.branch("good", e.c0, edit("a.txt", "A\n"), "fine", e.human)
	good := e.open("good")
	l, err = e.svc.Land(e.ctx, e.repo, good, LandRequest{Strategy: StrategyLandWhenGreen}, e.human)
	if err != nil || l.Status != LandingLanded || l.VerificationRunID == nil || *l.MergeOID != e.tip("refs/heads/main") {
		t.Fatalf("green head should land: %v %+v", err, l)
	}
	run, _ = e.db.PipelineRun(e.ctx, *l.VerificationRunID)
	if run.CommitOID != good.HeadOID {
		t.Fatal("land_when_green must verify the head")
	}
	// the default strategy comes from the policy
	e.svc.SetPolicy(e.ctx, e.repo, Policy{DefaultStrategy: StrategyLandWhenGreen}, e.human)
	e.branch("good2", e.tip("refs/heads/main"), edit("b.txt", "B\n"), "fine 2", e.human)
	l, err = e.svc.Land(e.ctx, e.repo, e.open("good2"), LandRequest{}, e.human)
	if err != nil || l.Strategy != StrategyLandWhenGreen {
		t.Fatalf("default strategy: %v %+v", err, l)
	}
}

func TestLandWhenGreenRefusesAHeadThatMovedDuringVerification(t *testing.T) {
	e := newEnv(t)
	f1 := e.branch("feature", e.c0, edit("a.txt", "A\n"), "feat", e.human)
	ch := e.open("feature")
	real := e.svc.Verifier
	e.svc.Verifier = &hookedVerifier{before: func() { e.push("feature", f1, edit("a.txt", "A2\n"), "sneaky push", e.human) }, real: real}
	_, err := e.svc.Land(e.ctx, e.repo, ch, LandRequest{Strategy: StrategyLandWhenGreen}, e.human)
	if !errors.Is(err, ErrVerificationFailed) || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("expected refusal, got %v", err)
	}
	if e.tip("refs/heads/main") != e.c0 {
		t.Fatal("unverified head was merged")
	}
	sets, _ := e.svc.loadPatchSets(e.ctx, ch)
	if len(sets) != 2 {
		t.Fatalf("the push should be a patch set: %+v", sets)
	}
}

func TestOptimisticLandingGreenNoRevert(t *testing.T) {
	e := newEnv(t)
	e.branch("feature", e.c0, edit("a.txt", "A\n"), "feat", e.human)
	ch := e.open("feature")
	l, err := e.svc.Land(e.ctx, e.repo, ch, LandRequest{Strategy: StrategyOptimistic}, e.agent)
	if err != nil || l.Status != LandingLanded || l.RevertOID != nil || l.IssueNumber != nil || l.VerificationRunID == nil {
		t.Fatalf("optimistic green: %v %+v", err, l)
	}
	if *l.MergeOID != e.tip("refs/heads/main") {
		t.Fatal("main should point at the merge")
	}
	run, _ := e.db.PipelineRun(e.ctx, *l.VerificationRunID)
	if run.CommitOID != *l.MergeOID || run.Status != "succeeded" {
		t.Fatalf("verification must run on the merge commit: %+v", run)
	}
	if issues, _ := e.db.Issues(e.ctx, e.repo); len(issues) != 0 {
		t.Fatal("no issue expected")
	}
	if len(e.events("io.ore.change.reverted")) != 0 {
		t.Fatal("no revert expected")
	}
	if e.change(ch.Number).State != "merged" {
		t.Fatal("change should be merged")
	}
}

func TestOptimisticLandingRedRevertsAndOpensIssue(t *testing.T) {
	e := newEnv(t)
	f1 := e.branch("feature", e.c0, []gitx.FileEdit{{Path: "STATUS", Content: []byte("broken\n")}, {Path: "a.txt", Content: []byte("A\n")}}, "break it", e.human)
	ch := e.open("feature")
	l, err := e.svc.Land(e.ctx, e.repo, ch, LandRequest{Strategy: StrategyOptimistic}, e.agent)
	if !errors.Is(err, ErrReverted) || l.Status != LandingReverted || l.MergeOID == nil || l.RevertOID == nil || l.IssueNumber == nil || l.VerificationRunID == nil {
		t.Fatalf("optimistic red: %v %+v", err, l)
	}
	tip := e.tip("refs/heads/main")
	if tip != *l.RevertOID {
		t.Fatalf("main should point at the revert: %s vs %s", short(tip), short(*l.RevertOID))
	}
	rc, _ := readCommit(e.ctx, e.rp, tip)
	if rc.Parents[0] != *l.MergeOID || !strings.HasPrefix(rc.Message, "Revert \"Merge change #1") || !strings.Contains(rc.Message, "Change-Id: "+ch.ChangeID) {
		t.Fatalf("revert commit: %+v", rc)
	}
	if e.file(tip, "STATUS") != "ok\n" || e.file(tip, "a.txt") != "1\n2\n3\n4\n5\n" {
		t.Fatal("revert did not restore the tree")
	}
	issues, _ := e.db.Issues(e.ctx, e.repo)
	if len(issues) != 1 || issues[0].Number != *l.IssueNumber || issues[0].Title != "Change #1 reverted: verification failed" {
		t.Fatalf("issue: %+v", issues)
	}
	if b := issues[0].Body; !strings.Contains(b, *l.VerificationRunID) || !strings.Contains(b, "[check]") || !strings.Contains(b, short(*l.RevertOID)) {
		t.Fatalf("issue body lacks the run log excerpt:\n%s", b)
	}
	if got := e.change(ch.Number); got.State != StateReverted {
		t.Fatalf("change state: %s", got.State)
	}
	evs := e.events("io.ore.change.reverted")
	if len(evs) != 1 || !strings.Contains(string(evs[0].Data), *l.RevertOID) || !strings.Contains(string(evs[0].Data), `"issue_number":`+itoa(*l.IssueNumber)) {
		t.Fatalf("reverted event: %+v", evs)
	}
	if len(e.events("io.ore.ref.updated")) < 3 { // push, merge, revert
		t.Fatal("revert ref update not recorded")
	}
	// a reverted change cannot be relanded as is
	if _, err := e.svc.Land(e.ctx, e.repo, ch, LandRequest{Strategy: StrategyOptimistic}, e.agent); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("relanding a reverted change: %v", err)
	}
	// the fix: a new patch set reopens the change and lands green. It must be
	// rebased onto the target: the original commits are already ancestors of
	// main through the reverted merge, so a commit on top of f1 would merge
	// as "no change" for everything the revert undid.
	e.push("feature", tip, []gitx.FileEdit{{Path: "STATUS", Content: []byte("ok\n")}, {Path: "a.txt", Content: []byte("A\n")}}, "fix, rebased onto the revert", e.human)
	_ = f1
	fixed, _, err := e.svc.SyncPatchSets(e.ctx, e.repo, ch)
	if err != nil || fixed.State != "open" || fixed.MergeOID != nil {
		t.Fatalf("reopen: %v %+v", err, fixed)
	}
	l2, err := e.svc.Land(e.ctx, e.repo, fixed, LandRequest{Strategy: StrategyOptimistic}, e.agent)
	if err != nil || l2.Status != LandingLanded {
		t.Fatalf("reland: %v %+v", err, l2)
	}
	if e.file(e.tip("refs/heads/main"), "a.txt") != "A\n" {
		t.Fatal("fix did not land")
	}
	if list, _ := e.svc.Landings(e.ctx, e.repo, ch); len(list) != 2 || list[0].Status != LandingReverted || list[1].Status != LandingLanded {
		t.Fatalf("landings: %+v", list)
	}
}

func TestOptimisticLandingsRaceCASRetry(t *testing.T) {
	e := newEnv(t)
	e.branch("fa", e.c0, edit("a.txt", "A\n"), "feat a", e.human)
	e.branch("fb", e.c0, edit("b.txt", "B\n"), "feat b", e.human)
	chA, chB := e.open("fa"), e.open("fb")
	calls := 0
	var hook func(string)
	hook = func(ref string) {
		calls++
		if calls == 1 {
			// B computed its merge against the old tip; A lands in between.
			beforeCAS = nil
			if l, err := e.svc.Land(e.ctx, e.repo, chA, LandRequest{Strategy: StrategyOptimistic}, e.agent); err != nil || l.Status != LandingLanded {
				t.Errorf("landing A inside the race: %v %+v", err, l)
			}
			beforeCAS = hook
		}
	}
	beforeCAS = hook
	defer func() { beforeCAS = nil }()
	lB, err := e.svc.Land(e.ctx, e.repo, chB, LandRequest{Strategy: StrategyOptimistic}, e.agent)
	if err != nil || lB.Status != LandingLanded {
		t.Fatalf("B: %v %+v", err, lB)
	}
	if calls != 2 {
		t.Fatalf("expected the compare-and-swap to fail once and retry, hook calls = %d", calls)
	}
	tip := e.tip("refs/heads/main")
	if tip != *lB.MergeOID {
		t.Fatal("main should be B's merge")
	}
	mb, _ := readCommit(e.ctx, e.rp, tip)
	lA, _ := e.svc.Landings(e.ctx, e.repo, chA)
	if len(lA) != 1 || mb.Parents[0] != *lA[0].MergeOID {
		t.Fatalf("B's merge should sit on A's merge: parents=%v A=%+v", mb.Parents, lA)
	}
	if e.file(tip, "a.txt") != "A\n" || e.file(tip, "b.txt") != "B\n" {
		t.Fatal("both changes should be on main")
	}
	if e.change(chA.Number).State != "merged" || e.change(chB.Number).State != "merged" {
		t.Fatal("both changes should be merged")
	}
}

func TestLandDeletedSourceBranch(t *testing.T) {
	e := newEnv(t)
	f1 := e.branch("feature", e.c0, edit("a.txt", "A\n"), "feat", e.human)
	ch := e.open("feature")
	if err := gitx.DeleteRef(e.ctx, e.rp, "refs/heads/feature", f1); err != nil {
		t.Fatal(err)
	}
	for _, strategy := range []string{StrategyQueue, StrategyLandWhenGreen, StrategyOptimistic} {
		if _, err := e.svc.Land(e.ctx, e.repo, ch, LandRequest{Strategy: strategy}, e.human); !errors.Is(err, ErrSourceGone) {
			t.Fatalf("%s: expected ErrSourceGone, got %v", strategy, err)
		}
	}
	if list, _ := e.svc.Landings(e.ctx, e.repo, ch); len(list) != 0 {
		t.Fatal("no landing should be recorded for a missing source")
	}
	// reads still work: the last known patch set stays
	sets, err := e.svc.PatchSets(e.ctx, e.repo, ch)
	if err != nil || len(sets) != 1 || sets[0].HeadOID != f1 {
		t.Fatalf("patch sets after deletion: %v %+v", err, sets)
	}
}

func TestVerifyingStrategiesFailClosedWithoutVerifier(t *testing.T) {
	e := newEnv(t)
	e.svc.Verifier = nil
	e.branch("feature", e.c0, edit("a.txt", "A\n"), "feat", e.human)
	ch := e.open("feature")
	for _, strategy := range []string{StrategyLandWhenGreen, StrategyOptimistic} {
		l, err := e.svc.Land(e.ctx, e.repo, ch, LandRequest{Strategy: strategy}, e.human)
		if !errors.Is(err, ErrNoVerifier) || l.Status != LandingFailed {
			t.Fatalf("%s: %v %+v", strategy, err, l)
		}
	}
	if e.tip("refs/heads/main") != e.c0 {
		t.Fatal("nothing should have merged")
	}
	if l, err := e.svc.Land(e.ctx, e.repo, ch, LandRequest{Strategy: StrategyQueue}, e.human); err != nil || l.Status != LandingLanded {
		t.Fatalf("queue needs no verifier: %v", err)
	}
}

// hookedVerifier runs before, then delegates to the real verifier.
type hookedVerifier struct {
	before func()
	real   Verifier
}

func (v *hookedVerifier) Run(ctx context.Context, repo ledger.Repo, commitOID string, changeID *string, actor ledger.Actor) (ledger.PipelineRun, error) {
	v.before()
	return v.real.Run(ctx, repo, commitOID, changeID, actor)
}

func itoa(n int) string { return strconv.Itoa(n) }
