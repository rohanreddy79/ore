# Architecture

This document describes how Ore is put together: its shape, the components, the data layer, how it scales from one binary to many regions, and the seams that let the repository core be swapped without rewriting the product. Decisions and their alternatives are recorded in `docs/adr/`.

## 1. Shape

Ore is **an agent-first platform with a native repository core and adapters for adoption** (ADR 0013, which supersedes ADR 0001).

- The core is Ore's: git repositories served by Ore itself (smart HTTP, SSH), server-side commits, merges and ref updates through the API (ADR 0014), issues, changes, pipelines, environments, deployments, the agent loop and the ledger. One Go binary from v0.
- The **Forge Provider Interface (FPI)** is the seam through which Ore's loop runs on repositories hosted elsewhere: the GitHub adapter first (so a team adopts Ore's delivery, autonomy and learning without migrating), GitLab and Forgejo adapters later. Adapters are for adoption, not a replacement core.
- Storage evolves without changing the API: bare repositories on local disk (v0–v1) → manifest-versioned packs on object storage with stateless replicas (v2) → sparse and virtual working copies (v2–v3).

One deployable, three **runtime profiles** chosen by configuration, never by forking the code:

| Profile | Who | Database | Object store | Queue/events | Runners | Git data plane |
|---|---|---|---|---|---|---|
| **solo** | a developer, a classroom, an air-gapped team | SQLite (+ Litestream to S3 for backups) | local filesystem | in-process outbox | Docker or gVisor on the same host | bare repos on local disk |
| **team** | an organization, a ministry, a company region | Postgres | S3-compatible | outbox + NATS JetStream | Firecracker on bare metal, gVisor/Docker, BYO | local NVMe (v1); object storage (v2) |
| **cloud** | Ore's hosted service | Postgres per cell | S3 per region | outbox + JetStream per cell; global router | Firecracker fleet, partner compute, BYO | object storage with stateless replicas (v2) |

The promise that holds from v0: **one install, one identity, one UI, one API, one ledger, and the whole loop on one machine.**

## 2. Component map

```
                 ┌───────────────────────────────────────────────────────────────────────┐
   humans ──────▶│  Web UI (TypeScript, CodeMirror 6, windowed diffs, SSE)                │
   CLI `ore` ▶│  Native API (Connect-RPC + OpenAPI)   GitHub-compatible REST subset     │
   agents ──────▶│  MCP server (stateless, OAuth 2.1)    Git over SSH / HTTPS (v2 only)    │
   integrations ▶│  Webhooks out (CloudEvents / Standard Webhooks)   SCIM / SAML / OIDC   │
                 └───────────────┬───────────────────────────────────────────────────────┘
                                 │  every request carries: actor (human | app | agent), org, policy context
                 ┌───────────────▼───────────────────────────────────────────────────────┐
                 │ IDENTITY & POLICY   users · orgs · teams · apps · agents · tokens       │
                 │                     Cedar policy engine · audit log · budgets           │
                 ├───────────────────────────────────────────────────────────────────────┤
                 │ THE LEDGER   typed entities + provenance edges + CloudEvents stream     │
                 │              Commit → Change → PipelineRun → Artifact(+Attestation)     │
                 │              → Deployment(Environment, Release) → Incident; Lesson,     │
                 │              Credential; transactional outbox; replayable               │
                 ├────────────┬────────────┬────────────┬────────────┬────────────┬───────┤
                 │ PIPELINES  │ DELIVERY   │ REVIEW     │ INTELLIGENCE│ LEARNING  │CONNECT│
                 │ planner    │ environments│ changes   │ search      │ lessons   │ apps  │
                 │ scheduler  │ releases   │ patch sets │ (Zoekt)     │ engine    │ MCP   │
                 │ dispatcher │ deployer   │ stacks     │ symbols     │ tutor     │ events│
                 │ runner API │ previews   │ merge queue│ (tree-sitter)│ gateway  │ SARIF │
                 │ CAS/cache  │ rollouts   │ comments   │ SCIP ingest │ credentials│ OTel │
                 │ secrets    │ flags      │ provenance │ embeddings  │ i18n      │ SCIM  │
                 │ OIDC issuer│ registry   │ bot lane   │ repo wiki   │ bundles   │ WASM  │
                 │ logs       │ secrets API│            │             │           │plugins│
                 ├────────────┴────────────┴────────────┴────────────┴────────────┴───────┤
                 │ FORGE PROVIDER INTERFACE (FPI)                                          │
                 │   repos · refs · objects · orgs · users · issues · PRs · packages ·     │
                 │   migrations · hooks                                                    │
                 │   ┌────────────────┐ ┌──────────────┐ ┌──────────────┐ ┌─────────────┐ │
                 │   │ Forgejo        │ │ GitHub       │ │ GitLab       │ │ Native core │ │
                 │   │ headless (P1)  │ │ adapter (P1) │ │ adapter (P2) │ │ Rust (P3)   │ │
                 │   └────────────────┘ └──────────────┘ └──────────────┘ └─────────────┘ │
                 ├───────────────────────────────────────────────────────────────────────┤
                 │ DATA   Postgres | SQLite · object storage · NATS JetStream · Valkey    │
                 │        (never authoritative) · search index · runner fleet             │
                 └───────────────────────────────────────────────────────────────────────┘
```

