// SPDX-License-Identifier: AGPL-3.0-only

package review

import (
	"errors"
	"testing"

	"github.com/orevcs/ore/internal/ledger"
)

func TestPushAfterApprovalResetsApprovals(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.SetPolicy(e.ctx, e.repo, Policy{RequiredApprovals: 1}, e.human); err != nil {
		t.Fatal(err)
	}
	f1 := e.branch("feature", e.c0, edit("a.txt", "1\n2\nX\n4\n5\n"), "feat", e.human)
	ch := e.open("feature")
	rv, err := e.svc.SubmitReview(e.ctx, e.repo, ch, ReviewRequest{Verdict: "approve", Findings: []Finding{{Severity: "info", Confidence: 0.9, Path: "a.txt", Message: "fine"}}}, e.human)
	if err != nil || rv.PatchSet != 1 || rv.Lane != LaneHuman || !rv.Counts {
		t.Fatalf("approve: %v %+v", err, rv)
	}
	st, _ := e.svc.Approvals(e.ctx, e.repo, ch)
	if !st.Landable || st.Approvals != 1 || st.PatchSet != 1 {
		t.Fatalf("approved status: %+v", st)
	}
	// a push after approval: new patch set, approvals reset
	e.push("feature", f1, edit("a.txt", "1\n2\nX\n4\n5\n6\n"), "feat v2", e.human)
	st, _ = e.svc.Approvals(e.ctx, e.repo, ch)
	if st.Landable || st.Approvals != 0 || st.PatchSet != 2 || st.Reason == "" {
		t.Fatalf("after push: %+v", st)
	}
	_, err = e.svc.Land(e.ctx, e.repo, ch, LandRequest{}, e.human)
	var pe *PolicyError
	if !errors.As(err, &pe) || pe.Status.PatchSet != 2 {
		t.Fatalf("expected policy refusal after push, got %v", err)
	}
	if got := e.change(ch.Number); got.State != "open" {
		t.Fatalf("change should still be open: %s", got.State)
	}
	// the old review no longer counts; a fresh approval on patch set 2 lands it
	list, _ := e.svc.Reviews(e.ctx, e.repo, ch)
	if len(list) != 1 || list[0].Counts {
		t.Fatalf("stale review still counts: %+v", list)
	}
	if _, err := e.svc.SubmitReview(e.ctx, e.repo, ch, ReviewRequest{Verdict: "approve"}, e.human); err != nil {
		t.Fatal(err)
	}
	l, err := e.svc.Land(e.ctx, e.repo, ch, LandRequest{}, e.human)
	if err != nil || l.Status != LandingLanded || l.Strategy != StrategyQueue || l.PatchSet != 2 {
		t.Fatalf("land: %v %+v", err, l)
	}
	if evs := e.events("io.ore.review.submitted"); len(evs) != 2 {
		t.Fatalf("review events: %d", len(evs))
	}
}

