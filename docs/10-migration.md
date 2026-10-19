# Migration: high fidelity in, no lock-in out

Every importer on the market drops something that matters: CI secrets and history, LFS binaries, approvals, rulesets, stars, webhook secrets, issue numbers (Zig restarted at #30000). GitLab-to-GitHub tooling only arrived in August 2026 and skips pipelines. CI incompatibility was the named blocker for the Dutch government's move to Forgejo. Migration is therefore a product, with a fidelity score, a dry run, continuity during the switch, and a documented way back. Evidence is in `research/01` §6.

## 1. Sources

| Source | Phase | Transport |
|---|---|---|
| GitHub.com and GitHub Enterprise Server | P1 | GitHub App (preferred) or token; GraphQL/REST; git over HTTPS; LFS batch API |
| GitLab.com and self-managed | P2 | Token; REST/GraphQL; project export archives for bulk |
| Gitea / Forgejo / Codeberg | P1 | API and git; near-lossless because the Forgejo core shares the model |
| Bitbucket Cloud and Data Center | P2 | API and git |
| Azure DevOps | P2 | API and git; Boards best-effort |
| Any git remote | P1 | Mirror only |

## 2. What moves, and how faithfully

| Object | Fidelity | Method |
|---|---|---|
| Git history, tags, branches | Exact | Mirror fetch, all refs, signatures preserved |
| LFS objects | Exact | Batch API download → Ore LFS store, verified by OID |
| Issues | Numbers preserved | Ore reserves the source number range per repository so `#1234` stays `#1234`; new issues continue after the highest imported number |
| Pull requests → Changes | High | Each PR becomes a Change with one patch set per observed head (from the PR's commit history and force-push events), comments anchored to blobs and lines; where the source diff no longer exists, comments attach to the nearest commit with a "migrated anchor" marker rather than being dropped |
| Reviews, approvals, requested reviewers | High | Mapped to Ore reviews; approval state recorded on the ledger with original timestamps |
| Labels, milestones, projects | High / best-effort | Labels and milestones exact; projects mapped to Ore projects with a report of unmapped fields |
| Releases and assets | Exact | Assets to object storage; release notes preserved; tags linked |
| Wiki | Exact | Git-backed wiki cloned |
| Discussions | High | Mapped to Q&A/discussions with categories and accepted answers |
| Webhooks | Recreated | Endpoints recreated with **new secrets** (the old ones cannot be read); a report lists endpoints for the owner to rotate; Standard Webhooks headers explained |
| Actions workflows | Compiled with report | Every workflow compiled to the Pipeline IR before migration; the **conformance report per repository** lists unsupported features and suggested fixes; nothing is migrated blind |
| Actions secrets and variables | Via secure hand-off | Secrets cannot be read from the source; Ore provides a one-time workflow that runs *in the source CI*, authenticates to Ore with the source's OIDC token, and writes the secrets into Ore's enclave for the mapped environment; audited on both sides |
| Environments and protection rules | High | Mapped to Ore Environments and Cedar policy; unsupported rules reported |
| Branch protection / rulesets / CODEOWNERS | High | Compiled to Cedar policy; a diff shows what each rule became |
| Packages and container images | Exact for OCI; high for others | OCI re-pushed by digest; package formats re-published through the Forgejo core where supported, otherwise listed |
| Code scanning alerts | Regenerated | Scanners re-run on import; historical dismissals imported via SARIF fingerprints where available |
| Stars, watchers, forks | With consent | Stars imported as "imported stars" attributed to mapped users who have claimed their identity; fork relationships preserved when both sides migrate |
| Identities | Claimable | Source authors become mannequins; users claim them by proving the source account (OAuth) or an email; organization admins can map in bulk from SCIM |
| Sponsorship | Linked | Funding links imported; Ore's portable sponsorship page created |

## 3. The process

1. **Plan**: `ore migrate plan <source>` or the web wizard produces a **fidelity report** per repository: what moves exactly, what degrades, what cannot move, the Actions conformance result, size and time estimates, and the identity map. Nothing changes yet.
2. **Dry run**: the migration runs into a staging organization on Ore; the team reviews Changes, pipelines (they actually run), environments and policy diffs; the fidelity score is recomputed.
3. **Sync period**: Ore mirrors the source continuously (git, issues, PRs, releases) so the team can trial Ore with live data; for GitHub, the Ore GitHub App keeps checks, comments and deployments flowing *both ways* during this period.
4. **Cut-over**: a chosen moment; the source is set to read-only or archived; Ore takes writes; a **redirect map** (old URL → new URL for repositories, issues, PRs, releases) is published; the Ore GitHub App can pin a redirect notice on the archived source repository and auto-reply on new issues opened there.
5. **Verify**: `ore migrate verify` re-checks every ref, LFS object, issue and release against the source and files a signed report on the ledger.
6. **Keep a mirror**: optional continued push-mirroring back to the source for discoverability, with the source clearly marked as a mirror.

## 4. Continuity features

- Issue and Change numbering continuity (number-range reservation).
- URL compatibility routes (`/owner/repo/pull/123` resolves to the Change).
- GitHub-compatible API and webhook payloads so existing bots keep working.
- Actions workflows run unmodified where the conformance report says so; divergences are explained inline in the pipeline view with a "Learn why" link.
- Identity claims after the fact: history re-attributes when a mannequin is claimed.

## 5. The way out (the no-lock-in promise)

`ore export` produces, for an organization or a repository: all git data (including `refs/meta/` review metadata and provenance), the ledger as CloudEvents JSONL, issues/changes/reviews as portable collaborative-object JSON, releases and assets, SBOMs and attestations, policies as Cedar, lessons progress and credentials. Documented importers exist for Forgejo and GitLab, and the GitHub-compatible API makes GitHub's own importer work. Export is free on every tier, always.

## 6. Organization-scale migration

Batch mode for hundreds of repositories: parallel workers, rate-limit awareness, resumability, per-repository fidelity scores rolled up, a progress dashboard, identity mapping from SCIM, and a public-sector-oriented runbook (change-management template, communication plan, training assignment through the learning layer so every member completes the "how we ship here" lesson before cut-over).
