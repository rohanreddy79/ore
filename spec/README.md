# spec — machine-readable contracts (Apache-2.0)

Everything another program must implement or embed to interoperate with Ore lives here under Apache-2.0, so that integrators, forks and competing implementations are never bound by the server's AGPL (GOVERNANCE.md §3).

| Folder | Contract | Status |
|---|---|---|
| `pipeline-ir/` | The Pipeline Intermediate Representation: the typed, content-addressed DAG that every authoring front-end emits and the control plane executes | v0 draft (JSON Schema 2020-12) |
| `events/` | The CloudEvents catalog emitted by the ledger and delivered over Standard Webhooks | v0 draft |
| `safe-outputs/` | The schema of writes an agent session may request through the gateway | v0 draft |
| `fpi/` | The Forge Provider Interface that repository cores implement | v0 sketch (Go interface) |
| `runner-protocol/` | The protocol between the control plane and runners | outline |
| `mcp/` | Toolsets exposed by the MCP server | v0 list |

Versioning: each contract carries a `version` field; breaking changes bump the major and are announced with a twelve-month deprecation window.