### 2.1 Edge

- **Web UI**: TypeScript/React with CodeMirror 6 for code and diffs, windowed rendering with GitHub-like lazy thresholds (400 lines / 20 KB per file auto-loaded, everything else on demand), server-sent events for live updates, HTTP-shape sync for issue and change lists (server-sequenced writes, Linear style), and a CRDT (Loro) only for shared text such as change descriptions. Performance budgets are tests in CI: p95 render of a 20,000-line change under 200 ms; log search results under 300 ms while the job streams.
- **CLI `ore`**: repo, change, stack, pipeline, env, deploy, learn, docs. Reads docs offline (`ore docs`), submits uncommitted diffs for validation (`ore check`), and speaks the same API as the UI.
- **Native API**: Connect-RPC (gRPC-compatible, works from browsers) with an OpenAPI projection; versioned; every call authorized by the policy engine with the actor's capability set.
- **GitHub-compatible REST subset**: enough of `/repos`, `/pulls`, `/issues`, `/checks`, `/git`, webhooks payload shapes and `GITHUB_TOKEN` semantics that existing bots, reviewers and agents work with a base-URL change. Documented divergences; conformance tests.
- **MCP server**: stateless per the 2026-07-28 spec, OAuth 2.1 with protected-resource metadata, PKCE, resource indicators and audience-bound tokens; toolsets (context, repos, changes, issues, pipelines, deployments, environments, search, wiki, learn); read-mostly default set; deterministic tool ordering with cache TTLs; `--read-only` mode; published to the official registry.
- **Git transport**: SSH (keys and certificates) and HTTPS smart protocol, v2 only; bundle-URI and packfile-URI offload from object storage in P3; LFS endpoint backed by content-defined-chunking storage in P3.

### 2.2 Identity and policy

- Principals: **users**, **apps** (installed into orgs with capability sets; installers may accept a subset), **agents** (a kind of app with session state: waiting / working / completed / errored, a human owner, and budgets), **runners** (ephemeral per-job identities). Teams and orgs group them.
- Tokens: short-lived installation tokens, fine-grained personal tokens with optional admin approval, SSH certificates with ≤366-day validity and source-address pinning, per-job OIDC tokens with immutable ID-based subjects.
- Federation: SAML 2.0 and OIDC for SSO; SCIM 2.0 for provisioning; passkeys for humans. Tested against Entra ID, Okta, PingFederate, Keycloak/Zitadel.
- **Policy engine**: Cedar policies evaluated on every API call and on every ledger transition (may this actor push to this ref, run this action, deploy this artifact to this environment, have this agent comment). Org admins write policy in the UI or as code in a policy repo; rulesets, branch protection, required checks, deployment approvals and agent permissions are all Cedar under the hood.
- **Audit log**: every policy decision and every ledger write, streamed to S3, Splunk, Datadog or Event Hubs with at-least-once delivery and health checks.
- **Budgets**: CI minutes, preview-environment minutes, AI spend and agent write quotas per org, team, repo and agent, with alerts at 75/90/100%.

