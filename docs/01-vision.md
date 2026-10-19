# Vision: the open, autonomous software forge

> **Ore**: the raw material a forge turns into tools. Three letters, one syllable, pronounceable in every launch language; trademark search pending (see `docs/08-business-and-licensing.md` §1).

## The one-line promise

**Run your own GitHub, CI, deploy platform and agent fleet with one binary, and let the agents fix, verify and ship your software end to end while you set the policy.**

## The problem, stated plainly

Software delivery is becoming something agents do. At Anthropic, merges went from 2,500 a day to 30,000 a day in four months and the team replaced git itself because "coding is no longer the bottleneck; source control, builds and CI are" (`research/07`). Six to eighteen percent of public commits are already agent-authored. Yet the tools everyone else has were built for humans clicking: a forge that stops at the pull request and has lost 112 hours to major outages in a year; a CI system attacked through mutable tags and runner memory all through 2025 and 2026; deploy platforms that know nothing about who wrote the code or whether it should ship; agents bolted on with unpredictable token bills; and nothing that teaches the thirty-six million developers who start every year. The company that solved it (Anthropic) says its solution is not for general use and will not be released. The vendor that is building a forge for agents (Cursor) is building it closed.

## The thesis

Build the first open-source platform in which the **whole loop** — signal, plan, change, verify, land, deploy, observe, fix — runs end to end by agents under policy a human wrote once, on top of a source-control service designed for agents and still compatible with every git tool, with:

- **one identity** for humans, apps and agents, with capability-scoped tokens and budgets;
- **one policy model** that applies from push to production and defines how autonomous each repository and environment may be;
- **one ledger** that links every commit, change, build, artifact, attestation, deployment, incident and lesson by provenance, so audit, compliance, metrics and agent context are views;
- **one footprint** that scales from a single binary on a laptop to region-pinned cells, without a fork of the code.

And make it adoptable by any company in the world: self-host-first, licensed so no one can take it away, cheap enough for a two-person platform team to run, localized and teachable from inside the product, and usable on top of GitHub before anyone migrates.

## What "better" means, concretely

| Dimension | Where the field is (sourced in `research/`) | Ore target |
|---|---|---|
| Autonomy | Agent HQ assigns agents and stops at the PR; Origin is review-centric and closed; Scion runs 30k merges/day internally and is not released | The full loop at six autonomy levels, verification as infrastructure, open and vendor-neutral (`docs/12`) |
| Source control for agents | Clients clone; merges are client-side; refs race; Scion's answer is internal | Server-side commits and merges through the API, compare-and-swap refs, stable change-ids, git-compatible (`ADR 0014`) |
| Reliability | 257 incidents, 112 h major downtime in a year; a quarter of CI jobs late on a bad day | Published error budgets; CI start-latency SLO; git on object storage with linearly scaling replicas |
| Pricing trust | "$4 / $21 for the first 12 months*"; a retracted tax on customers' own runners; token-billing bill shocks | Every price published and grandfathered; BYO runners free in the licence; AI flat-rate with hard budgets and bring-your-own-model |
| The path after merge | No environments worth the name, no rollouts, no previews on the forge; deploy platforms with no provenance | Environments, previews per change, progressive rollouts, registry, secrets and flags built in; every deploy on the ledger with deploy-time policy |
| Supply chain | Mutable tags, secrets in runner memory, poisoned caches, provenance without policy | SHA-resolved references, secrets enclave with OIDC, caches as a trust boundary, SLSA source and build attestations plus deploy-time policy |
| Review | Branch-centric PRs; bot noise in the same thread; stacks only since mid-2026 | Change-id review with patch sets and interdiffs, stacks, three landing strategies, a separate lane for bots, structured provenance per hunk |
| Learning | Nothing inside the product; English-only UI | Lessons that run as pipelines on your own repo, learn-at-the-error, repo wiki, guardrailed tutor, credentials, ten languages, offline |
| Sovereignty | Four residency regions; proprietary; AI training questions unanswered | AGPL + DCO + trademark successor; residency by pinning; air-gap; no-training flag honored and audited |
| Footprint | 16 GB recommended for 1,000 users; no HA for the trusted alternative | One binary, SQLite to Postgres, cells for scale |

