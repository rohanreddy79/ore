// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import "testing"

func TestStateMachine(t *testing.T) {
	ok := [][2]string{
		{StateRequested, StateAwaitingApproval}, {StateRequested, StateStarted}, {StateRequested, StateRefused},
		{StateAwaitingApproval, StateStarted}, {StateAwaitingApproval, StateRejected},
		{StateStarted, StateSucceeded}, {StateStarted, StateFailed},
		{StateSucceeded, StateRolledBack}, {StateSucceeded, StateStopped}, {StateFailed, StateRolledBack},
	}
	for _, p := range ok {
		if !CanTransition(p[0], p[1]) {
			t.Errorf("%s -> %s should be allowed", p[0], p[1])
		}
	}
	bad := [][2]string{
		{StateRequested, StateSucceeded}, {StateAwaitingApproval, StateSucceeded}, {StateStarted, StateRolledBack},
		{StateFailed, StateSucceeded}, {StateRefused, StateStarted}, {StateRejected, StateStarted},
		{StateRolledBack, StateSucceeded}, {StateStopped, StateStarted}, {StateSucceeded, StateStarted},
	}
	for _, p := range bad {
		if CanTransition(p[0], p[1]) {
			t.Errorf("%s -> %s must be refused", p[0], p[1])
		}
	}
	for _, s := range []string{StateRefused, StateRejected, StateRolledBack, StateStopped} {
		if !Terminal(s) {
			t.Errorf("%s is terminal", s)
		}
	}
	if Terminal(StateSucceeded) || Terminal(StateFailed) {
		t.Error("succeeded and failed can still be rolled back or stopped")
	}
	if !InFlight(StateStarted) || !InFlight(StateAwaitingApproval) || InFlight(StateSucceeded) {
		t.Error("in-flight states")
	}
	err := &TransitionError{From: StateFailed, To: StateSucceeded}
	if err.Error() == "" {
		t.Error("transition error text")
	}
}
