-- GitHub adapter (slice/github-adapter, internal/fpi/github): links between Ore
-- repositories and GitHub repositories, the issue and pull-request number
-- mappings, received webhook deliveries (idempotency), posted commit statuses
-- and the ledger cursor of the status reporter. Every table carries org_id
-- (ADR 0006); the matching events are emitted through ledger.Tx.Emit.
CREATE TABLE IF NOT EXISTS github_links (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL UNIQUE REFERENCES repos(id),
  owner TEXT NOT NULL,
  name TEXT NOT NULL,
  github_id INTEGER NOT NULL DEFAULT 0,
  secret TEXT NOT NULL,
  installation_id INTEGER NOT NULL DEFAULT 0,
  clone_url TEXT NOT NULL DEFAULT '',
  html_url TEXT NOT NULL DEFAULT '',
  default_branch TEXT NOT NULL DEFAULT '',
  last_sync_at TEXT,
  last_sync_status TEXT NOT NULL DEFAULT '',
  last_sync_error TEXT NOT NULL DEFAULT '',
  next_sync_at TEXT,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS github_links_owner_name ON github_links(owner, name);
CREATE TABLE IF NOT EXISTS github_issue_map (
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  github_number INTEGER NOT NULL,
  github_id INTEGER NOT NULL DEFAULT 0,
  issue_id TEXT NOT NULL REFERENCES issues(id),
  issue_number INTEGER NOT NULL,
  html_url TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  PRIMARY KEY (repo_id, github_number)
);
CREATE TABLE IF NOT EXISTS github_pr_map (
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  github_number INTEGER NOT NULL,
  github_id INTEGER NOT NULL DEFAULT 0,
  change_id TEXT NOT NULL REFERENCES changes(id),
  change_number INTEGER NOT NULL,
  head_sha TEXT NOT NULL,
  base_ref TEXT NOT NULL,
  state TEXT NOT NULL,
  html_url TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  PRIMARY KEY (repo_id, github_number)
);
CREATE TABLE IF NOT EXISTS github_deliveries (
  delivery_id TEXT NOT NULL,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  event TEXT NOT NULL,
  action TEXT NOT NULL DEFAULT '',
  outcome TEXT NOT NULL DEFAULT '',
  received_at TEXT NOT NULL,
  PRIMARY KEY (delivery_id, repo_id)
);
CREATE TABLE IF NOT EXISTS github_statuses (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  run_id TEXT NOT NULL,
  sha TEXT NOT NULL,
  state TEXT NOT NULL,
  context TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(run_id, state)
);
CREATE TABLE IF NOT EXISTS github_cursors (
  name TEXT PRIMARY KEY,
  org_id TEXT NOT NULL DEFAULT '',
  seq INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL
);