func TestAgentApprovalDoesNotCount(t *testing.T) {
	e := newEnv(t)
	e.svc.SetPolicy(e.ctx, e.repo, Policy{RequiredApprovals: 1}, e.human)
	e.branch("feature", e.c0, edit("a.txt", "1\n2\nX\n4\n5\n"), "feat", e.human)
	ch := e.open("feature")
	rv, err := e.svc.SubmitReview(e.ctx, e.repo, ch, ReviewRequest{Verdict: "approve", Body: "LGTM"}, e.agent)
	if err != nil || rv.Lane != LaneAgent || rv.Counts {
		t.Fatalf("agent approve: %v %+v", err, rv)
	}
	bot := ledger.Actor{Kind: "app", ID: "ci-bot"}
	if rv, err := e.svc.SubmitReview(e.ctx, e.repo, ch, ReviewRequest{Verdict: "approve"}, bot); err != nil || rv.Lane != LaneBot || rv.Counts {
		t.Fatalf("bot approve: %v %+v", err, rv)
	}
	st, _ := e.svc.Approvals(e.ctx, e.repo, ch)
	if st.Landable || st.Approvals != 0 || len(st.Ignored) != 2 || st.Required != 1 {
		t.Fatalf("agent/bot approvals counted: %+v", st)
	}
	if _, err := e.svc.Land(e.ctx, e.repo, ch, LandRequest{}, e.human); err == nil {
		t.Fatal("landed on agent approval alone")
	}
	// the repository may opt in
	e.svc.SetPolicy(e.ctx, e.repo, Policy{RequiredApprovals: 1, AllowAgentApprovals: true}, e.human)
	st, _ = e.svc.Approvals(e.ctx, e.repo, ch)
	if !st.Landable || st.Approvals != 2 {
		t.Fatalf("opt-in: %+v", st)
	}
	// an agent cannot pose as a human reviewer
	if _, err := e.svc.SubmitReview(e.ctx, e.repo, ch, ReviewRequest{Verdict: "approve", Lane: LaneHuman}, e.agent); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("expected bad request, got %v", err)
	}
	// a human can file in the bot lane (posting on a tool's behalf), and that does not count
	e.svc.SetPolicy(e.ctx, e.repo, Policy{RequiredApprovals: 3}, e.human)
	if rv, err := e.svc.SubmitReview(e.ctx, e.repo, ch, ReviewRequest{Verdict: "approve", Lane: LaneBot}, e.human); err != nil || rv.Counts {
		t.Fatalf("human-as-bot: %v %+v", err, rv)
	}
}

func TestRequestChangesBlocksUntilSuperseded(t *testing.T) {
	e := newEnv(t)
	e.branch("feature", e.c0, edit("a.txt", "1\n2\nX\n4\n5\n"), "feat", e.human)
	ch := e.open("feature")
	bob := ledger.Actor{Kind: "human", ID: "bob"}
	if _, err := e.svc.SubmitReview(e.ctx, e.repo, ch, ReviewRequest{Verdict: "request_changes", Findings: []Finding{{Severity: "high", Confidence: 1, Path: "a.txt", Range: &LineRange{3, 3}, Message: "X is wrong"}}}, bob); err != nil {
		t.Fatal(err)
	}
	st, _ := e.svc.Approvals(e.ctx, e.repo, ch)
	if st.Landable || len(st.ChangesRequestedBy) != 1 {
		t.Fatalf("request_changes should block even with 0 required approvals: %+v", st)
	}
	// an agent's request_changes does not block (its lane does not count)
	e.svc.SubmitReview(e.ctx, e.repo, ch, ReviewRequest{Verdict: "approve"}, bob)
	e.svc.SubmitReview(e.ctx, e.repo, ch, ReviewRequest{Verdict: "request_changes"}, e.agent)
	st, _ = e.svc.Approvals(e.ctx, e.repo, ch)
	if !st.Landable || st.Approvals != 1 {
		t.Fatalf("latest human verdict should win and the agent lane should not block: %+v", st)
	}
	// findings are validated against the spec shape
	for _, bad := range []Finding{{Severity: "huge", Confidence: 1, Path: "a", Message: "m"}, {Severity: "low", Confidence: 2, Path: "a", Message: "m"}, {Severity: "low", Confidence: 0.5, Message: "m"}} {
		if _, err := e.svc.SubmitReview(e.ctx, e.repo, ch, ReviewRequest{Verdict: "comment", Findings: []Finding{bad}}, bob); !errors.Is(err, ErrBadRequest) {
			t.Fatalf("finding %+v accepted: %v", bad, err)
		}
	}
	if _, err := e.svc.SubmitReview(e.ctx, e.repo, ch, ReviewRequest{Verdict: "maybe"}, bob); !errors.Is(err, ErrBadRequest) {
		t.Fatal("bad verdict accepted")
	}
	if _, err := e.svc.SetPolicy(e.ctx, e.repo, Policy{DefaultStrategy: "yolo"}, bob); !errors.Is(err, ErrBadRequest) {
		t.Fatal("bad strategy accepted")
	}
}
