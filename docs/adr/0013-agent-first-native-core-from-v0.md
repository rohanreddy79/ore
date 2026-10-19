# ADR 0013: Agent-first native core from v0 (supersedes ADR 0001)

**Status:** Proposed · **Date:** 2026-10-18 · **Supersedes:** 0001

## Context
ADR 0001 chose Forgejo as a headless day-one core because the goal was GitHub feature parity for human collaboration, which is a multi-year build. The goal has changed: Ore is an autonomous software factory whose primary users are agents and the people who set policy for them. Agents need an API-first, server-side source control service (the properties Anthropic's Scion demonstrates, `research/07`), not a web UI for pull requests. A minimal git host is small: bare repositories served by `git http-backend`, server-side merges with `git merge-tree --write-tree`, compare-and-swap ref updates with `git update-ref`, all shipped with git itself. Running Forgejo as a second process would add a feature set Ore does not need first and an architecture (single writer, local disk, PR-centric) that Ore would replace anyway.

## Decision
Ore's repository core is native from v0: one Go binary, bare git repositories on disk (object storage in v2), git smart HTTP (SSH in v0.x), server-side commit/merge/ref operations through the API, issues, changes, pipelines, deployments and the ledger in SQLite or Postgres. The Forge Provider Interface stays, but its purpose changes: **adapters for adoption** (GitHub first, GitLab and Forgejo later) so that Ore's loop runs on repositories hosted elsewhere, not as a core. Human-collaboration features (rich diff UI, wiki, package formats, migrations) are added in the order the autonomy loop needs them.

## Alternatives
- Keep Forgejo as core (ADR 0001): fastest path to a GitHub-like UI; wrong shape for agent scale; two processes; features Ore must later remove.
- Scion-style non-git store from day one: the best end state for monorepo scale, but it breaks every existing tool; Ore keeps git as the wire format and adds server-side operations above it, moving storage to object storage in v2 without changing the API.

## Consequences
Ore owns a git host immediately (auth, hooks, policy on push, maintenance); the UI starts minimal and API/MCP-first; Forgejo users can still adopt Ore through an adapter; `docs/03-architecture.md` §1, §2.5 and §4 are revised.
