# Releasing Ore

A release is a version in `VERSION`, four static binaries, their checksums, a
changelog and the install script. `scripts/release.sh` builds all of it into
`dist/`. Tagging is a deliberate step a maintainer takes by hand; pushing the
tag is what publishes: the `Release` workflow (`.github/workflows/release.yml`)
builds the web app, runs `scripts/release.sh` and attaches `dist/*` to the
GitHub release for that tag.

## 1. Prepare

```bash
scripts/e2e.sh                 # format, vet, tests, build, the Docker demo
scripts/test-postgres.sh       # the same tests on Postgres in a throwaway container
```

The same two scripts are the `CI` workflow (`.github/workflows/ci.yml`) on every
pull request and every push to `main`: the gate with the Playwright suite and the
Docker demo in one job, the Postgres suite in another, in parallel. Merge only on
green.

Bump `VERSION` (`X.Y.Z`, no `v`). Write the user-facing notes in `docs/changes/`
if the release carries a slice that has none.

## 2. Build

```bash
scripts/release.sh             # dist/ore_X.Y.Z_{linux,darwin}_{amd64,arm64}.tar.gz, SHA256SUMS, CHANGELOG.md, VERSION, install.sh
scripts/release.sh --only host # one target, for a quick local check
```

`CHANGELOG.md` is the git log since the last tag (the full history before the
first tag). Each archive holds `ore` and `LICENSE`; the version is stamped with
`-X main.version` so `ore version` and `/metrics` (`ore_build_info`) report it.

The binaries embed whatever is in `internal/webapp/dist` when they are built:
run `scripts/e2e.sh` (or `cd ui && npm ci && npm run build`) first so they serve
the web app rather than the "build the UI" page. The `Release` workflow does
this itself.

Container image (multi-arch, built from the same tree; the Dockerfile builds
the web app in a Node stage and embeds it, so the image always serves `/app/`):

```bash
docker buildx build --platform linux/amd64,linux/arm64 -t ghcr.io/orevcs/ore:X.Y.Z -f deploy/Dockerfile .
docker run --rm ghcr.io/orevcs/ore:X.Y.Z version
docker run --rm -d --name ore-check -p 127.0.0.1:7777:7777 ghcr.io/orevcs/ore:X.Y.Z serve --executor none
curl -s http://127.0.0.1:7777/app/ | grep -c '/app/assets/'   # > 0: the app shell, not the build page
docker rm -f ore-check
```

## 3. Verify

```bash
tar -xzf dist/ore_X.Y.Z_$(uname -s | tr A-Z a-z)_arm64.tar.gz -C /tmp
/tmp/ore_X.Y.Z_*/ore version
ORE_INSTALL_URL=file://$PWD/dist ORE_INSTALL_DIR=/tmp/orebin sh deploy/install.sh && /tmp/orebin/ore doctor
```

## 4. Tag and publish

```bash
git tag -s vX.Y.Z -m "Ore X.Y.Z"
git push origin vX.Y.Z
```

Pushing the tag runs the `Release` workflow: it checks that the tag matches
`VERSION`, builds the web app, runs `scripts/release.sh`, smoke-tests the
linux/amd64 archive and the checksums, and creates the GitHub release `vX.Y.Z`
with `dist/*` attached and `CHANGELOG.md` as the notes. The workflow's
"Run workflow" button is a dry run on any branch: it builds and attaches the
artifacts to the workflow run and creates no release.

If the release must also be served from the directory behind
`https://get.ore.dev/releases/`, copy `dist/*` there: `install.sh` fetches
`<url>/VERSION` (when `ORE_VERSION` is unset), `<url>/ore_X.Y.Z_<os>_<arch>.tar.gz`
and `<url>/SHA256SUMS`, and refuses an archive whose checksum does not match.
Build and push the image with the same tag by hand (the workflow does not).

## 5. Upgrade notes for operators

- `ore migrate status` shows pending migrations; `ore serve` applies them on
  start, `ore migrate apply` applies them ahead of time. Concurrent migrators
  serialize (Postgres advisory lock; SQLite write lock with re-checks).
- Take a backup first: `ore backup --to <dir>` (or rely on the scheduled one,
  `ORE_BACKUP_TO`). `ore restore --from <dir>` rebuilds an empty data directory.
- `ore doctor` before starting the new binary; `ore config` to see what it will use.
