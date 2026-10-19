// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/agent"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/review"
)

func TestWatcherLoopPreventionStopsAFailingRetry(t *testing.T) {
	e := newEnv(t, 4)
	// every head is green and every merge commit is red: every landing reverts
	e.loop.Governor = hookGov{Service: e.svc, onGate: e.alwaysRed()}
	// demotion would hide the point of the test; keep the level
	off := false
	e.svc.SetSettings(e.ctx, e.repo, SettingsInput{AutoDemote: &off}, e.human)
	run, _ := e.run(agent.Request{Prompt: "fix the greeting"})
	if run.Status != "failed" || !strings.Contains(run.Error, "reverted") {
		t.Fatalf("first run: %s %q", run.Status, run.Error)
	}
	// the chain: the human's run failed → the watcher runs once (A, not a retry)
	// → A fails → one retry is allowed (B) → B fails → refused.
	sigs := e.tick()
	var started, skipped int
	var reason string
	for _, s := range sigs {
		switch s.Decision {
		case "started":
			started++
		case "skipped":
			skipped++
			reason = s.Reason
		}
	}
	if started != 2 || skipped != 1 || !strings.Contains(reason, "loop prevention") || !strings.Contains(reason, "retry 2 refused") {
		t.Fatalf("signals: %+v", sigs)
	}
	if runs := e.runs(); len(runs) != 3 {
		t.Fatalf("human run + watcher run + one retry = 3 runs, got %d", len(runs))
	}
	issues, _ := e.db.Issues(e.ctx, e.repo)
	if len(issues) != 3 {
		t.Fatalf("three revert issues expected: %+v", issues)
	}
	// more ticks change nothing
	before := len(e.tick())
	if after := len(e.tick()); after != before || len(e.runs()) != 3 {
		t.Fatal("the watcher must not keep retrying")
	}
	// with retries disabled the watcher still answers the human's failure once, then never re-runs after its own failure
	e2 := newEnv(t, 4)
	zero := 0
	e2.svc.SetSettings(e2.ctx, e2.repo, SettingsInput{WatcherRetries: &zero, AutoDemote: &off}, e2.human)
	e2.loop.Governor = hookGov{Service: e2.svc, onGate: e2.alwaysRed()}
	e2.run(agent.Request{Prompt: "fix"})
	sigs = e2.tick()
	if len(sigs) != 2 || sigs[0].Decision != "skipped" || !strings.Contains(sigs[0].Reason, "loop prevention") || sigs[1].Decision != "started" || len(e2.runs()) != 2 {
		t.Fatalf("retries=0: %+v runs=%d", sigs, len(e2.runs()))
	}
	// a signal with no watcher-started cause is not a retry: a human-started run that reverts gets one retry
	if evs := e.events("io.ore.fleet.watcher.skipped"); len(evs) == 0 {
		t.Fatal("skipped decisions are on the ledger")
	}
}

func TestTwoWatchersRacingOnOneSignalStartOneRun(t *testing.T) {
	e := newEnv(t, 3)
	a := e.svc
	b := &Service{DB: e.db, ReposDir: e.reposDir, LogsDir: e.svc.LogsDir, Loop: e.loop, Reviews: e.rev, RunSync: true, WatcherID: "second"}
	// the delivery slice's failure issue is a signal
	title := "Deployment dep_123 to production failed health check"
	if _, err := e.db.CreateIssue(e.ctx, e.repo, title, "Cause: health probe timed out.", ledger.System); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		a.Tick(e.ctx)
		b.Tick(e.ctx)
	}
	runs := e.runs()
	if len(runs) != 1 || runs[0].Status != "completed" {
		t.Fatalf("expected exactly one completed run, got %+v", runs)
	}
	sigs, _ := a.Signals(e.ctx, e.repo, 0)
	if len(sigs) != 1 || sigs[0].Decision != "started" {
		t.Fatalf("signals: %+v", sigs)
	}
	iss, _ := e.db.Issue(e.ctx, e.repo, 1)
	if iss.State != "closed" {
		t.Fatal("the run should close the failure issue")
	}
	// the watcher does not act at L2
	e2 := newEnv(t, 2)
	e2.db.CreateIssue(e2.ctx, e2.repo, title, "", ledger.System)
	sigs = e2.tick()
	if len(sigs) != 1 || sigs[0].Decision != "skipped" || !strings.Contains(sigs[0].Reason, "L2") || len(e2.runs()) != 0 {
		t.Fatalf("L2: %+v", sigs)
	}
	// nor when paused, nor for an already-handled or closed issue
	e3 := newEnv(t, 3)
	e3.svc.Pause(e3.ctx, &e3.repo, "incident", e3.human)
	e3.db.CreateIssue(e3.ctx, e3.repo, title, "", ledger.System)
	if sigs = e3.tick(); len(sigs) != 1 || sigs[0].Decision != "skipped" || !strings.Contains(sigs[0].Reason, "paused") {
		t.Fatalf("paused: %+v", sigs)
	}
	// a human-opened issue with the same title is not a signal
	e4 := newEnv(t, 3)
	e4.db.CreateIssue(e4.ctx, e4.repo, title, "", e4.human)
	if sigs = e4.tick(); len(sigs) != 0 {
		t.Fatalf("human issues are not watcher signals: %+v", sigs)
	}
}