## The pillars

0. **The loop.** Signal → plan → change → verify → land → deploy → observe → fix, run by agents at a per-repository autonomy level from L0 (manual) to L5 (operate), with verification as infrastructure, budgets, adversarial review, structural provenance and kill switches (`docs/12-autonomy.md`).
1. **The ledger.** An append-only, content-addressed event log with typed entities and provenance edges; every page is a view over it; DORA, CRA reports, audit streaming, SBOM export and the agent context server are derived from it.
2. **Source control for agents, compatible with git.** Server-side commits, merges and rebases through the API; compare-and-swap refs; stable change-ids and patch sets; sparse and virtual working copies on an object-storage core; every git client keeps working (`ADR 0014`).
3. **Delivery built in.** Environments with N-of-M approvals and scoped secrets; imperative and reconciler paths; preview environments per change; staged rollouts with metric analysis; OpenFeature flags; an OCI registry; an OpenBao-compatible secrets API; OpenTofu; cost on the page.
4. **Pipelines you can trust.** Ore owns the control plane; execution is pluggable; GitHub Actions workflows run unmodified against a public conformance suite; the native model is a typed, content-addressed DAG billed by work; customer-owned runners are never charged.
5. **Agent-native and connector-native.** App capability sets; a native MCP server; a mandatory write gateway; CloudEvents webhooks with retry and replay; one standard implemented per category; WASM plugins.
6. **Teaching inside.** The learning layer from `docs/06`, so a company adopts without outside training and a newcomer anywhere learns in their language.
7. **Sovereign by design.** AGPL-3.0 server, Apache-2.0 embeddables, DCO, trademark successor, one binary to cells, air-gap, CRA features, published prices.

## Who it is for, in order

1. **Teams that want agents to ship, safely**: they install Ore in a minute, run the loop on their repositories (on GitHub through the adapter, or on Ore), and raise the autonomy level as the evidence allows.
2. **Public-sector and regulated organizations choosing a sovereign forge**: self-hosted, OSI-licensed, all features free, SSO without a tax, air-gap, residency.
3. **Open-source projects leaving GitHub** over reliability, AI bundling or sponsorship: high-fidelity migration, portable sponsorship, a no-training policy that is honored.
4. **Companies in the fast-growing developer populations**, where the learning layer and localization matter more than any feature.

## Non-goals for the first two years

A new version-control wire format (Ore stays a git remote); an IDE; foundation models; Jira-class portfolio management; FedRAMP before a federal pipeline; Kubernetes-only anything; physical-world products (robots, hiring, finance) as features rather than applications on the platform (`docs/13`).

## Principles that settle arguments

- **If an individual developer touches it, it is free and open.** Charge for assurance, hosting, compute and scale.
- **Autonomy is earned by evidence, not declared.** Levels go up with measured acceptance, fix and rollback rates, and down automatically.
- **Compatibility is the on-ramp; the native model is the destination.** Run GitHub workflows unmodified; keep git as the wire; compile and extend, never fork the ecosystem.
- **The ledger is the product.** A feature that does not write to or read from the ledger is probably a bolt-on.
- **Agents are delegates, not assignees.** A human owns every outcome; an agent has identity, budget and a write gateway.
- **Never meter a customer's own compute.**
- **Measure, do not trust self-report.** Time from signal to production, mean time to trust a change, failed-deployment recovery time, CI start latency. Publish the numbers.
- **Build on what exists when it is table stakes; build new when it is the differentiator.** git, Zoekt, OpenBao, OpenTofu, Sigstore, Firecracker are reused; the loop, the ledger, server-side source control, the pipeline control plane, delivery, the agent gateway and the learning engine are Ore's.
