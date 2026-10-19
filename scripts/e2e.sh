#!/usr/bin/env bash
# The merge gate: format, vet, unit and integration tests, then the real loop on Docker.
# Usage: scripts/e2e.sh [--no-docker]
set -euo pipefail
cd "$(dirname "$0")/.."
export CGO_ENABLED=0
echo "== gofmt"; unformatted=$(gofmt -l cmd internal); if [ -n "$unformatted" ]; then echo "unformatted: $unformatted"; exit 1; fi
echo "== vet";   go vet ./...
echo "== test";  go test -count=1 ./...
echo "== build"; go build -trimpath -o bin/ore ./cmd/ore
if [ "${1:-}" != "--no-docker" ] && docker info >/dev/null 2>&1; then
  echo "== demo (docker)"; ./bin/ore demo --exit | tail -3
else
  echo "== demo (host executor; deployments recorded only)"; ./bin/ore demo --executor host --exit | tail -3
fi
echo "== gate passed"
