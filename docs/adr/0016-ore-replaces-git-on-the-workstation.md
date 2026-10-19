# ADR 0016: Ore replaces git and GitHub for the developer; git stays inside

**Status:** Accepted · **Date:** 2026-10-19 · Amends ADR 0014 §6

## Context
The purpose of the project, stated by its owner on 2026-10-19: developers and agents stop using git and GitHub and use Ore instead. ADR 0014 kept git as the wire format and said Ore "adds, it does not replace". Today the server already owns every operation (server-side commits, merges, patch sets, landing, pipelines, agents, deployments), but a human developer still reaches a repository with a git client over HTTP or SSH, and the vocabulary a developer meets (branches, remotes, rebases, staging) is git's.

## Decision
1. **The product surface is Ore's, not git's.** The nouns are repository, change, patch set, intent (issue), run, environment; the verbs are open, save, sync, land, revert. Ore's own interfaces (web app, MCP, API, the `ore` client) expose no branch names, remotes, rebases, merge commits or staging areas to a human or an agent. Where a mechanism exists underneath (a source ref for a change), it is an implementation detail with a stable Ore id in front of it.
2. **A workstation client, `ore`, replaces the git client (v0.2).** `ore open org/repo` materializes the repository's active area (sparse by default, chosen server-side from `.ore/pipeline.yaml` paths); `ore save` records the working tree's edits as a patch set through the API, with no local history; `ore sync` refreshes the materialized tree; `ore land` asks for landing under the repository's autonomy level; `ore status` shows the ledger's view of the work. Working copies are caches; the ledger is the source of truth. Edits made offline are queued and reconciled on the server.
3. **Git stays as the internal object format and as an optional bridge.** `git clone` and `git push` keep working for migration and for existing tools; the GitHub importer and mirror remain for moving teams over; an organization can switch the git transport off. Hosting or using Ore never requires GitHub.
4. **The engine behind the bridge is replaceable** (ADR 0014 §5, docs/03 §6): object-storage-backed, chunked, content-addressed storage behind the same API, so removing git from the inside is a later, invisible change rather than a product change.

## Consequences
- Roadmap (docs/09): the `ore` client and the per-organization "git transport off" switch become v0.2; docs/10 gains a "last day of git" checklist for a team.
- The scale programme (docs/scale) stays valid: it measures the server operations the client will call through the same API, so server-side commit latency, sparse materialization time and landing throughput are the client's numbers too.
- New measurements: `ore open` to an editable tree on a 100M-line repository (target under 5 s, sparse), `ore save` round trip, `ore land` latency at each autonomy level.
- Documentation and the web app are to be read with this lens: any place that teaches git vocabulary is a defect to file.
