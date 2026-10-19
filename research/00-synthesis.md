# Synthesis: what the evidence says about building a better forge (October 2026)

This document distills the six research slices in this folder into the findings that drive the design. Every claim below is sourced in the numbered reports (01–06); this file deliberately repeats few URLs. Read this first, then the vision in `docs/01-vision.md`.

---

## 1. The incumbents are weaker than their market share suggests

**GitHub is losing on the things that used to be unquestioned: reliability, trust, and performance.**
- 257 incidents and 112 hours of major-outage time in the twelve months to April 2026; Actions was the worst-hit service (57 incidents). On 5 October 2026 a quarter of hosted jobs did not start within five minutes. (01, 02)
- The attempted $0.002/minute "platform charge" on customers' *own* self-hosted runners (December 2025) was postponed within a week after backlash. It was postponed, not cancelled. (01, 02, spot-checked)
- Copilot's move to token billing (June 2026) produced reported 10–50× bill shocks; AI is now bundled into every surface, and projects with no-AI policies (Zig, Gentoo, Ghostty) have left for Codeberg. (01, 03)
- The UI rewrite regressed: diffs are unusable past ~5,000 lines, logs are not searchable, and an ex-GitHub engineer confirmed on Hacker News that a year-long performance effort "did not achieve its main goals". (01)
- Pricing is no longer transparent: Team and Enterprise are now "$4 / $21 for the first 12 months*". (01)
- GitHub lost its CEO in August 2025 and was folded into Microsoft's CoreAI org. (01)
- GitHub stops at the pull request. It has no environments model worth the name (one approval out of up to six suffices), no rollouts, no preview environments, no registry economics beyond "free for now". (02)

**GitLab is the closest thing to "one application" and it is exactly what sovereignty buyers now reject.**
- ~$1B revenue, but growth slowed from 26% to 21%, gross margin fell from 88% to 84% as AI credits ate COGS, and GAAP operating margin is still −20%. (05)
- The useful parts are tiered: deployment approvals and protected environments are Premium ($29), DORA metrics and most security are Ultimate (quote-only; the $99 list price was quietly removed). (01, 02)
- The Netherlands rejected GitLab for its open-core model and chose Forgejo; Fedora did the same. Public bodies deciding in 2025–26 chose Forgejo; those that decided earlier chose GitLab. (05)
- Resource footprint: 8 vCPU / 16 GB recommended for 1,000 users, versus ~1 GB for Gitea/Forgejo. (01)

**Forgejo has the trust and none of the product above the repository.**
- GPLv3+, trademarks held by a non-profit, all features free, LTS releases, chosen by Fedora, the Dutch government, Zig and Gentoo. (01, 05)
- But: its Actions are documented as "not the same" as GitHub's; CI was explicitly the migration blocker for the Dutch rollout; no deploy, no environments, no HA writer (Codeberg runs one read-write instance plus read-only replicas); federation is still experimental after years; projects are "rudimentary"; no MCP server; UI accessibility was flagged by Fedora. (01, 05, 06)
- It is maintained on roughly €40K/year plus an €85K grant. (05)

