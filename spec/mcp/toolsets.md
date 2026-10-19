# MCP toolsets v0

Stateless server per the 2026-07-28 MCP specification; OAuth 2.1 with RFC 9728 protected-resource metadata, PKCE, RFC 8707 resource indicators, audience-bound tokens, client-ID metadata documents; no token passthrough; deterministic `tools/list` ordering with cache TTLs; OpenTelemetry trace context in `_meta`. Default toolset is read-mostly; every write is routed through the safe-outputs gateway (`spec/safe-outputs/`).

| Toolset | Tools (read unless marked write) | Default |
|---|---|---|
| `context` | `whoami`, `repository`, `agents_md` (nearest-file-wins), `wiki_structure` | on |
| `repos` | `list`, `get`, `file`, `tree`, `log`, `diff`, `blame`, `search_code` | on |
| `changes` | `list`, `get` (stack, patch sets, interdiff summary, checks, provenance), `comments`, `open` (write), `comment` (write), `review` (write), `request_review` (write) | on (reads) |
| `issues` | `list`, `get`, `open` (write), `comment` (write), `label` (write) | on (reads) |
| `pipelines` | `runs`, `run`, `task_logs` (ranged), `conformance_report`, `dispatch` (write), `check_uncommitted` (write; runs named tasks on a diff) | on (reads) |
| `deployments` | `list`, `get`, `policy_explain`, `request` (write) | off |
| `environments` | `list`, `get`, `previews` | off |
| `search` | `text`, `symbol`, `semantic` (if embeddings enabled) | on |
| `wiki` | `structure`, `page`, `ask` | on |
| `learn` | `lessons`, `progress`, `explain_error` | on |
| `policy` | `explain` (why an action was denied; which Cedar policy) | on |
| `security` | `alerts`, `sbom`, `attestations` | off |
| `ledger` | `events` (filtered, paginated), `subscribe` (`subscriptions/listen`) | off |

Modes: `--read-only`, `--repository <id>` (single-repository scope; the default for agent sessions), `--toolsets a,b,c`.
