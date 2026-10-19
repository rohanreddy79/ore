# ADR 0003: Own the pipeline control plane; runners are pluggable; BYO runners are free forever

**Status:** Proposed · **Date:** 2026-10-18

## Context
GitHub Actions' dispatcher is both its least reliable component (57 outages in a year) and the thing GitHub tried to monetize with a $0.002/min charge on customers' own runners, retracted within a week (research/02 §1). Depot CI proves a third party can run Actions syntax without GitHub's control plane. Runner vendors raised $67M in 2026 selling execution.

## Decision
Ore owns dispatch, secrets issuance, caching, OIDC token issuance, log ingest and status. Execution speaks a versioned, Apache-2.0 Runner Protocol implemented by Ore's microVM runner, a compatibility executor for self-hosters, customer-owned machines, and partner fleets. The licence and the price list guarantee that customer-owned runners never incur a platform fee.

## Consequences
Ore must run its own scheduler, cache (content-addressed, scoped), secrets enclave and OIDC issuer from phase 1; partner compute becomes a channel rather than a competitor.
