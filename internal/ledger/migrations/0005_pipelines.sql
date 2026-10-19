-- Pipelines slice (docs/changes/pipelines-actions.md): secrets by reference
-- (encrypted at rest), artifacts per run, and the compiled plan (IR and
-- divergences) stored on every run. Every table carries org_id (ADR 0006).
CREATE TABLE IF NOT EXISTS secrets (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  environment TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  ciphertext BLOB NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(repo_id, environment, name)
);
CREATE TABLE IF NOT EXISTS artifacts (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  run_id TEXT NOT NULL,
  task_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  size INTEGER NOT NULL DEFAULT 0,
  files INTEGER NOT NULL DEFAULT 0,
  digest TEXT NOT NULL DEFAULT '',
  path TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(run_id, name)
);
CREATE INDEX IF NOT EXISTS artifacts_run ON artifacts(run_id);
CREATE TABLE IF NOT EXISTS pipeline_plans (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  run_id TEXT NOT NULL,
  source TEXT NOT NULL,
  frontend TEXT NOT NULL DEFAULT '',
  ir TEXT NOT NULL,
  divergences TEXT NOT NULL DEFAULT '[]',
  refused TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS pipeline_plans_run ON pipeline_plans(run_id);
