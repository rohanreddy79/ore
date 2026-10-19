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
