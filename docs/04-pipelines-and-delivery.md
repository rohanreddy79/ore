# Pipelines and delivery: from push to production in one place

This is the design of the two pillars the incumbents do not cover end to end: trustworthy pipelines and built-in delivery. The evidence behind each choice is in `research/02` and `research/06`.

---

## Part A — Pipelines

### A.1 Goals

1. Run existing GitHub Actions workflows unmodified, with a public conformance suite, so migration has no CI cliff.
2. Make the native model a typed, content-addressed DAG so results are cacheable, reproducible and billable by work.
3. Make the 2025–26 class of supply-chain attacks structurally impossible: imposter commits, secrets in runner memory, poisoned caches, mutable tags.
4. Own the control plane; make execution pluggable; never charge for a customer's own machines.
5. Serve agents: validate uncommitted diffs before a commit exists, and absorb 10–25× volume without 10–25× cost.

### A.2 The Pipeline IR

The IR is the contract between authoring front-ends and the control plane (schema in `spec/pipeline-ir/`).

```
Pipeline
  id, repo, trigger (event + filters), concurrency group, policy context
  tasks[]:
    id, name, needs[] (DAG edges), condition (CEL expression over context)
    inputs:
      source   { commit, paths[] (sparse), submodules }
      image    { ref resolved to digest }            -- OCI
      actions[]{ ref resolved to SHA, verified ancestry, integrity }
      params   { ... }                                -- hashed
      secrets[]{ path, mount }                        -- by reference, never by value
      caches[] { key, scope: trust-level, restore-keys[] }
      artifacts_in[] { from task, name }
    steps[]: run | uses | composite  (compiled from YAML or emitted by SDK)
    executor: { kind: microvm|gvisor|docker|host|partner, size, os/arch, egress policy }
    outputs: artifacts[], caches[], attestations[], summary
    limits: timeout, retries, budget
  environment binding (optional): { env id, approval gates } -- links to Delivery
```

Every task has a **content hash** over its resolved inputs. A task whose hash matches a prior successful run in the same trust scope is a cache hit: it is not re-executed, its outputs are linked, and it is billed at the repeat rate (or not at all on self-hosted). This is the mechanism behind "bill by work".

### A.3 Authoring front-ends

- **Actions YAML compiler** (P1). Parses workflow syntax, resolves `uses:` references to SHAs (see A.5), maps `runs-on` to executors, expands matrices, translates expressions to CEL, maps `actions/cache` and `actions/upload-artifact` to native cache/artifact tasks, and compiles reusable workflows to sub-DAGs. Divergences are documented and tested by the **conformance suite**: a corpus of real public workflows and the top marketplace actions, run on every release, with pass rates published per category (contexts, expressions, services, containers, matrices, environments, OIDC, cache, artifacts, concurrency).
- **GitLab CI compiler** (P2). Stages, rules, needs, includes, environments map naturally onto the IR.
- **SDKs** (P2): TypeScript, Python, Go — typed builders that emit IR; a `ore pipeline plan` command shows the DAG and the cache status of each task before running.
- **Visual builder** (P2): drag-and-drop DAG editing that round-trips to IR (OneDev is the only forge with this today).

### A.4 Control plane components

- **Planner**: event → pipeline selection → IR compilation → SHA resolution → policy check (may this actor trigger this pipeline on this ref with these secrets?) → cache lookup → execution plan.
- **Scheduler**: priority queues per org with fairness, concurrency groups, budgets, executor matching (os/arch/size/labels), spot-aware retry; emits `io.ore.pipeline.run.queued|started|finished` events.
- **Dispatcher / Runner Protocol server**: leases tasks to runners, streams logs (NDJSON, indexed as they arrive so log search works mid-run), receives artifacts and attestations, issues per-job OIDC tokens and secrets channels, enforces heartbeats and timeouts.
- **CAS and cache**: content-addressed object store for task outputs, layer caches and build caches; keys are scoped by **trust level** (default branch > protected branches > internal branches > fork PRs) so a fork can read but never write what the default branch reads. Remote Execution API compatible endpoint for Bazel/Buck2 (P2).
- **Secrets enclave and OIDC issuer**: see ADR 0012. Claims: `sub` (immutable repo id + ref kind + ref + environment), `repository`, `ref`, `environment`, `pipeline`, `task`, `actor_kind` (human|app|agent), `provenance` (whether the triggering change has agent-authored hunks).
- **Status and checks**: results flow to the Change (required checks), to the GitHub-compatible checks API, to the ledger, and to OpenTelemetry as `cicd.pipeline.*` spans.

### A.5 Supply-chain hardening (all P1 unless noted)

