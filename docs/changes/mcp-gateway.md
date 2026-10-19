# Slice: MCP server + safe-outputs gateway

Branch `rohan/slice-mcp-gateway-95d488` (the `slice/mcp-gateway` row of the ownership map). Registers `connect.gateway` and `connect.mcp` through `internal/registry`; migration `internal/ledger/migrations/0002_gateway.sql`. Nothing outside the slice's files changed (no edits to `cmd/ore`, `internal/web`, `internal/ledger/*.go`).

## What landed

### `internal/connect/gateway` (ADR 0009)

The only way an agent session writes. Attached as `s.Services["gateway"]` (`gateway.From(s)`).

- **Sessions** (`gateway_sessions`): one agent on one repository (`scope = repo:<org>/<name>`), a bearer token `gws_…` of which only the SHA-256 is stored, counters (`writes`, `refused`, `window_start`, `window_writes`), state `open|closed`. Opening and closing emit `io.ore.agent.session.gateway.opened` / `.closed`.
- **Requests** are safe-outputs envelopes (`spec/safe-outputs/safe-outputs.schema.json`, version `0`): `version`, `session {agent_id, session_id, repository_id}`, `requests[1..50]`. Validation is schema-level in Go, no external validator (`validate.go`): known `kind`, `additionalProperties: false`, required properties, `title ≤ 256` characters, comment body ≤ 65,536 **bytes** (stricter than the schema's character count; the task is stated in bytes), `provenance` with `author_kind ∈ {agent, human_with_agent}`, `agent_id`, `model`.
- **Decisions**, one per request, in order: schema → scope (the envelope's agent, session and repository must match the token's session; `repository_id` may be the id or `org/name`) → policy (`push`, `label`, `review` are schema-valid but refused as `unsupported` in v0; `open_change` from the protected default branch is refused `protected_branch`; a source branch or non-default base that does not exist, a missing comment/review target, or an unknown release is refused `bad_target`) → rate limit (`rate_limited`) → execute. Every refusal is `io.ore.agent.session.write.gated` with `{session_id, agent_id, repo_id, scope, kind, decision: refused, code, reason, request}` (bodies truncated to 256 bytes) and bumps `refused`. Every execution runs with actor `{kind: agent, id: <agent_id>}` and is followed by `io.ore.agent.session.write.executed` carrying the session id and the result reference (same transaction for comments, review and deployment requests; the issue/change entity events are emitted by the ledger/review packages in their own transaction, so for those the executed event follows).
- **Rate limit**: per session, `RateLimit` writes per `RateWindow`, default **20 per 10 minutes** (`ORE_GATEWAY_RATE_LIMIT`, `ORE_GATEWAY_RATE_WINDOW`). A slot is reserved under a mutex before execution, so the limit is exact under concurrency; refused requests never consume a slot; a failed execution does (recorded as `code: error`).
- **Kinds executed**: `open_issue` → `DB.CreateIssue`; `open_change` → `review.Service.Open` from an existing branch (an open change for that branch is returned with `existing: true`; provenance JSON stores the request's provenance plus `session_id`); `comment` → new `comments` table + `io.ore.comment.created` (targets `issues/N`, `changes/N`, `#N`, `iss_…`, `chg_…`); `request_review` → `io.ore.change.review.requested`; `request_deployment` → `io.ore.deployment.request.created` with the resolved `commit_oid` (a request only; nothing deploys).
- **REST**: `POST /api/v0/gateway/sessions {agent_id, repo | repository_id}` (admin) → `201 {session, token}`; `GET /api/v0/gateway/sessions[?repo=org/name]` (admin); `GET /api/v0/gateway/{session}`; `POST /api/v0/gateway/{session}/close {reason}`; `POST /api/v0/gateway/{session}/requests <envelope>` → `200 {session, executed, refused, results[]}`, or `403` with the same body when every request was refused. The session's own token or the admin token may drive `/{session}/…`; another session's token gets `403`. Comments are readable at `GET /api/v0/repos/{org}/{name}/(issues|changes)/{n}/comments`.

### `internal/connect/mcp`

A stateless Streamable-HTTP MCP endpoint at `/mcp`: JSON-RPC 2.0 over `POST`, one message per request, `202` for notifications, `405` for `GET`/`DELETE`, no `Mcp-Session-Id`, no server-initiated stream, batches rejected (`-32600`). Methods: `initialize` (negotiates to the client's version when in `2024-11-05, 2025-03-26, 2025-06-18, 2025-11-25`, else `2025-06-18`), `ping`, `tools/list` (sorted by name, `_meta.ttlMs = 300000`), `tools/call`. Error codes: `-32700` parse error, `-32600` invalid request, `-32601` method not found (including `resources/*`, `prompts/*`), `-32602` invalid params (unknown tool), tool failures as `isError: true` results with `structuredContent.code`.

Auth is a bearer token: the **admin token** reads every repository (`repo` argument required as `org/name`) and the write tools refuse with `session_required`; a **gateway session token** scopes every tool to the session's repository (`repo` optional; another repository is `outside_scope`: reads fail in the tool, writes are forwarded to the gateway so the refusal is on the ledger). A closed session's token gets `401` on every call and a `session_closed` gated event. `ORE_MCP_READ_ONLY=1` (read in `register.go`, not in `cmd/ore`) hides the write tools and makes calling one an `isError` result.

Tool names use underscores (`toolset_tool`) because Claude Code prefixes them as `mcp__<server>__<tool>` and the Claude API's tool-name pattern does not allow dots; `spec/mcp/toolsets.md` was updated to the implemented names.

| Tool | Reads/writes | Notes |
|---|---|---|
| `context_whoami` | read | actor, session, scope, rate-limit counters, `writes: gateway\|session_required\|disabled` |
| `repos_list`, `repos_get`, `repos_tree`, `repos_file`, `repos_log` | read | `repos_file` truncates at 512 KiB and rejects `..` |
| `changes_list`, `changes_get`, `changes_diff` | read | `changes_get` adds diffstat, runs and comments |
| `issues_list`, `issues_get` | read | `issues_get` adds comments |
| `pipelines_runs`, `pipelines_run_log` | read | `run_log` is ranged by line: `start` (1-based), `count` (≤ 2000), returns `total_lines`, `truncated` |
| `deployments_list`, `ledger_events` | read | `ledger_events`: `after`, `type` (exact or `prefix.`), `subject`, `limit`; a session sees only its repository's subjects |
| `issues_open`, `changes_open`, `changes_request_review`, `deployments_request`, `issues_comment` | write | each builds a safe-outputs request and calls `Gateway.Submit`; the result is the gateway decision |

## Tests

`CGO_ENABLED=0 go test ./internal/connect/...` (all green; `scripts/e2e.sh` passes, see below).

Unit (`internal/connect/gateway`): `TestParseEnvelope`, `TestParseRequest` (27 cases incl. unknown property, null required field, limits), `TestKindsCoverSchema`, `TestSessionTokenRoundTrip`, `TestScopeRefusesOtherRepository`, `TestRateLimit` (fake clock, window reset, batch gated request by request, refusals don't consume), `TestDefaultRateLimitIsTwentyPerTenMinutes`, `TestProtectedDefaultBranch`, `TestOpenChangeExistingAndMissingBase`, `TestCommentReviewAndDeployment`, `TestClosedSession`, `TestInvalidEnvelopeIsRecorded`. REST: `TestRESTSessionsAndRequests`.

End-to-end over httptest with the registry-wired server (`internal/connect/mcp`): `TestMCPEndToEnd` creates a session, initializes, lists tools (deterministic, `ttlMs`), reads the repository (refs, tree, file, log) and the scoped ledger, opens an issue through the gateway and sees `io.ore.issue.opened` by actor `agent/bot` and `io.ore.agent.session.write.executed` with the session id, then comments, opens a change with provenance, requests review and a deployment, reads a ranged pipeline log, and checks the admin token's cross-repository reads and `session_required` on writes.

Edge cases, one named test each:

| Edge case | Test | Expected |
|---|---|---|
| malformed JSON-RPC | `TestMCPMalformedJSONRPC` | `-32700` with `id: null`; trailing data `-32700`; `jsonrpc: "1.0"` and batches `-32600` |
| unknown method | `TestMCPUnknownMethod` | `-32601` |
| unknown tool | `TestMCPUnknownTool` | `-32602` (also for a missing `name`) |
| request outside the session's repository | `TestMCPRequestOutsideSessionRepository` | read: `isError` `outside_scope`; write: refused and `io.ore.agent.session.write.gated` recorded, no issue written |
| rate limit exceeded | `TestMCPRateLimitExceeded` | third write with limit 2 is `rate_limited` and recorded; reads unaffected |
| write attempted in read-only mode | `TestMCPWriteInReadOnlyMode` | 15 tools listed, `issues_open` is `isError` "read-only mode", nothing written |
| `changes_open` for a branch that is already an open change | `TestMCPChangesOpenForBranchAlreadyOpen` | same change id/number, `existing: true`, one change in total |
| change targeting a non-default branch that does not exist | `TestMCPChangesOpenMissingNonDefaultBase` | refused `bad_target` and recorded; default branch as source refused `protected_branch` |
| comment over 65,536 bytes | `TestMCPCommentOver65536Bytes` | exactly 65,536 passes; 65,537 refused `invalid` and recorded; the gated event does not store the body |
| session token used after the session was closed | `TestMCPSessionTokenAfterClose` | `401` with `WWW-Authenticate … session closed` on every method, `session_closed` gated events, REST shows `refused: 2` |

The same cases are covered at the gateway level (`TestScopeRefusesOtherRepository`, `TestRateLimit`, `TestOpenChangeExistingAndMissingBase`, `TestInvalidEnvelopeIsRecorded`, `TestClosedSession`).

## Manual verification with Claude Code

Server: `./bin/ore serve --data <tmp> --addr 127.0.0.1:7799 --token ore-admin-token-verify --executor host` (prints `features: connect.gateway, connect.mcp`), then `ore org create acme`, `ore repo create acme/app`, `ore commit acme/app -m import README.md`, `ore issue create acme/app "Flaky retry in the uploader"`.

### 1. Register the server in Claude Code (2.1.154) with the admin token

```
$ claude mcp add --transport http ore http://localhost:7799/mcp --header "Authorization: Bearer ore-admin-token-verify"
Added HTTP MCP server ore with URL: http://localhost:7799/mcp to local config
Headers: {
  "Authorization": "Bearer ore-admin-token-verify"
}
File modified: /Users/rohanreddy/.claude.json [project: /Users/rohanreddy/Git opensource/.claude/worktrees/slice-mcp-gateway-95d488]

$ claude mcp get ore
ore:
  Scope: Local config (private to you in this project)
  Status: ✓ Connected
  Type: http
  URL: http://localhost:7799/mcp
  Headers:
    Authorization: Bearer ore-admin-token-verify

To remove this server, run: claude mcp remove "ore" -s local
```

Claude Code's MCP client completed the Streamable-HTTP handshake (`initialize`) against `/mcp`: `Status: ✓ Connected`.

### 2. Ask Claude Code to list repositories: could not be verified

```
$ claude -p "Using only the ore MCP server tools, list the repositories and say which tool you called." --allowedTools "mcp__ore__repos_list,mcp__ore__context_whoami" --output-format text
Failed to authenticate. API Error: 401 OAuth access token has expired. Re-authenticate to continue.
exit: 1
```

The model-driven call failed before reaching the server: the Claude Code OAuth token on this machine has expired (`claude auth status` still reports `loggedIn: true`, but the refresh fails; the same error with a clean environment). This needs an interactive `claude` login by the user, then the command above can be rerun as is. Everything below was driven with `curl`, sending exactly the JSON-RPC messages an MCP client sends.

### 3. The same calls over JSON-RPC with the admin token

```
$ curl -s http://localhost:7799/mcp -H "Authorization: Bearer <admin token>" -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"tools":{"listChanged":false}},"instructions":"Ore: the open, autonomous software forge. Tools read repositories, changes, issues, pipeline runs, deployments and the ledger. Pass repo as org/name. Writes over MCP need a gateway session token (POST /api/v0/gateway/sessions); with the admin token the write tools are listed but refuse.","protocolVersion":"2025-06-18","serverInfo":{"name":"ore","title":"Ore","version":"0"}}}

$ curl -s http://localhost:7799/mcp ... -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | jq '{ttlMs: ._meta.ttlMs, tools: [.result.tools[].name]}'
{
  "ttlMs": 300000,
  "tools": [
    "changes_diff",
    "changes_get",
    "changes_list",
    "changes_open",
    "changes_request_review",
    "context_whoami",
    "deployments_list",
    "deployments_request",
    "issues_comment",
    "issues_get",
    "issues_list",
    "issues_open",
    "ledger_events",
    "pipelines_run_log",
    "pipelines_runs",
    "repos_file",
    "repos_get",
    "repos_list",
    "repos_log",
    "repos_tree"
  ]
}
$ curl -s http://localhost:7799/mcp ... -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"repos_list","arguments":{}}}'
{"jsonrpc":"2.0","id":3,"result":{"content":[{"text":"{\n  \"repos\": [\n    {\n      \"id\": \"repo_000038AHYV8KJG08NNNJ\",\n      \"org_id\": \"org_000038AHYV8JX3DB05EJ\",\n      \"org\": \"acme\",\n      \"name\": \"app\",\n      \"default_branch\": \"main\",\n      \"autonomy_level\": 2,\n      \"deploy_port\": 8080,\n      \"created_at\": \"2026-10-19T02:21:32.345Z\"\n    }\n  ]\n}","type":"text"}],"structuredContent":{"repos":[{"id":"repo_000038AHYV8KJG08NNNJ","org_id":"org_000038AHYV8JX3DB05EJ","org":"acme","name":"app","default_branch":"main","autonomy_level":2,"deploy_port":8080,"created_at":"2026-10-19T02:21:32.345Z"}]}}}

$ curl -s http://localhost:7799/mcp ... -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"issues_open","arguments":{"repo":"acme/app","title":"x"}}}' | jq .result
{
  "content": [
    {
      "text": "writes over MCP require a gateway session token: POST /api/v0/gateway/sessions {\"agent_id\": ..., \"repo\": \"org/name\"} with the admin token, then connect with the returned token",
      "type": "text"
    }
  ],
  "isError": true,
  "structuredContent": {
    "code": "session_required"
  }
}
```

### 4. A gateway session token: scoped reads, a write through the gateway, refusals on the ledger

```
# session token (gws_…) minted with: POST /api/v0/gateway/sessions {"agent_id":"claude-code","repo":"acme/app"}
$ curl -s http://localhost:7799/mcp -H "Authorization: Bearer <session token>" ... -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"context_whoami","arguments":{}}}' | jq .result.structuredContent
{
  "actor": {
    "kind": "agent",
    "id": "claude-code"
  },
  "rate_limit": {
    "used_in_window": 0,
    "window": "10m0s",
    "writes": 20
  },
  "read_only": false,
  "server": {
    "name": "ore",
    "protocol": "2025-06-18",
    "version": "0"
  },
  "session": {
    "id": "gws_000038AHYV8SWPFW7KCJ",
    "org_id": "org_000038AHYV8JX3DB05EJ",
    "agent_id": "claude-code",
    "repo_id": "repo_000038AHYV8KJG08NNNJ",
    "repo": "acme/app",
    "scope": "repo:acme/app",
    "state": "open",
    "writes": 0,
    "refused": 0,
    "window_writes": 0,
    "created_at": "2026-10-19T02:21:32.446Z"
  },
  "writes": "gateway"
}
$ ... -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"issues_open","arguments":{"title":"Retry loop never backs off","body":"Opened over MCP through the gateway."}}}' | jq .result.structuredContent
{
  "event_id": "evt_000038AHZ21F8PP2JDXT",
  "index": 0,
  "issue": {
    "body": "Opened over MCP through the gateway.",
    "created_at": "2026-10-19T02:23:23.380Z",
    "id": "iss_000038AHZ21F8N09BGRK",
    "number": 2,
    "org_id": "org_000038AHYV8JX3DB05EJ",
    "repo_id": "repo_000038AHYV8KJG08NNNJ",
    "state": "open",
    "title": "Retry loop never backs off"
  },
  "kind": "open_issue",
  "status": "executed"
}
$ ... -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"repos_get","arguments":{"repo":"acme/elsewhere"}}}' | jq .result
{
  "content": [
    {
      "text": "acme/elsewhere is outside the session scope (repo:acme/app)",
      "type": "text"
    }
  ],
  "isError": true,
  "structuredContent": {
    "code": "outside_scope",
    "scope": "repo:acme/app"
  }
}
$ ... -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"issues_open","arguments":{"repo":"acme/elsewhere","title":"x"}}}' | jq .result
{
  "content": [
    {
      "text": "refused (outside_scope): repository \"acme/elsewhere\" is outside the session scope (repo:acme/app)",
      "type": "text"
    }
  ],
  "isError": true,
  "structuredContent": {
    "code": "outside_scope",
    "event_id": "evt_000038AHZ21G5VEFPB7T",
    "index": 0,
    "kind": "open_issue",
    "reason": "repository \"acme/elsewhere\" is outside the session scope (repo:acme/app)",
    "status": "refused"
  }
}
$ curl -s "http://127.0.0.1:7799/api/v0/events?type=io.ore.agent.session." -H "Authorization: Bearer <admin token>" | jq -c '.[] | {type, subject, actor: .oreactor, data: (.data | {kind, decision, code, issue})}'
{"type":"io.ore.agent.session.gateway.opened","subject":"acme/app/gateway/gws_000038AHYV8SWPFW7KCJ","actor":{"kind":"human","id":"admin"},"data":{"kind":null,"decision":null,"code":null,"issue":null}}
{"type":"io.ore.agent.session.write.executed","subject":"acme/app/gateway/gws_000038AHYV8SWPFW7KCJ","actor":{"kind":"agent","id":"claude-code"},"data":{"kind":"open_issue","decision":"executed","code":null,"issue":2}}
{"type":"io.ore.agent.session.write.gated","subject":"acme/app/gateway/gws_000038AHYV8SWPFW7KCJ","actor":{"kind":"agent","id":"claude-code"},"data":{"kind":"open_issue","decision":"refused","code":"outside_scope","issue":null}}
$ curl -s "http://127.0.0.1:7799/api/v0/events?type=io.ore.issue.opened" -H "Authorization: Bearer <admin token>" | jq -c '.[] | {type, subject, actor: .oreactor}'
{"type":"io.ore.issue.opened","subject":"acme/app/issues/1","actor":{"kind":"human","id":"admin"}}
{"type":"io.ore.issue.opened","subject":"acme/app/issues/2","actor":{"kind":"agent","id":"claude-code"}}
```

The registration was removed afterwards (`claude mcp remove ore -s local`) and the server stopped.

## Not verified / not in scope

- A Claude Code model turn calling the tools (section 2): blocked by the expired OAuth login; the transport handshake and every JSON-RPC message were verified by hand.
- OAuth 2.1, RFC 9728 protected-resource metadata, PKCE, resource indicators (docs/05 §4.7): not implemented; auth is bearer tokens only (`WWW-Authenticate: Bearer` is sent on 401).
- OpenTelemetry trace context in `_meta`, `Origin` validation, the `search`, `wiki`, `learn`, `policy`, `security`, `environments` toolsets, `subscriptions/listen`: not implemented (listed as planned in `spec/mcp/toolsets.md`).
- Cedar policy checks and loop prevention on actor type (ADR 0009): the gateway has the hooks (one decision per request, recorded) but no policy engine; `push`, `label` and `review` are refused as `unsupported`.
- A scoped `ledger_events` post-filters by subject prefix over up to 1,000 org events per call, so `next_after` paging can skip events in a busy org; the admin path uses the ledger's own filters.
- Gated events for invalid envelopes are not rate-limited, so a misbehaving client can fill the ledger with refusals (each at most ~2 KiB); counted in `refused` per session.
