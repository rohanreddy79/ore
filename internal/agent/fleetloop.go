// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/review"
)

// This file holds the governed parts of the loop: per-step gates, the role
// lookup, the adversarial review with one retry, landing by autonomy level
// through the review service's strategies, and deploy refusals. agent.go
// calls into it; nothing here runs when Loop.Governor is nil except landing
// by level, which is the v1 behaviour of the loop (ADR 0014 §4, ADR 0015).

// gate asks the governor whether the run may proceed with step. On refusal
// the run is finished as blocked and false is returned.
func (l *Loop) gate(ctx context.Context, repo ledger.Repo, run *ledger.AgentRun, principal ledger.Actor, step string, lg *stepLog, actor ledger.Actor) bool {
	if l.Governor == nil {
		return true
	}
	if err := l.Governor.Gate(ctx, repo, run, principal, step); err != nil {
		l.finish(ctx, repo, run, "blocked", err.Error(), lg, actor)
		return false
	}
	return true
}

// roleSpec returns the repository's configuration for a role, if a governor
// provides one.
func (l *Loop) roleSpec(ctx context.Context, repo ledger.Repo, name string) (RoleSpec, bool) {
	if l.Governor == nil {
		return RoleSpec{}, false
	}
	return l.Governor.Role(ctx, repo, name)
}

// StrategyForLevel maps an autonomy level to a landing strategy (docs/12 §2):
// L3 lands when green, L4 and above land optimistically with post-merge
// verification and automatic revert. Below L3 agents do not land.
func StrategyForLevel(level int) string {
	switch {
	case level >= 4:
		return review.StrategyOptimistic
	default:
		return review.StrategyLandWhenGreen
	}
}

// land lands a verified change with the strategy the repository's level
// calls for, through the review service (what the fleet publishes as
// Services["review.landing"]). The verification run already done on the head
// is reused by land_when_green, so the head is not verified twice.
func (l *Loop) land(ctx context.Context, repo ledger.Repo, ch ledger.Change, verified *ledger.PipelineRun, actor ledger.Actor) (review.Landing, error) {
	if l.Review.Verifier == nil && l.Runner != nil {
		l.Review.Verifier = l.Runner
	}
	req := review.LandRequest{Strategy: StrategyForLevel(repo.AutonomyLevel)}
	if verified != nil {
		req.VerificationRunID = verified.ID
	}
	return l.Review.Land(ctx, repo, ch, req, actor)
}

// landOutcome turns a landing error into the run's final status and message.
func landOutcome(err error) (status, msg string) {
	var le *review.LandingError
	var pe *review.PolicyError
	switch {
	case errors.As(err, &pe):
		return "blocked", "land: " + err.Error()
	case errors.As(err, &le) && errors.Is(err, review.ErrReverted):
		msg := fmt.Sprintf("land: change #%d landed optimistically as %s but post-merge verification failed; reverted as %s", le.Landing.ChangeNumber, short(le.Landing.MergeOID), short(le.Landing.RevertOID))
		if le.Landing.IssueNumber != nil {
			msg += fmt.Sprintf("; issue #%d opened as the fix signal", *le.Landing.IssueNumber)
		}
		return "failed", msg
	case errors.As(err, &le) && errors.Is(err, review.ErrVerificationFailed):
		return "failed", "land: " + le.Err.Error()
	default:
		return "failed", "land: " + err.Error()
	}
}

func short(p *string) string {
	if p == nil {
		return "?"
	}
	if len(*p) > 8 {
		return (*p)[:8]
	}
	return *p
}

// deployOutcome classifies a deployment error or parked deployment: refused
// by the environment's policy or awaiting approval → blocked; otherwise failed.
func deployOutcome(dep ledger.Deployment, err error) (status, msg string, done bool) {
	var re *delivery.RefusedError
	switch {
	case errors.As(err, &re):
		return "blocked", "deploy: " + err.Error(), true
	case err != nil:
		return "failed", "deploy: " + err.Error(), true
	case dep.Status == delivery.StateAwaitingApproval:
		return "blocked", fmt.Sprintf("deploy: deployment %s to %s awaits approval under the environment's policy", dep.ID, dep.Environment), true
	}
	return "", "", false
}

