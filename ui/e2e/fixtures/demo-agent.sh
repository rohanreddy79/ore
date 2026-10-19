#!/bin/sh
# A deterministic stand-in for an AI agent (the same script `ore demo` uses):
# it fixes the greeting so the seeded failing test passes.
set -e
echo "demo agent: resolving '$ORE_ISSUE_TITLE' on $ORE_REPO ($ORE_BRANCH)"
sed -i.bak 's/Hello, World!/Hello, Ore!/' main.go && rm -f main.go.bak
echo "demo agent: edited main.go; verify with: $ORE_VERIFY"
