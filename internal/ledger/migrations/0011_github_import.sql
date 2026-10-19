-- GitHub importer (slice/github-import, internal/fpi/github/import_*.go): resumable
-- import jobs with their fidelity report, progress and cursors; the mapping of
-- every imported GitHub object to its Ore entity (idempotency on resume);
-- mannequin identities to claim later; webhooks recreated with new secrets;
-- releases and their downloaded assets. Every table carries org_id (ADR 0006).
CREATE TABLE IF NOT EXISTS import_jobs (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  source TEXT NOT NULL,
  owner TEXT NOT NULL,
  name TEXT NOT NULL,
  options TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL,
  phase TEXT NOT NULL DEFAULT '',
  cursors TEXT NOT NULL DEFAULT '{}',
  progress TEXT NOT NULL DEFAULT '{}',
  errors TEXT NOT NULL DEFAULT '[]',
  report TEXT NOT NULL DEFAULT '',
  conformance TEXT NOT NULL DEFAULT '[]',
  verification TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  next_attempt_at TEXT,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  finished_at TEXT
);
CREATE INDEX IF NOT EXISTS import_jobs_repo ON import_jobs(repo_id, created_at);
CREATE TABLE IF NOT EXISTS import_mappings (
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  job_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  source_id TEXT NOT NULL,
  target_id TEXT NOT NULL DEFAULT '',
  target_number INTEGER NOT NULL DEFAULT 0,
  extra TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  PRIMARY KEY (repo_id, kind, source_id)
);
CREATE TABLE IF NOT EXISTS import_identities (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  login TEXT NOT NULL,
  github_id INTEGER NOT NULL DEFAULT 0,
  kind TEXT NOT NULL DEFAULT 'User',
  roles TEXT NOT NULL DEFAULT '[]',
  mentions INTEGER NOT NULL DEFAULT 0,
  suggested_user_id TEXT NOT NULL DEFAULT '',
  claimed_user_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(org_id, login)
);
CREATE TABLE IF NOT EXISTS import_webhooks (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  job_id TEXT NOT NULL,
  github_id INTEGER NOT NULL,
  url TEXT NOT NULL,
  events TEXT NOT NULL DEFAULT '[]',
  content_type TEXT NOT NULL DEFAULT 'json',
  active INTEGER NOT NULL DEFAULT 1,
  secret TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(repo_id, github_id)
);
CREATE TABLE IF NOT EXISTS import_releases (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  job_id TEXT NOT NULL,
  github_id INTEGER NOT NULL,
  tag TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL DEFAULT '',
  draft INTEGER NOT NULL DEFAULT 0,
  prerelease INTEGER NOT NULL DEFAULT 0,
  author TEXT NOT NULL DEFAULT '',
  html_url TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  published_at TEXT,
  imported_at TEXT NOT NULL,
  UNIQUE(repo_id, github_id)
);
CREATE TABLE IF NOT EXISTS import_assets (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  release_id TEXT NOT NULL REFERENCES import_releases(id),
  github_id INTEGER NOT NULL,
  name TEXT NOT NULL,
  size INTEGER NOT NULL DEFAULT 0,
  content_type TEXT NOT NULL DEFAULT '',
  path TEXT NOT NULL DEFAULT '',
  sha256 TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(repo_id, github_id)
);
