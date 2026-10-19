# ADR 0007: Go for the platform now, Rust for the native data plane later

**Status:** Proposed · **Date:** 2026-10-18

## Context
Go owns the forge control-plane ecosystem (Forgejo, Gitaly, Zoekt, Woodpecker, OpenBao, OpenTofu, Dagger); Rust owns the 2025–26 git-storage and search data planes (Blackbird, Mononoke, Enroute, Tangled, gitoxide, git 3.0 itself). gitoxide is ready for reads but not push or tree merge (research/06 §8).

## Decision
The platform (`cmd/`, `internal/`) is Go, so Forgejo and the Go ecosystem can be reused directly and a single static binary is natural. The native git core and the search core in phase 3 are Rust services behind the FPI and the search interface, using gitoxide for reads and the git binary for writes until parity.

## Consequences
Two languages in the long run with a hard process boundary between them (FPI over Connect-RPC); hiring for Rust can wait until phase 3.