// adversarialReview runs the governor's reviewer on the verified change. A
// blocking verdict is a fix signal to the implementer: the adapter runs once
// more in the same workspace with the findings, the branch is pushed (a new
// patch set), verified and reviewed again. A second block, a failed review
// or a fix that changes nothing ends the run as blocked. It returns the
// change and verification run to land.
func (l *Loop) adversarialReview(ctx context.Context, repo ledger.Repo, run *ledger.AgentRun, ch ledger.Change, prun ledger.PipelineRun, work, rp, branch, prompt, verifyCmd string, req Request, lg *stepLog, actor, agentActor ledger.Actor) (ledger.Change, ledger.PipelineRun, bool) {
	out, err := l.Governor.Review(ctx, repo, run, ch, prompt, lg.f)
	if err != nil {
		l.finish(ctx, repo, run, "failed", "adversarial review: "+err.Error(), lg, actor)
		return ch, prun, false
	}
	if !out.Ran {
		lg.printf("[review] no reviewer configured for this repository")
		return ch, prun, true
	}
	if out.Failed {
		l.finish(ctx, repo, run, "blocked", "adversarial review failed; landing blocked: "+out.Summary, lg, actor)
		return ch, prun, false
	}
	if !out.Blocked {
		lg.printf("[review] passed: %s", out.Summary)
		return ch, prun, true
	}
	lg.printf("[review] blocked: %s", out.Summary)
	lg.printf("[review] fix signal to the implementer (one retry)")
	findings, _ := json.Marshal(out.Findings)
	fixPrompt := prompt + "\n\nThe adversarial reviewer blocked this change. Fix these findings, then stop:\n" + out.Summary + "\n"
	if err := l.amend(ctx, work, rp, branch, fixPrompt, string(findings), verifyCmd, repo, req, lg, agentActor); err != nil {
		l.finish(ctx, repo, run, "blocked", "adversarial review blocked landing and the fix attempt failed: "+err.Error()+"; findings: "+out.Summary, lg, actor)
		return ch, prun, false
	}
	ch2, err := l.Review.Open(ctx, repo, review.OpenRequest{SourceRef: branch}, agentActor)
	if err != nil {
		l.finish(ctx, repo, run, "failed", "reopen after fix: "+err.Error(), lg, actor)
		return ch, prun, false
	}
	lg.printf("[review] fix pushed: change #%d head %s", ch2.Number, ch2.HeadOID[:8])
	prun2, err := l.Runner.Run(ctx, repo, ch2.HeadOID, &ch2.ID, agentActor)
	if err != nil {
		l.finish(ctx, repo, run, "failed", "pipeline after fix: "+err.Error(), lg, actor)
		return ch2, prun2, false
	}
	run.PipelineRunID = &prun2.ID
	lg.printf("[verify] pipeline %s: %s", prun2.ID, prun2.Status)
	if prun2.Status != "succeeded" {
		l.finish(ctx, repo, run, "failed", "verification failed after the review fix: pipeline "+prun2.Status, lg, actor)
		return ch2, prun2, false
	}
	out2, err := l.Governor.Review(ctx, repo, run, ch2, fixPrompt, lg.f)
	if err != nil {
		l.finish(ctx, repo, run, "failed", "adversarial review: "+err.Error(), lg, actor)
		return ch2, prun2, false
	}
	if out2.Blocked || out2.Failed {
		l.finish(ctx, repo, run, "blocked", "adversarial review blocked landing after one retry: "+out2.Summary, lg, actor)
		return ch2, prun2, false
	}
	lg.printf("[review] passed after fix: %s", out2.Summary)
	return ch2, prun2, true
}

// amend runs the adapter again in the workspace with the findings as the
// fix signal, commits and pushes the branch. It fails when the adapter fails
// or changes nothing.
func (l *Loop) amend(ctx context.Context, work, rp, branch, prompt, findings, verifyCmd string, repo ledger.Repo, req Request, lg *stepLog, agentActor ledger.Actor) error {
	if err := l.runAdapter(ctx, work, prompt, verifyCmd, repo, branch, req, lg, "ORE_FINDINGS="+findings, "ORE_RETRY=1"); err != nil {
		return fmt.Errorf("adapter: %w", err)
	}
	if _, err := gitx.Run(ctx, work, nil, "add", "-A"); err != nil {
		return err
	}
	status, _ := gitx.Run(ctx, work, nil, "status", "--porcelain")
	if strings.TrimSpace(status) == "" {
		return errors.New("the fix attempt made no changes")
	}
	msg := "Address adversarial review findings\n\nOre-Agent: " + agentActor.ID + "\n"
	env := []string{"GIT_AUTHOR_NAME=Ore Agent", "GIT_AUTHOR_EMAIL=agent@ore.invalid", "GIT_COMMITTER_NAME=Ore Agent", "GIT_COMMITTER_EMAIL=agent@ore.invalid"}
	if _, err := gitx.RunStdin(ctx, work, env, []byte(msg), "commit", "--quiet", "-F", "-"); err != nil {
		return err
	}
	before, _ := gitx.Refs(ctx, rp)
	if _, err := gitx.Run(ctx, work, nil, "push", "--quiet", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return fmt.Errorf("push: %w", err)
	}
	after, _ := gitx.Refs(ctx, rp)
	for _, u := range gitx.DiffRefs(before, after) {
		_ = l.DB.RecordRefUpdate(ctx, repo, u.Ref, u.Old, u.New, "agent", agentActor)
	}
	return nil
}
