# ADR 0015: Autonomy levels, with verification as infrastructure

**Status:** Proposed · **Date:** 2026-10-18 · **Extends:** 0009

## Context
The founder's goal is software that fixes itself and ships to production with no human in the loop. The evidence says agent changes currently need more follow-up fixes and that reviewer engagement predicts quality (`research/03`), and that provenance alone does not stop bad releases (`research/02`). ADR 0009 made agents delegates with a write gateway; that governs *how* agents write, not *how far* they may go.

## Decision
Autonomy is a per-repository and per-environment level from L0 (manual) to L5 (operate), defined in `docs/12-autonomy.md`. New repositories default to L2 (agents author, humans land). Promotion to L3–L5 is evidence-gated (acceptance rate, post-merge fix rate, rollback rate, recovery time) and requires the verification stack the level needs (tests, previews, staged rollouts, SLOs, watchers). Demotion is automatic on defined signals. Budgets bound every level. Kill switches exist at repository, environment and instance scope. L5 is explicitly supported where verification is strong; Ore's product work is to make that verification default and cheap.

## Alternatives
A single "autonomous mode" switch (unsafe and uninsurable); human approval always (does not meet the goal and does not scale past a few hundred merges a day).

## Consequences
The ledger gains level-change events with evidence; policy gains level-aware rules; the loop runner implements three landing strategies; the UI shows the level and the evidence for it on every repository.
