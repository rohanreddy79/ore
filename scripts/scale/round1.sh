#!/usr/bin/env bash
# Round 1 (and its reruns): the 100-million-line repository on SQLite.
# Usage: scripts/scale/round1.sh <scratch dir with repo100m> <results dir> [prefix]
# The repository comes from `bin/orebench genrepo --files 100000 --lines 1000`
# with `paths:` declared in .ore/pipeline.yaml (sparse area); the prefix
# names the result file (default r1b).
set -euo pipefail
cd "$(dirname "$0")/../.."
S="$1"; OUT="$2"; P="${3:-r1b}"; mkdir -p "$OUT"
R="$OUT/$P-100m-sqlite.jsonl"
rm -f "$R" "$R.samples" "$R.log"
ORE_SCALE_DATA="$S/data-$P-100m" scripts/scale/run.sh "$R" --executor host -- \
  "bin/orebench push --url \$ORE_URL --token \$ORE_TOKEN --dir $S/repo100m --repos 1 --concurrency 1 --prefix big" \
  "bin/orebench api --url \$ORE_URL --token \$ORE_TOKEN --repo bench/big0000 --concurrency 20 --duration 20s --mix tree,file,log,refs" \
  "bin/orebench api --url \$ORE_URL --token \$ORE_TOKEN --repo bench/big0000 --concurrency 200 --duration 20s --mix treetop,treedir,file,log,refs" \
  "bin/orebench commits --url \$ORE_URL --token \$ORE_TOKEN --repo bench/big0000 --concurrency 5 --count 20" \
  "bin/orebench pipelines --url \$ORE_URL --token \$ORE_TOKEN --repo bench/big0000 --concurrency 2 --count 4 --timeout 20m" \
  "bin/orebench agents --url \$ORE_URL --token \$ORE_TOKEN --repo bench/big0000 --concurrency 2 --count 4 --timeout 20m"
