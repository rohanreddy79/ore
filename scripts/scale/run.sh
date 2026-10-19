#!/usr/bin/env bash
# Scale harness driver: start an isolated `ore serve`, sample its memory and
# descriptors every second, run orebench scenarios, and append JSON results.
#
# Usage: scripts/scale/run.sh <results.jsonl> [--db postgres://...] [--executor host|docker|none] -- <scenario commands...>
# Environment for scenarios: ORE_URL, ORE_TOKEN, ORE_PID, ORE_DATA.
# Example:
#   scripts/scale/run.sh docs/scale/results/r1.jsonl -- \
#     'bin/orebench push --url $ORE_URL --token $ORE_TOKEN --dir /tmp/repo --repos 5 --concurrency 5' \
#     'bin/orebench api --url $ORE_URL --token $ORE_TOKEN --repo bench/r0000 --concurrency 100 --duration 20s'
set -euo pipefail
cd "$(dirname "$0")/../.."
RESULTS="$1"; shift
DB=""; EXECUTOR="${ORE_SCALE_EXECUTOR:-host}"
while [ $# -gt 0 ]; do
  case "$1" in
    --db) DB="$2"; shift 2 ;;
    --executor) EXECUTOR="$2"; shift 2 ;;
    --) shift; break ;;
    *) echo "unknown flag $1" >&2; exit 2 ;;
  esac
done
mkdir -p "$(dirname "$RESULTS")"
export ORE_DATA="${ORE_SCALE_DATA:-$(mktemp -d)}"; mkdir -p "$ORE_DATA"
PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
export ORE_URL="http://127.0.0.1:$PORT"
export ORE_TOKEN="scale-$PORT"
args=(serve --data "$ORE_DATA" --addr "127.0.0.1:$PORT" --token "$ORE_TOKEN" --executor "$EXECUTOR" --quiet --agent-name bench-script --agent-cmd "sh '$PWD/scripts/scale/agent.sh'")
[ -n "$DB" ] && args+=(--db "$DB")
ORE_SSH_ADDR=127.0.0.1:0 ORE_LOG_LEVEL="${ORE_LOG_LEVEL:-warn}" ./bin/ore "${args[@]}" >"$ORE_DATA/serve.log" 2>&1 &
export ORE_PID=$!
cleanup() { kill "$ORE_PID" 2>/dev/null || true; wait "$ORE_PID" 2>/dev/null || true; kill "${SAMPLER:-0}" 2>/dev/null || true; }
trap cleanup EXIT
for i in $(seq 1 100); do curl -fs "$ORE_URL/healthz" >/dev/null 2>&1 && break; sleep 0.1; done
curl -fs "$ORE_URL/healthz" >/dev/null || { echo "server did not start; log:"; tail -20 "$ORE_DATA/serve.log"; exit 1; }
# sampler: rss_mb, open fds, goroutines (from /metrics when present)
( while kill -0 "$ORE_PID" 2>/dev/null; do
    rss=$(ps -o rss= -p "$ORE_PID" 2>/dev/null | tr -d ' ')
    fds=$(lsof -p "$ORE_PID" 2>/dev/null | wc -l | tr -d ' ')
    gor=$(curl -fs "$ORE_URL/metrics" 2>/dev/null | awk '/^go_goroutines|^ore_goroutines/ {print $2; exit}')
    printf '{"sample":"server","t":%s,"rss_mb":%d,"fds":%s,"goroutines":"%s"}\n' "$(date +%s)" "$(( ${rss:-0} / 1024 ))" "${fds:-0}" "${gor:-}" >> "$RESULTS.samples"
    sleep 1
  done ) &
SAMPLER=$!
echo "server pid=$ORE_PID url=$ORE_URL data=$ORE_DATA executor=$EXECUTOR db=${DB:-sqlite}" | tee -a "$RESULTS.log"
for cmd in "$@"; do
  echo "== $cmd" | tee -a "$RESULTS.log"
  start=$(date +%s)
  if ! bash -c "$cmd" >> "$RESULTS" 2>>"$RESULTS.log"; then echo "   scenario exited non-zero" | tee -a "$RESULTS.log"; fi
  echo "   ($(( $(date +%s) - start ))s)" | tee -a "$RESULTS.log"
  tail -n 1 "$RESULTS"
done
peak=$(awk -F'"rss_mb":' '{split($2,a,","); if (a[1]+0>m) m=a[1]+0} END{print m+0}' "$RESULTS.samples" 2>/dev/null)
fds=$(awk -F'"fds":' '{split($2,a,","); if (a[1]+0>m) m=a[1]+0} END{print m+0}' "$RESULTS.samples" 2>/dev/null)
printf '{"scenario":"server","peak_rss_mb":%s,"peak_fds":%s,"errors_in_log":%s,"panics":%s}\n' "${peak:-0}" "${fds:-0}" "$(grep -c 'level=ERROR' "$ORE_DATA/serve.log" || true)" "$(grep -c 'panic' "$ORE_DATA/serve.log" || true)" | tee -a "$RESULTS"
grep -E "level=ERROR|panic" "$ORE_DATA/serve.log" | sort | uniq -c | sort -rn | head -10 | tee -a "$RESULTS.log" || true
