// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

// Decision outcomes.
const (
	OutcomeStart  = "start"  // every rule passed: the deployment may start now
	OutcomeAwait  = "await"  // approvals or the wait timer are outstanding
	OutcomeRefuse = "refuse" // a hard rule failed: the deployment is refused
)

// Check is one evaluated rule.
type Check struct {
	Rule   string `json:"rule"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// Decision is the recorded result of evaluating an environment's policy for a
// deployment request; it is attached to every transition event so an auditor
// can replay it (docs/04 §B.7).
type Decision struct {
	Outcome           string   `json:"outcome"`
	Reasons           []string `json:"reasons,omitempty"`
	Checks            []Check  `json:"checks"`
	RequiredApprovals int      `json:"required_approvals"`
	Approvals         int      `json:"approvals"`
	Approvers         []string `json:"approvers,omitempty"`
	WaitUntil         string   `json:"wait_until,omitempty"`
	EvaluatedAt       string   `json:"evaluated_at"`
	// Bypass names a reason the rules were waived (auto-rollback).
	Bypass string `json:"bypass,omitempty"`
}

// Allowed reports whether the deployment may proceed (now or after approval).
func (d Decision) Allowed() bool { return d.Outcome != OutcomeRefuse }

// PolicyRequest is the evidence a policy decision is made from.
type PolicyRequest struct {
	Ref       string
	CommitOID string
	Actor     ledger.Actor
	// PipelineGreen is nil when not looked up, otherwise whether a succeeded
	// pipeline run exists for the commit.
	PipelineGreen *bool
	Approvals     []Approval
	RequestedAt   time.Time
	Now           time.Time
	// Bypass waives approvals and the wait timer (automatic rollback to a
	// release that already passed policy); recorded on the decision.
	Bypass string
}

// Evaluate applies an environment's policy to a request. It is a pure function.
func Evaluate(env Environment, req PolicyRequest) Decision {
	p := env.Policy
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	d := Decision{Outcome: OutcomeStart, RequiredApprovals: p.RequiredApprovals, EvaluatedAt: now.UTC().Format(time.RFC3339), Bypass: req.Bypass}
	fail := func(rule, detail string) {
		d.Checks = append(d.Checks, Check{Rule: rule, Pass: false, Detail: detail})
		d.Reasons = append(d.Reasons, detail)
		d.Outcome = OutcomeRefuse
	}
	pass := func(rule, detail string) { d.Checks = append(d.Checks, Check{Rule: rule, Pass: true, Detail: detail}) }

	// 1. source ref
	if len(p.AllowedRefs) == 0 {
		pass("allowed_refs", "any ref")
	} else if RefAllowed(p.AllowedRefs, req.Ref) {
		pass("allowed_refs", req.Ref)
	} else {
		fail("allowed_refs", fmt.Sprintf("ref %q is not in the environment's allowed refs %v", orUnknown(req.Ref), p.AllowedRefs))
	}
	// 2. actor kind
	if req.Actor.Kind == "agent" && !p.AllowAgentDeploys {
		fail("allow_agent_deploys", fmt.Sprintf("agent %s may not deploy to %s", req.Actor.ID, env.Name))
	} else {
		pass("allow_agent_deploys", req.Actor.Kind+"/"+req.Actor.ID)
	}
	// 3. green pipeline
	if p.RequireGreenPipeline {
		if req.PipelineGreen != nil && *req.PipelineGreen {
			pass("require_green_pipeline", "a succeeded pipeline run exists for "+short(req.CommitOID))
		} else {
			fail("require_green_pipeline", "no succeeded pipeline run for commit "+short(req.CommitOID))
		}
	} else {
		pass("require_green_pipeline", "not required")
	}
	// 4. rejection ends it
	for _, a := range req.Approvals {
		if a.Decision == "reject" {
			fail("approvals", "rejected by "+a.Approver)
			break
		}
	}
	if d.Outcome == OutcomeRefuse {
		return d
	}
	// 5. approvals (distinct approvers)
	seen := map[string]bool{}
	for _, a := range req.Approvals {
		if a.Decision == "approve" && !seen[a.Approver] {
			seen[a.Approver] = true
			d.Approvers = append(d.Approvers, a.Approver)
		}
	}
	d.Approvals = len(seen)
	switch {
	case req.Bypass != "":
		pass("approvals", "waived: "+req.Bypass)
	case d.Approvals >= p.RequiredApprovals:
		pass("approvals", fmt.Sprintf("%d of %d", d.Approvals, p.RequiredApprovals))
	default:
		d.Checks = append(d.Checks, Check{Rule: "approvals", Pass: false, Detail: fmt.Sprintf("%d of %d approvals", d.Approvals, p.RequiredApprovals)})
		d.Reasons = append(d.Reasons, fmt.Sprintf("needs %d more approval(s)", p.RequiredApprovals-d.Approvals))
		d.Outcome = OutcomeAwait
	}
	// 6. wait timer
	if p.WaitSeconds > 0 && req.Bypass == "" {
		start := req.RequestedAt
		if start.IsZero() {
			start = now
		}
		until := start.Add(time.Duration(p.WaitSeconds) * time.Second)
		d.WaitUntil = until.UTC().Format(time.RFC3339)
		if now.Before(until) {
			d.Checks = append(d.Checks, Check{Rule: "wait", Pass: false, Detail: "wait timer until " + d.WaitUntil})
			d.Reasons = append(d.Reasons, "wait timer until "+d.WaitUntil)
			if d.Outcome == OutcomeStart {
				d.Outcome = OutcomeAwait
			}
		} else {
			pass("wait", "elapsed")
		}
	} else {
		pass("wait", "none")
	}
	return d
}

// RefAllowed matches a full ref name against glob patterns ("refs/heads/main",
// "refs/tags/v*", "refs/heads/release/*"). Bare branch names in patterns are
// taken as refs/heads/<name>.
func RefAllowed(patterns []string, ref string) bool {
	if ref == "" {
		return false
	}
	for _, pat := range patterns {
		if !strings.HasPrefix(pat, "refs/") && !strings.HasPrefix(pat, "*") {
			pat = "refs/heads/" + pat
		}
		if ok, _ := path.Match(pat, ref); ok {
			return true
		}
		if pat == ref {
			return true
		}
	}
	return false
}

// CanApprove reports whether actor may approve or reject deployments into env:
// a listed approver, or any non-agent actor when no approvers are listed.
func CanApprove(env Environment, actor ledger.Actor) bool {
	if len(env.Policy.Approvers) == 0 {
		return actor.Kind != "agent"
	}
	for _, a := range env.Policy.Approvers {
		if a == actor.ID {
			return true
		}
	}
	return false
}

func short(oid string) string {
	if len(oid) > 12 {
		return oid[:12]
	}
	return oid
}

func orUnknown(s string) string {
	if s == "" {
		return "(no ref points at this commit)"
	}
	return s
}
