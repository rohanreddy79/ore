#!/usr/bin/env bash
# The merge gate: the web app (when Node is present), format, vet, unit and
# integration tests, then the real loop on Docker.
# Usage: scripts/e2e.sh [--no-docker]
#   ORE_UI_E2E=1      also run the Playwright suite against a real `ore serve`
#                     (needs `npx playwright install chromium` once)
#   ORE_SKIP_UI=1     skip the UI build and tests even when Node is present
set -euo pipefail
cd "$(dirname "$0")/.."
export CGO_ENABLED=0
if [ "${ORE_SKIP_UI:-}" != "1" ] && command -v npm >/dev/null 2>&1; then
  echo "== ui: install"; (cd ui && if [ -f package-lock.json ]; then npm ci --no-audit --no-fund --silent; else npm install --no-audit --no-fund --silent; fi)
  echo "== ui: typecheck + build"; (cd ui && npm run build --silent)
  echo "== ui: unit tests"; (cd ui && npm test --silent)
  echo "== ui: size budget"; (cd ui && npm run size --silent | tail -1)
else
  echo "== ui: skipped (npm not found or ORE_SKIP_UI=1); the binary will serve the 'build the UI' page under /app/"
fi
echo "== gofmt"; unformatted=$(gofmt -l cmd internal); if [ -n "$unformatted" ]; then echo "unformatted: $unformatted"; exit 1; fi
echo "== vet";   go vet ./...
echo "== test";  go test -count=1 ./...
echo "== build"; go build -trimpath -o bin/ore ./cmd/ore
if [ "${ORE_UI_E2E:-}" = "1" ]; then
  echo "== ui: playwright (real ore serve, host executor)"; (cd ui && npx playwright test)
fi
if [ "${1:-}" != "--no-docker" ] && docker info >/dev/null 2>&1; then
  echo "== demo (docker)"; ./bin/ore demo --exit | tail -3
else
  echo "== demo (host executor; deployments recorded only)"; ./bin/ore demo --executor host --exit | tail -3
fi
echo "== gate passed"
