# deploy

- `../cmd/ore` builds to one static binary; `ore serve` needs `git` on the host and `docker` for container pipelines and deployments (`--executor host` runs pipelines on the host; `--executor none` records only).
- `Dockerfile` builds the web app in a Node stage, embeds it in the binary and packages that with git (including `git-http-backend`, from Alpine's `git-daemon`) and the docker CLI (multi-arch with `docker buildx build --platform linux/amd64,linux/arm64`); `solo/compose.yaml` runs it with the Docker socket mounted, an optional Postgres (`COMPOSE_PROFILES=postgres`) and an optional Litestream replicator (`COMPOSE_PROFILES=litestream`).
- `systemd/ore.service` runs the binary on a host; `install.sh` installs a release (checksum-verified, no sudo); `litestream.yml` replicates the SQLite ledger to S3; `RELEASING.md` is the release process.
- `ore demo` runs the whole loop on a sample repository in a temporary data directory (see the README).

Data layout under `--data`: `ore.db` (SQLite ledger; unused with `--db postgres://...`), `repos/<org>/<name>.git` (bare repositories), `logs/` (pipeline and agent logs), `token` (the admin token, mode 0600), `secrets.key` (the secrets store key), `artifacts/`, `cache/`.

Operations: `ore doctor` (git, docker, ports, data directory, database, disk), `ore config` (effective configuration, secrets redacted), `ore migrate status|apply`, `ore backup --to DIR|s3://bucket/prefix`, `ore restore --from DIR`, `ORE_BACKUP_TO` + `ORE_BACKUP_INTERVAL` + `ORE_BACKUP_KEEP` for scheduled backups, `--log-format json`, `--log-level`, `/metrics` (Prometheus text format), `X-Request-ID` on every response, graceful shutdown that drains running pipelines and agent runs for `--drain-timeout`. Details in `docs/changes/ops.md`.

Not yet: Helm, object storage for git, cells. See `docs/09-roadmap.md`.
