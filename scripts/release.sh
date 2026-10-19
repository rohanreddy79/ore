#!/usr/bin/env bash
# Build release artifacts: static binaries for linux/amd64, linux/arm64,
# darwin/arm64 and darwin/amd64 with the version stamped, a SHA256SUMS file and
# a CHANGELOG.md generated from the git log since the last tag. Nothing is
# tagged or pushed; see deploy/RELEASING.md.
#
# Usage: scripts/release.sh [--version X.Y.Z] [--out dist] [--only host|os/arch,...]
set -euo pipefail
cd "$(dirname "$0")/.."
export CGO_ENABLED=0
VERSION=$(tr -d '[:space:]' < VERSION)
OUT="dist"
TARGETS="linux/amd64 linux/arm64 darwin/arm64 darwin/amd64"
while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    --only)
      if [ "$2" = "host" ]; then TARGETS="$(go env GOOS)/$(go env GOARCH)"; else TARGETS="${2//,/ }"; fi
      shift 2 ;;
    *) echo "unknown flag $1" >&2; exit 2 ;;
  esac
done
case "$VERSION" in
  [0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "VERSION must look like X.Y.Z (got $VERSION)" >&2; exit 2 ;;
esac
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
mkdir -p "${OUT:?}"
find "${OUT:?}" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
echo "== release $VERSION ($COMMIT) -> $OUT"
for t in $TARGETS; do
  os="${t%/*}"; arch="${t#*/}"
  name="ore_${VERSION}_${os}_${arch}"
  echo "   build $os/$arch"
  GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "$OUT/$name/ore" ./cmd/ore
  cp LICENSE "$OUT/$name/" 2>/dev/null || true
  tar -C "$OUT" -czf "$OUT/$name.tar.gz" "$name"
  rm -rf "${OUT:?}/${name:?}"
done
( cd "$OUT" && shasum -a 256 *.tar.gz > SHA256SUMS )
cp VERSION "$OUT/VERSION"
LAST_TAG=$(git describe --tags --abbrev=0 2>/dev/null || true)
{
  echo "# Changelog"
  echo
  if [ -n "$LAST_TAG" ]; then
    echo "## $VERSION ($DATE) — changes since $LAST_TAG"
    echo
    git log --no-merges --pretty='- %s (%h)' "$LAST_TAG"..HEAD
  else
    echo "## $VERSION ($DATE) — first release, full history"
    echo
    git log --no-merges --pretty='- %s (%h)'
  fi
  echo
} > "$OUT/CHANGELOG.md"
cp deploy/install.sh "$OUT/install.sh"
echo "== artifacts"
ls -1 "$OUT"
echo "== SHA256SUMS"
cat "$OUT/SHA256SUMS"
