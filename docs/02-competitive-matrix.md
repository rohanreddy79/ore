# Competitive matrix (October 2026)

Facts in the incumbent columns come from `research/01`–`06` and were current as of 18 October 2026; "—" means not offered, "partial" means offered with documented gaps, "paid tier" names the lowest tier that includes it. The Ore column states the *target* and the phase in which it lands (see `docs/09-roadmap.md`: P1 = first 12 months, P2 = months 12–24, P3 = months 24+).

## Licence, governance, footprint

| | GitHub | GitLab | Forgejo | Gitea | Cursor Origin | Harness OSS | **Ore** |
|---|---|---|---|---|---|---|---|
| Licence | Proprietary | CE MIT + EE proprietary (open core) | GPLv3+ | MIT (+ CLA, CommitGo control) | Proprietary | Apache-2.0 (vendor-steered) | **AGPL-3.0 server; Apache-2.0 SDK/API/CLI/runner protocol** (P1) |
| Trademark holder | Microsoft | GitLab Inc. | Codeberg e.V. (non-profit) | CommitGo Inc. | Cursor | Harness | Company, with published policy and binding successor to a non-profit (P1) |
| Contributor agreement | n/a | DCO | DCO | CLA | n/a | CLA | **DCO** (P1) |
| Self-host | GHES (quarterly, paid) | Yes; 8 vCPU/16 GB for 1k users | Yes; ~1 GB | Yes; ~1 GB | — | Yes | Yes; one binary, SQLite→Postgres, ~1 GB idle (P1) |
| HA / multi-writer | Cloud only; GHES async replica, manual failover | Praefect cluster; Geo is not HA | Read-only replicas only | Same | Cloud | — | Object-storage-authoritative git with stateless replicas (P3); Postgres HA + outbox (P2) |
| Data residency | EU, AU, US, JP (EMU required) | Dedicated (quote) | Where you run it | Same | US cloud | Where you run it | Region-pinned orgs ("cells") on cloud; any region self-hosted (P2) |
| Air-gap / FIPS | GHES | Yes (Ultimate) | Yes | Yes | — | Yes | Yes, with docs and lessons bundled; FIPS build in Enterprise subscription (P2) |

## Pricing (per user per month unless noted)

| | GitHub | GitLab | Forgejo | Gitea | Cursor Origin | **Ore** |
|---|---|---|---|---|---|---|
| Free tier | 2,000 CI min; no SSO | 5 users/group (enforced Aug 2026); 400 min | Everything | Everything (CE) | — (paid plans only) | Everything in the open-source build; cloud free tier with verified-identity CI quota |
| Team / collaboration | $4 "first 12 months*" | $29 Premium | $0 | $0 | Bundled in Cursor plans | Cloud Team $4–8, no teaser pricing (P1) |
| Enterprise / SSO tier | from $21 "first 12 months*" (SAML/SCIM only here) | $29 Premium; Ultimate quote-only (~$99 formerly listed) | $0 | $9.50–19 (Enterprise) | n/a | Self-hosted Enterprise subscription $12–19 (supported builds, SLA, compliance pack); **SSO/SCIM/audit free for ≤25 users** (P1) |
| CI compute | $0.006/min 2-core Linux; macOS $0.062 | $10/1,000 min; macOS 6–12× | BYO | BYO | Partner (Depot) | ≤$0.004/vCPU-min; bill-by-work for cached tasks (P2); **BYO runners free in the licence** |
| Self-hosted runner fee | $0.002/min announced Dec 2025, postponed | — | — | — | — | **Never** (contractual) |
| AI | Copilot $10–100 + credits; token billing since Jun 2026 | Duo credits ($1 each, promotional allowance) | — | — | Cursor subscription | Flat per-seat with hard budgets; bring-your-own-model; local models (P1) |
| Marketplace take | 5% | n/a | n/a | n/a | n/a | 5% (P3) |

## Pipelines

