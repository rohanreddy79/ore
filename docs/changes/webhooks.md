# Slice: webhooks out and audit export

Branch `rohan/slice-mcp-gateway-95d488` (wave 2, the `slice/webhooks` row of the ownership map). Owns `internal/connect/webhooks/` and `internal/ledger/migrations/0010_webhooks.sql`; registers as `connect.webhooks`; mounts under `/api/v0/webhooks/`; attaches itself as `s.Services["webhooks"]` (`*webhooks.Service` with `Store`, `Dispatcher`, `Exporter`). Standard library only.

**Integration note for the ops slice (owner of `cmd/ore/main.go`):** the binary loads extensions by blank import, and `main.go` does not import this package yet. One line is needed for `ore serve` to register `connect.webhooks`:

```go
_ "github.com/orevcs/ore/internal/connect/webhooks"
```

Until it lands, the slice is exercised by its tests through `registry.Apply`, not by the Docker demo.

## What landed

### Delivery contract (docs/05 §3, spec/events/README.md)

- **Subscription.** An endpoint belongs to one org (`POST /api/v0/webhooks/endpoints {org}`) or one repository (`POST /api/v0/webhooks/repos/{org}/{name}/endpoints`, or `{repo: "org/name"}`); a repository endpoint only sees subjects of that repository. Filters, all optional and ANDed: `types` (type prefixes such as `io.ore.deployment.`), `subject` (a glob: `*` within a segment, `**` across), `actor_kinds` (`human`, `agent`, `app`, `system`), `expr` (an expression, below). `payload` is `thin` (default) or `full`. An endpoint starts at the ledger head: it receives what happens after it was created (including its own `endpoint.created` event when nothing filters it out).
- **Message.** `POST <url>` with `Content-Type: application/json`, `User-Agent: ore-webhooks/0`, `ce-type: <event type>`, and the Standard Webhooks headers: `webhook-id` (the event id, identical across retries, replays and redeliveries, the idempotency key), `webhook-timestamp` (unix seconds of the attempt), `webhook-signature` (`v1,<base64 HMAC-SHA256 of "<id>.<timestamp>.<body>">`; two space-separated signatures while a secret rotates). Secrets are `whsec_<base64 of 24 random bytes>`, returned once on creation and rotation; listings show ids and expiry only.
- **Body.** The CloudEvents 1.0 envelope (`specversion`, `id`, `source`, `type`, `subject`, `time`, `datacontenttype`, `oreactor`, `oreorg`, `oreseq`). Thin: `data = {"url": <signed link>, "thin": true}`; the link is `GET /api/v0/webhooks/events/{id}?ep=&exp=&sig=`, valid 24 h, verified against the endpoint's active secrets, needs no bearer token and returns the full event. Full: `data` is the event's data; a full body over `MaxPayload` (1 MiB) falls back to thin. Pings (`io.ore.webhook.ping`, from the test button) are always full.
- **Retries.** 30 s per attempt (`ORE_WEBHOOKS_TIMEOUT`). 2xx succeeds. 410 Gone marks the delivery `failed`, disables the endpoint with the reason and emits `io.ore.webhook.endpoint.disabled`. Anything else (a timeout, a connection error, another status) retries: immediately, then after 5 s, 5 min, 30 min, 2 h, 5 h, then 10 h steps, each with ±10 % jitter, until 72 h after the first attempt (12 attempts), after which the delivery is `exhausted`. `Retry-After` (seconds or an HTTP date) replaces the scheduled delay; one past the 72 h budget exhausts the delivery. Each delivery row keeps its attempts (code, latency, error).
- **Queues.** One goroutine per enabled endpoint reads the ledger from the endpoint's own cursor (bounded by the head read first, so a cursor can move past non-matching events without missing one committed meanwhile), queues one delivery per matching event (deduplicated by event id), sends the due ones in event order, and sleeps until the next retry or the poll interval (2 s, `ORE_WEBHOOKS_POLL`). A slow receiver only slows its own queue; a disabled endpoint's queue stops; the dispatcher rescans for new or re-enabled endpoints every poll. Re-enabling resumes from the cursor, so events that happened while an endpoint was disabled are delivered (not lost, not duplicated). At-least-once: a receiver may see the same `webhook-id` twice after a crash between the send and the acknowledgement.
- **Addresses.** Endpoint URLs must be `http(s)` and may not point at loopback, private, link-local or unspecified addresses (literal or resolved) unless `ORE_WEBHOOKS_ALLOW_PRIVATE=1`; checked at creation (400) and again at delivery time (the delivery fails permanently).