| Attack seen in 2025–26 | Ore control |
|---|---|
| Mutable tags moved to imposter commits (tj-actions, actions-cool) | Every `uses:` reference is resolved to a SHA at plan time; the SHA must be an ancestor in the referenced repository's protected history; tags on releases are immutable; the resolved SHA is recorded in the run and in the attestation |
| Marketplace action compromised upstream | Actions are **vendored** into Ore's action registry on first use with digest, licence and SBOM; updates show a diff and require re-approval per policy; org allow-lists are unlimited in size |
| Secrets scraped from runner memory | Secrets are delivered into the sandbox over a per-job channel and mounted as files or injected into the specific step's process; the runner agent process never holds them; microVM memory is discarded after the job |
| Cache poisoned from a fork PR, then consumed by the release workflow | Caches are write-scoped by trust level; a fork cannot write into a scope the default branch reads; cache entries carry the provenance of the run that produced them |
| Secrets exfiltrated to raw IPs and look-alike domains | Per-job egress policy (allow-list by default for protected refs) with alerts; DNS and connection logs attached to the run |
| Malicious packages with valid SLSA L3 provenance | Provenance is necessary, not sufficient: deploy-time policy (Part B) checks the *source* attestation (who reviewed, two-party review, no force-push) and the dependency delta, not just the build |
| Pwn-request from `pull_request_target` | Fork PRs run with read-only tokens, no secrets, no cache writes; `pull_request_target` compiles to a policy-gated task that requires a maintainer label |
| Stale or vulnerable self-hosted runners | Runner protocol version negotiation with graceful deprecation windows, published in the changelog months ahead (not 30-day cut-offs); signed runner releases; optional auto-update |

### A.6 Executors and isolation

- **Ore microVM** (P2): Firecracker per task, snapshot-restored root filesystem for the common images (~125 ms), per-second accounting, nested virtualization where the host allows, Linux x64 and arm64. macOS and Windows through partner fleets speaking the Runner Protocol.
- **Compatibility executor** (P1): Docker, gVisor (recommended) or host, running the same task manifest; this is what a self-hoster installs on day one and what the solo profile uses.
- **Partner compute** (P2): Depot/Blacksmith/Namespace-style fleets register as executors; customers choose per label; Ore bills nothing on top.
- **Runner credentials**: ephemeral, single-job, revoked at completion; runners never hold long-lived tokens.

### A.7 Agents and pipelines

- `ore check --task test` ships the uncommitted diff as a patch, runs the named tasks in the same sandbox class, and returns structured results; the result hash is reused when the same tree is later pushed, so the post-push run is a cache hit (P2).
- Agent-triggered runs are labelled by actor kind, counted against the agent's budget, and can be throttled per repository.
- Test selection from the ledger (which tests covered which paths in the last N runs) is an optional planner pass (P3).

### A.8 Pricing rules that the design enforces

Hosted execution ≤ $0.004 per vCPU-minute on Linux (parity with the cheapest credible vendors), per-second accounting, repeat work at ≤ 20% of the new-work rate, macOS and GPU at cost-plus, cache and artifact storage at a published per-GB-month rate, **zero platform fee for customer-owned runners, written into the licence and the price list**.

---

## Part B — Delivery

### B.1 Goals