### 2.3 The ledger

The ledger is the product's spine (ADR 0005).

- **Entities** (all with `org_id`, stable IDs, content hashes where applicable): Repository, Commit, Change, PatchSet, Review, PipelineRun, Task, Artifact, Attestation, Environment, Release, Deployment, Rollout, Incident, Lesson, LessonRun, Credential, Policy, Actor.
- **Edges** are provenance: `PatchSet → Commit[]`, `PipelineRun → Change | Commit`, `Artifact → PipelineRun`, `Attestation → Artifact`, `Deployment → Release → Artifact[]`, `Deployment → Environment`, `Incident → Deployment`, `LessonRun → Actor, Repository, PipelineRun`.
- **Events**: every transition emits a CloudEvent (`io.ore.change.merged`, `io.ore.deployment.succeeded`, …) written in the same database transaction as the entity change (transactional outbox) and then fanned out to webhooks, the search indexer, the learning engine, the MCP subscriptions, and JetStream consumers.
- **Replay**: any consumer can replay from an event ID or timestamp; webhook endpoints get a delivery dashboard and manual or ranged redelivery.
- **Views**: DORA metrics (deployment frequency, lead time, change failure rate, failed-deployment recovery time, rework rate), CRA reports (SBOM per release, vulnerability-handling timeline), audit exports, cost reports and the agent context server are queries over the ledger. None of them has its own store.

### 2.4 Pipelines, delivery, review, intelligence, learning, connectors

Each has its own design document:

- `docs/04-pipelines-and-delivery.md`
- `docs/05-connectors-and-agents.md` (including review provenance and the agent gateway)
- `docs/06-learning-system.md`
- `docs/07-security-and-compliance.md`

Review model summary (ADR 0008): a **Change** has a stable change-id (read from the Jujutsu `change-id` commit header, else a Gerrit `Change-Id:` trailer, else assigned on first push and recorded in `refs/meta/`), ordered **PatchSets** (each a commit range), **interdiffs** between any two patch sets, comments anchored to (change, patch set, blob, range) that migrate across rebases, **stacks** (an ordered list of changes with per-layer approval and whole-stack merge), and a **merge queue** that batches, tests and bisects. Bot output goes into a separate lane that reviewers can collapse. Review metadata is mirrored to `refs/meta/changes/<id>` so it survives migration and reads offline.

Intelligence summary: Zoekt-class trigram search over **blobs** (deduplicated by blob SHA, sharded by SHA), tree-sitter symbol extraction at index time, SCIP uploads from pipelines for precise cross-repository navigation, optional embeddings for semantic search; all indexed from the commit-ordered ledger stream for commit-level consistency; budget 0.25–0.5× of source bytes. Search and navigation are MCP tools.

### 2.5 Forge Provider Interface

The FPI is a Go interface (and a Connect-RPC service for out-of-process providers) covering repositories, refs (compare-and-swap updates), objects (blob, tree, commit, diff, blame, log, merge-base, merge-tree, cherry-pick, range-diff), principals (read-through; Ore is the source of truth), issues and pull requests (read-through, mapped to Ore Changes), packages, migrations and hooks (pre-receive policy, post-receive ledger events).

Providers:

- **Native (v0)**: Ore's own git host. Bare repositories, `git http-backend` behind Ore's authentication and policy, per-repository pre-receive hooks for protected refs and secret scanning, ref snapshots around every receive to emit ledger events, and server-side operations implemented with `git merge-tree --write-tree`, `git commit-tree` and `git update-ref` compare-and-swap (ADR 0014). The object-storage layout (section 4) replaces the on-disk layout in v2 behind the same interface.
- **GitHub adapter (v0.x)**: a GitHub App with minimal permissions, webhooks into the ledger, checks API for pipeline status, deployments API for environments; the loop runs on GitHub repositories with Ore as the control plane. This is the "adopt before you migrate" wedge.
- **GitLab and Forgejo adapters (v1)**: the same shape over their APIs and webhooks; Forgejo users keep Forgejo and gain the loop.

