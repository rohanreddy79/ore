// SPDX-License-Identifier: AGPL-3.0-only

package review

import (
	"context"
	"errors"
	"fmt"

	"github.com/orevcs/ore/internal/ledger"
)

// Review lanes: who wrote a comment or review. Human approvals count toward
// the repository's policy; bot and agent lanes render separately and never
// count unless the policy allows agent approvals (ADR 0008, spec/safe-outputs).
const (
	LaneHuman = "human"
	LaneBot   = "bot"
	LaneAgent = "agent"
)

// LaneFor derives the lane from an actor: humans → human, agents → agent,
// apps and the system → bot.
func LaneFor(a ledger.Actor) string {
	switch a.Kind {
	case "human":
		return LaneHuman
	case "agent":
		return LaneAgent
	default:
		return LaneBot
	}
}

// Landing strategies (ADR 0014 §4, ADR 0015). Queue is the v0 behaviour.
const (
	StrategyQueue         = "queue"
	StrategyLandWhenGreen = "land_when_green"
	StrategyOptimistic    = "optimistic"
)

// Landing statuses.
const (
	LandingRequested    = "requested"
	LandingVerifying    = "verifying"
	LandingLanded       = "landed"
	LandingFailed       = "failed"
	LandingReverted     = "reverted"
	LandingRevertFailed = "revert_failed"
)

// StateReverted is the change state after an optimistic landing was reverted.
// The next patch set reopens the change.
const StateReverted = "reverted"

// Verifier runs the repository's verification on a commit and reports the
// run. *pipelines.Runner satisfies it; tests use the host executor.
type Verifier interface {
	Run(ctx context.Context, repo ledger.Repo, commitOID string, changeID *string, actor ledger.Actor) (ledger.PipelineRun, error)
}

// Errors the HTTP layer maps to status codes.
var (
	// ErrBadRequest marks validation failures (400).
	ErrBadRequest = errors.New("bad request")
	// ErrSourceGone means the change's source branch was deleted (409).
	ErrSourceGone = errors.New("source branch no longer exists")
	// ErrNotOpen means the change is merged, closed or reverted (409).
	ErrNotOpen = errors.New("change is not open")
	// ErrVerificationFailed means land_when_green saw a red head (409).
	ErrVerificationFailed = errors.New("verification failed")
	// ErrReverted means an optimistic landing was merged and then reverted (409).
	ErrReverted = errors.New("verification failed after landing; the merge was reverted")
	// ErrNoVerifier means a verifying strategy was requested without a runner.
	ErrNoVerifier = errors.New("no verifier configured: this strategy needs the pipeline runner")
	// ErrStackMoved means a stacked branch moved while the stack was landing.
	ErrStackMoved = errors.New("branch moved while the stack was landing")
)

// PolicyError reports why the approval policy refuses a landing.
type PolicyError struct{ Status ApprovalStatus }

func (e *PolicyError) Error() string { return "policy: " + e.Status.Reason }

// LandingError carries the landing record of an attempt that did not stick.
type LandingError struct {
	Landing Landing
	Err     error
}

func (e *LandingError) Error() string {
	return fmt.Sprintf("landing %s (%s): %v", e.Landing.ID, e.Landing.Strategy, e.Err)
}

// Unwrap exposes the cause for errors.Is.
func (e *LandingError) Unwrap() error { return e.Err }

// StackError carries the stack state after a landing aborted.
type StackError struct {
	Stack    Stack
	Landings []Landing
	Err      error
}

func (e *StackError) Error() string { return "stack " + e.Stack.ID + ": " + e.Err.Error() }

// Unwrap exposes the cause for errors.Is.
func (e *StackError) Unwrap() error { return e.Err }

// PatchSet is one revision of a change: the (base, head) pair that was
// reviewable at a point in time. Patch sets survive rebases because the
// base is recorded per patch set.
type PatchSet struct {
	ID        string       `json:"id"`
	OrgID     string       `json:"org_id"`
	RepoID    string       `json:"repo_id"`
	ChangeID  string       `json:"change_id"`
	Number    int          `json:"number"`
	BaseOID   string       `json:"base_oid"`
	HeadOID   string       `json:"head_oid"`
	PushedBy  ledger.Actor `json:"pushed_by"`
	CreatedAt string       `json:"created_at"`
}

