// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/agent"
	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/review"
)

func TestLevel3LandsWhenGreenThroughTheLoop(t *testing.T) {
	e := newEnv(t, 3)
	issue, _ := e.db.CreateIssue(e.ctx, e.repo, "Greeting should say Hello, Ore!", "", e.human)
	run := e.mustComplete(e.run(agent.Request{Issue: &issue}))
	if e.file(e.tip(), "greeting.txt") != "Hello, Ore!\n" {
		t.Fatal("main not fixed")
	}
	ch, _ := e.db.Change(e.ctx, e.repo, 1)
	landings, _ := e.rev.Landings(e.ctx, e.repo, ch)
	if len(landings) != 1 || landings[0].Strategy != review.StrategyLandWhenGreen || landings[0].Status != review.LandingLanded {
		t.Fatalf("landing: %+v", landings)
	}
	// the head was verified once by the loop and reused by land_when_green: one pipeline run for the head
	runs, _ := e.db.PipelineRuns(e.ctx, e.repo, 10)
	if len(runs) != 1 || runs[0].CommitOID != ch.HeadOID || landings[0].VerificationRunID == nil || *landings[0].VerificationRunID != runs[0].ID {
		t.Fatalf("pipeline runs: %+v landing %+v", runs, landings[0])
	}
	if !strings.Contains(e.runLog(run), "strategy land_when_green") {
		t.Fatal("log should name the strategy")
	}
	if iss, _ := e.db.Issue(e.ctx, e.repo, issue.Number); iss.State != "closed" {
		t.Fatal("issue should be closed")
	}
}

func TestLevel4LandsOptimisticallyGreen(t *testing.T) {
	e := newEnv(t, 4)
	e.mustComplete(e.run(agent.Request{Prompt: "fix the greeting"}))
	ch, _ := e.db.Change(e.ctx, e.repo, 1)
	landings, _ := e.rev.Landings(e.ctx, e.repo, ch)
	if len(landings) != 1 || landings[0].Strategy != review.StrategyOptimistic || landings[0].Status != review.LandingLanded || landings[0].RevertOID != nil {
		t.Fatalf("landing: %+v", landings)
	}
	runs, _ := e.db.PipelineRuns(e.ctx, e.repo, 10)
	if len(runs) != 2 { // head, then the merge commit
		t.Fatalf("expected head and merge verification, got %d runs", len(runs))
	}
	if e.tip() != *landings[0].MergeOID || ch.State != "merged" {
		t.Fatal("main should be the merge")
	}
	if len(e.events("io.ore.change.reverted")) != 0 {
		t.Fatal("no revert expected")
	}
}

