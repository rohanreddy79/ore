-- Delivery slice (docs/changes/delivery-envs.md): environments as first-class
-- objects with policy, deployment approvals, preview TTLs and the rollout
-- record (policy decision, health result, recovery time) per deployment.
-- Every table carries org_id (ADR 0006). The deployments table is unchanged;
-- deployment_rollouts extends it one-to-one so this migration is additive.
CREATE TABLE IF NOT EXISTS environments (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  name TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'preview',
  target TEXT NOT NULL DEFAULT '{"kind":"local-docker"}',
  policy TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(repo_id, name)
);
CREATE TABLE IF NOT EXISTS deployment_approvals (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  deployment_id TEXT NOT NULL REFERENCES deployments(id),
  approver TEXT NOT NULL,
  decision TEXT NOT NULL,
  comment TEXT NOT NULL DEFAULT '',
  at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS deployment_approvals_dep ON deployment_approvals(deployment_id, at);
CREATE TABLE IF NOT EXISTS preview_ttls (
  deployment_id TEXT PRIMARY KEY REFERENCES deployments(id),
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  extensions INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS preview_ttls_expires ON preview_ttls(expires_at);
CREATE TABLE IF NOT EXISTS deployment_rollouts (
  deployment_id TEXT PRIMARY KEY REFERENCES deployments(id),
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  environment_id TEXT NOT NULL DEFAULT '',
  source_ref TEXT NOT NULL DEFAULT '',
  decision TEXT NOT NULL DEFAULT '{}',
  health TEXT NOT NULL DEFAULT '{}',
  failure TEXT NOT NULL DEFAULT '',
  recovery_seconds REAL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS deployment_rollouts_env ON deployment_rollouts(repo_id, environment_id, created_at);