// Interdiff is the difference between two patch sets of the same change.
type Interdiff struct {
	From      PatchSet `json:"from"`
	To        PatchSet `json:"to"`
	Identical bool     `json:"identical"`
	// Rebased is true when the two patch sets have different bases; the diff
	// then factors the base change out (three-way) when it can.
	Rebased bool `json:"rebased"`
	// Diff is a unified diff of what the patch changed between the two patch
	// sets; empty when they are identical.
	Diff string `json:"diff"`
	// RangeDiff is `git range-diff` output when requested.
	RangeDiff string `json:"range_diff,omitempty"`
	// Note explains a fallback (e.g. the rebase could not be factored out).
	Note string `json:"note,omitempty"`
}

// Anchor is where a comment sits on the latest patch set.
type Anchor struct {
	PatchSet  int `json:"patch_set"`
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
}

// Comment is anchored to (patch set, path, line range) and re-anchored to
// the latest patch set when its lines still exist; otherwise it is outdated.
type Comment struct {
	ID        string       `json:"id"`
	OrgID     string       `json:"org_id"`
	RepoID    string       `json:"repo_id"`
	ChangeID  string       `json:"change_id"`
	PatchSet  int          `json:"patch_set"`
	Path      string       `json:"path,omitempty"`
	StartLine *int         `json:"start_line,omitempty"`
	EndLine   *int         `json:"end_line,omitempty"`
	Body      string       `json:"body"`
	Author    ledger.Actor `json:"author"`
	Lane      string       `json:"lane"`
	Resolved  bool         `json:"resolved"`
	CreatedAt string       `json:"created_at"`
	// Anchor is the comment's position on the latest patch set (nil for
	// change-level comments or when outdated).
	Anchor   *Anchor `json:"anchor,omitempty"`
	Outdated bool    `json:"outdated"`
}

