# Executive summary

**Ore** is an open-source platform for the whole software loop — signal, plan, change, verify, land, deploy, observe, fix — run end to end by agents under policy a human wrote once, on a source-control service designed for agents and compatible with every git tool. One binary hosts your repositories, runs your pipelines, deploys your software, governs your agents and teaches your people. It is built to be adopted by any company in the world: self-host-first, AGPL with a non-profit trademark successor, light enough for a two-person team, localized, and usable on top of GitHub before anyone migrates.

## Why now (from `research/`)

- Anthropic replaced git internally with Scion when merges went from 2,500 to 30,000 a day: server-side commits and merges, API-first, no clones for jobs, post-merge verification, agents resolving conflicts. They say it is not for general use and will not release it. (`research/07`)
- GitHub is losing on reliability (257 incidents, 112 hours of major outages in a year), pricing trust and UI performance, and stops at the pull request. Cursor's Origin is a closed forge for agents. GitLab has the shape and sells the useful parts by the seat. Forgejo has the trust and nothing above the repository.
- Agents author 6–18% of public commits; agent changes need 1.6× more follow-up fixes; CI volume is multiplying; 2025–26 saw a wave of CI supply-chain attacks defeated only by pinning and isolation; EU Cyber Resilience Act reporting is live.
- Agent-native repositories were the fastest-growing on GitHub in 2026 (openclaw +115k stars, superpowers ~280k, anthropics/skills ~180k). The window is open.

## The product

Pillar 0 is **the loop**, with six autonomy levels per repository and environment (L0 manual to L5 operate), verification as infrastructure (previews, staged rollouts, watchers, adversarial review), budgets, structural provenance and kill switches (`docs/12`). Around it: **the ledger** (every object linked by provenance; audit, DORA, compliance and agent context are views), **source control for agents** (server-side commits and merges, compare-and-swap refs, change-ids, object-storage core, git-compatible; `ADR 0014`), **delivery built in**, **pipelines you can trust** (own control plane, Actions-compatible, content-addressed, BYO runners free), **agent-native and connector-native** (app capability sets, native MCP, write gateway, CloudEvents webhooks), **teaching inside**, and **sovereign by design**.

## What is being built now

v0 in this repository: one Go binary with git hosting, issues, changes with server-side merge, container pipelines, container previews and production deployments with rollback, the agent loop with pluggable adapters (Claude Code by default), a CloudEvents ledger, REST API and CLI, and `ore demo`, the deterministic three-minute demonstration. The versioned roadmap to v3 is in `docs/09`.

## The key decisions (`docs/adr/`)

Agent-first native core from v0 with adapters for adoption (0013, superseding 0001); Scion-class server-side source control over git (0014); autonomy levels with verification as infrastructure (0015); AGPL-3.0 server, Apache-2.0 embeddables, DCO, trademark successor (0002); own the pipeline control plane, BYO runners free (0003); Actions-compatible on-ramp, typed IR (0004); the ledger as the spine (0005); two SQL targets, tenancy key everywhere (0006); Go now, Rust for the data plane later (0007); change-centric review (0008); agents as delegates with a write gateway (0009); learning on the pipeline primitive (0010); Cedar policy (0011); OpenBao-compatible secrets with OIDC (0012).

## Business and scale

Everything a developer touches is free and open. Revenue: seats, compute, dedicated cells, an autonomy add-on, public-sector contracts, a marketplace; an illustrative path to $1B is in `docs/08` §7. Scale: one codebase from laptop to region-pinned cells; the network layer (public provenance graph, cross-organization agents, federated changes) connects companies without merging them; robotics, data, hiring and the "organization brain" are horizons delivered as mechanisms on the platform, not promises (`docs/13`). The path to 200k stars is a launch sequence, not a wish (`docs/14`).

## Decisions the founder must make now

1. Confirm the name **Ore** (free handles: `orevcs`, `ore-vcs`, `useore`; domains `oreforge.dev`, `getore.dev`, `orevcs.dev`, `ore.tools`; one crypto-token collision to weigh).
2. Default autonomy level for new repositories: L2 (agents author, humans land) as designed, or higher.
3. Default agent adapter and keys for the hosted demo (Claude Code is the local default).
4. Path A (1–3 people) or Path B (funded team) for v0.x–v1.
5. Ratify the ADRs.

## Where to look

`research/00` for the evidence; `research/07` for Scion; `docs/12` for autonomy; `docs/01` for the thesis; `docs/02` for positioning; `docs/03`–`07` for designs; `docs/08` for money; `docs/09` for the roadmap; `docs/13`–`14` for scale, horizons and stars; `spec/` for contracts; `cmd/` and `internal/` for the code.
