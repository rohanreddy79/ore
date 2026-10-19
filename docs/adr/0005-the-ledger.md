# ADR 0005: The ledger is the central primitive

**Status:** Proposed · **Date:** 2026-10-18

## Context
GitLab's DORA is blind to pull-based deployments because nothing records them; GitHub's webhooks have no retry; audit, compliance and agent context are sold as separate products everywhere (research/02 §5, research/03 §7). The product promise is "one place".

## Decision
All entities (commit, change, patch set, pipeline run, artifact, attestation, environment, release, deployment, incident, lesson run, credential) live in one typed graph with provenance edges. Every transition writes a CloudEvent in the same transaction (outbox) and fans out to webhooks, indexers, learning, MCP subscriptions and JetStream. DORA, CRA reports, audit exports, cost reports and the MCP context server are views over the ledger and have no stores of their own. Consumers can replay.

## Consequences
Schema discipline: a feature that does not model its entity in the ledger is rejected at design review. Event catalog in `spec/events/` is a public contract. Storage grows with activity; retention and compaction policies are required from phase 1.
