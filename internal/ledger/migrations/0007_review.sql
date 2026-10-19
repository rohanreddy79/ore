-- Review v1 (ADR 0008, ADR 0014 §3-4): patch sets, comments, structured reviews,
-- stacks, landings and the per-repository review policy. Every table carries
-- org_id (ADR 0006). change_id columns reference changes.id (the row id), not the
-- Gerrit-style change-id string.
CREATE TABLE IF NOT EXISTS patch_sets (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  change_id TEXT NOT NULL REFERENCES changes(id),
  number INTEGER NOT NULL,
  base_oid TEXT NOT NULL,
  head_oid TEXT NOT NULL,
  pushed_by_kind TEXT NOT NULL,
  pushed_by_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(change_id, number)
);
CREATE TABLE IF NOT EXISTS change_comments (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  change_id TEXT NOT NULL REFERENCES changes(id),
  patch_set INTEGER NOT NULL,
  path TEXT NOT NULL DEFAULT '',
  start_line INTEGER,
  end_line INTEGER,
  body TEXT NOT NULL,
  author_kind TEXT NOT NULL,
  author_id TEXT NOT NULL,
  lane TEXT NOT NULL CHECK (lane IN ('human', 'bot', 'agent')),
  resolved INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS change_comments_change ON change_comments(change_id, created_at);
CREATE TABLE IF NOT EXISTS reviews (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  change_id TEXT NOT NULL REFERENCES changes(id),
  patch_set INTEGER NOT NULL,
  reviewer_kind TEXT NOT NULL,
  reviewer_id TEXT NOT NULL,
  lane TEXT NOT NULL CHECK (lane IN ('human', 'bot', 'agent')),
  verdict TEXT NOT NULL CHECK (verdict IN ('comment', 'approve', 'request_changes')),
  findings TEXT NOT NULL DEFAULT '[]',
  body TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS reviews_change ON reviews(change_id, patch_set, created_at);
CREATE TABLE IF NOT EXISTS stacks (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  target_ref TEXT NOT NULL,
  change_ids TEXT NOT NULL DEFAULT '[]',
  state TEXT NOT NULL DEFAULT 'open',
  failed_change_id TEXT,
  error TEXT NOT NULL DEFAULT '',
  created_by_kind TEXT NOT NULL,
  created_by_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS stacks_repo ON stacks(repo_id, created_at);
CREATE TABLE IF NOT EXISTS landings (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  change_id TEXT NOT NULL REFERENCES changes(id),
  patch_set INTEGER NOT NULL,
  strategy TEXT NOT NULL CHECK (strategy IN ('queue', 'land_when_green', 'optimistic')),
  status TEXT NOT NULL,
  merge_oid TEXT,
  revert_oid TEXT,
  verification_run_id TEXT,
  issue_number INTEGER,
  error TEXT NOT NULL DEFAULT '',
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  finished_at TEXT
);
CREATE INDEX IF NOT EXISTS landings_change ON landings(change_id, created_at);
CREATE TABLE IF NOT EXISTS review_policies (
  repo_id TEXT PRIMARY KEY REFERENCES repos(id),
  org_id TEXT NOT NULL,
  required_approvals INTEGER NOT NULL DEFAULT 0,
  allow_agent_approvals INTEGER NOT NULL DEFAULT 0,
  default_strategy TEXT NOT NULL DEFAULT 'queue',
  updated_at TEXT NOT NULL
);