## 3. Data layer

- **SQL**: exactly two targets, SQLite (solo) and Postgres (team, cloud). No MySQL. Migrations tested against both in CI. Every table carries `org_id`; a linter forbids queries that cross schema domains without going through an explicit interface, so that sharding by org is possible without a rewrite (ADR 0006).
- **Transactional outbox**: jobs and events are inserted in the same transaction as the business row (River-style queue on Postgres and SQLite). Workers are in-process in solo and separate processes in team/cloud.
- **Object storage**: S3-compatible for artifacts, logs, caches, attestations, LFS, bundles and (P3) git packs; local filesystem in solo. Content-addressed keys everywhere.
- **NATS JetStream** (team/cloud only): fan-out of ledger events to indexers, webhook dispatchers, runner schedulers and MCP subscriptions; never the source of truth.
- **Valkey** (optional): sessions, rate limits, hot caches; losing it must not lose data.
- **Search index**: Zoekt shards on local NVMe, rebuilt from the ledger stream; snapshotted to object storage.
- **Backups**: Litestream for SQLite; Postgres base backups plus WAL to object storage; object storage versioning; documented restore drills.

## 4. Git data plane

**v0–v1 (native, on disk)**: bare repositories under the data directory, served by `git http-backend` (SSH in v0.x) behind Ore's auth; protocol v2 only; server-side commits and merges with no working copy (`commit-tree`, `merge-tree --write-tree`, compare-and-swap `update-ref`); geometric repacking, multi-pack-index bitmaps and commit-graph Bloom filters on a maintenance schedule; LFS to object storage (v0.x).

**v2 (native, on object storage)**: object storage is the source of truth and local NVMe a disposable cache. A per-repository **manifest** lists the active packfiles and reftables; readers pin a manifest (MVCC); writers stage immutable packs and publish a new manifest with a conditional PUT; conflicting ref updates are resolved by three-way manifest merge, same-ref races conflict and retry; pushes are acknowledged only after persistence. This is the design GitLab presented at Git Merge 2026 and that Cursor, Netlify, Enroute and Tigris run variants of; it is compatible with git's pluggable object database. Hash-agnostic object IDs, SHA-1 default, SHA-256 opt-in ahead of Git 3.0; reftable only; bundle-URI and packfile-URI from object storage through a CDN; promisor serving; server-stored sparse profiles; CODEOWNERS compiled into policy.

**v2–v3 (Scion-class working copies)**: tree- and blob-based fetching with regional caches, sparse checkouts materialized on demand, and a virtual-filesystem client that lays out only what is read, so agents and CI jobs need seconds, not minutes, to start on a large repository. `gix` (gitoxide) for reads, the `git` binary for writes until parity; the data plane may move to Rust at this stage (ADR 0007).

Large files: content-defined chunking with an LFS-protocol bridge (v2). Replicas are stateless and scale linearly; a repository is pinned to a cell for writes and readable from any replica in the cell.

## 5. Pipelines execution

The control plane is Ore's (ADR 0003). Runners are pluggable through a versioned **Runner Protocol** (Apache-2.0): registration, lease, job manifest, log streaming, artifact upload, cache get/put with scoped tokens, heartbeat, OIDC token delivery. Executors:

- **Ore microVM runner** (P2): Firecracker per job with snapshot restore, egress allow-lists, per-second accounting, Linux x64/arm64; macOS via partner fleets.
- **Compatibility executor** (P1): runs Actions-syntax jobs using a Forgejo-runner/act-derived executor in Docker, gVisor or on the host; this is what self-hosters use on day one.
- **BYO** (P1): any machine running the runner, free forever.
- **Partner compute** (P2+): Depot, Blacksmith, Namespace-style fleets speaking the Runner Protocol.

## 6. Scaling and residency

