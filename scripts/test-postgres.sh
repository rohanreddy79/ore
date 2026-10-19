#!/usr/bin/env bash
# Run the whole test suite against Postgres: start a throwaway container, point
# ORE_TEST_DB_URL at it (every ledger.Open in the tests then gets its own schema
# there), run `go test ./...`, and stop the container whatever happens.
# Usage: scripts/test-postgres.sh [go test args...]      e.g. scripts/test-postgres.sh -run TestAPI ./internal/web/
set -euo pipefail
cd "$(dirname "$0")/.."
export CGO_ENABLED=0
IMAGE="${ORE_PG_IMAGE:-postgres:16-alpine}"
NAME="ore-test-pg-$$"
PORT="${ORE_PG_PORT:-}"
if [ -z "$PORT" ]; then
  PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()' 2>/dev/null || echo 55432)
fi
if ! docker info >/dev/null 2>&1; then
  echo "docker is not available; cannot start Postgres" >&2
  exit 2
fi
echo "== postgres: starting $IMAGE as $NAME on 127.0.0.1:$PORT"
docker run -d --rm --name "$NAME" -e POSTGRES_PASSWORD=ore -e POSTGRES_USER=ore -e POSTGRES_DB=ore -p "127.0.0.1:$PORT:5432" "$IMAGE" >/dev/null
cleanup() { echo "== postgres: stopping $NAME"; docker stop "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT
for i in $(seq 1 60); do
  if docker exec "$NAME" pg_isready -U ore -d ore >/dev/null 2>&1; then break; fi
  sleep 0.5
  if [ "$i" = 60 ]; then echo "postgres did not become ready" >&2; docker logs "$NAME" | tail -20 >&2; exit 1; fi
done
export ORE_TEST_DB_URL="postgres://ore:ore@127.0.0.1:$PORT/ore?sslmode=disable"
echo "== test (postgres): ORE_TEST_DB_URL=$ORE_TEST_DB_URL"
if [ $# -eq 0 ]; then set -- ./...; fi
go test -count=1 "$@"
echo "== postgres suite passed"
