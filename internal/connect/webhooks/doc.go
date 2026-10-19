// SPDX-License-Identifier: AGPL-3.0-only

// Package webhooks delivers the ledger's CloudEvents to HTTP endpoints over
// Standard Webhooks (docs/05 §3, ADR 0005) and exports the ledger as an
// audit log.
//
// Endpoints subscribe with filters (type prefixes, a subject glob, actor
// kinds, an expression). A dispatcher runs one queue per endpoint, reading
// the ledger from the endpoint's own cursor, so a slow receiver never
// delays another. Each delivery is signed (webhook-id, webhook-timestamp,
// webhook-signature with HMAC-SHA256, several signatures while a secret
// rotates), sent with a 30 s timeout, and retried with exponential backoff
// and jitter for 72 hours; 410 Gone disables the endpoint; Retry-After is
// honored. Payloads are thin by default (the envelope plus a signed URL to
// the full event) and full on request.
//
// The audit exporter appends every event (or a filtered subset) as
// newline-delimited CloudEvents JSON to daily files and, optionally, to an
// S3-compatible bucket signed with SigV4 from the standard library.
//
// The package registers as "connect.webhooks" from register.go, mounts its
// API under /api/v0/webhooks/ and attaches itself as Services["webhooks"].
package webhooks
