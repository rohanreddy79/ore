# ADR 0011: Cedar is the policy engine for humans, apps, agents and deployments

**Status:** Proposed · **Date:** 2026-10-18

## Context
Rulesets, branch protection, required checks, deployment approvals, CODEOWNERS, agent permissions and app scopes are all authorization decisions; incumbents implement each separately and CODEOWNERS is silently ignored past 3 MB (research/06 §2).

## Decision
All authorization is expressed as Cedar policies over a typed entity model (principals, repositories, refs, environments, artifacts, actions, agents). Org admins edit policy in the UI or as code; CODEOWNERS and rulesets are compiled into Cedar. Every decision is logged.

## Alternatives
OPA/Rego (more general, slower to analyze, less typed); bespoke rule tables (the incumbent approach; not composable).

## Consequences
Policy authoring UX is a product surface; a policy test harness ships with the CLI; performance of evaluation on the git push path must be budgeted (sub-millisecond per decision).
