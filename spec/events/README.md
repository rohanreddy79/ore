# Events

The ledger emits one CloudEvent (1.0.2) per transition, written in the same database transaction as the entity change (transactional outbox) and delivered to webhooks over Standard Webhooks (`webhook-id`, `webhook-timestamp`, `webhook-signature` with HMAC-SHA256 and ed25519; exponential backoff with jitter over 72 h; `410 Gone` disables; `Retry-After` honored), to MCP `subscriptions/listen`, and to internal consumers (indexers, learning engine, JetStream in team/cloud profiles).

## Envelope

```json
{
  "specversion": "1.0",
  "id": "evt_01J9XQ4K7V3N2M",
  "source": "https://ore.example.org/orgs/org_01J9W",
  "type": "io.ore.deployment.succeeded",
  "subject": "repo_01J9X/deployments/dep_01J9Y",
  "time": "2026-10-18T15:04:05Z",
  "datacontenttype": "application/json",
  "dataschema": "https://ore.invalid/spec/events/v0/deployment.json",
  "oreactor": { "kind": "human", "id": "usr_01J9A" },
  "oreorg": "org_01J9W",
  "oretrust": "protected",
  "data": { "...": "thin payload by default; `data.url` is a signed link to the full object" }
}
```

Extension attributes: `oreactor` (who caused it: human | app | agent | system, with id), `oreorg` (tenant), `oretrust` (ref trust level where applicable), `oreprovenance` (whether the related change has agent-authored hunks).

## Catalog v0

| Type | Emitted when |
|---|---|
| `io.ore.repository.created` / `.archived` / `.transferred` | repository lifecycle |
| `io.ore.ref.updated` / `.deleted` | any ref change; `data` has old/new object ids, forced flag, policy decision id |
| `io.ore.change.opened` / `.patchset.pushed` / `.review.submitted` / `.approved` / `.merged` / `.closed` | the review lifecycle |
| `io.ore.stack.created` / `.updated` / `.merged` | stacks of changes |
| `io.ore.issue.opened` / `.updated` / `.delegated` / `.closed` | issues; `.delegated` carries the agent id and the human assignee |
| `io.ore.pipeline.run.queued` / `.started` / `.task.started` / `.task.finished` / `.finished` | pipelines; task events carry `content_hash` and `cache_hit` |
| `io.ore.artifact.published` / `.attested` | artifacts and their attestations |
| `io.ore.release.created` / `.signed` | releases |
| `io.ore.environment.created` / `.policy.updated` | environments |
| `io.ore.deployment.requested` / `.approved` / `.started` / `.succeeded` / `.failed` / `.rolled_back` | deployments on either delivery path, or reported via API |
| `io.ore.rollout.stage.started` / `.stage.promoted` / `.aborted` | progressive rollouts |
| `io.ore.incident.opened` / `.resolved` | incidents (closes the DORA loop) |
| `io.ore.security.secret.detected` / `.secret.bypassed` / `.alert.opened` / `.alert.resolved` | security |
| `io.ore.agent.session.started` / `.waiting` / `.completed` / `.errored` / `.write.gated` | agent sessions and gateway decisions |
| `io.ore.lesson.run.started` / `.step.completed` / `.completed` | learning |
| `io.ore.credential.issued` | credentials |
| `io.ore.policy.decision` | sampled policy decisions (the full stream goes to the audit log) |
| `io.ore.schedule.fired` | scheduled triggers |
| `io.ore.pipeline.dispatch` | manual or API dispatch |

Payload schemas per type will live beside this file as `v0/<entity>.json`.