**New entrants validate the direction and leave the gaps open.**
- Cursor's Origin ("a git forge for the agentic era", beta August 2026 on paid plans) is review-centric, closed, and lists neither deployment nor environments nor open source. (01, 02)
- East River Source Control (Jujutsu's creator as CTO) is in preview. Tangled raised €3.8M for an ATProto-based social forge. Depot, Blacksmith and Namespace raised $67M in 2026 to sell CI compute, and Depot now runs Actions syntax *without* GitHub. (01, 02)
- The hyperscaler forges are unstable: CodeCommit was deprecated then un-deprecated, Cloud Source Repositories is end-of-sale, Azure DevOps is on minimal investment. Regulated buyers want in-VPC git with native IAM and nobody serves the mid-market there. (01)

## 2. Nobody owns the whole path, and the path is where the value moved

- Review's remaining value is change management, not validation: bot comments went from under 1% of PR comments (pre-2023) to the high 30s, and open PRs per developer went from ~4 to 20+. (06)
- Agent-authored PRs are accepted less, need 1.62× more follow-up fixes, and reviewer engagement is the strongest predictor of merge. 6–18% of public commits are already AI-attributed. (03)
- CI volume is exploding under agents (one Depot customer: 25× in six months). Per-minute pricing does not survive this; content-addressed "bill by work" (Dagger charges one fifth for repeat work, RWX, Bazel's Remote Execution API) does. (02)
- Every 2025–26 CI supply-chain incident (tj-actions, trivy-action, actions-cool, GhostAction, Shai-Hulud) was defeated only by SHA pinning; secrets were scraped from runner memory; the Actions cache was poisoned from a fork PR; malicious packages shipped with *valid* SLSA L3 provenance. Provenance proves origin, not intent, so policy must be enforced at deploy time. (02)
- Deploy is fragmenting: Heroku frozen, App Runner closed to new customers, while Coolify (63k stars), Dokploy (38k) and Kamal (Rails 8 default) boom. Vercel's Rolling Releases is the best built-in canary on the market and is gated to one project on Pro. (02)
- Argo CD runs in production at 97% of surveyed CNCF users, but GitLab's own DORA admits that pull-based deployments create no deployment record. The deployment ledger is missing everywhere. (02)
- Backstage contributions fell 24% year over year; the portal category is pivoting to "context and policy for agents". The single pane of glass is becoming an MCP server over the forge's graph, not a catalog UI. (02)

## 3. Learning is the unoccupied growth surface

- 36M developers joined GitHub last year, roughly one per second, most of them outside the US (India +5.2M; Kenya, Nigeria, Brazil, Philippines growing 27–33%). 80% of new developers use Copilot in week one. (04)
- Stack Overflow new questions fell ~98% from peak to December 2025. The public commons that taught a generation is gone. (04)
- The market has already run the experiments: free anonymous sandboxes die of abuse (Katacoda, Microsoft Learn sandboxes, Codeberg Pages v2); bespoke learning bots die of neglect (GitHub Learning Lab); standalone curricula die of economics (Exercism's cash crisis, Codecademy's curriculum team laid off). What survived: learning built on the host product's own automation (GitHub Skills runs on Actions), auto-graded real artifacts, gated sandboxes (Killercoda), and guardrailed AI with human endorsement (Harvard CS50's duck: 88% accuracy, ~$1.90/student/month, bot answers endorsed by staff). (04)
- Enterprises spend $360–779 per seat per year on learning libraries and $1–2K/day on instructor-led git/DevOps training, all outside the forge. Median time-to-first-commit is two to three weeks. (04)
- GitHub's product UI is English-only. GitLab ships 78 locales. Nobody ships a localized, offline-capable learning layer on private repositories. (04)

## 4. Connectors have standardized; the forge should implement each standard once

- MCP is governed by the Linux Foundation's Agentic AI Foundation, has 10,000+ public servers, a current stateless spec (2026-07-28), and OAuth 2.1 authorization. GitHub (22 toolsets), GitLab (GA October 2026) and Gitea all ship forge MCP servers; Forgejo and Codeberg do not. (03)
- The reference security architecture exists: GitHub's Agentic Workflows run agents read-only and route every write through a schema-validated "safe outputs" gateway. Invariant Labs showed the alternative: a prompt injection in a public issue exfiltrating private data through the GitHub MCP server. (03)
- One ingest standard per category covers the whole category: SARIF for scanners (Semgrep, Snyk, SonarQube, Trivy), OpenTelemetry for observability, SCIM 2.0 plus SAML/OIDC for identity, CloudEvents over Standard Webhooks for outbound events (GitHub's webhooks have no automatic retry and a 10-second deadline). (03)
- Linear's agent model is the one to copy: an issue keeps a human assignee and a *delegated* agent, because "an agent cannot be held accountable". Provenance belongs in structured metadata, not commit trailers, which are contested and stripped by squash merges. (03)
- Plugins: WASI 0.3 (June 2026) with the Component Model is the long-term sandbox; Extism today. GitHub's marketplace keeps 5%. (03)

## 5. The architecture consensus of 2026

- Object storage is the source of truth and disks are caches: Cursor's Continuity (120–300 pushes/s, 100 linearly scaling replicas), GitLab's Git Merge 2026 manifest design (being upstreamed via git's pluggable object database), Netlify, Enroute, Tigris. Three-replica 3PC (GitHub Spokes) is "rough to operate". (06)
- Git 3.0 (spring 2027) flips SHA-256 and reftable defaults for new repos and makes Rust mandatory; forge readiness is the explicit gating factor and no large host advertises SHA-256 today. (01, 06)
- Rust owns the data plane (Blackbird, Mononoke, Enroute, Tangled, GitButler, gitoxide, git itself); Go owns the control plane (Gitea/Forgejo, Gitaly, Zoekt, Woodpecker, Dagger). gitoxide is ready for reads, not for push/merge; Gitaly proves subprocess git scales. (06)
- Firecracker microVMs: ~125 ms boot, ~5 MB overhead, the isolation every funded runner vendor converged on. (06)
- The review model of the future is Gerrit's change with Jujutsu's change-id header, patch sets, interdiffs, first-class stacks, and metadata stored in-repo (GitButler's Git Meta, Radicle's collaborative objects) so it is portable. GitHub shipped stacked PRs natively in 2026 and Cursor bought Graphite: stacking and merge queues are now table stakes. (06)
- Index blobs not files, budget 0.25–0.5× of source bytes for search, use SCIP for precise navigation (GitHub archived stack-graphs), never bundle CodeQL (its licence forbids it). (06)
- Two SQL targets (SQLite and Postgres), tenancy key on every row from day one, transactional outbox for jobs and events, region-pinned cells for residency. GitHub spent years retrofitting partitioning; GitLab's Cells are still an experiment. (06)

## 6. Business model and licensing consensus

- Every source-available relicensing since 2021 produced a surviving Linux Foundation fork (OpenSearch, OpenTofu, Valkey) and two reversals to AGPL (Elastic, Redis). AGPL is now the consensus "protective but OSI-approved" licence (Grafana, Elastic, Redis 8, Plausible, Zitadel, Sourcehut). (05)
- Open-core with a proprietary edition directory scaled GitLab to $1B and is now explicitly rejected by Fedora and the Dutch government. Fair Source / FSL / SUL is excluded by OSI-policy public buyers. (05)
- Revenue follows cloud, not the licence (Supabase ~$170M ARR on Apache-2.0, PostHog ~$58M on MIT, n8n $40M+ on a non-OSI licence). Self-host is a funnel: PostHog cut Kubernetes self-hosting because 3.5% of users consumed outsized support. (05)
- Selling "faster CI" as the wedge killed Earthly ("CI compute is a commodity"); selling scarce compute (macOS, bare metal) and cache storage works (Blacksmith, Namespace, Depot). (02, 05)
- Enterprises pay $21 (GitHub) to $29 (GitLab Premium) per seat for the collaboration tier; Gitea Enterprise clears $19. SSO/SCIM/audit/residency define the paid tier everywhere. (05)
- The Gitea trademark grab (October 2022) cost it Fedora, the Dutch government and Codeberg; the trademark and domains are the single most important governance asset. DCO, not CLA, is the credible anti-rug-pull commitment. (05)
- EU Cyber Resilience Act reporting duties are live since 11 September 2026; SBOM and conformity obligations land 11 December 2027. A commercially supported forge sold in the EU is a "manufacturer". (02, 05)
- A one-to-five-person team cannot build git hosting, PRs, issues, CI, packages, SSO and migration tooling from scratch in a year; Forgejo (GPLv3+, API spec MIT), Zoekt, Woodpecker, OpenBao, OpenTofu, Keycloak/Zitadel, Sigstore are reusable. (05)

## 7. The gap, in one paragraph

No product delivers code → review → pipeline → artifact → environment → production → learning in one open-source place with one identity, one policy model and one ledger, usable by humans and agents alike, light enough for a two-person platform team to run, and sovereign enough for a ministry to adopt. GitHub stops at the PR and is losing trust; GitLab has the shape but sells the useful parts by the seat and is heavy; Forgejo has the trust and stops at the repository; the new AI forges are closed and review-centric; the deploy tools have no forge; the learning products have no product. That intersection is the product to build.

## 8. Decisions the research forces

| Decision | Choice | Driven by |
|---|---|---|
| Licence | AGPL-3.0-only server; Apache-2.0 for API spec, SDKs, CLI, runner protocol, schemas | 05 |
| Contributor agreement | DCO, never a CLA; written stewardship charter; trademark successor plan | 05 |
| Day-1 forge core | Reuse Forgejo headlessly behind a Forge Provider Interface; GitHub and GitLab adapters for "adopt before you migrate"; native object-storage core later | 05, 06 |
| Control plane | Own pipelines dispatch, secrets issuance, caching, status; runners pluggable; BYO runners free forever | 02 |
| Pipeline model | GitHub-Actions-compatible on-ramp with a public conformance suite; native typed, content-addressed DAG; bill by work | 01, 02 |
| Isolation | Firecracker per job (cloud), gVisor/Docker fallback (self-host); SHA-pinned actions; cache as trust boundary; secrets enclave + OIDC | 02, 06 |
| Delivery | Environments as first-class objects; imperative (Kamal-style) and reconciler (Argo-style) paths; previews per PR; progressive rollouts; OpenFeature flags; built-in OCI registry; OpenBao-compatible secrets; OpenTofu | 02 |
| Review | Change-id, patch sets, interdiff, stacks, merge queue; agent lane; structured provenance; review metadata in-repo | 03, 06 |
| Agents | App identity with capability scopes; native MCP server; safe-outputs gateway; delegated-not-assigned; per-repo agent policy and budgets | 03 |
| Events | CloudEvents over Standard Webhooks from a transactional outbox, with replay | 03 |
| Learning | Lessons as pipelines on the learner's own repo; learn-at-the-error; repo wiki; guardrailed tutor with human endorsement; Open Badges 3.0; ten languages first; offline bundle | 04 |
| Data | SQLite (solo) and Postgres (team/cloud); tenancy key everywhere; outbox; region-pinned cells; object storage authoritative for git in the native core | 06 |
| Search | Zoekt-class trigram over blobs + tree-sitter + SCIP; embeddings optional; exposed via MCP | 03, 06 |
| Compliance | SBOM/VEX/attestations and CRA clocks as product features; SOC 2 Type II within 18 months; FedRAMP deferred | 02, 05 |
| Pricing | Cloud Team $4–8; self-hosted Enterprise $12–19/seat; free SSO for small teams; CI at or below $0.004/vCPU-min; 5% marketplace take; every price published, grandfathered | 01, 05 |
