# ADR 0012: Secrets via an OpenBao-compatible API; credentials reach runners only through OIDC exchange

**Status:** Proposed · **Date:** 2026-10-18

## Context
GhostAction and the actions-cool compromise scraped secrets from runner process memory; the TanStack wave poisoned caches from a fork PR; HashiCorp Vault is BSL under IBM (research/02 §1, §5).

## Decision
Ore exposes an OpenBao-compatible secrets API (embedded in solo/team, OpenBao proper in cloud). Pipelines reference secrets by path; the secrets enclave injects them into the job sandbox through a per-job channel, never as inherited environment variables of a shared runner process; cloud credentials are obtained by exchanging the per-job OIDC token (immutable subject, repository, ref, environment claims). Fork pull requests get no secrets by default. Egress policies are enforced per job with alerts.

## Consequences
The runner protocol carries a secrets channel; the OIDC issuer is a phase-1 component; self-hosters can point Ore at an existing OpenBao or Vault.