### Expression language (`expr.go`)

A CEL-like subset for `expr`: fields `type`, `subject`, `id`, `source`, `time`, `org`, `seq`, `actor.kind`, `actor.id`, `data.<path>`; literals (strings, numbers, `true`, `false`, `null`, `[lists]`); `== != < <= > >= in && || !` and parentheses; functions `startsWith`, `endsWith`, `contains`, `matches(s, glob)`, `has`, `lower`, `size`. Syntax errors are 400 at creation and update; an expression that errors at evaluation (a type mismatch) does not match.

```
type == "io.ore.deployment.succeeded" && data.environment == "production"
startsWith(type, "io.ore.change.") && actor.kind in ["agent", "app"]
```

### REST (`/api/v0/webhooks/`, bearer token, admin or identity)

| Method and path | Does |
|---|---|
| `POST /endpoints`, `POST /repos/{org}/{name}/endpoints` | create (`url` required; `org` or `repo` for the first form); returns `{endpoint, secret}` |
| `GET /endpoints?org=&repo=`, `GET /endpoints/{id}`, `PATCH /endpoints/{id}`, `DELETE /endpoints/{id}` | list, read, update (`url`, `description`, filters, `payload`, `enabled`), delete (with its deliveries) |
| `POST /endpoints/{id}/test` | sends `io.ore.webhook.ping` synchronously; returns the delivery |
| `GET /endpoints/{id}/deliveries?status=&after=&limit=` | deliveries after an event sequence, by status |
| `GET /endpoints/{id}/health` | success rate, p50/p95 latency and last error over the last 100 attempted deliveries, counts by status, next attempt, cursor vs head |
| `POST /endpoints/{id}/replay {from_seq \| since, to_seq?}` | queues the matching events in `(from, to]` again (kind `replay`, original webhook-ids); 409 when disabled; emits `io.ore.webhook.replay.requested` |
| `POST /endpoints/{id}/rotate` | adds a secret; the previous one keeps signing for 24 h; emits `io.ore.webhook.endpoint.secret.rotated` |
| `GET /deliveries/{id}` | the delivery with its event and the rendered payload (payload inspection) |
| `POST /deliveries/{id}/redeliver` | sends the same event again now (same webhook-id); 409 when disabled |
| `GET /events/{id}?ep=&exp=&sig=` | the signed link from thin payloads (no bearer token) |
| `GET /audit` | export status with lag behind the ledger head |
| `POST /audit/exports`, `DELETE /audit/exports/{id}` | create (`kind: file` with `dir`, or `s3` with `bucket`, `endpoint`, `region`, `prefix`; `types`, `org`, `from_seq`) and remove exports |

The delivery dashboard's data (filters, payload inspection, replay by id or range, health) is this API; the pages belong to the UI slice.

### Audit export (`audit.go`, `s3.go`)

A worker (every 10 s, `ORE_AUDIT_INTERVAL`) appends every event, or the `types` subset, as newline-delimited CloudEvents JSON. `file`: `<dir>/events-YYYY-MM-DD.ndjson`, rotated daily by the exporter's clock, fsync per append. `s3`: objects `<prefix>/YYYY/MM/DD/events-<first seq>-<last seq>.ndjson` of `ORE_AUDIT_BATCH` (500) matching events, or a partial batch once `ORE_AUDIT_MAX_DELAY` (60 s) has passed; path-style with `endpoint` (MinIO and friends) or virtual-host AWS URLs; AWS Signature Version 4 from the standard library (`SigV4Sign`, reproducing the two documented S3 examples). Credentials come from `ORE_AUDIT_S3_ACCESS_KEY` / `ORE_AUDIT_S3_SECRET_KEY` and are never stored. The cursor is persisted after each append or PUT; a restarted exporter resumes without rewriting. `ORE_AUDIT_DIR` and `ORE_AUDIT_S3_BUCKET` (with `_ENDPOINT`, `_REGION`, `_PREFIX`) create exports at startup.

### Schema (`0010_webhooks.sql`)

`webhook_endpoints` (org, optional repo, url, secrets JSON for rotation, filter columns, payload, enabled and reason, cursor, created by), `webhook_deliveries` (endpoint, event seq and id, kind `event|ping|replay|redeliver`, attempt, status `pending|succeeded|failed|exhausted`, response code, latency, next and first attempt, last error, attempts JSON), `audit_exports` (org or all, kind `file|s3`, config, type filter, cursor, exported count, enabled, last error). Events: `io.ore.webhook.endpoint.created/updated/deleted/disabled`, `io.ore.webhook.endpoint.secret.rotated`, `io.ore.webhook.replay.requested`, `io.ore.audit.export.created/deleted`; delivery attempts are rows, not events.

