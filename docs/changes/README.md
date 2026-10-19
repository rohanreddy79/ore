# Parallel slices: ownership map

Each slice is built in its own worktree and branch, verified with `scripts/e2e.sh` (format, vet, tests, the Docker demo) and merged into `main` only when green. To keep merges conflict-free, every slice owns its own files and wires itself in through `internal/registry` (a `register.go` with an `init()` that calls `registry.Register`), never by editing `cmd/ore/main.go` or `internal/web/web.go`.

| Slice | Branch | Owns (new files only unless listed) | Migration file | Registers as |
|---|---|---|---|---|
| MCP server + safe-outputs gateway | `slice/mcp-gateway` | `internal/connect/mcp/`, `internal/connect/gateway/`, `spec/mcp/`, `spec/safe-outputs/` | `0002_gateway.sql` | `connect.mcp`, `connect.gateway` |
| GitHub adapter | `slice/github-adapter` | `internal/fpi/github/`, `internal/fpi/fpi.go` (new) | `0003_github.sql` | `fpi.github` |
| Identity: users, tokens, SSH keys, SSH transport | `slice/identity-ssh` | `internal/identity/` (incl. an `sshd` subpackage) | `0004_identity.sql` | `identity` (sets `Server.Authenticate` and the git SSH listener) |
| Pipelines: Actions YAML compiler, caches, artifacts, secrets by reference | `slice/pipelines-actions` | `internal/pipelines/actions/`, new files in `internal/pipelines/` (existing files: additive edits only, exported API unchanged) | `0005_pipelines.sql` | `pipelines.actions` |
| Delivery: environments with policy and approvals, remote SSH deploys, health-gated rollouts with auto-rollback, preview TTL | `slice/delivery-envs` | new files in `internal/delivery/` (existing files: additive edits only) | `0006_delivery.sql` | `delivery.environments` |
| Review: patch sets, interdiffs, comments, stacks, landing strategies incl. optimistic with auto-revert, bot lane | `slice/review-v1` | new files in `internal/review/` (existing files: additive edits only) | `0007_review.sql` | `review.v1` |

Shared rules:
- Build and test with `CGO_ENABLED=0`; never call `/usr/bin/make` on this machine.
- New tables carry `org_id`; new events use the `io.ore.<entity>.<verb>` form and are written through `ledger.Tx.Emit` in the same transaction as the entity change.
- Routes: `s.AddRoutes(func(mux *http.ServeMux){ ... })` with `s.RequireActor` for auth, `web.JSON`/`web.JSONError`/`web.Decode` for bodies. Services other slices may need go in `s.Services["<name>"]`.
- Tests: unit tests per file, an httptest end-to-end test per feature, and an explicit edge-case test list in the slice's `docs/changes/<slice>.md`.
- Do not edit `docs/09-roadmap.md`, `README.md` or another slice's files; describe what landed in `docs/changes/<slice>.md`.
- Commit on your branch with `git commit -s` (DCO) and descriptive messages; do not push (there is no remote yet); do not merge into `main`.

## Wave 1 (merged into `main` on 2026-10-18)

All six slices merged; one integration fix followed (change comments live in the review store, the gateway keeps issue comments). `scripts/e2e.sh` passes on the merged `main`. Hand-over notes: `mcp-gateway.md`, `github-adapter.md`, `identity-ssh.md`, `pipelines-actions.md`, `delivery-envs.md`, `review-v1.md`.

## Wave 2: ownership map

Same rules as wave 1. Wave 2 is built by the wave-1 sessions on their existing branches after merging `main` (review → agent fleet, pipelines → learning, identity → UI, mcp → webhooks, delivery → operations, github → importer); the services from wave 1 are available on `web.Server` (`s.Services["gateway"]`, `s.Services["identity"]`, `s.Services["review.landing"]`, `s.Services["review.v1"]`, delivery environments under `internal/delivery/environments`, pipelines actions and secrets under `internal/pipelines`).

| Slice | Branch | Owns | Migration file | Registers as |
|---|---|---|---|---|
| Agent fleet: roles, adversarial review, watchers, budgets, evidence-based autonomy levels, landing by level | `slice/agent-fleet` | `internal/agent/fleet/` (new), new files in `internal/agent/` (existing `agent.go`: additive edits only, exported API unchanged) | `0008_fleet.sql` | `agent.fleet` |
| Learning engine: lessons as pipelines, lesson runs, learn-at-the-error, first track | `slice/learn` | `internal/learn/`, `lessons/` | `0009_learn.sql` | `learn` |
| Web UI v1: pages for everything wave 1 added, live logs over SSE, ledger filters | `slice/ui-v1` | `internal/web/templates.go`, new `internal/web/ui_*.go` files, additive edits to `internal/web/web.go` (this slice alone may edit `web.go`) | none | n/a (part of web) |
| Webhooks out: CloudEvents over Standard Webhooks from the ledger, retry, replay, delivery dashboard; audit export | `slice/webhooks` | `internal/connect/webhooks/` | `0010_webhooks.sql` | `connect.webhooks` |
| Operations: Postgres profile, backups and restore, release packaging, install script, `ore migrate`/`backup` commands | `slice/ops` | `internal/ledger/ledger.go` and existing migration SQL files (compatibility edits only), `internal/ledger/pg/` (new), `deploy/`, `scripts/release.sh`, `cmd/ore/main.go` (this slice alone may edit `main.go`) | none new | n/a |
| GitHub importer with fidelity report (follow-up in the GitHub adapter session) | `slice/github-import` | new files in `internal/fpi/github/` | `0011_github_import.sql` | `fpi.github` (existing) |

Routes: every slice mounts under its own prefix (`/api/v0/fleet/`, `/api/v0/learn/`, `/api/v0/webhooks/`, `/api/v0/repos/{org}/{name}/import/`) so patterns cannot collide; the UI slice owns the HTML routes.

## Wave 2 addition: the web app (GitHub-class UI)

| Slice | Branch | Owns | Migration file | Registers as |
|---|---|---|---|---|
| Web app: a TypeScript single-page application with a design system, served by the binary; the primary UI | `slice/webapp` | `ui/` (the app), `internal/webapp/` (embeds the built app and mounts it under `/app/`; no edits to `internal/web/`) | none | `webapp` |

The server-rendered pages from the "UI v1" slice stay as the no-JavaScript fallback and keep providing the SSE and diff endpoints the app consumes.

## Wave 2 (merged into `main` on 2026-10-19)

All six slices merged cleanly; two integration fixes followed: the `connect/webhooks` blank import in `cmd/ore/main.go` (the webhooks slice could not edit that file), and a dialect-aware `ledger.DB.TableExists` replacing a `sqlite_master` query in the UI that failed on Postgres. `scripts/e2e.sh` (SQLite, Docker demo) and `scripts/test-postgres.sh` pass on the merged `main`. Hand-over notes: `agent-fleet.md`, `learn.md`, `ui-v1.md`, `webhooks.md`, `ops.md`, `github-import.md`. The web app (`slice/webapp`) arrives separately as a pull request.
