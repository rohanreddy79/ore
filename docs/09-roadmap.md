# Roadmap: v0 to the whole product

Versions are capability gates. v0 is being built in this repository now; everything after it is ordered so that each release is runnable, demoable and useful on its own. Team assumptions and the small-team path are in `docs/08` §4.

## v0 — The loop on one machine (done 2026-10-18)

One Go binary (`ore`), SQLite, bare git repositories, Docker for isolation. Implemented in `cmd/ore` and `internal/`; `ore demo` runs it end to end (`deploy/solo/DEMO.md`).

- Git hosting over smart HTTP with token auth; protected default branch; ref updates recorded as CloudEvents on the ledger.
- Organizations, repositories, issues.
- **Changes**: created from a branch with a stable change-id; diff; **server-side merge** (`merge-tree` + compare-and-swap ref update, no working copy); provenance recorded.
- **Pipelines**: `.ore/pipeline.yaml` (tasks with image and command) run in containers from a server-side archive; logs streamed and stored; results on the change.
- **Deployments**: build the repository's Dockerfile and run it as a preview or production container with a URL; stop, redeploy, roll back to the previous release; recorded on the ledger.
- **Agent loop**: `ore agent run --issue N` runs an adapter (Claude Code by default; any command) in a workspace, commits with provenance, opens a change, verifies, lands at the repository's autonomy level, deploys, closes the issue; every step a ledger event.
- REST API, CLI, `ore demo` (the three-minute demo, deterministic with a scripted adapter), unit and integration tests.

**Gate (met):** `ore demo` passes on a machine with Docker in ~15 s; every package has tests, including a real push over HTTP and the full loop. Known v0 limits: single admin token (no users yet), HTTP only (no SSH), no secrets, pipelines have network access, host executor is for trusted use only, no UI for diffs beyond a basic view.

## v0.2 — Working without git (ADR 0016; client landed 2026-10-19)

The developer stops using git: the `ore` binary is the workstation client. `ore open org/repo` materializes the repository's active area (the paths the pipeline declares) from a streamed server-side archive, sparse by default so a 100k-file repository opens in seconds; `ore save` records the working tree's edits as a server-side commit and a patch set of a change, with no local history and an offline queue; `ore sync` refreshes the tree and merges the default branch into the change on the server, writing conflicts side by side (or inline with markers); `ore land` asks for landing under the repository's autonomy level; `ore status` shows the ledger's view. Branches, remotes, rebases and staging never reach the user. Delivered: `docs/changes/client.md`; the per-organization git transport switch (`ore org git-transport ORG on|read-only|off`, `GET/PATCH /api/v0/orgs/{org}/settings`: smart HTTP and SSH refuse clones and pushes with a message naming `ore open`, everything else keeps working; `docs/changes/git-transport-off.md`) and the "last day of git" checklist for a team (`docs/10` §7). Still to do for v0.2: `ore open` of the 100M-line repository under 5 s (measured: 10,000 files in 1.1 s, sparse open of a 2,000-file area in 0.1 s); `ore revert`.

**Gate:** a team works a week on Ore with no git client installed; the measurements in `docs/scale` include `ore open`, `ore save` and `ore land` latencies.

## v0.x — Weekly releases (months 1–3)

- SSH transport; LFS to object storage; secret scanning on push; geometric repack maintenance.
- MCP server (read-mostly toolsets, OAuth 2.1); AGENTS.md serving; `ore check` on uncommitted diffs.
- Agent adapters: Codex, Cursor, Jules, open-weight models via a generic HTTP adapter; adversarial reviewer role; budgets per agent and repository.
- GitHub adapter: run the loop on GitHub repositories (App, webhooks, checks, deployments).
- Pipelines: GitHub Actions YAML compiler for the common subset with the conformance suite scaffold; caches; artifacts; services.
- Webhooks out (CloudEvents, Standard Webhooks, retry, replay).
- Minimal web UI: repositories, changes with diff, pipeline logs, deployments, ledger timeline, autonomy level per repository.
- `ore init --template`, skills folder, hosted playground with gated sandboxes.

