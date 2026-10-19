# ADR 0009: Agents are delegates with identity, a mandatory write gateway, and structured provenance

**Status:** Proposed · **Date:** 2026-10-18

## Context
Invariant Labs exfiltrated private data through the GitHub MCP server via a prompt injection in a public issue; GitHub's Agentic Workflows answer with read-only agent jobs and a schema-validated "safe outputs" gateway; Linear's model keeps a human assignee and a delegated agent; commit trailers are contested and stripped by squash merges; agent PRs need 1.62× more follow-up fixes (research/03 §2–3).

## Decision
Agents are a principal type with a human owner, session state, budgets and capability scopes. Agent sessions get read-only tokens; every write passes through the safe-outputs gateway (schema validation, per-type limits, policy check, separately scoped credentials, loop prevention on actor type). Issues keep a human assignee and may have a delegated agent. Provenance (human, agent, model, session id, prompt hash) is structured metadata on commits and hunks, surfaced in the UI, filters and audit, and can be required per repository. Repositories can set agent PR quotas, draft-by-default and required rationale/validation fields.

## Consequences
A new gateway service and schema registry in phase 1; the MCP server and the GitHub-compatible API both route agent writes through it; trailers are still accepted for compatibility but never relied on.
