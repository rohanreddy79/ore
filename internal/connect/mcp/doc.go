// SPDX-License-Identifier: AGPL-3.0-only

// Package mcp is Ore's native MCP server: a stateless Streamable-HTTP
// endpoint at /mcp speaking JSON-RPC 2.0 over POST. Reads cover
// repositories, changes, issues, pipelines, deployments and the ledger;
// every write tool builds a safe-outputs request and routes it through the
// gateway (internal/connect/gateway, ADR 0009), so an agent's writes are
// validated, scoped to one repository, rate-limited and recorded with the
// session id.
//
// Authentication is a bearer token: the admin token (reads across every
// repository; writes need a session) or a gateway session token (one
// repository; writes through the gateway). ORE_MCP_READ_ONLY=1 removes the
// write tools. The package registers as "connect.mcp" from register.go.
package mcp
