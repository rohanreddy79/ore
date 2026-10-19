# Connectors and agents: "all the connectors" without building all the connectors

The goal is that every tool a company already uses, and every AI agent it will use next year, works with Ore on day one, and that agents are governed rather than merely hosted. The strategy has two halves: implement each *standard* once so whole categories light up, and make the forge itself the safest place for an agent to work. Evidence is in `research/03`.

## 1. One standard per category

| Category | Standard Ore implements | What it unlocks with no per-vendor code |
|---|---|---|
| AI clients and agents | **MCP** server (2026-07-28 stateless spec, OAuth 2.1) + **AGENTS.md** rendering and serving | Claude Code, Codex, Cursor, Jules, Devin, Amp, Factory, Augment, Copilot-class clients, IDEs, chat assistants |
| Existing bots and integrations | **GitHub-compatible REST subset** and webhook payload shapes | Review bots, merge tools, dashboards and scripts written for GitHub work with a base-URL change |
| Outbound events | **CloudEvents** over **Standard Webhooks** | Zapier, n8n, Make, Knative, Argo Events, EventBridge, Event Grid, PagerDuty, Slack workflows, any HTTP endpoint |
| Security scanners | **SARIF** upload with fingerprints | Semgrep, Snyk, SonarQube, Trivy, CodeQL (customer-licensed), Checkov, Bandit, Gitleaks, ZAP |
| Observability | **OpenTelemetry** (traces, metrics, logs) and deployment-ID propagation | Datadog, Grafana, Honeycomb, New Relic, Sentry, Prometheus, any OTel collector |
| Identity | **SAML 2.0**, **OIDC**, **SCIM 2.0**, passkeys | Okta, Entra ID, Google Workspace, PingFederate, Keycloak, Zitadel, Authentik, JumpCloud |
| Audit and SIEM | Audit-log streaming (S3, Azure Blob, GCS, Splunk HEC, Datadog, Event Hubs) | Every SIEM, with at-least-once semantics and health checks |
| Package publishing | **OIDC trusted publishing** from pipelines | npm, PyPI, RubyGems, crates.io, Maven Central provenance without stored tokens |
| Artifacts | **OCI** registry with referrers; **Remote Execution API** cache | Docker, Helm, ORAS, OpenTofu modules, Bazel/Buck2 |
| Feature flags | **OpenFeature** provider | Any OpenFeature SDK; migration from LaunchDarkly/Unleash/Flagsmith via flag import |
| Secrets | **OpenBao/Vault-compatible** API | Existing Vault/OpenBao tooling, external-secrets operators, CI plugins |
| Infrastructure | **OpenTofu** (and Terraform-compatible providers), Crossplane manifests via the reconciler | All major clouds and hundreds of providers |
| Deploy targets | SSH + Docker/Compose; Kubernetes; OCI | Hetzner, DigitalOcean, AWS, GCP, Azure, OVH, bare metal, any cluster |
| Email workflows | `git send-email` / `b4`-compatible patch bridge | Kernel-style and Sourcehut-style projects |
| Docs | Per-page Markdown, `llms.txt`, docs MCP tools | Any docs consumer; Stripe/GitBook pattern |
| Federation | ForgeFed/ActivityPub outbound (stars, follows, comments); COB export of issues/changes | Forgejo, Mastodon; portable review data for any future forge |

Vendor-specific connectors are then thin: a Slack app, a Jira/Linear sync, a Notion/Confluence link unfurler, a Figma embed, a PagerDuty/incident.io incident bridge. Each is an **App** (section 2) built on the public API, maintained in the open, and listed in the catalog. The first-party set in P1: Slack, Microsoft Teams, Matrix, Jira, Linear, PagerDuty, Sentry, Datadog, Grafana. P2: Notion, Confluence, incident.io, Opsgenie, Plane, Huly, Figma, Discord.

## 2. The App model (the only third-party identity)

