// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"errors"
	"io"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/review"
)

// Governor governs the loop (docs/12 §3, ADR 0015): admission and per-step
// gates (budgets, kill switches), per-repository roles, and the adversarial
// reviewer that runs after verification and before landing. The fleet
// extension (internal/agent/fleet) implements it; a nil Governor is the v0
// loop. The loop never imports the fleet, so the two can evolve apart.
type Governor interface {
	// Admit decides whether a run may start at all (a paused repository or
	// instance, a run budget). Start and RunSync return its error unchanged.
	Admit(ctx context.Context, repo ledger.Repo, principal ledger.Actor) error
	// Gate runs before every step of a run. A non-nil error stops the run
	// with status "blocked" and the error text; the governor records why.
	Gate(ctx context.Context, repo ledger.Repo, run *ledger.AgentRun, principal ledger.Actor, step string) error
	// Role returns the repository's configuration for a role (implementer,
	// reviewer, ...), if one is set.
	Role(ctx context.Context, repo ledger.Repo, name string) (RoleSpec, bool)
	// Review runs the adversarial reviewer on a verified change. The log
	// writer is the agent run's log.
	Review(ctx context.Context, repo ledger.Repo, run *ledger.AgentRun, ch ledger.Change, plan string, log io.Writer) (ReviewOutcome, error)
}

// RoleSpec is what the loop needs from a role: the adapter to run, how to
// label its provenance, and a prompt profile prepended to the task.
type RoleSpec struct {
	Name          string `json:"name"`
	AdapterCmd    string `json:"adapter_cmd"`
	AdapterName   string `json:"adapter_name"`
	PromptProfile string `json:"prompt_profile"`
}

// ReviewOutcome is the adversarial reviewer's verdict on one patch set.
type ReviewOutcome struct {
	// Ran is false when no reviewer is configured for the repository.
	Ran bool `json:"ran"`
	// Blocked means landing must not proceed with this patch set.
	Blocked bool `json:"blocked"`
	// Failed means the reviewer itself failed (malformed findings, adapter
	// error); landing is blocked and the implementer is not retried.
	Failed   bool             `json:"failed"`
	Summary  string           `json:"summary"`
	ReviewID string           `json:"review_id,omitempty"`
	Findings []review.Finding `json:"findings"`
}

// ErrRefused marks a run the governor refused to admit (paused, over budget).
var ErrRefused = errors.New("agent run refused")
