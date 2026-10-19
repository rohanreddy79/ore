// SPDX-License-Identifier: AGPL-3.0-only

// Package gateway is the safe-outputs gateway (ADR 0009): the only way an
// agent session writes to Ore. A session is bound to one agent and one
// repository; every request is validated against spec/safe-outputs,
// checked against the session's scope, rate-limited, refused when it would
// touch the repository's protected default branch, and recorded on the
// ledger as io.ore.agent.session.write.gated (refused) or executed with the
// agent actor and followed by io.ore.agent.session.write.executed.
//
// The package wires itself in through internal/registry from register.go as
// "connect.gateway" and attaches its service as Services["gateway"] so the
// MCP server (internal/connect/mcp) can route writes through it.
package gateway
