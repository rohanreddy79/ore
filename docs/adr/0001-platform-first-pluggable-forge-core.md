# ADR 0001: Platform-first, with a pluggable forge core

**Status:** Superseded by ADR 0013 · **Date:** 2026-10-18

## Context
A forge from scratch (git hosting, PRs, issues, packages, SSO, migrations, 5,000+ UI strings) is a multi-year effort; Forgejo is maintained by roughly a dozen part-paid contributors on about €40K/year and still ships quarterly (research/05 §7). The differentiators the research identifies — the ledger, delivery, the pipeline control plane, agent governance, learning — are all *above* the repository (research/00 §7). Teams on GitHub will not migrate first; tools that live on top of GitHub (Vercel, Graphite, CodeRabbit) get distribution without asking anyone to move.

## Decision
Build Ore as a platform layer that owns everything above the repository, behind a Forge Provider Interface (FPI). Back the FPI with Forgejo running headlessly on day one, add a GitHub adapter in the same phase so Ore's delivery, learning and agent features work on GitHub repositories without migration, add a GitLab adapter in phase 2, and replace Forgejo with a native object-storage git core (Rust) in phase 3 when the triggers in `docs/09-roadmap.md` are met.

## Alternatives
- **Fork Forgejo and add features inside it.** Fastest demo, but inherits a single-writer architecture, a Fomantic/Vue UI, three database backends and a codebase that drifts from upstream; the differentiators would be entangled with the core and impossible to offer on GitHub.
- **Rust monolith from scratch.** The right end-state architecture (research/06), but 18–30 months to reach parity on basics, with no product and no revenue meanwhile.

## Consequences
- Two processes in phase 1 (Ore + Forgejo), one installer; the single static binary arrives with the native core.
- Identity lives in Ore and is pushed down to Forgejo; some upstream contributions to Forgejo are required (OIDC token exchange, hook payloads).
- The FPI must be designed as a public, versioned contract from the start, because four providers will implement it.
