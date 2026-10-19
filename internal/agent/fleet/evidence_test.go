// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"strings"
	"testing"
)

func TestRecommenderMath(t *testing.T) {
	st := DefaultSettings()
	// nothing known: hold
	ev := Evidence{Level: 3}
	if rec := Recommend(ev, st); rec.Action != "hold" {
		t.Fatalf("empty evidence: %+v", rec)
	}
	// rollback rate above threshold over enough deployments: demote one level
	ev = Evidence{Level: 4}
	ev.Production.Deployments, ev.Production.Failed, ev.Production.RollbackRate = 4, 2, f64(0.5)
	rec := Recommend(ev, st)
	if rec.Action != "demote" || rec.To != 3 || !rec.Automatic || !strings.Contains(rec.Reasons[0], "rollback_rate 0.50 > 0.20") {
		t.Fatalf("rollback demotion: %+v", rec)
	}
	// too few deployments to judge
	ev.Production.Deployments, ev.Production.Failed, ev.Production.RollbackRate = 1, 1, f64(1)
	if rec := Recommend(ev, st); rec.Action != "hold" {
		t.Fatalf("one failed deployment is not evidence: %+v", rec)
	}
	// fix rate over enough merges
	ev = Evidence{Level: 3}
	ev.Changes.Merged, ev.Changes.Reverted, ev.Changes.PostMergeFixRate = 3, 2, f64(2.0/3)
	if rec := Recommend(ev, st); rec.Action != "demote" || rec.To != 2 {
		t.Fatalf("fix-rate demotion: %+v", rec)
	}
	// demotion never goes below L2, and L2 never demotes
	ev = Evidence{Level: 2}
	ev.SecurityAlerts = 1
	if rec := Recommend(ev, st); rec.Action != "hold" {
		t.Fatalf("L2 should hold: %+v", rec)
	}
	ev = Evidence{Level: 3, SecurityAlerts: 1}
	if rec := Recommend(ev, st); rec.Action != "demote" || rec.To != 2 {
		t.Fatalf("security demotion: %+v", rec)
	}
	ev = Evidence{Level: 5, BudgetOverruns: 3}
	if rec := Recommend(ev, st); rec.Action != "demote" || rec.To != 4 {
		t.Fatalf("budget demotion: %+v", rec)
	}
	ev = Evidence{Level: 4}
	ev.Changes.Flagged = 1
	if rec := Recommend(ev, st); rec.Action != "demote" || !strings.Contains(rec.Reasons[0], "flagged") {
		t.Fatalf("flag demotion: %+v", rec)
	}
	// promotion: enough accepted changes, clean record, auto_promote off → recommended, not automatic
	ev = Evidence{Level: 2}
	ev.Changes.Opened, ev.Changes.Merged, ev.Changes.Decided, ev.Changes.AcceptanceRate, ev.Changes.PostMergeFixRate = 12, 10, 12, f64(10.0/12), f64(0)
	rec = Recommend(ev, st)
	if rec.Action != "promote" || rec.To != 3 || rec.Automatic {
		t.Fatalf("promotion: %+v", rec)
	}
	st.AutoPromote = true
	if rec := Recommend(ev, st); !rec.Automatic {
		t.Fatal("auto_promote should make the promotion automatic")
	}
	// not enough changes, or acceptance too low, or a flag: hold
	ev.Changes.Decided = 9
	if rec := Recommend(ev, st); rec.Action != "hold" {
		t.Fatalf("9 decided changes: %+v", rec)
	}
	ev.Changes.Decided, ev.Changes.AcceptanceRate = 12, f64(0.5)
	if rec := Recommend(ev, st); rec.Action != "hold" {
		t.Fatalf("low acceptance: %+v", rec)
	}
	ev.Changes.AcceptanceRate, ev.Changes.Flagged = f64(1), 1
	if rec := Recommend(ev, st); rec.Action != "hold" {
		t.Fatalf("a flag blocks promotion at L2: %+v", rec)
	}
	// never past the automatic cap
	ev = Evidence{Level: 4}
	ev.Changes.Decided, ev.Changes.Merged, ev.Changes.AcceptanceRate = 50, 50, f64(1)
	if rec := Recommend(ev, st); rec.Action != "hold" {
		t.Fatalf("L4 -> L5 is a human decision: %+v", rec)
	}
	// thresholds: zero fields take defaults, set fields win
	th := Thresholds{PromoteMinChanges: 2}.withDefaults()
	if th.PromoteMinChanges != 2 || th.DemoteMaxRollbackRate != 0.2 || th.MaxAutoPromoteLevel != 4 {
		t.Fatalf("thresholds: %+v", th)
	}
}

func TestEvidenceFromTheLedger(t *testing.T) {
	e := newEnv(t, 3)
	// two agent changes land; one is flagged later
	run := e.mustComplete(e.run(agent_Request("fix one")))
	_ = run
	ev, rec, err := e.svc.Evidence(e.ctx, e.reload())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Changes.Opened != 1 || ev.Changes.Merged != 1 || ev.Changes.Decided != 1 || *ev.Changes.AcceptanceRate != 1 || *ev.Changes.PostMergeFixRate != 0 || ev.Runs.Completed != 1 {
		t.Fatalf("evidence: %+v", ev)
	}
	if rec.Action != "hold" {
		t.Fatalf("one change is not enough to promote: %+v", rec)
	}
}