- **Cells**: a cell is one Postgres, one object-storage bucket set, one JetStream, one runner pool, one search cluster, in one region. An organization is pinned to a cell; data residency is a property of the pin. A thin **global router** maps org → cell and handles login, public discovery and cross-cell search federation.
- **DR**: async replication of Postgres and object storage to a second region with documented RPO/RTO; no multi-writer SQL anywhere.
- **Self-hosted HA** (team): Postgres HA (Patroni or managed), stateless Ore replicas behind a load balancer, object storage with versioning, runners autoscaled; git writes to a single primary per repository until the native core (P3) makes replicas stateless.

## 7. Observability

OpenTelemetry traces, metrics and logs from every component; `cicd.pipeline.*` semantic conventions on pipeline spans; deployment IDs propagated to downstream observability; Prometheus endpoints; public status page with per-service error budgets for the hosted service.

## 8. Technology choices (summary; rationale in ADR 0007)

| Layer | Choice | Why |
|---|---|---|
| Control plane / platform | Go | Reuses Forgejo, Zoekt, OpenBao, OpenTofu, Woodpecker, River-style queues; fastest path to a product; single static binary |
| Native git data plane, search core (P3) | Rust | Where every 2025–26 git-storage and search system landed; gitoxide; memory safety on the hot path |
| Web UI | TypeScript, React, CodeMirror 6 | Ecosystem of editor/diff components; Forgejo moved to CodeMirror for performance and accessibility |
| Policy | Cedar | Typed, analyzable, fast, Apache-2.0; capability policies for humans, apps and agents in one language |
| Queue/events | Transactional outbox + NATS JetStream | Correctness first; fan-out second; nothing in Redis is authoritative |
| Isolation | Firecracker, gVisor fallback | 125 ms boot, 5 MB overhead; strongest boundary every funded runner vendor converged on |
| Secrets | OpenBao-compatible API | MPL, Linux Foundation, namespaces, dynamic secrets, post-quantum signing |
| Infra as code | OpenTofu | MPL, OCI-distributed modules, state encryption |
| Attestations | Sigstore (cosign, in-toto, SLSA v1.2) | The standard every registry now verifies |
| Search | Zoekt + tree-sitter + SCIP | Proven at Sourcegraph and GitLab scale; stack-graphs is archived |
| Plugins | WASM components (WASI 0.3), Extism bridge | Capability-based sandboxing; many SDK languages |

## 9. Repository layout

```
ore/
  cmd/ore/            the binary (serve, migrate, runner, cli)
  internal/
    ledger/              entities, events, outbox, views (v0)
    gitx/                git subprocess layer: bare repos, refs CAS, merge-tree, commit-tree, archive (v0)
    githttp/             git smart HTTP behind auth, hooks, ref snapshots → ledger (v0)
    identity/            principals, tokens, SSO, SCIM, policy (Cedar)
    fpi/                 Forge Provider Interface + providers/{native,github,gitlab,forgejo}
    pipelines/           planner, scheduler, dispatcher, runner protocol server, cache, secrets, oidc, logs (v0: container runner)
    delivery/            environments, releases, deployer/{imperative,reconciler}, previews, rollouts, flags, registry (v0: container previews)
    review/              changes, patchsets, stacks, landing strategies, comments, provenance (v0: changes + server-side merge)
    agent/               the loop runner, roles, adapters, budgets, gateway (v0: loop + adapters)
    intel/               search, symbols, scip, embeddings, wiki
    learn/               lessons, runs, tutor gateway, credentials, i18n
    connect/             apps, mcp, events (webhooks), sarif, otel, plugins
    web/                 HTTP routers, SSE, REST API, GitHub-compat API (v0: REST)
  ui/                    TypeScript app
  runner/                the runner (separate module, Apache-2.0)
  sdk/                   TypeScript, Python, Go SDKs (Apache-2.0)
  spec/                  OpenAPI, Connect protos, pipeline IR schema, event catalog, MCP toolsets (Apache-2.0)
  lessons/               the built-in curriculum (CC BY-SA 4.0)
  deploy/                Compose, Helm (Ore-run only), install scripts
  docs/                  this folder
```