func TestLevel4FailingPostMergeVerificationRevertsAndWatcherRetrySucceeds(t *testing.T) {
	e := newEnv(t, 4)
	e.loop.AdapterCmd = fixAndUnbreak
	// main breaks between verification and landing: the merge commit is red
	e.loop.Governor = hookGov{Service: e.svc, onGate: func(step string) {
		if step == "land" && e.file(e.tip(), "BROKEN") == "" {
			e.breakMain()
		}
	}}
	run, err := e.run(agent.Request{Prompt: "fix the greeting"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || !strings.Contains(run.Error, "post-merge verification failed") || !strings.Contains(run.Error, "reverted as") || !strings.Contains(run.Error, "issue #1 opened") {
		t.Fatalf("run: %s %q\n%s", run.Status, run.Error, e.runLog(run))
	}
	ch, _ := e.db.Change(e.ctx, e.repo, 1)
	if ch.State != review.StateReverted {
		t.Fatalf("change should be reverted: %s", ch.State)
	}
	landings, _ := e.rev.Landings(e.ctx, e.repo, ch)
	if len(landings) != 1 || landings[0].Status != review.LandingReverted || e.tip() != *landings[0].RevertOID {
		t.Fatalf("landing: %+v", landings)
	}
	if e.file(e.tip(), "greeting.txt") != "Hello, World!\n" {
		t.Fatal("the revert should restore the greeting")
	}
	issues, _ := e.db.Issues(e.ctx, e.repo)
	if len(issues) != 1 || issues[0].Title != "Change #1 reverted: verification failed" || issues[0].State != "open" {
		t.Fatalf("fix signal: %+v", issues)
	}
	// the watcher picks the signal up and the retry fixes main
	sigs := e.tick()
	if len(sigs) != 1 || sigs[0].Decision != "started" || sigs[0].Depth != 0 || sigs[0].CauseRunID == nil || *sigs[0].CauseRunID != run.ID {
		t.Fatalf("signals: %+v", sigs)
	}
	runs := e.runs()
	if len(runs) != 2 {
		t.Fatalf("expected the retry run, got %d", len(runs))
	}
	retry := runs[0]
	if retry.Status != "completed" || retry.IssueID == nil || *retry.IssueID != issues[0].ID {
		t.Fatalf("retry run: %+v\n%s", retry, e.runLog(retry))
	}
	if e.file(e.tip(), "greeting.txt") != "Hello, Ore!\n" || e.file(e.tip(), "BROKEN") != "" {
		t.Fatal("the retry should fix main and remove BROKEN")
	}
	if iss, _ := e.db.Issue(e.ctx, e.repo, 1); iss.State != "closed" {
		t.Fatal("the fix signal should be closed by the retry")
	}
	for _, typ := range []string{"io.ore.change.reverted", "io.ore.fleet.watcher.started", "io.ore.fleet.evidence.evaluated"} {
		if len(e.events(typ)) == 0 {
			t.Fatalf("missing %s", typ)
		}
	}
}

func TestAdversarialReviewerBlocksThenTheRetryFixesIt(t *testing.T) {
	e := newEnv(t, 3)
	e.enableReviewer()
	e.loop.AdapterCmd = fixmeAdapter
	run := e.mustComplete(e.run(agent.Request{Prompt: "fix the greeting"}))
	log := e.runLog(run)
	if !strings.Contains(log, "[review] blocked") || !strings.Contains(log, "fix signal") || !strings.Contains(log, "[review] passed after fix") {
		t.Fatalf("log:\n%s", log)
	}
	ch, _ := e.db.Change(e.ctx, e.repo, 1)
	if ch.State != "merged" || e.file(e.tip(), "greeting.txt") != "Hello, Ore!\n" {
		t.Fatalf("change %s, greeting %q", ch.State, e.file(e.tip(), "greeting.txt"))
	}
	reviews, _ := e.rev.Reviews(e.ctx, e.repo, ch)
	if len(reviews) != 2 || reviews[0].Verdict != "request_changes" || reviews[0].Lane != review.LaneAgent || reviews[0].PatchSet != 1 || reviews[1].Verdict != "approve" || reviews[1].PatchSet != 2 {
		t.Fatalf("reviews: %+v", reviews)
	}
	if reviews[0].Findings[0].Severity != "high" || reviews[0].Findings[0].Confidence < BlockConfidence || reviews[0].Findings[0].Path != "greeting.txt" {
		t.Fatalf("finding: %+v", reviews[0].Findings)
	}
	sets, _ := e.rev.PatchSets(e.ctx, e.repo, ch)
	if len(sets) != 2 {
		t.Fatalf("the fix should be patch set 2: %+v", sets)
	}
	if evs := e.events("io.ore.agent.review.completed"); len(evs) != 2 {
		t.Fatalf("review events: %d", len(evs))
	}
	// a second block ends the run: the adapter that never fixes
	e2 := newEnv(t, 3)
	e2.enableReviewer()
	e2.loop.AdapterCmd = `printf 'Hello, Ore!\nFIXME %s\n' "$ORE_RETRY" > greeting.txt`
	run2, err := e2.run(agent.Request{Prompt: "fix the greeting"})
	if err != nil {
		t.Fatal(err)
	}
	if run2.Status != "blocked" || !strings.Contains(run2.Error, "after one retry") {
		t.Fatalf("expected a block after one retry, got %s %q", run2.Status, run2.Error)
	}
	if ch, _ := e2.db.Change(e2.ctx, e2.repo, 1); ch.State != "open" {
		t.Fatal("blocked change should stay open")
	}
	if e2.tip() != e2.c0 {
		t.Fatal("nothing should have landed")
	}
}

func TestReviewerReturningMalformedFindingsBlocksLanding(t *testing.T) {
	e := newEnv(t, 3)
	cmd := `echo 'not json at all' > "$ORE_FINDINGS_FILE"`
	if _, err := e.svc.SetRole(e.ctx, e.repo, RoleReviewer, RoleInput{AdapterCmd: &cmd}, e.human); err != nil {
		t.Fatal(err)
	}
	run, err := e.run(agent.Request{Prompt: "fix the greeting"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "blocked" || !strings.Contains(run.Error, "adversarial review failed") || !strings.Contains(run.Error, "malformed findings") {
		t.Fatalf("run: %s %q", run.Status, run.Error)
	}
	if e.tip() != e.c0 {
		t.Fatal("nothing should have landed")
	}
	if evs := e.events("io.ore.agent.review.failed"); len(evs) != 1 {
		t.Fatalf("review.failed events: %d", len(evs))
	}
	ch, _ := e.db.Change(e.ctx, e.repo, 1)
	if reviews, _ := e.rev.Reviews(e.ctx, e.repo, ch); len(reviews) != 0 {
		t.Fatal("a failed review posts nothing")
	}
	// a reviewer that writes nothing, and one whose findings break the spec
	for _, bad := range []string{`true`, `echo '[{"severity":"enormous","confidence":1,"path":"a","message":"m"}]' > "$ORE_FINDINGS_FILE"`} {
		e := newEnv(t, 3)
		b := bad
		e.svc.SetRole(e.ctx, e.repo, RoleReviewer, RoleInput{AdapterCmd: &b}, e.human)
		if run, _ := e.run(agent.Request{Prompt: "x"}); run.Status != "blocked" || !strings.Contains(run.Error, "adversarial review failed") {
			t.Fatalf("%q: %s %q", bad, run.Status, run.Error)
		}
	}
}

func TestAgentApprovalNeverCountsTowardRequiredApprovals(t *testing.T) {
	e := newEnv(t, 3)
	e.enableReviewer()
	if _, err := e.rev.SetPolicy(e.ctx, e.repo, review.Policy{RequiredApprovals: 1}, e.human); err != nil {
		t.Fatal(err)
	}
	run, err := e.run(agent.Request{Prompt: "fix the greeting"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "blocked" || !strings.Contains(run.Error, "policy") || !strings.Contains(run.Error, "needs 1 approval") {
		t.Fatalf("run: %s %q", run.Status, run.Error)
	}
	ch, _ := e.db.Change(e.ctx, e.repo, 1)
	reviews, _ := e.rev.Reviews(e.ctx, e.repo, ch)
	if len(reviews) != 1 || reviews[0].Verdict != "approve" || reviews[0].Lane != review.LaneAgent || reviews[0].Counts {
		t.Fatalf("the reviewer's approval must not count: %+v", reviews)
	}
	st, _ := e.rev.Approvals(e.ctx, e.repo, ch)
	if st.Landable || st.Approvals != 0 || len(st.Ignored) != 1 {
		t.Fatalf("approvals: %+v", st)
	}
	if e.tip() != e.c0 {
		t.Fatal("nothing should have landed")
	}
}

func TestEnvironmentThatForbidsAgentsBlocksTheRun(t *testing.T) {
	e := newEnv(t, 4)
	// a Dockerfile makes the loop deploy to production after landing
	tip := e.tip()
	c1, _ := gitx.CommitFiles(e.ctx, e.rp, tip, []gitx.FileEdit{{Path: "Dockerfile", Content: []byte("FROM scratch\n")}}, "dockerfile\n", gitx.Author{Name: "t", Email: "t@t"})
	gitx.UpdateRef(e.ctx, e.rp, "refs/heads/main", c1, tip)
	envs := &delivery.Service{DB: e.db, ReposDir: e.reposDir, Deployer: e.deployer}
	e.deployer.Environments = envs
	pol := delivery.DefaultPolicy(delivery.KindProduction)
	pol.AllowAgentDeploys = false
	pol.RequiredApprovals = 0
	env := delivery.Environment{Name: "production", Kind: delivery.KindProduction, Target: delivery.Target{Kind: delivery.TargetNone}, Policy: pol}
	if err := envs.CreateEnvironment(e.ctx, e.repo, &env, e.human); err != nil {
		t.Fatal(err)
	}
	run, err := e.run(agent.Request{Prompt: "fix the greeting"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "blocked" || run.Step != "deploy" || !strings.Contains(run.Error, "refused by policy") {
		t.Fatalf("run: %s %q at %s\n%s", run.Status, run.Error, run.Step, e.runLog(run))
	}
	ch, _ := e.db.Change(e.ctx, e.repo, 1)
	if ch.State != "merged" {
		t.Fatal("the change landed before the deploy was refused")
	}
	deps, _ := e.db.Deployments(e.ctx, e.repo, 10)
	found := false
	for _, d := range deps {
		if d.Environment == "production" && d.Status == delivery.StateRefused {
			found = true
		}
	}
	if !found {
		t.Fatalf("refused deployment should be recorded: %+v", deps)
	}
	// without a record the implicit environment lets the agent deploy (ore demo)
	e2 := newEnv(t, 4)
	tip2 := e2.tip()
	c2, _ := gitx.CommitFiles(e2.ctx, e2.rp, tip2, []gitx.FileEdit{{Path: "Dockerfile", Content: []byte("FROM scratch\n")}}, "dockerfile\n", gitx.Author{Name: "t", Email: "t@t"})
	gitx.UpdateRef(e2.ctx, e2.rp, "refs/heads/main", c2, tip2)
	run2 := e2.mustComplete(e2.run(agent.Request{Prompt: "fix the greeting"}))
	if run2.DeploymentID == nil {
		t.Fatal("implicit production deployment expected")
	}
}

func TestPausedRepositoryRefusesRunsAndStopsInFlightRuns(t *testing.T) {
	e := newEnv(t, 3)
	if err := e.svc.Pause(e.ctx, &e.repo, "incident 42", e.human); err != nil {
		t.Fatal(err)
	}
	_, err := e.run(agent.Request{Prompt: "fix"})
	if err == nil || !strings.Contains(err.Error(), "paused") || !strings.Contains(err.Error(), "incident 42") {
		t.Fatalf("expected refusal, got %v", err)
	}
	if len(e.runs()) != 0 {
		t.Fatal("a refused run must not be recorded")
	}
	if err := e.svc.Resume(e.ctx, &e.repo, e.human); err != nil {
		t.Fatal(err)
	}
	// pause while a run is in flight: it stops at the next step
	e.loop.Governor = hookGov{Service: e.svc, onGate: func(step string) {
		if step == "verify" {
			_ = e.svc.Pause(e.ctx, &e.repo, "kill switch", e.human)
		}
	}}
	run, err := e.run(agent.Request{Prompt: "fix"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "blocked" || run.Step != "verify" || !strings.Contains(run.Error, "paused") {
		t.Fatalf("in-flight run: %s %q at %s", run.Status, run.Error, run.Step)
	}
	// the instance switch covers every repository
	e.svc.Resume(e.ctx, &e.repo, e.human)
	e.loop.Governor = e.svc
	if err := e.svc.Pause(e.ctx, nil, "maintenance", e.human); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(agent.Request{Prompt: "fix"}); err == nil || !strings.Contains(err.Error(), "instance paused") {
		t.Fatalf("instance pause: %v", err)
	}
	paused, reason, _ := e.svc.InstancePaused(e.ctx)
	if !paused || reason != "maintenance" {
		t.Fatal("instance should be paused")
	}
	e.svc.Resume(e.ctx, nil, e.human)
	e.mustComplete(e.run(agent.Request{Prompt: "fix"}))
	if evs := e.events("io.ore.fleet.paused"); len(evs) != 3 {
		t.Fatalf("paused events: %d", len(evs))
	}
}

func TestLevel2OpensTheChangeAndStops(t *testing.T) {
	e := newEnv(t, 2)
	run, err := e.run(agent.Request{Prompt: "fix"})
	if err != nil || run.Status != "blocked" || !strings.Contains(run.Error, "human must land") {
		t.Fatalf("L2: %v %+v", err, run)
	}
	ch, _ := e.db.Change(e.ctx, e.repo, 1)
	if ch.State != "open" {
		t.Fatal("change should be open")
	}
	if landings, _ := e.rev.Landings(e.ctx, e.repo, ch); len(landings) != 0 {
		t.Fatal("no landing at L2")
	}
}

func TestRolesDefaultsAndOverrides(t *testing.T) {
	e := newEnv(t, 3)
	roles, err := e.svc.Roles(e.ctx, e.repo)
	if err != nil || len(roles) != 4 || roles[0].Name != RoleImplementer || !roles[0].Default || roles[0].AdapterCmd != fixAdapter || roles[1].Enabled {
		t.Fatalf("defaults: %v %+v", err, roles)
	}
	cmd, name, profile := `printf 'Hello, Ore!\n' > greeting.txt`, "scripted", "Always keep the greeting on one line."
	r, err := e.svc.SetRole(e.ctx, e.repo, RoleImplementer, RoleInput{AdapterCmd: &cmd, AdapterName: &name, PromptProfile: &profile, Budgets: &Budget{RunsPerDay: 5}}, e.human)
	if err != nil || r.Default || r.AdapterName != "scripted" {
		t.Fatalf("set role: %v %+v", err, r)
	}
	run := e.mustComplete(e.run(agent.Request{Prompt: "fix"}))
	if run.Adapter != "scripted" || !strings.Contains(e.runLog(run), profile) {
		t.Fatalf("role not applied: adapter %s\n%s", run.Adapter, e.runLog(run))
	}
	ch, _ := e.db.Change(e.ctx, e.repo, 1)
	if !strings.Contains(ch.Provenance, `"agent_id":"scripted"`) {
		t.Fatalf("provenance: %s", ch.Provenance)
	}
	budgets, _ := e.svc.Budgets(e.ctx, e.repo)
	found := false
	for _, b := range budgets {
		if b.Principal == "scripted" && b.Budget.RunsPerDay == 5 && b.Source == "fleet" {
			found = true
		}
	}
	if !found {
		t.Fatalf("role budget should become the principal budget: %+v", budgets)
	}
	if _, err := e.svc.SetRole(e.ctx, e.repo, "janitor", RoleInput{}, e.human); err == nil {
		t.Fatal("unknown role accepted")
	}
	if spec, ok := e.svc.Role(e.ctx, e.repo, RoleReviewer); ok || spec.AdapterCmd != "" {
		t.Fatal("the reviewer is off until configured")
	}
	_ = ledger.System
}
