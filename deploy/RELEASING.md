# Releasing Ore

A release is a version in `VERSION`, four static binaries, their checksums, a
changelog and the install script. `scripts/release.sh` builds all of it into
`dist/`; tagging and publishing are deliberate, separate steps that this
repository does not automate (there is no remote yet).

## 1. Prepare

```bash
scripts/e2e.sh                 # format, vet, tests, build, the Docker demo
scripts/test-postgres.sh       # the same tests on Postgres in a throwaway container
```

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

Container image (multi-arch, built from the same tree):

```bash
docker buildx build --platform linux/amd64,linux/arm64 -t ghcr.io/orevcs/ore:X.Y.Z -f deploy/Dockerfile .
docker run --rm ghcr.io/orevcs/ore:X.Y.Z version
```

## 3. Verify

```bash
tar -xzf dist/ore_X.Y.Z_$(uname -s | tr A-Z a-z)_arm64.tar.gz -C /tmp
/tmp/ore_X.Y.Z_*/ore version
ORE_INSTALL_URL=file://$PWD/dist ORE_INSTALL_DIR=/tmp/orebin sh deploy/install.sh && /tmp/orebin/ore doctor
```

## 4. Tag and publish (by a maintainer, by hand)

```bash
git tag -s vX.Y.Z -m "Ore X.Y.Z"
git push origin vX.Y.Z
```

Upload `dist/*` to the release page or to the directory that
`https://get.ore.dev/releases/` serves: `install.sh` fetches
`<url>/VERSION` (when `ORE_VERSION` is unset), `<url>/ore_X.Y.Z_<os>_<arch>.tar.gz`
and `<url>/SHA256SUMS`, and refuses an archive whose checksum does not match.
Push the image with the same tag.

## 5. Upgrade notes for operators

- `ore migrate status` shows pending migrations; `ore serve` applies them on
  start, `ore migrate apply` applies them ahead of time. Concurrent migrators
  serialize (Postgres advisory lock; SQLite write lock with re-checks).
- Take a backup first: `ore backup --to <dir>` (or rely on the scheduled one,
  `ORE_BACKUP_TO`). `ore restore --from <dir>` rebuilds an empty data directory.
- `ore doctor` before starting the new binary; `ore config` to see what it will use.
