-- Webhooks out (docs/05 §3, ADR 0005): endpoints subscribed to the ledger,
-- one delivery row per (endpoint, event) with retry state, and audit
-- exports that stream the ledger to files or an S3-compatible bucket.
-- Every table carries org_id (ADR 0006).
CREATE TABLE IF NOT EXISTS webhook_endpoints (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL REFERENCES orgs(id),
  repo_id TEXT,
  url TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  secrets TEXT NOT NULL DEFAULT '[]',
  filter_types TEXT NOT NULL DEFAULT '[]',
  filter_subject TEXT NOT NULL DEFAULT '',
  filter_actor_kinds TEXT NOT NULL DEFAULT '[]',
  filter_expr TEXT NOT NULL DEFAULT '',
  payload TEXT NOT NULL DEFAULT 'thin',
  enabled INTEGER NOT NULL DEFAULT 1,
  disabled_reason TEXT NOT NULL DEFAULT '',
  cursor INTEGER NOT NULL DEFAULT 0,
  created_by_kind TEXT NOT NULL,
  created_by_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS webhook_endpoints_org ON webhook_endpoints(org_id, created_at);
CREATE TABLE IF NOT EXISTS webhook_deliveries (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  endpoint_id TEXT NOT NULL REFERENCES webhook_endpoints(id),
  event_seq INTEGER NOT NULL,
  event_id TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'event',
  attempt INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  response_code INTEGER NOT NULL DEFAULT 0,
  latency_ms INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT,
  first_attempt_at TEXT,
  last_error TEXT NOT NULL DEFAULT '',
  attempts TEXT NOT NULL DEFAULT '[]',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS webhook_deliveries_queue ON webhook_deliveries(endpoint_id, status, next_attempt_at, event_seq);
CREATE INDEX IF NOT EXISTS webhook_deliveries_event ON webhook_deliveries(endpoint_id, event_id);
CREATE TABLE IF NOT EXISTS audit_exports (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL,
  config TEXT NOT NULL DEFAULT '{}',
  filter_types TEXT NOT NULL DEFAULT '[]',
  cursor INTEGER NOT NULL DEFAULT 0,
  exported INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  last_error TEXT NOT NULL DEFAULT '',
  last_export_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
