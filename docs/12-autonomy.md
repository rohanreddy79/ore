# Autonomy: software that fixes, ships and watches itself

Ore's reason to exist is that software delivery is becoming something agents do end to end. This document defines what "no human in the loop" means precisely, how Ore makes it safe enough to be real, and how it goes further than GitHub's Agent HQ, Cursor's Origin and Anthropic's internal Scion (see `research/07-anthropic-scion.md`).

## 1. The loop

Every unit of autonomous work in Ore runs the same loop, recorded step by step on the ledger:

```
  signal ──▶ plan ──▶ change ──▶ verify ──▶ land ──▶ deploy ──▶ observe ──▶ (fix | revert | done)
   │                                                                         │
   └──── an issue, a failing pipeline, an alert, a dependency update, ◀──────┘
         a security finding, a schedule, a user request, another agent
```

- **Signal**: anything on the ledger can start work: an issue, a red pipeline on main, a production alert, a new CVE in the SBOM, a dependency release, a schedule, an MCP request from a person or another agent.
- **Plan**: the agent reads the repository through the API (no clone needed for reading), the repo wiki, AGENTS.md, the relevant ledger history (what was tried before), and writes a plan with scope, rationale and the validation it intends to run. The plan is a ledger object; policy can require it.
- **Change**: the agent edits in a sandbox (or purely through the API for small changes: server-side `commit-tree`), producing a change with provenance (agent, model, session, prompt hash) and a stable change-id.
- **Verify**: the pipeline runs the change's tests, the repository's policy checks, scanners, and the agent's declared validation; a preview environment is deployed and probed; an adversarial review agent (a different model or prompt) critiques the change in the bot lane. Results are structured findings, not prose.
- **Land**: depending on the autonomy level (section 2): wait for a human; merge if green; or merge optimistically and verify after merge. Merges are server-side (`merge-tree`, compare-and-swap ref update), so nothing has to be pushed and conflicts are resolved by an agent in the loop, as Anthropic reports for Scion.
- **Deploy**: a release is cut with attestations and an SBOM; deploy-time policy runs; the rollout proceeds in stages with metric analysis.
- **Observe**: watcher agents ("superfans" in Anthropic's term) subscribed to the component's telemetry, logs, error rates and SLOs compare the rollout against the baseline.
- **Fix or revert**: regression → automatic rollback and a new signal ("fix the regression introduced by change X"), with the full ledger context. Success → the issue closes with the evidence attached.

Nothing in this loop requires a human to click. Everything in it requires **policy** a human wrote once and **budgets** a human set once.

## 2. Autonomy levels

Like driving automation, autonomy is a per-repository, per-environment setting, not a global switch.

| Level | Name | Agents may | Humans do | Default for |
|---|---|---|---|---|
| L0 | Manual | nothing; the forge is a tool | everything | regulated code paths, by policy |
| L1 | Assist | comment, review, suggest fixes | author and merge | new teams |
| L2 | Author | open changes with provenance, run pipelines, deploy previews | review and merge | **default for new repositories** |
| L3 | Land | merge when verification is green and policy allows; deploy to non-production | watch dashboards, set policy, handle escalations | teams with good tests |
| L4 | Ship | merge optimistically with post-merge verification; deploy to production through staged rollouts; auto-revert | set budgets and SLOs; review weekly | mature services with SLOs and canaries |
| L5 | Operate | all of L4 plus: open their own signals from telemetry, update dependencies, fix security findings, refactor toward declared goals, within budget | own the goals | what the founder asked for: "no human contact" |

Promotion between levels is itself evidence-driven: Ore computes, per repository, the agent change acceptance rate, post-merge fix rate, rollback rate and failed-deployment recovery time, and recommends (or, by policy, performs) level changes. Demotion is automatic on defined signals (section 5).

L5 is a real target, not marketing, with two honest caveats the evidence imposes: today's agent changes need more follow-up fixes than human ones and reviewer engagement predicts merge quality (`research/03`), so **L5 is only safe where verification is strong** (tests, previews, canaries, SLOs, rollbacks). Ore's job is to make that verification cheap and default, which is exactly what the delivery and pipelines pillars build.

## 3. What makes "no human" safe enough

1. **Verification is infrastructure, not a step.** Preview environments per change, staged rollouts with metric analysis, instant rollback and watcher agents are built in (`docs/04`). An agent cannot skip them; policy decides how much is required at each level.
2. **Policy is code a human wrote once.** Cedar policies express what may change where (paths, kinds of change, environments, budgets, required validation). Anthropic's Antique expresses "semantic approval constraints" internally; Ore makes that a first-class, open policy language.
3. **Every write goes through the gateway.** Agents hold read-only tokens; writes are schema-validated, policy-checked, rate-limited and logged (`docs/05` §4). Prompt injection in an issue cannot exfiltrate or push.
4. **Budgets bound the blast radius.** CI minutes, preview minutes, model spend, open changes, writes per hour, deployments per day, per agent and per repository. An agent that exceeds a budget stops and files a signal; it does not escalate silently.
5. **Adversarial review is built in.** A second agent, with a different model or a different prompt, reviews every agent change for correctness, security and scope creep before landing at L3+; disagreements become findings a human can audit later.
6. **Provenance is structural.** Every hunk knows its author kind, agent, model and session. Blame, audit and rollback decisions use it.
7. **Optimistic landing is bounded.** At L4+, post-merge verification runs immediately on main; a red main triggers an automatic revert within minutes and a fix signal. This is the Scion pattern (no global merge lock, post-merge and pre-deploy testing) made available with a safety net.
8. **Kill switches.** Any human with the right role can drop a repository or environment to a lower level instantly; the agent fleet pauses; in-flight rollouts roll back.

## 4. The agent fleet

Ore does not ship a model. It ships the **roles**, the **context** and the **harness**:

- **Roles**: implementer, reviewer (adversarial), tester, releaser, watcher, dependency-updater, security-fixer, documenter, planner. Each is an app with capabilities, a budget and a prompt profile; any model vendor or local model can back any role.
- **Context**: the ledger (what happened), the repo wiki (how it is built), AGENTS.md (how to work here), policies (what is allowed), telemetry (what is happening), and memory (what this agent learned about this repo, stored as ledger-attached notes). This is the "organization brain": a knowledge graph of everything the organization has built, decided, shipped and learned, queryable over MCP. It is not a model and it is not a claim about intelligence; it is the context that makes any model dramatically more useful here than anywhere else.
- **Harness**: sandboxes without credentials, the write gateway, structured findings, pre-commit validation (`ore check`), server-side commits for small changes, and the loop runner that advances work and records it.

Agents from any vendor plug in through MCP and the GitHub-compatible API; Claude Code, Codex, Cursor, Jules, Devin and open-weight models all work on day one. Ore's own loop runner is model-agnostic.

## 5. Signals that demote a level automatically

Rollback rate above threshold in a window; post-merge fix rate above threshold; a security finding introduced by an agent change; budget overrun; a verification component unavailable (no preview capacity, metric source down); a human marks a change "should not have landed". Each demotion is a ledger event with the evidence.

## 6. Why this beats the alternatives

- **GitHub Agent HQ** assigns agents to issues and shows a mission-control view; it stops at the pull request, has no environments, previews, rollouts or budgets, and bills tokens unpredictably. Ore owns the whole loop and prices autonomy by the seat.
- **Cursor Origin** is a forge for agent-authored code with review at the center; it is closed, has no deployment, and ties you to one vendor's agents. Ore is open and vendor-neutral.
- **Scion** proves the server-side, API-first, post-merge-verified model works at 30,000 merges a day, and Anthropic says it is not for general use and will not be released. Ore delivers the same properties to everyone, git-compatible.
- **Vercel and Railway** deploy well and know nothing about who wrote the code, why, or whether it should ship; Ore's deploy-time policy, rollouts and watchers close the loop.

## 7. What v0 demonstrates

The v0 in this repository runs the loop end to end on one machine with one binary: an issue is filed, an agent (any command, Claude Code by default) produces a change, the pipeline verifies it in a container, the change is merged server-side, a preview is deployed, and every step is a CloudEvent on the ledger. `docs/09-roadmap.md` lists what each later version adds.