## Tests

`CGO_ENABLED=0 go test ./internal/connect/webhooks/` (30 tests, all green; `scripts/e2e.sh` passes).

Unit: `TestSignVector` (the standard-webhooks reference example; the published signature's first 30 characters reproduce, the full value is pinned), `TestSignVerifyRoundTrip`, `TestSignAllDuringRotation`, `TestDefaultScheduleSteps` (the exact plan: 12 attempts, the last at +67h35m05s), `TestScheduleJitterAndRetryAfter`, `TestMatchGlob`, `TestFilterMatches`, `TestFilterValidate`, `TestExpressions` (29 expressions plus evaluation errors), `TestSigV4AWSExamples` (the documented GET and PUT signatures), `TestSigV4SignSetsHeaders`, `TestEnqueueAdvancesCursorAndDeduplicates` (cursor past non-matching events, dedup, paging, thin/full bodies, signed links), `TestAuditFileExportPagesAndErrors`, `TestAuditS3PartialBatchFlushesAfterMaxDelay`.

End-to-end over httptest with the registry-wired server and in-test receivers: `TestWebhooksEndToEnd` (endpoint created, events caused, signed deliveries verified, thin link resolves, deliveries and health, repository scope, full payloads, ping, patch, delete, ledger events), `TestRetryAfterFiveHundredThenSucceeds`, `TestRetryAfterHeaderAndExhaustion`, `TestGoneDisablesEndpoint` (410, no further delivery, 409 on replay and redeliver, re-enable resumes from the cursor), `TestReplayRange` (by sequence and by time, original webhook-ids), `TestRotateSecrets` (two signatures, both validate, old one expires), `TestS3ExportWithSigV4` (an in-test S3 that recomputes SigV4 with the known secret; a wrong secret is a 403 on the export's status).

Edge cases, one named test each:

| Edge case | Test | Expected |
|---|---|---|
| receiver never answers | `TestReceiverNeverAnswers` | attempt 1 records `timeout after 150ms`, the retry succeeds with the same webhook-id, health shows the error |
| payload over the size limit | `TestPayloadOverSizeLimitUsesThin` | a full endpoint gets the thin envelope with the signed link; small events stay full |
| endpoint on localhost or a private network | `TestPrivateURLRefusedUnlessAllowed` | 400 for loopback, `localhost`, `*.localhost`, 10/8, 169.254/16, `[::1]`; a public literal is accepted; accepted with `ORE_WEBHOOKS_ALLOW_PRIVATE`; the check also runs at delivery time |
| duplicate webhook-id on redeliver | `TestDuplicateWebhookIDOnRedeliver` | the receiver sees the same `webhook-id` twice, both signed; a second delivery row of kind `redeliver` |
| invalid filter expression | `TestInvalidFilterExpression` | 400 on create and on update, the stored filter unchanged; bad globs, actor kinds, payload and unknown fields are 400 too |
| replay on a disabled endpoint | `TestReplayOnDisabledEndpoint` | 409 "endpoint is disabled"; nothing delivered |
| worker restart resumes from cursors | `TestWorkerRestartResumesFromCursors` | after a stop, events produced meanwhile are delivered by a new dispatcher; the acknowledged ones are not re-sent; order kept |
| 1,000 events to 3 endpoints, one slow | `TestThousandEventsThreeEndpointsOneSlow` | the two fast endpoints finish all 1,000 (in 1.3 s on a laptop) while the slow one (25 ms per request) had received 38; per-endpoint order kept, no duplicates |
| audit file rotation across a day boundary | `TestAuditFileRotationAcrossDayBoundary` | events after midnight go to the next day's file, the previous file is untouched, a restarted exporter does not rewrite |

## Deferred / not verified

- ed25519 signatures (Standard Webhooks' asymmetric scheme): only `v1` HMAC-SHA256 is produced; the header format leaves room for `v1a`.
- OpenTelemetry streaming of deliveries and MCP `subscriptions/listen` (the same stream): not built.
- `oretrust`, `oreprovenance` and `dataschema` on the envelope: the ledger does not carry them yet.
- Rate limits per endpoint or plan, endpoint health alerts (the health endpoint exists; nothing pages anyone), retention of delivery rows (they grow with activity).
- The race detector was not run (the gate builds with `CGO_ENABLED=0`); the dispatcher is exercised under concurrency by the 1,000-event test.
- `ore serve` does not load the slice until the `main.go` import above lands.
