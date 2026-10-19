# ADR 0008: Review unit is a Change with patch sets, interdiffs, stacks and a merge queue

**Status:** Proposed · **Date:** 2026-10-18

## Context
GitHub shipped stacked PRs natively in 2026, Cursor bought Graphite, Gerrit is adopting Jujutsu's change-id header, GitButler and Radicle store review metadata in-repo, and bot comments are now a third of all PR comments (research/06 §3). Branch-centric PRs lose comments across rebases and cannot express stacks or interdiffs.

## Decision
A Change has a stable id (Jujutsu `change-id` header → Gerrit trailer → assigned), ordered PatchSets, interdiffs between any two patch sets, comments anchored to (change, patch set, blob, range) that migrate across rebases, first-class Stacks with per-layer approval and whole-stack merge, and a merge queue that batches, tests and bisects. Bot comments live in a separate lane. Metadata is mirrored into `refs/meta/changes/<id>`.

## Consequences
The GitHub-compatible API maps Change ↔ pull request with documented loss; the Forgejo provider's PR model is read-through only; the UI must render stacks and interdiffs from day one.
