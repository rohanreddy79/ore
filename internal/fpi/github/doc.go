// SPDX-License-Identifier: AGPL-3.0-only

// Package github is the GitHub adapter behind the Forge Provider Interface
// (docs/03 §2.5): Ore's loop on repositories that stay hosted on GitHub.
//
// An Ore repository is linked to owner/name (POST
// /api/v0/repos/{org}/{name}/github/link); the link mirrors every branch and
// tag into Ore's bare repository through a refs/github/* tracking namespace
// and records each ref update on the ledger with via "github". GitHub
// webhooks (POST /hooks/github, HMAC-SHA256 per link) keep the mirror
// current and translate issues and pull requests into Ore issues and
// changes with their GitHub numbers preserved; pipeline results on mirrored
// commits are posted back as commit statuses; a reconciliation worker
// re-fetches linked repositories in case a delivery was missed. The adapter
// registers as "fpi.github" through internal/registry (register.go) and
// talks to GitHub's REST API with the standard library only.
package github
