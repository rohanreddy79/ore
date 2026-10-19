-- Agent fleet (docs/12 §2-5, ADR 0009, ADR 0015): roles, budgets, per-repository
-- fleet settings, the instance kill switch, watcher decisions (loop prevention),
-- change flags and watcher cursors. Rows carry org_id except the two
-- instance-scoped tables (fleet_instance, fleet_cursors).
CREATE TABLE IF NOT EXISTS fleet_roles (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  name TEXT NOT NULL CHECK (name IN ('implementer', 'reviewer', 'watcher', 'dependency-updater')),
  adapter_cmd TEXT NOT NULL DEFAULT '',
  adapter_name TEXT NOT NULL DEFAULT '',
  prompt_profile TEXT NOT NULL DEFAULT '',
  budgets TEXT NOT NULL DEFAULT '{}',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(repo_id, name)
);
CREATE TABLE IF NOT EXISTS fleet_budgets (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  principal TEXT NOT NULL DEFAULT '',
  ci_minutes_per_day INTEGER NOT NULL DEFAULT 0,
  runs_per_day INTEGER NOT NULL DEFAULT 0,
  open_changes INTEGER NOT NULL DEFAULT 0,
  writes_per_hour INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  UNIQUE(repo_id, principal)
);
CREATE TABLE IF NOT EXISTS fleet_settings (
  repo_id TEXT PRIMARY KEY REFERENCES repos(id),
  org_id TEXT NOT NULL,
  paused INTEGER NOT NULL DEFAULT 0,
  pause_reason TEXT NOT NULL DEFAULT '',
  auto_promote INTEGER NOT NULL DEFAULT 0,
  auto_demote INTEGER NOT NULL DEFAULT 1,
  watcher_retries INTEGER NOT NULL DEFAULT 1,
  watcher_window_minutes INTEGER NOT NULL DEFAULT 60,
  evidence_window_days INTEGER NOT NULL DEFAULT 30,
  thresholds TEXT NOT NULL DEFAULT '{}',
  last_level_change_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS fleet_instance (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  paused INTEGER NOT NULL DEFAULT 0,
  pause_reason TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS fleet_signals (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  watcher_id TEXT NOT NULL,
  event_seq INTEGER NOT NULL,
  event_type TEXT NOT NULL,
  issue_id TEXT,
  cause_run_id TEXT,
  run_id TEXT,
  depth INTEGER NOT NULL DEFAULT 0,
  decision TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(repo_id, event_seq)
);
CREATE INDEX IF NOT EXISTS fleet_signals_run ON fleet_signals(run_id);
CREATE INDEX IF NOT EXISTS fleet_signals_issue ON fleet_signals(issue_id);
CREATE TABLE IF NOT EXISTS fleet_change_flags (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  change_id TEXT NOT NULL REFERENCES changes(id),
  reason TEXT NOT NULL DEFAULT '',
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(change_id)
);
CREATE TABLE IF NOT EXISTS fleet_cursors (
  watcher_id TEXT PRIMARY KEY,
  after_seq INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL
);
