# Runner Protocol v0 outline (Apache-2.0)

Between the control plane (dispatcher) and any executor: Ore's microVM runner, the compatibility executor, customer-owned machines (free forever), partner fleets.

- **Register**: runner presents a registration token (scoped to org/repo/labels, single-use for ephemeral runners) and its capabilities (os, arch, sizes, executor kind, nested virtualization, egress control); receives a short-lived runner credential.
- **Lease**: long-poll or stream for a task matching labels; a lease carries the task manifest (resolved IR task), a per-job OIDC token, a secrets-channel ticket, cache tokens scoped by trust level, and artifact upload tickets.
- **Secrets channel**: the runner opens an authenticated channel per job; secrets are delivered into the sandbox (file mounts or per-step environment) and never to the runner agent's own process memory.
- **Logs**: NDJSON stream with step ids and timestamps; the control plane indexes as it ingests so log search works mid-run.
- **Cache**: content-addressed get/put with trust-scoped tokens; Remote Execution API endpoints (Phase 2).
- **Artifacts and attestations**: upload by digest; the control plane signs build provenance (SLSA) with platform-held keys.
- **Heartbeat / cancel / timeout**: lease renewal; cancellation is cooperative then forced.
- **Version negotiation**: runners announce protocol versions; deprecations are announced with at least a six-month window; no 30-day cut-offs.
