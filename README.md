# Ore

**Run your own GitHub, CI, deploy platform and agent fleet with one binary, and let the agents fix, verify and ship your software end to end while you set the policy.**

Ore is an open-source platform for the whole software loop — signal, plan, change, verify, land, deploy, observe, fix — run by agents under policy a human wrote once, on a source-control service designed for agents and compatible with every git tool. One binary hosts repositories, runs pipelines, deploys software, governs agents and teaches people. Self-host-first, AGPL-licensed with a non-profit trademark successor, light enough for a two-person team, usable on top of GitHub before anyone migrates.

## Status

**v0.1 on `main`** (18–19 October 2026): one Go binary, `ore`, with

- **Source control for agents**: git over HTTP and SSH with users, scoped tokens, SSH keys and agent principals; a protected default branch; server-side commits through the API (no clone) and server-side merges with compare-and-swap refs.
- **Review**: changes with stable change-ids, patch sets, interdiffs, comments in human/bot/agent lanes, structured reviews, stacks, and three landing strategies (queue, land-when-green, optimistic with automatic revert).
- **Pipelines**: `.ore/pipeline.yaml` or unmodified GitHub Actions workflows (compiled, with divergences surfaced), content-addressed tasks, trust-scoped caches, artifacts, encrypted secrets by reference, container or host execution.
- **Delivery**: environments with policy and N-of-M approvals, local or remote (SSH) Docker targets, health-gated rollouts with automatic rollback and failure issues, preview TTLs, a DORA view.
- **The loop**: an agent (Claude Code by default, any command otherwise) plans, changes, verifies, lands and deploys according to the repository's autonomy level (L0–L5), with an adversarial reviewer, budgets, watcher agents that react to failures, evidence-based level changes and a pause switch.
- **Connectors**: an MCP server with a safe-outputs write gateway, outbound CloudEvents webhooks per Standard Webhooks with retry and replay, audit export, a GitHub adapter (link, mirror, webhooks, statuses) and a GitHub importer with a fidelity report.
- **Learning**: lessons that run as pipelines on your own repository, learn-at-the-error, and the first track (five lessons; two of them in Hindi and Brazilian Portuguese).
- **Operations**: SQLite or Postgres, backups and restore, `ore migrate`, `ore doctor`, metrics, graceful drain, release packaging and an install script.
- **Two user interfaces**: a GitHub-class web app at `/app/` (design system, diff viewer with patch sets, live logs and ledger, command palette, keyboard navigation, dark and light) and server-rendered pages as the no-JavaScript fallback; a REST API, a CLI, and `ore demo`.
- **The ledger** under all of it: every transition is a CloudEvent written with the entity it describes.

Every package has tests (309 Go test functions, 70 web unit tests, 25 browser end-to-end tests); the merge gate `scripts/e2e.sh` runs them with the Docker demo, and `scripts/test-postgres.sh` runs the suite on Postgres. What each slice delivered and what it could not verify is in `docs/changes/`. The roadmap to v1 and beyond is `docs/09-roadmap.md`.

## The three-minute demo

```bash
CGO_ENABLED=0 go build -o bin/ore ./cmd/ore   # add `(cd ui && npm ci && npm run build)` first for the web app
./bin/ore demo
```

`ore demo` starts a server, creates a repository, commits a sample app **through the API without a clone**, runs the pipeline (red), files an issue, runs an agent on it, verifies the fix in a container, deploys a preview, merges server-side, deploys to production, fetches the result and prints the ledger. It is deterministic (a scripted agent) so it works without any API key. To use Claude Code as the agent on your own repositories:

```bash
./bin/ore serve --agent-cmd 'claude -p "$ORE_PROMPT" --permission-mode acceptEdits'
ore org create acme && ore repo create acme/app --level 3
ore commit acme/app -m "import" $(git ls-files)     # or: git push http://u:<token>@localhost:7777/acme/app.git main
ore issue create acme/app "Fix the flaky retry in the uploader"
ore agent run acme/app --issue 1 --wait
```

Claude Code authenticates with `ANTHROPIC_API_KEY` in the server's environment (agent runs get their own `CLAUDE_CONFIG_DIR`, so a developer's interactive login on the same machine is never used by mistake) or, on a solo machine, with a `claude` sign-in; any other agent is one command away through `ORE_AGENT_CMD`, which receives the prompt in `ORE_PROMPT` and runs inside a checkout on the agent's branch.

Autonomy levels per repository: L2 opens the change and stops (default), L3 lands it when the pipeline is green, L4 also deploys to production. The ledger (`ore events`) records every step with the actor and provenance.

## Start here

1. `docs/00-executive-summary.md` — two pages.
2. `docs/12-autonomy.md` — the loop and the autonomy levels.
3. `research/07-anthropic-scion.md` — what Anthropic built and the open equivalent of each property.
4. `docs/03-architecture.md` and `docs/adr/` — how it is built and why.
5. `docs/09-roadmap.md` — v0 to v3.

## The pillars

0. **The loop** at six autonomy levels, with verification as infrastructure, budgets, adversarial review, provenance and kill switches.
1. **The ledger** — every commit, change, build, artifact, attestation, deployment, incident and lesson linked by provenance.
2. **Source control for agents, compatible with git** — server-side commits and merges, compare-and-swap refs, change-ids, an object-storage core.
3. **Delivery built in** — environments, previews, staged rollouts, registry, secrets, flags; every deploy recorded.
4. **Pipelines you can trust** — Ore owns the control plane; GitHub Actions workflows run unmodified; typed content-addressed DAG billed by work; your own runners are never charged.
5. **Agent-native and connector-native** — app capability sets, native MCP, a mandatory write gateway, CloudEvents webhooks, one standard per category.
6. **Teaching inside** — lessons as pipelines on your own repository, learn-at-the-error, a repo wiki, a guardrailed tutor, credentials, ten languages, offline.
7. **Sovereign by design** — AGPL-3.0 server, Apache-2.0 embeddables, DCO, trademark successor, one binary to cells, air-gap, published prices.

## Repository layout

```
docs/        vision, autonomy, architecture, designs, roadmap, ADRs
research/    seven research slices and the synthesis (sourced, dated 2026-10-18)
spec/        machine-readable contracts (Apache-2.0)
cmd/ore/     the binary: serve, CLI, demo (AGPL-3.0)
internal/    ledger, gitx, githttp, pipelines, delivery, review, agent, web (AGPL-3.0)
runner/      the runner and its protocol (Apache-2.0)
sdk/         client SDKs (Apache-2.0)
lessons/     the built-in curriculum (CC BY-SA 4.0)
deploy/      Compose, install scripts, demo
```

## Licensing

- `cmd/`, `internal/`, `ui/`: [GNU AGPL-3.0-only](LICENSE)
- `spec/`, `sdk/`, `runner/`: [Apache-2.0](LICENSES/Apache-2.0.txt)
- `lessons/`: CC BY-SA 4.0
- Contributions under the [Developer Certificate of Origin](CONTRIBUTING.md); there is no CLA and there never will be (see [`GOVERNANCE.md`](GOVERNANCE.md)).
