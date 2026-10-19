// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"errors"
	"fmt"
)

// Deployment states. A deployment is requested → awaiting_approval (when the
// policy needs approvals or a wait timer) → started → succeeded | failed →
// optionally rolled_back (replaced by a rollback deployment) or stopped.
// refused (policy) and rejected (an approver) are terminal.
const (
	StateRequested        = "requested"
	StateAwaitingApproval = "awaiting_approval"
	StateStarted          = "started"
	StateSucceeded        = "succeeded"
	StateFailed           = "failed"
	StateRolledBack       = "rolled_back"
	StateStopped          = "stopped"
	StateRefused          = "refused"
	StateRejected         = "rejected"
)

var transitions = map[string][]string{
	StateRequested:        {StateAwaitingApproval, StateStarted, StateRefused},
	StateAwaitingApproval: {StateStarted, StateRejected, StateRefused},
	StateStarted:          {StateSucceeded, StateFailed},
	StateSucceeded:        {StateRolledBack, StateStopped},
	StateFailed:           {StateRolledBack},
}

// CanTransition reports whether a deployment may move from one state to another.
func CanTransition(from, to string) bool {
	for _, t := range transitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// Terminal reports whether no further transition is possible from the state.
func Terminal(state string) bool { return len(transitions[state]) == 0 }

// Live reports whether the deployment's container is (or should be) running.
func Live(state string) bool { return state == StateSucceeded }

// InFlight reports whether a deployment in this state blocks another request
// for the same environment.
func InFlight(state string) bool { return state == StateAwaitingApproval || state == StateStarted }

// ErrInvalid marks a malformed request (HTTP 400).
var ErrInvalid = errors.New("invalid")

// ConflictError marks a request that conflicts with the deployment's state
// (HTTP 409): a second deployment into a busy environment, approving a rejected
// deployment, rolling back with nothing to roll back to.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// RefusedError marks a deployment the policy refused (HTTP 403); the decision
// is recorded on the ledger.
type RefusedError struct {
	DeploymentID string
	Decision     Decision
}

func (e *RefusedError) Error() string {
	if len(e.Decision.Reasons) == 0 {
		return "refused by policy"
	}
	return "refused by policy: " + e.Decision.Reasons[0]
}

// TransitionError marks an illegal state change.
type TransitionError struct{ From, To string }

func (e *TransitionError) Error() string {
	return fmt.Sprintf("deployment cannot go from %s to %s", e.From, e.To)
}
