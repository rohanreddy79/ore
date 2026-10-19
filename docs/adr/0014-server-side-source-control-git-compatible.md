# ADR 0014: Scion-class server-side source control, git-compatible

**Status:** Proposed · **Date:** 2026-10-18

## Context
Anthropic replaced git internally with Scion (`research/07`): commits, merges and rebases happen on the server; changesets are write-ahead-log entries visible everywhere at once; refs are compare-and-swap rows; clients are always online and rarely need a working copy; verification moves after merge and before deploy; agents resolve conflicts. Anthropic states Scion is not for general use and will not be released. The 30,000-merges-per-day figure shows what agent-scale delivery looks like.

## Decision
Ore provides the same properties over git:
1. **Server-side operations through the API**: create commits from file edits (`commit-tree` over the current tree, no clone), merge changes (`merge-tree --write-tree` then compare-and-swap `update-ref`), rebase and cherry-pick server-side, all recorded on the ledger with the actor and policy decision.
2. **Ref updates are compare-and-swap** everywhere (API and git transport), never last-writer-wins.
3. **Changes have stable ids and patch sets**; the ledger is the write-ahead log of record.
4. **Verification after landing is a supported mode** (autonomy L4+): optimistic merge, immediate post-merge verification, automatic revert and fix signals.
5. **Sparse and virtual working copies** arrive with the object-storage core (v2) and a virtual filesystem client (v3); until then, partial clone and sparse checkout are supported server-side.
6. **Git remains the wire format**; every git client and tool keeps working. Ore adds, it does not replace.

## Consequences
The v0 API already exposes server-side commit and merge; the merge queue (ADR 0008) becomes one landing strategy among three (queue, land-when-green, optimistic); benchmarks to publish: merges/day/repo, time from change to production, conflict auto-resolution rate, infrastructure cost per merge.