| | GitHub Actions | GitLab CI | Forgejo Actions | Gitea Actions | Depot CI | **Ore** |
|---|---|---|---|---|---|---|
| Syntax | Actions YAML | GitLab YAML | Actions YAML, "not the same" | Actions-like | Actions YAML, no GitHub | Actions YAML (conformance-suite compatible) compiled to a typed DAG; SDKs; visual builder (P1/P2) |
| Control plane owner | GitHub (57 outages/yr) | GitLab | Forgejo | Gitea | Depot | **Ore** (P1) |
| Isolation | VM per job (hosted) | VM per job (hosted) | Docker/LXC/Podman/host | Docker | microVM | Firecracker per job (cloud, P2); gVisor/Docker fallback (self-host, P1) |
| Action pinning policy | Org policy, allow-list ≤1,000 | n/a | Advised | Advised | Mirrored | **SHA resolution at plan time, ancestry check, immutable tags, vendored registry** (P1) |
| Secrets model | Env vars in runner | Env vars | Env vars | Env vars | Enclave + OIDC | Enclave + per-job OIDC + egress policy (P1/P2) |
| Cache trust boundary | Shared (poisoned May 2026) | Shared | Shared | Shared | — | Content-addressed, write-scoped by trust level (P2) |
| Billing | Per minute | Per minute | n/a | n/a | Per second | **By work** (content-addressed task cache) (P2) |
| Pre-commit validation for agents | — | — | — | — | API | Named steps on uncommitted diffs via API and CLI (P2) |
| OpenTelemetry CI spans | — | — | — | — | — | Native `cicd.pipeline.*` spans (P1) |

## Delivery

| | GitHub | GitLab | Forgejo/Gitea | Vercel | Coolify/Dokploy | **Ore** |
|---|---|---|---|---|---|---|
| Environments | Yes; 1 of ≤6 approvals suffices; not on Free private | Premium: protected envs, multi-rule approvals | — | Implicit | Implicit | First-class: N-of-M approvals, wait timers, scoped secrets, custom rules via apps (P1) |
| Preview environments per PR | — (via partners) | Review apps (needs K8s or custom) | — | Yes (frontend) | Yes | Any stack via Compose or Helm, TTL and sleep, cost on the PR (P1) |
| Progressive rollouts | — | Canary via K8s | — | Rolling Releases (1 project on Pro) | Rollback only | Stages, sticky bucketing, metric analysis, instant rollback, for VMs and K8s (P2) |
| Delivery paths | — | GitOps agent | — | Proprietary | SSH/Docker | Imperative SSH/Docker **and** reconciler; export to Argo/Flux (P1/P2) |
| Feature flags | — | Yes | — | Yes | — | OpenFeature-compatible service (P2) |
| Registry | ghcr.io free "for now"; Packages metered | Bundled | 23 formats (Gitea) | — | — | OCI with referrers and attestations; free intra-platform transfer; package formats via Forgejo core (P1) |
| Secrets manager | — | Bundled CI vars | — | — | Vault provider | OpenBao-compatible API, dynamic secrets (P2) |
| Deployment ledger / DORA | Deployments API; no DORA | DORA Ultimate-only; blind to pull-based deploys | — | — | — | Every deploy on the ledger regardless of path; DORA incl. rework rate and failed-deploy recovery time, free (P1) |
| Cost attribution | — | — | — | Partial | — | CI, preview and runtime cost per PR, env and deploy by default (P2) |

## Review, agents, connectors

| | GitHub | GitLab | Forgejo | Cursor Origin | **Ore** |
|---|---|---|---|---|---|
| Stacked PRs | Public preview Jul 2026 | — | — | "Not yet" at beta | Native from P1 |
| Merge queue | Yes (reverted 2,092 PRs in Apr 2026 regression) | Merge trains (Premium) | — | "Not yet" | Native, with stack support (P1) |
| Change-id / patch sets / interdiff | — | — | — | — | Yes (P1/P2) |
| Review metadata portable | — | — | — | — | Mirrored to `refs/meta/` (P2) |
| Agent identity | Agent HQ (Pro+/Enterprise) | Duo composite identity | Bot accounts (Gitea 28) | Native | App identity + delegation + budgets + session states (P1) |
| Agent write path | Agentic Workflows "safe outputs" (tech preview) | — | — | ? | Safe-outputs gateway mandatory for agent sessions (P1) |
| Structured provenance | Trailers (`Co-authored-by`) | — | — | ? | Per-commit and per-hunk metadata (human/agent/model/session), filters, audit (P1) |
| MCP server | 22 toolsets, GA | GA Oct 2026 | — (community only) | Yes | Native, stateless 2026-07-28 spec, OAuth 2.1, read-mostly default (P1) |
| Webhooks | No auto-retry, 10 s deadline | Retries | Basic | ? | CloudEvents over Standard Webhooks, outbox, 72 h backoff, replay (P1) |
| Scanner ingest | SARIF | Own formats + SARIF | — | — | SARIF with fingerprints and PR annotations (P1) |
| Identity connectors | SAML/SCIM Enterprise only; audit streaming | SAML/SCIM Premium | SAML/OIDC free | ? | SAML/OIDC/SCIM free ≤25 users; audit streaming to S3/Splunk/Datadog/Event Hubs (P1/P2) |
| Plugin runtime | Apps + Actions | Integrations | Actions | API/MCP | Apps + WASM components (P3) |
| GitHub-compatible API | n/a | — | Subset | Mirroring | Subset sufficient for `gh`-style clients and existing bots (P1) |