// CommentRequest posts a comment. PatchSet 0 means the latest.
type CommentRequest struct {
	PatchSet  int    `json:"patch_set"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Body      string `json:"body"`
	// Lane may lower the lane (a human token posting a bot's comment); it can
	// never raise a non-human actor to the human lane.
	Lane string `json:"lane"`
}

// LineRange is a 1-based inclusive line range.
type LineRange struct {
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
}

// Finding is the spec/safe-outputs Finding shape: structured review output.
type Finding struct {
	Severity     string     `json:"severity"`
	Confidence   float64    `json:"confidence"`
	Category     string     `json:"category,omitempty"`
	Path         string     `json:"path"`
	Range        *LineRange `json:"range,omitempty"`
	Message      string     `json:"message"`
	SuggestedFix string     `json:"suggested_fix,omitempty"`
	Fingerprint  string     `json:"fingerprint,omitempty"`
}

var severities = map[string]bool{"info": true, "low": true, "medium": true, "high": true, "critical": true}

// Validate checks a finding against the spec.
func (f Finding) Validate() error {
	if !severities[f.Severity] {
		return fmt.Errorf("%w: finding severity %q must be info|low|medium|high|critical", ErrBadRequest, f.Severity)
	}
	if f.Confidence < 0 || f.Confidence > 1 {
		return fmt.Errorf("%w: finding confidence must be in [0,1]", ErrBadRequest)
	}
	if f.Path == "" || f.Message == "" {
		return fmt.Errorf("%w: finding path and message are required", ErrBadRequest)
	}
	return nil
}

// Review is a verdict with structured findings on one patch set.
type Review struct {
	ID        string       `json:"id"`
	OrgID     string       `json:"org_id"`
	RepoID    string       `json:"repo_id"`
	ChangeID  string       `json:"change_id"`
	PatchSet  int          `json:"patch_set"`
	Reviewer  ledger.Actor `json:"reviewer"`
	Lane      string       `json:"lane"`
	Verdict   string       `json:"verdict"` // comment | approve | request_changes
	Findings  []Finding    `json:"findings"`
	Body      string       `json:"body,omitempty"`
	CreatedAt string       `json:"created_at"`
	// Counts reports whether this review weighs on the policy right now: an
	// approve or request_changes on the latest patch set in a counting lane.
	Counts bool `json:"counts"`
}

// ReviewRequest submits a review. PatchSet 0 means the latest.
type ReviewRequest struct {
	Verdict  string    `json:"verdict"`
	Findings []Finding `json:"findings"`
	Body     string    `json:"body"`
	Lane     string    `json:"lane"`
	PatchSet int       `json:"patch_set"`
}

// Policy is the per-repository review policy.
type Policy struct {
	// RequiredApprovals is how many counting approvals the latest patch set
	// needs before it may land. Default 0 (the v0 behaviour; `ore demo`).
	RequiredApprovals int `json:"required_approvals"`
	// AllowAgentApprovals lets agent and bot approvals count. Default false.
	AllowAgentApprovals bool `json:"allow_agent_approvals"`
	// DefaultStrategy is used when a landing request names none.
	DefaultStrategy string `json:"default_strategy"`
}

// IgnoredApproval is an approval that did not count, and why.
type IgnoredApproval struct {
	Reviewer ledger.Actor `json:"reviewer"`
	Reason   string       `json:"reason"`
}

// ApprovalStatus is the policy evaluated against the latest patch set.
type ApprovalStatus struct {
	PatchSet           int               `json:"patch_set"`
	Required           int               `json:"required"`
	Approvals          int               `json:"approvals"`
	Approvers          []ledger.Actor    `json:"approvers"`
	Ignored            []IgnoredApproval `json:"ignored,omitempty"`
	ChangesRequestedBy []ledger.Actor    `json:"changes_requested_by,omitempty"`
	Landable           bool              `json:"landable"`
	Reason             string            `json:"reason,omitempty"`
}

// Stack is an ordered list of changes where each change targets the previous
// one's source branch; it lands bottom-up.
type Stack struct {
	ID                 string          `json:"id"`
	OrgID              string          `json:"org_id"`
	RepoID             string          `json:"repo_id"`
	TargetRef          string          `json:"target_ref"`
	ChangeIDs          []string        `json:"change_ids"`
	State              string          `json:"state"` // open | landing | landed | aborted
	FailedChangeID     *string         `json:"failed_change_id,omitempty"`
	FailedChangeNumber int             `json:"failed_change_number,omitempty"`
	Error              string          `json:"error,omitempty"`
	CreatedBy          ledger.Actor    `json:"created_by"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
	Changes            []ledger.Change `json:"changes,omitempty"`
}

// StackRequest opens a stack from branches ordered bottom (closest to the
// target) to top.
type StackRequest struct {
	Branches  []string `json:"branches"`
	TargetRef string   `json:"target_ref"`
}

// Landing is one attempt to land a change with a strategy.
type Landing struct {
	ID                string       `json:"id"`
	OrgID             string       `json:"org_id"`
	RepoID            string       `json:"repo_id"`
	ChangeID          string       `json:"change_id"`
	ChangeNumber      int          `json:"change_number"`
	PatchSet          int          `json:"patch_set"`
	Strategy          string       `json:"strategy"`
	Status            string       `json:"status"`
	MergeOID          *string      `json:"merge_oid,omitempty"`
	RevertOID         *string      `json:"revert_oid,omitempty"`
	VerificationRunID *string      `json:"verification_run_id,omitempty"`
	IssueNumber       *int         `json:"issue_number,omitempty"`
	Error             string       `json:"error,omitempty"`
	Actor             ledger.Actor `json:"actor"`
	CreatedAt         string       `json:"created_at"`
	FinishedAt        *string      `json:"finished_at,omitempty"`
}

// LandRequest picks a strategy; empty means the repository's default.
type LandRequest struct {
	Strategy string `json:"strategy"`
	// VerificationRunID names a succeeded pipeline run on the change's current
	// head that land_when_green may reuse instead of verifying the head again
	// (the agent loop verifies before it reviews and lands). Ignored unless the
	// run exists, succeeded and ran on exactly the head being landed.
	VerificationRunID string `json:"verification_run_id,omitempty"`
}