1. Make environments, releases and deployments first-class ledger entities so every deploy is recorded whether it was pushed, pulled or clicked.
2. Serve the two worlds that actually exist: VMs/bare metal with SSH and Docker (Rails 8's Kamal default; Coolify's and Dokploy's 100k combined stars), and Kubernetes with a reconciler (Argo CD in 97% of surveyed CNCF production).
3. Preview environments for any stack, with a TTL and a cost on the PR.
4. Progressive rollouts with metric analysis and instant rollback, for backends, not just frontends.
5. Registry, secrets, flags and infrastructure inside the same policy model.
6. Compliance as a by-product: SBOM, attestations, deploy-time policy, CRA clocks, DORA.

### B.2 Object model

- **Environment**: name, kind (production | staging | preview | ephemeral), target(s) (hosts over SSH, a Kubernetes namespace, a Compose project, an external provider), protection policy (Cedar: who may deploy from which refs, N-of-M approvals, wait timers, business hours, required attestations), scoped secrets and variables, observability links, cost center.
- **Release**: an immutable set of artifacts (by digest) plus their attestations, SBOM, source attestation (the Change, its reviewers, the policy decisions at merge) and release notes generated from the ledger. Releases are what get deployed; tags point at releases.
- **Deployment**: (release, environment, strategy, actor, approvals, started, finished, result, health, rollback-of). Emits `io.ore.deployment.*` events. Created by the imperative deployer, by the reconciler when it observes a sync, or by the API when an external tool deploys.
- **Rollout**: a deployment with stages (e.g. 5% → 25% → 100%), bucketing (cookie, header, IP hash, user id), analysis queries (Prometheus, Datadog, OpenTelemetry metrics in Ore), promotion rules, automatic or manual rollback.
- **Incident**: linked to the deployment(s) in effect; opened from alerts, from the API, or manually; closes the DORA loop (failed-deployment recovery time).

### B.3 Two delivery paths, one model

**Imperative path (P1)** — "Kamal/Coolify class":
- Targets are hosts reachable over SSH (or a Docker socket / Swarm), registered to an environment with host keys and labels.
- Ore builds or receives an OCI image (registry below), pushes it, and performs a zero-downtime swap behind an embedded reverse proxy (health checks, draining, TLS via ACME), with Compose support for multi-service apps and volumes.
- Rollback = redeploy the previous release's digest; every step is a ledger event with logs.
- Works from the solo profile on a single VPS up to fleets of hosts; no Kubernetes required.

**Reconciler path (P2)** — "Argo/Flux class":
- Ore renders manifests (Helm, Kustomize, plain YAML, OpenTofu for cloud resources) into a **rendered-manifest branch** per environment (the hydrator pattern, signed), and an in-cluster agent reconciles it, reporting sync and health back as Deployment entities.
- Progressive delivery via the rollout controller (Gateway API, Istio, NGINX, ALB) with the same Rollout object as the imperative path.
- Export: the rendered branches are plain git and work with Argo CD or Flux unchanged, so no one is locked in.

### B.4 Preview environments (P1)

- Enabled per repository with a Compose file or Helm chart and a size class. A Change opens → a preview environment is created (imperative path on a preview host pool, or a namespace on the reconciler path), seeded from a snapshot or fixture, with a URL on the Change page.
- TTL and idle sleep by default; cost (runner minutes + runtime) shown on the Change; per-org quota with metering beyond it.
- Learning lessons on deployment use the same mechanism.

### B.5 Progressive rollouts and flags (P2)

- Rollouts copy the shape of the best built-in canary on the market (stages, sticky bucketing, metric comparison against the baseline, instant rollback, a full API lifecycle: start, approve stage, complete, roll back) and make it work for backends on VMs and Kubernetes.
- An **OpenFeature-compatible** flag service with environments, targeting rules, audit, and flags tied to releases (a flag created in a Change is linked to it on the ledger; stale flags are surfaced).

### B.6 Registry, secrets, infrastructure

- **OCI registry** (P1): images, Helm charts, OpenTofu modules and providers, SBOMs and attestations as referrers; immutable tags on release; garbage collection by ledger reachability; free transfer within the platform; storage at a published per-GB-month rate; pull-through cache for Docker Hub/ghcr with digest pinning. Package formats (npm, PyPI, Maven, Go, Cargo, Debian/RPM, …) are served by the Forgejo core in P1 and move to Ore's registry in P3.
- **Secrets** (P2): OpenBao-compatible API (ADR 0012); environment-scoped; dynamic cloud credentials via OIDC exchange; rotation events on the ledger.
- **Infrastructure** (P2): OpenTofu runs as pipeline tasks with state in Ore's object store (encrypted), plan output on the Change, apply gated by environment policy; drift detection as a scheduled task.

### B.7 Deploy-time policy (P1, extended in P2)

Before a deployment starts, the policy engine evaluates, with the ledger as evidence:

- the release's artifacts have build attestations (SLSA Build L2 by default, L3 when built in isolated reusable tasks) from Ore's own issuer;
- the source attestation shows the Change was reviewed by the required reviewers (SLSA Source L4 two-party review for production, if the org wants it), with no force-push on the protected ref;
- the SBOM delta against the previous release has no new critical vulnerabilities (OSV) or has an approved exception;
- the deployer is allowed for this environment and ref; approvals are satisfied; the wait timer has elapsed; agent-authored hunks are allowed in this environment or have a human co-sign.

Policy decisions are recorded on the Deployment so an auditor can replay them.

### B.8 Compliance and metrics as by-products

- **SBOM** (CycloneDX and SPDX) generated per release from build inputs and image scans; **VEX** statements attached when a vulnerability is assessed; both downloadable and served as OCI referrers.
- **CRA workflow** (P2): a vulnerability-handling board with the ENISA clocks (24 h early warning, 72 h notification, 14-day final report) pre-wired, support-period metadata per product, and an export in the format the single reporting platform expects.
- **DORA** (P1): deployment frequency, lead time for changes (from first commit on the Change to production deployment), change failure rate (deployments linked to incidents or rollbacks), failed-deployment recovery time, and rework rate — computed from the ledger, free on every tier, with medians and distributions, never means alone.
- **Cost** (P2): CI minutes, preview minutes and runtime cost attributed to Change, environment, team and deployment; shown by default.

### B.9 What a developer sees

On a Change page: the stack, the patch sets and interdiffs, the pipeline DAG with cache hits greyed out, the preview URL and its cost, the SBOM delta, the attestations, the approvals required for each target environment, and a "deploy to staging" button that is enabled only when policy says so. After merge: the release, its rollout progress with metrics, and the one-click rollback. Every item links to its ledger entry and, through the learning layer, to the lesson that explains it.
