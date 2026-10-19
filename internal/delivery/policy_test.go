// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"testing"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

var (
	human = ledger.Actor{Kind: "human", ID: "alice"}
	bob   = ledger.Actor{Kind: "human", ID: "bob"}
	agent = ledger.Actor{Kind: "agent", ID: "claude-code"}
)

func prodEnv(p Policy) Environment {
	_ = p.Normalize()
	return Environment{ID: "env_1", Name: "production", Kind: KindProduction, Target: Target{Kind: TargetNone}, Policy: p}
}

func TestEvaluateStartsWhenEveryRulePasses(t *testing.T) {
	env := prodEnv(Policy{AllowedRefs: []string{"refs/heads/main"}, AllowAgentDeploys: true})
	d := Evaluate(env, PolicyRequest{Ref: "refs/heads/main", CommitOID: "abc", Actor: human})
	if d.Outcome != OutcomeStart || !d.Allowed() || len(d.Reasons) != 0 {
		t.Fatalf("%+v", d)
	}
	for _, c := range d.Checks {
		if !c.Pass {
			t.Fatalf("check %s failed: %s", c.Rule, c.Detail)
		}
	}
}

func TestEvaluateRefusesDisallowedRef(t *testing.T) {
	env := prodEnv(Policy{AllowedRefs: []string{"refs/heads/main", "refs/tags/v*"}})
	d := Evaluate(env, PolicyRequest{Ref: "refs/heads/feature", Actor: human})
	if d.Outcome != OutcomeRefuse || d.Allowed() {
		t.Fatalf("expected refuse, got %+v", d)
	}
	if Evaluate(env, PolicyRequest{Ref: "refs/tags/v1.2", Actor: human}).Outcome != OutcomeStart {
		t.Fatal("tag glob should match")
	}
	if Evaluate(env, PolicyRequest{Ref: "", Actor: human}).Outcome != OutcomeRefuse {
		t.Fatal("unknown ref must be refused when refs are restricted")
	}
	// bare branch names in patterns mean refs/heads/<name>
	if !RefAllowed([]string{"main"}, "refs/heads/main") || RefAllowed([]string{"main"}, "refs/heads/maintenance") {
		t.Fatal("bare pattern")
	}
}

func TestEvaluateRefusesAgentsWhenForbidden(t *testing.T) {
	env := prodEnv(Policy{AllowAgentDeploys: false})
	d := Evaluate(env, PolicyRequest{Ref: "refs/heads/main", Actor: agent})
	if d.Outcome != OutcomeRefuse || len(d.Reasons) != 1 {
		t.Fatalf("%+v", d)
	}
	env.Policy.AllowAgentDeploys = true
	if Evaluate(env, PolicyRequest{Ref: "refs/heads/main", Actor: agent}).Outcome != OutcomeStart {
		t.Fatal("agent should be allowed")
	}
}

func TestEvaluateRequiresGreenPipeline(t *testing.T) {
	env := prodEnv(Policy{RequireGreenPipeline: true})
	if Evaluate(env, PolicyRequest{Ref: "refs/heads/main", Actor: human}).Outcome != OutcomeRefuse {
		t.Fatal("unknown pipeline state must refuse")
	}
	red, green := false, true
	if Evaluate(env, PolicyRequest{Ref: "refs/heads/main", Actor: human, PipelineGreen: &red}).Outcome != OutcomeRefuse {
		t.Fatal("red must refuse")
	}
	if Evaluate(env, PolicyRequest{Ref: "refs/heads/main", Actor: human, PipelineGreen: &green}).Outcome != OutcomeStart {
		t.Fatal("green must start")
	}
}