## Autonomy and source control for agents

| | GitHub Agent HQ | Cursor Origin | Anthropic Scion + Antique (internal) | GitLab Duo Agent Platform | **Ore** |
|---|---|---|---|---|---|
| Availability | Pro+/Enterprise, GitHub only | Paid Cursor plans, beta | Internal only; "not a suitable replacement for git for general use" | Premium/Ultimate, credits | Open source, any model, self-host or cloud |
| Agent identity and governance | Agent identity, branch controls, enterprise policy | Native agents | IAM with per-directory permissions | Composite identity, most-restrictive role | Apps with capability sets, delegation, budgets, write gateway, provenance per hunk (v0–v1) |
| Loop coverage | Issue → PR | Code → review → merge | Change → merge → post-merge test → deploy gates; superfan watchers | Issue → MR → pipeline | Signal → plan → change → verify → land → deploy → observe → fix (v0 demo, v1 complete) |
| Autonomy levels | None (assign per task) | None | Implicit (optimistic landing, post-merge tests) | None | L0–L5 per repository and environment, evidence-gated, automatic demotion (v1) |
| Server-side commits and merges | — | — | Yes (changesets, no push) | — | Yes over git: `commit-tree`, `merge-tree`, CAS refs (v0) |
| Landing strategy | Merge queue | Merge queue "not yet" | No global lock; post-merge + pre-deploy testing; Claude resolves conflicts | Merge trains | Queue, land-when-green, or optimistic with auto-revert (v1) |
| Verification after landing | — | — | Yes | — | Yes at L4+, with staged rollouts and watchers (v1–v2) |
| Review of intent / semantic constraints | — | AI review | Antique: semantic approval constraints | Duo review | Cedar policy over change semantics; structured rationale and validation fields (v1) |
| Working copy | Clone | Clone | Optional, sparse, virtualized | Clone | API-first for jobs (v0); sparse server-side (v2); virtual filesystem client (v3) |
| Scale evidence | — | 22.6 commits/s launch demo | 30,000 merges/day; 25M checkouts/day; ~100 GB history | — | Published benchmarks from v1; object-storage core v2 |
| Deployment | — | — | Pre-deploy gates (internal) | Environments (Premium) | Environments, previews, rollouts, watchers built in (v0 preview, v1 full) |
| Model lock-in | Copilot + listed partners | Cursor's agents | Claude | Duo | None; bring-your-own-model, local models |

## Learning and localization

| | GitHub | GitLab | Forgejo | **Ore** |
|---|---|---|---|---|
| In-product learning | GitHub Skills (Actions-based, public repos, now Copilot-centric) | External LMS (EdCast) | — | Lessons as pipelines on private repos; learn-at-the-error (P1) |
| Repo explanation / wiki | Copilot chat | Duo | — | Auto-generated repo wiki, served over MCP (P2) |
| Tutor | Copilot | Duo | — | Guardrailed, bring-your-own-model, human-endorsed answers (P2) |
| Credentials | $99 exams | $150 exams | — | Open Badges 3.0, free, mapped to exam objectives (P2) |
| UI languages | English | 78 locales | Weblate community | Lessons in 10 languages first (P1), UI via Forgejo locales + Weblate (P1), RTL (P2) |
| Offline docs/lessons | — | Docs offline | Docs | Bundled in the binary (P2) |

## Where Ore deliberately does not compete

- Social scale (180M developers): Ore is not a social network; it federates outbound and syncs with GitHub.
- IDE and model: no editor, no foundation model. Cursor's Origin bundles both; Ore integrates with any of them through MCP.
- Jira-class planning: issues and projects for engineering, not enterprise portfolio management (GitLab sells that at +$15/seat).
- Kubernetes platform engineering portals: Ore exposes its graph over MCP rather than building a Backstage replacement.