- **Capability sets, not scopes.** An app declares the capabilities it needs per feature ("post check results", "read changes", "comment on issues", "create deployments"). Installers see them grouped by feature and may **accept a subset**; the app receives what was granted and must degrade gracefully. This fixes the all-or-nothing permission sets that make a 13-permission install the norm today.
- **Installation tokens** are short-lived, scoped to the granted capabilities and the chosen repositories, and issued by exchange from the app's signed JWT. User-to-server tokens exist for apps acting on a user's behalf, with the same partial-acceptance model.
- **Apps are also agents** when they declare the `agent` capability bundle (section 4).
- **Verification and listing**: publisher verification, signed manifests, a diff shown on every capability change with re-consent, and a 5% marketplace take (P3) with a metering API so vendors can bill by usage and customers can cap spend per repository.

## 3. Events out: webhooks that work

- Every ledger transition emits a CloudEvent (`io.ore.<entity>.<verb>`, versioned) from the transactional outbox.
- Delivery follows Standard Webhooks: `webhook-id`, `webhook-timestamp`, `webhook-signature` (HMAC-SHA256 and ed25519, multiple signatures during rotation), exponential backoff with jitter over 72 hours, `410 Gone` disables the endpoint, `Retry-After` honored, 30-second timeout, thin payloads by default with a signed URL to the full object.
- Per-endpoint queues so one slow consumer never blocks another; a delivery dashboard with filters, payload inspection, replay by ID or time range; endpoint health alerts; a self-service "test event" button.
- Subscriptions can filter by entity, verb, repository, environment, actor kind and CEL expression, so an integration receives only what it needs.
- The same stream powers MCP subscriptions (`subscriptions/listen`) for agents waiting on a review or a deployment.

## 4. Agents: delegates with identity, budgets and a write gateway

### 4.1 Principals and delegation
- An **agent** is an app with a human **owner**, a declared model/vendor, a session model (waiting / working / completed / errored, visible in the UI), and **budgets** (CI minutes, AI spend, writes per hour, open changes).
- An issue keeps a **human assignee** and may have a **delegated agent**; a Change has a human owner even when every hunk was written by an agent. "An agent cannot be held accountable" is a product rule, not a slogan.
- Agents can be `@mentioned`, assigned work by label or API, and run in Ore's sandboxes (section 4.5) or externally.

