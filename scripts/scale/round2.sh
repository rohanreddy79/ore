#!/usr/bin/env bash
# Round 2: mixed concurrent traffic on the 1M-line repository, on SQLite and Postgres.
# Usage: scripts/scale/round2.sh <scratch dir with repo1m> <results dir>
set -euo pipefail
cd "$(dirname "$0")/../.."
S="$1"; OUT="$2"; mkdir -p "$OUT"
MIXED="bin/orebench api --url \$ORE_URL --token \$ORE_TOKEN --repo bench/r0000 --concurrency 200 --duration 30s & \
bin/orebench commits --url \$ORE_URL --token \$ORE_TOKEN --repo bench/r0001 --concurrency 50 --count 500 & \
bin/orebench pipelines --url \$ORE_URL --token \$ORE_TOKEN --repo bench/r0002 --concurrency 20 --count 40 --timeout 15m & \
bin/orebench agents --url \$ORE_URL --token \$ORE_TOKEN --repo bench/r0003 --concurrency 10 --count 20 --timeout 15m & \
wait"
run_round() { # <results file> [--db url]
  local R="$1"; shift
  rm -f "$R" "$R.samples" "$R.log"
  ORE_SCALE_DATA="$S/data-$(basename "$R" .jsonl)" scripts/scale/run.sh "$R" --executor host "$@" -- \
    "bin/orebench push --url \$ORE_URL --token \$ORE_TOKEN --dir $S/repo1m --repos 10 --concurrency 10" \
    "$MIXED" \
    "bin/orebench api --url \$ORE_URL --token \$ORE_TOKEN --repo bench/r0004 --concurrency 300 --duration 20s"
}
echo "### sqlite"; run_round "$OUT/r2-sqlite.jsonl"
if docker info >/dev/null 2>&1; then
  PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
  NAME="ore-scale-pg-$$"
  docker run -d --rm --name "$NAME" -e POSTGRES_PASSWORD=ore -e POSTGRES_USER=ore -e POSTGRES_DB=ore -p "127.0.0.1:$PORT:5432" postgres:16-alpine >/dev/null
  trap 'docker stop "$NAME" >/dev/null 2>&1 || true' EXIT
  for i in $(seq 1 60); do docker exec "$NAME" pg_isready -U ore -d ore >/dev/null 2>&1 && break; sleep 0.5; done
  echo "### postgres"; run_round "$OUT/r2-postgres.jsonl" --db "postgres://ore:ore@127.0.0.1:$PORT/ore?sslmode=disable"
fi