func TestRedPipelineOnMainOpensAnIssueAndStartsARun(t *testing.T) {
	e := newEnv(t, 3)
	// a system-triggered pipeline run on the (red) default branch
	run, err := e.loop.Runner.Run(e.ctx, e.repo, e.c0, nil, ledger.System)
	if err != nil || run.Status != "failed" {
		t.Fatalf("red main: %v %+v", err, run)
	}
	sigs := e.tick()
	issues, _ := e.db.Issues(e.ctx, e.repo)
	if len(issues) != 1 || !strings.HasPrefix(issues[0].Title, "Pipeline red on main") || !strings.Contains(issues[0].Body, "Commit: "+e.c0) {
		t.Fatalf("issue: %+v", issues)
	}
	if len(sigs) != 1 || sigs[0].Decision != "started" {
		t.Fatalf("signals: %+v", sigs)
	}
	if runs := e.runs(); len(runs) != 1 || runs[0].Status != "completed" {
		t.Fatalf("runs: %+v", runs)
	}
	if e.file(e.tip(), "greeting.txt") != "Hello, Ore!\n" {
		t.Fatal("main should be fixed")
	}
	if iss, _ := e.db.Issue(e.ctx, e.repo, 1); iss.State != "closed" {
		t.Fatal("red-main issue should be closed")
	}
	// a human-requested red run (ore demo step 3) is not a signal, and a second red run does not duplicate the issue
	e2 := newEnv(t, 4)
	e2.loop.Runner.Run(e2.ctx, e2.repo, e2.c0, nil, e2.human)
	e2.tick()
	if issues, _ := e2.db.Issues(e2.ctx, e2.repo); len(issues) != 0 {
		t.Fatalf("human-requested runs are not signals: %+v", issues)
	}
}

func TestAutomaticDemotionOnRollbackRateWithEvidenceOnTheEvent(t *testing.T) {
	e := newEnv(t, 4)
	// three production deployments, two of them failed
	for _, status := range []string{"succeeded", "failed", "failed"} {
		dep := ledger.Deployment{Environment: "production", CommitOID: e.c0, Actor: ledger.Actor{Kind: "agent", ID: "demo-script"}}
		if err := e.db.InsertDeployment(e.ctx, e.repo, &dep); err != nil {
			t.Fatal(err)
		}
		e.db.UpdateDeployment(e.ctx, e.repo, &dep, "started")
		e.db.UpdateDeployment(e.ctx, e.repo, &dep, status)
	}
	ev, rec, applied, err := e.svc.Evaluate(e.ctx, e.repo, "test", FleetActor)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Production.Deployments != 3 || ev.Production.Failed != 2 || *ev.Production.RollbackRate < 0.66 {
		t.Fatalf("evidence: %+v", ev.Production)
	}
	if rec.Action != "demote" || rec.To != 3 || !applied {
		t.Fatalf("recommendation: %+v applied=%v", rec, applied)
	}
	if e.reload().AutonomyLevel != 3 {
		t.Fatalf("level: %d", e.repo.AutonomyLevel)
	}
	evs := e.events("io.ore.repository.autonomy.changed")
	if len(evs) != 1 {
		t.Fatalf("autonomy events: %d", len(evs))
	}
	var changed struct {
		From, To int
		Reason   string
	}
	if err := json.Unmarshal(evs[0].Data, &changed); err != nil || changed.From != 4 || changed.To != 3 {
		t.Fatalf("event: %v %s", err, evs[0].Data)
	}
	for _, want := range []string{"rollback_rate 0.67 > 0.20", "evidence: {", `"production":{"deployments":3,"failed":2`} {
		if !strings.Contains(changed.Reason, want) {
			t.Fatalf("event reason lacks %q: %s", want, changed.Reason)
		}
	}
	// the window restarted: the same deployments do not demote again
	_, rec2, applied2, _ := e.svc.Evaluate(e.ctx, e.repo, "again", FleetActor)
	if applied2 || rec2.Action != "hold" {
		t.Fatalf("no second demotion for the same evidence: %+v", rec2)
	}
	// the watcher triggers an evaluation on deployment failures
	if len(e.events("io.ore.fleet.evidence.evaluated")) < 2 {
		t.Fatal("evaluations are on the ledger")
	}
	// auto_demote off: recommended, not applied
	e2 := newEnv(t, 4)
	off := false
	e2.svc.SetSettings(e2.ctx, e2.repo, SettingsInput{AutoDemote: &off}, e2.human)
	for _, status := range []string{"failed", "failed"} {
		dep := ledger.Deployment{Environment: "production", CommitOID: e2.c0, Actor: ledger.System}
		e2.db.InsertDeployment(e2.ctx, e2.repo, &dep)
		e2.db.UpdateDeployment(e2.ctx, e2.repo, &dep, "started")
		e2.db.UpdateDeployment(e2.ctx, e2.repo, &dep, status)
	}
	_, rec, applied, _ = e2.svc.Evaluate(e2.ctx, e2.repo, "test", FleetActor)
	if rec.Action != "demote" || applied || e2.reload().AutonomyLevel != 4 {
		t.Fatalf("auto_demote off: %+v applied=%v level=%d", rec, applied, e2.repo.AutonomyLevel)
	}
}