func TestEvaluateApprovalsCountDistinctApproversAndRejectionWins(t *testing.T) {
	env := prodEnv(Policy{RequiredApprovals: 2, Approvers: []string{"alice", "bob"}})
	d := Evaluate(env, PolicyRequest{Ref: "refs/heads/main", Actor: human})
	if d.Outcome != OutcomeAwait || d.Approvals != 0 || d.RequiredApprovals != 2 {
		t.Fatalf("%+v", d)
	}
	twice := []Approval{{Approver: "alice", Decision: "approve"}, {Approver: "alice", Decision: "approve"}}
	d = Evaluate(env, PolicyRequest{Ref: "refs/heads/main", Actor: human, Approvals: twice})
	if d.Outcome != OutcomeAwait || d.Approvals != 1 {
		t.Fatalf("double approval must count once: %+v", d)
	}
	both := append(twice, Approval{Approver: "bob", Decision: "approve"})
	d = Evaluate(env, PolicyRequest{Ref: "refs/heads/main", Actor: human, Approvals: both})
	if d.Outcome != OutcomeStart || d.Approvals != 2 {
		t.Fatalf("%+v", d)
	}
	rejected := append(both, Approval{Approver: "bob", Decision: "reject"})
	if Evaluate(env, PolicyRequest{Ref: "refs/heads/main", Actor: human, Approvals: rejected}).Outcome != OutcomeRefuse {
		t.Fatal("a rejection must refuse")
	}
}

func TestEvaluateWaitTimer(t *testing.T) {
	env := prodEnv(Policy{WaitSeconds: 600})
	t0 := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	d := Evaluate(env, PolicyRequest{Ref: "refs/heads/main", Actor: human, RequestedAt: t0, Now: t0.Add(time.Minute)})
	if d.Outcome != OutcomeAwait || d.WaitUntil != "2026-10-18T12:10:00Z" {
		t.Fatalf("%+v", d)
	}
	d = Evaluate(env, PolicyRequest{Ref: "refs/heads/main", Actor: human, RequestedAt: t0, Now: t0.Add(11 * time.Minute)})
	if d.Outcome != OutcomeStart {
		t.Fatalf("after the timer: %+v", d)
	}
	// bypass (auto-rollback) waives approvals and the timer but not the hard rules
	env.Policy.RequiredApprovals = 1
	d = Evaluate(env, PolicyRequest{Ref: "refs/heads/main", Actor: ledger.System, RequestedAt: t0, Now: t0, Bypass: "auto-rollback"})
	if d.Outcome != OutcomeStart || d.Bypass == "" {
		t.Fatalf("bypass: %+v", d)
	}
	env.Policy.AllowedRefs = []string{"refs/heads/main"}
	if Evaluate(env, PolicyRequest{Ref: "refs/heads/other", Actor: ledger.System, Bypass: "auto-rollback"}).Outcome != OutcomeRefuse {
		t.Fatal("bypass must not waive allowed refs")
	}
}

func TestCanApprove(t *testing.T) {
	open := prodEnv(Policy{RequiredApprovals: 1})
	if !CanApprove(open, human) || CanApprove(open, agent) {
		t.Fatal("without approvers any non-agent may approve")
	}
	listed := prodEnv(Policy{RequiredApprovals: 1, Approvers: []string{"alice"}})
	if !CanApprove(listed, human) || CanApprove(listed, bob) || CanApprove(listed, agent) {
		t.Fatal("listed approvers only")
	}
}

func TestDefaultPolicyByKind(t *testing.T) {
	p := DefaultPolicy(KindProduction)
	if p.AllowAgentDeploys || !p.AutoRollback || p.HealthPath != "/" || p.HealthTimeoutSeconds != 60 {
		t.Fatalf("production defaults: %+v", p)
	}
	pv := DefaultPolicy(KindPreview)
	if !pv.AllowAgentDeploys || pv.AutoRollback || pv.PreviewTTLSeconds != 7200 {
		t.Fatalf("preview defaults: %+v", pv)
	}
	bad := Policy{HealthPath: "health"}
	if bad.Normalize() == nil {
		t.Fatal("health path must start with /")
	}
	var tgt Target
	if tgt.Validate() != nil || tgt.Kind != TargetLocalDocker {
		t.Fatal("empty target defaults to local-docker")
	}
	ssh := Target{Kind: TargetSSHDocker}
	if ssh.Validate() == nil {
		t.Fatal("ssh-docker needs a host")
	}
	ssh.Host = "h"
	if ssh.Validate() != nil || ssh.User != "root" || ssh.Port != 22 {
		t.Fatalf("ssh defaults: %+v", ssh)
	}
}
