#!/bin/sh
# Deterministic bench agent: records the issue in a per-run file so every run
# produces a distinct change, then exits. Replace with a real agent via ORE_AGENT_CMD.
set -e
mkdir -p bench-agent
echo "issue ${ORE_ISSUE_NUMBER:-none}: ${ORE_ISSUE_TITLE:-prompt} at $(date -u +%FT%TZ)" > "bench-agent/issue-${ORE_ISSUE_NUMBER:-x}-$$.txt"
