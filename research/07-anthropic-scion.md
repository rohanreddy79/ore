# Anthropic's Scion: what they built, and what "beating it" means for an open product

Source: "Redesigning source control at Anthropic", Anthropic Engineering, published 13 October 2026, updated 15 October 2026 (https://www.anthropic.com/engineering/redesigning-source-control). Secondary coverage: xenospectrum.com, cellcog.ai, stackademic.com (same week). Everything below not marked as inference is from the primary post.

## What Scion is

- An **internal** version control system that replaced git for Anthropic's monorepo; built and migrated in about five months by a team of seven, together with **Antique** (code review), code search, a source-control IAM system, and "half a dozen related systems".
- **Not public, not open source, and by Anthropic's own statement "by no means a suitable replacement for git for general use"**; the concepts "really only make sense for truly enormous monorepos". Anthropic shares the thinking so git users can adopt parts of it.
- Prior art credited: Google's Piper and Meta's Sapling (Mononoke server, Bonsai data model).

## The problem it solves

- Merge volume went from ~2,500 changes/day (May 2026) to >30,000/day (September 2026): "coding is no longer the bottleneck; source control, builds and CI are."
- Nearly all clients are "agents and jobs that are online by definition", so git's offline-first full clones were "actively undermining" scale: shallow/sparse/partial clones, several hundred internal mirrors, drift from tip, hard blame/diff on partial clones, packfile maintenance.
- Scale numbers: main-branch history ~100 GB; compressed working copy >25 GB; ~25 million checkouts/day for CI; naive git fetch traffic would exceed 600 PB/day.

## Design principles (verbatim where quoted)

1. "Clients are always online." Fetch on demand; no full copies.
2. Client-server asymmetry: the **server** does history, blame, commits, merges, rebases; the client keeps a bounded object cache, lays out files, watches the filesystem.
3. "A comprehensive source control service can power many novel applications." Jobs often need no local repository at all (API-first).
4. "Everything must be horizontally scalable." No full copy of the repo exists anywhere, even on the server.
5. "Commits must be cheap, incremental, and lock-free." Commit cost scales with the change, not the repo.
6. "The working copy is optional, and sparse by design." Virtualized checkouts materialize only what is read.

## Concrete mechanics

- **Changeset** = the unit of work, an entry in a write-ahead log, created server-side and visible everywhere immediately; stores the list of files changed relative to the first parent with content-addressed pointers to compressed blobs (not a tree pointer like a git commit).
- **Derived data** (blame, path history, commit-graph skip lists) computed recursively from parents, eagerly or lazily; invisible to clients.
- **Refs** are compare-and-swap updates on a single Spanner row, separate from changeset creation; refs are discouraged except main and release branches.
- **No global merge-queue lock**: post-merge and pre-deploy automated testing instead; Claude typically resolves the resulting conflicts.
- **Antique** review expresses **semantic approval constraints** (comment-only changes vs sensitive logic changes); reviewers examine *intent*, not every line.
- **IAM**: repository access control and per-directory permissions.
- **Storage**: Spanner for metadata and refs; blob/cloud storage for content; `scn` CLI, per-node `scion-daemon`, regional `scion-cache` streaming trees and blobs from memory, authoritative `scion-server`; tree- and blob-based fetching (no packfiles), resumable, >99% cache hit at each tier; strict tier isolation.
- **Agents in the loop**: APIs for automated bisect and revert pipelines; "superfan" Claude agents monitor individual components via telemetry and logs; adversarial review, hermetic simulations, fuzzing, rate limiting, path-based file search, copy-detection indices.
- Results: checkouts in seconds instead of minutes (~two orders of magnitude); infrastructure CPU and memory ~200× smaller than the git-mirror setup; +25% CI CPU capacity; a Claude rewrite of the ingestion pipeline used 20× less memory and ran 4× faster.

## What this means for Ore

Scion is the strongest evidence yet for Ore's thesis: at agent scale, source control becomes a **service with an API**, commits and merges move **server-side**, review becomes **intent and policy**, and verification moves **after merge and before deploy**. Anthropic built it for one monorepo and says it is not for general use. The opening is to deliver the same properties to everyone, in the open, without asking anyone to abandon git:

| Scion property | Ore's open, git-compatible equivalent | Phase |
|---|---|---|
| Server-side commits, merges, rebases; nothing to push | Server-side merge via `git merge-tree --write-tree` + CAS ref update (no worktree); API-created commits via `commit-tree`; agents never need a clone to land a change | v0 |
| Changesets as WAL entries, visible everywhere at once | The ledger: every ref update and change is an append-only event; changes have stable ids and patch sets | v0 |
| Refs as CAS on a single row | Ref updates are compare-and-swap through the API and the git transport, recorded with the policy decision | v0 |
| Clients always online, API-first, no local repo for jobs | REST/MCP/CLI for every operation; pipelines and agents operate on server-side trees; `git archive`-style materialization only when a tool needs files | v0–v1 |
| Sparse, virtualized working copies | Partial-clone and sparse-checkout profiles served by the native core; a FUSE/virtual filesystem client later | v2–v3 |
| No full copy anywhere; horizontally scalable | Object-storage-authoritative git with manifests and stateless replicas (the GitLab/Cursor 2026 design), SHA-256 and reftable | v2–v3 |
| Derived data (blame, path history) computed incrementally | Blame/path-history indexes maintained from the ledger stream; code search over blobs | v1–v2 |
| No merge-queue lock; post-merge + pre-deploy testing; agents fix conflicts | Autonomy levels: optimistic merge with post-merge verification and automatic revert/fix at the higher levels; a conventional merge queue at the lower levels | v1 |
| Antique: semantic approval constraints, review of intent | Cedar policies over change *semantics* (which paths, which kinds of change) and a review model that records intent, rationale and validation as structured fields | v1 |
| Per-directory permissions | Cedar policies compiled from CODEOWNERS-style rules, enforced on push and merge | v1 |
| Superfan agents, automated bisect and revert | Ore's watchers: agents subscribed to the ledger and telemetry per component, with budgets and the write gateway; `ore bisect` and automatic revert as pipeline tasks | v1 |
| Internal, one monorepo, Spanner | Open source, any repository size, SQLite to Postgres to cells | — |

What Ore will **not** copy: abandoning git compatibility (Ore stays a git remote so every existing tool works), and depending on Spanner.

Numbers to beat in public benchmarks when the native core lands: merges per day per repository (Scion: 30,000+ across the monorepo), checkout latency (seconds), infrastructure cost per merge, and conflict-resolution success rate of agents.
