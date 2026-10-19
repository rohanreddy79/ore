// SPDX-License-Identifier: AGPL-3.0-only

// Package learn is the learning engine (docs/06, ADR 0010): lessons are
// versioned packages (a manifest with assert queries per step, localized
// step text, an optional template for a scratch repository) loaded from the
// embedded curriculum and ORE_LESSONS_DIR; a lesson run is a ledger entity
// that advances when the learner's real repository state satisfies the
// current step's assert, detected from ledger events by a worker; the
// learn-at-the-error catalog maps an error state (a ledger event) to the
// lesson that explains it. It wires itself in through internal/registry
// (register.go) as "learn" with routes under /api/v0/learn/.
package learn
