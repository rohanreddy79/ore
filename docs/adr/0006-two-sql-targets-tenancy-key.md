# ADR 0006: Exactly two SQL targets; tenancy key on every row; schema domains

**Status:** Proposed · **Date:** 2026-10-18

## Context
GitHub spent years retrofitting partitioning; GitLab's Cells are still an experiment; Forgejo's three-backend matrix costs it HA work; Codeberg hit a single-writer ceiling (research/06 §5).

## Decision
SQLite for the solo profile, Postgres for team and cloud, nothing else. Every table carries `org_id`. Tables are grouped into schema domains; a CI linter rejects cross-domain joins outside explicit interfaces. Jobs and events use a transactional outbox in the same database. Valkey is never authoritative.

## Consequences
Migrations are tested twice; some Postgres features are unavailable or emulated on SQLite; sharding by organization is possible later without an application rewrite.