### 4.2 Read-only sessions and the safe-outputs gateway
- An agent session gets a **read-only token** scoped by default to one repository (the Invariant Labs mitigation and the Copilot default).
- Every write (comment, label, push, open change, request review, approve, deploy) passes through the **safe-outputs gateway**: a schema-validated request (`spec/safe-outputs/`), checked against policy (Cedar), rate-limited per type, executed with a separately scoped credential, logged on the ledger with the agent and session id. The gateway refuses writes to repositories outside the session scope, strips credentials from payloads, and blocks loops (an agent's write cannot trigger the same agent unless explicitly allowed).
- Actor kind is a first-class event attribute; pipelines and apps can gate on "triggered by human with write access" exactly as GitHub-compatible actions expect.

### 4.3 Untrusted content
- Comments, issues and changes carry a **provenance tag**: member, external contributor, bot, agent. Agents reading untrusted content through MCP receive it with the tag, and repositories can enable a **quarantine view** in which agents see external text only through a sanitized, summarized rendering until a member marks it as reviewed.
- Tool definitions and plugins are pinned by digest; a changed description or schema triggers re-consent (defense against tool poisoning and rug pulls).

### 4.4 Provenance on the ledger, not in trailers
- Each commit and, where known, each hunk records `{author_kind, agent_id, model, session_id, prompt_hash}` as structured metadata, mirrored into `refs/meta/` and surfaced in the change view, blame, filters and audit.
- Repositories can **require** provenance (reject pushes without it from agents), display an "agent-authored" badge, and set per-repository policies: agent changes draft-by-default, maximum open agent changes, required rationale and validation sections, auto-close for non-member agent submissions, and whether agent approvals count toward required reviews (never, by default).
- `Co-authored-by` / `Assisted-by` trailers are still parsed for compatibility and converted into provenance; they are never the source of truth.

### 4.5 Sandboxes for agent work
- Ore offers the same microVM isolation used for pipelines to agent sessions: ephemeral VM, repository checkout, no git credentials inside the VM (a credential proxy attaches scoped tokens to outbound git operations), egress allow-list, network secrets by reference, per-session budget, session logs on the ledger. Claude Code, Codex and comparable agents can be launched into these sandboxes from an issue, a change, chat or the CLI.

### 4.6 Agent-grade review API
- Reviews are structured: findings with severity, confidence, file ranges, suggested fixes, and a category; duplicates are clustered; findings persist across patch sets; batch "apply suggestion" exists; reviewers can accept, dismiss with reason, or escalate.
- Bot and agent reviews render in a **separate lane** collapsed by default; human reviewers see a summary count and can expand.
- The **agent inbox** gives maintainers a triage view across repositories: pending agent changes, their validation status, cost so far, and one-click actions (request changes, close, convert to issue).

### 4.7 The MCP server
- Toolsets: `context` (who am I, what repo, AGENTS.md, repo wiki), `repos`, `changes`, `issues`, `pipelines`, `deployments`, `environments`, `search` (text, symbol, semantic), `wiki`, `learn`, `policy` (explain why an action is denied). Default set is read-mostly; writes go through the gateway.
- Auth: OAuth 2.1 with RFC 9728 protected-resource metadata, PKCE, resource indicators, audience-bound tokens, client ID metadata documents; no token passthrough.
- Deterministic tool ordering and cache TTLs for prompt caching; OpenTelemetry trace context in `_meta`; published to the official registry; `--read-only` and single-repository modes; rate limits per plan.

## 5. Plugins (P3)

- Runtime: WASM components (WASI 0.3) with capability-based imports (which API calls, which events, which storage), executed in-process with CPU and memory limits; Extism host for broad SDK language coverage until the Component Model matures.
- Extension points: event handlers, UI panels (sandboxed iframes with a typed bridge), pipeline task types, policy functions, importers, renderers for file types.
- Signed, pinned, reviewed; the catalog shows the capability manifest and its diff on every update.

## 6. Identity connectors for buyers

SAML and SCIM free for organizations up to 25 users and in every Enterprise subscription; tested against Entra ID, Okta and PingFederate; mixed providers allowed; IP allow-lists apply to tokens and SSH, not just the web; break-glass admin; SSH certificate authorities with ≤366-day certificates; signed-commit verification that honors SSH certificates and Sigstore/gitsign (a gap at GitHub today); fine-grained tokens with admin approval and expiry.

## 7. Event catalog (excerpt)

```
io.ore.repository.{created,archived,transferred}
io.ore.ref.{updated,deleted}                      (with old/new, actor_kind, policy decision)
io.ore.change.{opened,patchset.pushed,review.submitted,approved,merged,closed}
io.ore.stack.{created,updated,merged}
io.ore.issue.{opened,updated,delegated,closed}
io.ore.pipeline.run.{queued,started,task.started,task.finished,finished}
io.ore.artifact.{published,attested}
io.ore.release.{created,signed}
io.ore.environment.{created,policy.updated}
io.ore.deployment.{requested,approved,started,succeeded,failed,rolled_back}
io.ore.rollout.{stage.started,stage.promoted,aborted}
io.ore.incident.{opened,resolved}
io.ore.security.{secret.detected,secret.bypassed,alert.opened,alert.resolved}
io.ore.agent.session.{started,waiting,completed,errored,write.gated}
io.ore.lesson.run.{started,step.completed,completed}
io.ore.credential.issued
io.ore.policy.decision                             (sampled; full stream in audit)
```