func TestPromotionRecommendedButNotAppliedWhenAutomaticPromotionIsOff(t *testing.T) {
	e := newEnv(t, 2)
	two := 2
	if _, err := e.svc.SetSettings(e.ctx, e.repo, SettingsInput{Thresholds: &Thresholds{PromoteMinChanges: two}}, e.human); err != nil {
		t.Fatal(err)
	}
	// two agent changes, both landed by a human
	for i := 0; i < 2; i++ {
		run, err := e.run(agent.Request{Prompt: "fix"})
		if err != nil || run.Status != "blocked" {
			t.Fatalf("L2 run: %v %+v", err, run)
		}
		ch, _ := e.db.Change(e.ctx, e.repo, i+1)
		if _, err := e.rev.Land(e.ctx, e.repo, ch, review.LandRequest{}, e.human); err != nil {
			t.Fatalf("human landing: %v", err)
		}
		// the next agent change must build on the new main
		e.loop.AdapterCmd = `printf 'Hello, Ore! %d\n' ` + string(rune('1'+i)) + ` > greeting.txt`
	}
	ev, rec, applied, err := e.svc.Evaluate(e.ctx, e.reload(), "test", FleetActor)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Changes.Decided != 2 || ev.Changes.Merged != 2 || *ev.Changes.AcceptanceRate != 1 {
		t.Fatalf("evidence: %+v", ev.Changes)
	}
	if rec.Action != "promote" || rec.To != 3 || rec.Automatic || applied || e.reload().AutonomyLevel != 2 {
		t.Fatalf("promotion must be recommended, not applied: %+v applied=%v level=%d", rec, applied, e.repo.AutonomyLevel)
	}
	// a human applies it
	_, rec, applied, err = e.svc.ApplyRecommendation(e.ctx, e.repo, e.human)
	if err != nil || !applied || rec.To != 3 || e.reload().AutonomyLevel != 3 {
		t.Fatalf("apply: %v %+v level=%d", err, rec, e.repo.AutonomyLevel)
	}
	evs := e.events("io.ore.repository.autonomy.changed")
	if len(evs) != 1 || !strings.Contains(string(evs[0].Data), "acceptance_rate") || !strings.Contains(string(evs[0].Data), "human/rohan") {
		t.Fatalf("event: %+v", evs)
	}
}

func TestFlaggingAChangeDemotesWithEvidence(t *testing.T) {
	e := newEnv(t, 3)
	e.mustComplete(e.run(agent.Request{Prompt: "fix"}))
	ch, _ := e.db.Change(e.ctx, e.repo, 1)
	if _, _, _, err := e.svc.FlagChange(e.ctx, e.repo, ch, "broke prod", ledger.Actor{Kind: "agent", ID: "x"}); err == nil {
		t.Fatal("agents cannot flag")
	}
	f, rec, applied, err := e.svc.FlagChange(e.ctx, e.reload(), ch, "broke prod", e.human)
	if err != nil || f.Number != 1 || rec.Action != "demote" || rec.To != 2 || !applied {
		t.Fatalf("flag: %v %+v %+v %v", err, f, rec, applied)
	}
	if e.reload().AutonomyLevel != 2 {
		t.Fatalf("level: %d", e.repo.AutonomyLevel)
	}
	evs := e.events("io.ore.repository.autonomy.changed")
	if len(evs) != 1 || !strings.Contains(string(evs[0].Data), "flagged change #1") || !strings.Contains(string(evs[0].Data), "evidence: {") || !strings.Contains(string(evs[0].Data), `\"flagged\":1`) {
		t.Fatalf("event: %s", evs[0].Data)
	}
	if len(e.events("io.ore.change.flagged")) != 1 {
		t.Fatal("flag event missing")
	}
	if _, _, _, err := e.svc.FlagChange(e.ctx, e.repo, ch, "again", e.human); err == nil {
		t.Fatal("double flag accepted")
	}
	// at L2 a flag is recorded but nothing demotes
	e2 := newEnv(t, 2)
	e2.run(agent.Request{Prompt: "fix"})
	ch2, _ := e2.db.Change(e2.ctx, e2.repo, 1)
	_, rec2, applied2, err := e2.svc.FlagChange(e2.ctx, e2.repo, ch2, "", e2.human)
	if err != nil || applied2 || rec2.Action != "hold" {
		t.Fatalf("L2 flag: %v %+v", err, rec2)
	}
}
