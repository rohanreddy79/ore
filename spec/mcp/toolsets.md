# MCP toolsets v0

Stateless Streamable-HTTP server at `/mcp` (JSON-RPC 2.0 over POST, one message per request, notifications answered with 202, no server-initiated stream, no session id); `tools/list` is ordered by name and carries `_meta.ttlMs` for client caching; every write is a safe-outputs request (`spec/safe-outputs/`) through the gateway (ADR 0009), which validates, scopes to one repository, rate-limits and records it. Implemented in `internal/connect/mcp` and `internal/connect/gateway` (see `docs/changes/mcp-gateway.md`).

Tool names are `<toolset>_<tool>` (underscores: MCP clients prefix tool names and the Claude API's tool-name pattern does not allow dots).

## Authentication

Bearer token. The admin token reads every repository (`repo` argument `org/name` required) and cannot write; a gateway session token (`POST /api/v0/gateway/sessions {agent_id, repo}` → `gws_…`) scopes every tool to one repository (`repo` optional) and routes writes through the gateway. A closed session's token is refused with 401 and the refusal is on the ledger. Planned, not implemented: OAuth 2.1 with RFC 9728 protected-resource metadata, PKCE, RFC 8707 resource indicators, audience-bound tokens, client-ID metadata documents; no token passthrough.

## Implemented tools

| Toolset | Tools (read unless marked write) | Notes |
|---|---|---|
| `context` | `context_whoami` | actor, session, scope, rate-limit counters, write mode |
| `repos` | `repos_list`, `repos_get`, `repos_tree`, `repos_file`, `repos_log` | `repos_get` includes refs; `repos_file` truncates at 512 KiB |
| `changes` | `changes_list`, `changes_get`, `changes_diff`, `changes_open` (write), `changes_request_review` (write) | `changes_open` from an existing branch, never the protected default branch; an open change for the branch is returned (`existing: true`); provenance from the session plus `model` |
| `issues` | `issues_list`, `issues_get`, `issues_open` (write), `issues_comment` (write) | `issues_comment` targets an issue or a change (`issue`, `change`, or `target: issues/N | changes/N`), body ≤ 65,536 bytes |
| `pipelines` | `pipelines_runs`, `pipelines_run_log` | `run_log` ranged by line (`start`, `count` ≤ 2000) |
| `deployments` | `deployments_list`, `deployments_request` (write) | a request event only; policy and approvals still apply |
| `ledger` | `ledger_events` | `after`, `type` (exact or `prefix.`), `subject`, `limit`; a session sees only its repository |

Write tools map to safe-outputs kinds: `issues_open` → `open_issue`, `changes_open` → `open_change`, `changes_request_review` → `request_review`, `deployments_request` → `request_deployment`, `issues_comment` → `comment`. The gateway's decision (`executed` or `refused` with a code: `invalid`, `outside_scope`, `rate_limited`, `protected_branch`, `bad_target`, `unsupported`, `session_closed`) is the tool result; refusals are `isError` results.

## Modes

- `ORE_MCP_READ_ONLY=1`: the write tools are not listed and calling one is an `isError` result.
- Single-repository scope: implied by a gateway session token (the default for agent sessions).
- Rate limit: per session, 20 writes per 10 minutes by default (`ORE_GATEWAY_RATE_LIMIT`, `ORE_GATEWAY_RATE_WINDOW`).

## Planned (not implemented)

| Toolset | Tools | Default |
|---|---|---|
| `context` | `repository`, `agents_md` (nearest-file-wins), `wiki_structure` | on |
| `repos` | `diff`, `blame`, `search_code` | on |
| `changes` | `comments`, `comment` (write), `review` (write) with structured findings | on (reads) |
| `issues` | `label` (write) | on (reads) |
| `pipelines` | `run`, `task_logs` (ranged), `conformance_report`, `dispatch` (write), `check_uncommitted` (write; runs named tasks on a diff) | on (reads) |
| `deployments` | `get`, `policy_explain` | off |
| `environments` | `list`, `get`, `previews` | off |
| `search` | `text`, `symbol`, `semantic` (if embeddings enabled) | on |
| `wiki` | `structure`, `page`, `ask` | on |
| `learn` | `lessons`, `progress`, `explain_error` | on |
| `policy` | `explain` (why an action was denied; which Cedar policy) | on |
| `security` | `alerts`, `sbom`, `attestations` | off |
| `ledger` | `subscribe` (`subscriptions/listen`) | off |

Also planned: `--toolsets a,b,c`, OpenTelemetry trace context in `_meta`, publication to the official registry, rate limits per plan.
