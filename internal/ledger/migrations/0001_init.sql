-- Ore ledger v0. Every table carries org_id (ADR 0006). Events are the outbox (ADR 0005).
CREATE TABLE IF NOT EXISTS orgs (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS repos (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL REFERENCES orgs(id),
  name TEXT NOT NULL,
  default_branch TEXT NOT NULL DEFAULT 'main',
  autonomy_level INTEGER NOT NULL DEFAULT 2,
  deploy_port INTEGER NOT NULL DEFAULT 8080,
  created_at TEXT NOT NULL,
  UNIQUE(org_id, name)
);
CREATE TABLE IF NOT EXISTS ref_updates (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  ref TEXT NOT NULL,
  old_oid TEXT NOT NULL,
  new_oid TEXT NOT NULL,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  via TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ref_updates_repo ON ref_updates(repo_id, created_at);
CREATE TABLE IF NOT EXISTS issues (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  number INTEGER NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'open',
  created_at TEXT NOT NULL,
  closed_at TEXT,
  UNIQUE(repo_id, number)
);
CREATE TABLE IF NOT EXISTS changes (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  number INTEGER NOT NULL,
  change_id TEXT NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  source_ref TEXT NOT NULL,
  target_ref TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'open',
  base_oid TEXT NOT NULL,
  head_oid TEXT NOT NULL,
  merge_oid TEXT,
  provenance TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  merged_at TEXT,
  UNIQUE(repo_id, number),
  UNIQUE(repo_id, change_id)
);
CREATE TABLE IF NOT EXISTS pipeline_runs (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  change_id TEXT,
  commit_oid TEXT NOT NULL,
  status TEXT NOT NULL,
  tasks TEXT NOT NULL DEFAULT '[]',
  log_path TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  started_at TEXT,
  finished_at TEXT
);
CREATE INDEX IF NOT EXISTS pipeline_runs_repo ON pipeline_runs(repo_id, created_at);
CREATE TABLE IF NOT EXISTS deployments (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  environment TEXT NOT NULL,
  commit_oid TEXT NOT NULL,
  image TEXT NOT NULL DEFAULT '',
  container_id TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  previous_id TEXT,
  rollback_of TEXT,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  finished_at TEXT
);
CREATE INDEX IF NOT EXISTS deployments_repo_env ON deployments(repo_id, environment, created_at);
CREATE TABLE IF NOT EXISTS agent_runs (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  issue_id TEXT,
  adapter TEXT NOT NULL,
  status TEXT NOT NULL,
  step TEXT NOT NULL DEFAULT '',
  branch TEXT NOT NULL DEFAULT '',
  change_id TEXT,
  pipeline_run_id TEXT,
  deployment_id TEXT,
  log_path TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  finished_at TEXT
);
CREATE TABLE IF NOT EXISTS events (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  org_id TEXT NOT NULL,
  type TEXT NOT NULL,
  subject TEXT NOT NULL,
  time TEXT NOT NULL,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  data TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_type ON events(type, seq);
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);