**Gate:** 10 external contributors; first teams running the loop on GitHub repositories; launch.

## v1 — The product (months 3–12)

- **Review**: patch sets, interdiffs, comments that survive rebases, stacks, three landing strategies (queue, land-when-green, optimistic with auto-revert), bot lane, structured findings.
- **Autonomy**: levels L0–L5 with evidence-gated promotion and automatic demotion; watcher agents on telemetry; dependency-updater and security-fixer roles; kill switches.
- **Delivery**: environments with policy, N-of-M approvals and scoped secrets; imperative deployer over SSH to fleets; previews with TTL and cost; staged rollouts with metric analysis; OCI registry with referrers; SBOM per release; deploy-time policy; DORA.
- **Pipelines**: Actions conformance ≥ 85%; secrets enclave and per-job OIDC; vendored action registry with SHA resolution; cache trust scopes; Postgres profile; BYO runners; OpenTelemetry spans.
- **Identity**: SSO (SAML/OIDC), SCIM, audit log, IP allow-lists, SSH CAs, signature verification incl. SSH certificates and gitsign; Cedar policy as code.
- **Learning**: lesson engine, tracks 1–3, learn-at-the-error, onboarding checklist, five languages.
- **Intelligence**: Zoekt search over blobs, symbols, repo wiki v1.
- **Migration** v1 from GitHub with a fidelity report; hosted beta on one cell; Enterprise subscription v1.

**Gate:** five design partners in production (one at L3+, one public-sector, one on the GitHub adapter); hosted beta with paying teams; 50k stars.

## v2 — Scale (months 12–24)

- **Git on object storage** with manifests and stateless replicas; SHA-256 opt-in GA; reftable; bundle-URI; sparse profiles; LFS with content-defined chunking.
- **Pipelines**: Firecracker runner, bill-by-work, Remote Execution API cache, GitLab CI compiler, SDKs, visual builder, partner fleets.
- **Delivery**: reconciler path with rendered-manifest branches; OpenFeature flags; OpenBao-compatible secrets; OpenTofu; cost attribution; CRA workflow; hardware-in-the-loop runners and over-the-air environments for device fleets.
- **Network**: public provenance graph; cross-organization agents with scoped trust; GitLab and Forgejo adapters; cells with region pinning and DR; customer-managed keys; FIPS/STIG builds; Dedicated; SOC 2 Type II.
- **Learning**: repo wiki on private repos over MCP; tutor; credentials; ten languages; RTL; offline bundles.

**Gate:** positive compute margins; a public benchmark report (merges/day/repo, change-to-production time, auto-resolution rate); 100k stars.

## v3 — The ecosystem (months 24+)

Virtual-filesystem client; Rust data plane where it pays; WASM plugins and the marketplace; federated changes; email patch bridge; test selection from the ledger; hermetic/Nix path; package formats in Ore's registry. **Gate:** 200k stars; the default forge for agent-native teams.

## Metrics that decide the versions

| Metric | v1 | v2 |
|---|---|---|
| Signal-to-production time, median (design partners, L3+) | < 1 hour | < 15 minutes |
| Agent change acceptance rate / post-merge fix rate | measured and published | improving quarter over quarter |
| Actions conformance | ≥ 85% | ≥ 95% |
| CI start latency p95 (hosted) | < 60 s | < 15 s |
| Change page render p95, 20k lines | < 200 ms | < 150 ms |
| Deployments on the ledger as a share of all deployments | > 90% | > 98% |
| Hosted availability | 99.9% | 99.95% |

## Not on the roadmap

A new wire format replacing git; an IDE; foundation models; Jira-class planning; FedRAMP before a federal pipeline; Kubernetes-only anything; physical-world products as features (see `docs/13`).
