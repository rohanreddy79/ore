# deploy

- `../cmd/ore` builds to one static binary; `ore serve` needs `git` on the host and `docker` for container pipelines and deployments (`--executor host` runs pipelines on the host; `--executor none` records only).
- `Dockerfile` packages the binary with git and the docker CLI; `solo/compose.yaml` runs it with the Docker socket mounted.
- `ore demo` runs the whole loop on a sample repository in a temporary data directory (see the README).

Data layout under `--data`: `ore.db` (SQLite ledger), `repos/<org>/<name>.git` (bare repositories), `logs/` (pipeline and agent logs), `token` (the admin token, mode 0600).

Not yet: Postgres profile, Helm, backups (Litestream), SSH transport. See `docs/09-roadmap.md`.
