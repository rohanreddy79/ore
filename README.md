# Ore

> **Name: Ore.** Raw material, refined into tools. Three letters, one syllable, pronounceable in every launch language. Trademark search pending; see `docs/08-business-and-licensing.md` §1.

**Run your own GitHub, CI, deploy platform and agent fleet with one binary, and let the agents fix, verify and ship your software end to end while you set the policy.**

Ore is an open-source platform for the whole software loop — signal, plan, change, verify, land, deploy, observe, fix — run by agents under policy a human wrote once, on a source-control service designed for agents and compatible with every git tool. One binary hosts repositories, runs pipelines, deploys software, governs agents and teaches people. Self-host-first, AGPL-licensed with a non-profit trademark successor, light enough for a two-person team, usable on top of GitHub before anyone migrates.

## Status

**v0 runs.** One Go binary (`ore`) with: git hosting over smart HTTP with a protected default branch; issues; changes with a stable change-id and **server-side merge** (`merge-tree` + compare-and-swap, no working copy); **server-side commits through the API** (no clone); container pipelines from `.ore/pipeline.yaml` (or inferred); container deployments to preview and production with rollback; **the agent loop** (any command as the agent, Claude Code by default) that plans, changes, verifies, lands and deploys according to the repository's autonomy level; a CloudEvents **ledger** behind every step; a REST API, a CLI and a minimal UI. `ore demo` runs the whole loop on a sample repository in about 15 seconds on a laptop (transcript: `deploy/solo/DEMO.md`). Tests cover every package, including a real `git push` over HTTP and the full loop. Everything after v0 is in `docs/09-roadmap.md`; the research is in `research/`, the design in `docs/`, the decisions in `docs/adr/`, the contracts in `spec/`.

## The three-minute demo

```bash
CGO_ENABLED=0 go build -o bin/ore ./cmd/ore
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

Claude Code must be signed in on the machine running `ore serve` (run `claude` once interactively); any other agent is one command away through `ORE_AGENT_CMD`, which receives the prompt in `ORE_PROMPT` and runs inside a checkout on the agent's branch.

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
