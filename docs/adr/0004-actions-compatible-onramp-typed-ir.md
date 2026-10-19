# ADR 0004: GitHub-Actions-compatible on-ramp; a typed, content-addressed DAG as the native model

**Status:** Proposed · **Date:** 2026-10-18

## Context
CI compatibility was the named blocker for the Dutch government's Forgejo migration; Forgejo documents that its Actions are "not the same" (research/01 §4, research/05 §4). YAML sprawl and per-minute billing do not survive agent-driven CI volume (25× in six months at one Depot customer); content-addressed execution lets Dagger charge one fifth for repeat work (research/02 §1).

## Decision
A planner compiles GitHub Actions YAML (and later GitLab CI YAML) into the **Pipeline IR**: a typed DAG of tasks whose inputs (sources, images, actions by SHA, parameters, secrets by reference) are content-addressed, so results are cacheable and billable by work. SDKs (TypeScript, Python, Go) and a visual builder emit the same IR. A public conformance suite runs real-world workflows and marketplace actions and publishes pass rates per release.

## Consequences
Two front-ends to maintain (YAML compiler, SDKs); the IR schema in `spec/` is a public contract; cache keys and billing derive from IR hashes, which forces determinism discipline in the planner.
