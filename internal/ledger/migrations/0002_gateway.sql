-- Safe-outputs gateway (ADR 0009): agent sessions with a single-repository scope,
-- write counters for the per-session rate limit, and comments written through
-- the gateway. Every table carries org_id (ADR 0006).
CREATE TABLE IF NOT EXISTS gateway_sessions (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  scope TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  state TEXT NOT NULL DEFAULT 'open',
  writes INTEGER NOT NULL DEFAULT 0,
  refused INTEGER NOT NULL DEFAULT 0,
  window_start TEXT NOT NULL DEFAULT '',
  window_writes INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  closed_at TEXT
);
CREATE INDEX IF NOT EXISTS gateway_sessions_repo ON gateway_sessions(repo_id, created_at);
CREATE TABLE IF NOT EXISTS comments (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  target_kind TEXT NOT NULL,
  target_id TEXT NOT NULL,
  target_number INTEGER NOT NULL,
  body TEXT NOT NULL,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS comments_target ON comments(repo_id, target_kind, target_id, created_at);
