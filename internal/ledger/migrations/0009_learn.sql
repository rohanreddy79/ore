-- Learning engine (docs/changes/learn.md): lesson runs are ledger entities;
-- progress is per learner. Every table carries org_id (ADR 0006).
CREATE TABLE IF NOT EXISTS lesson_runs (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  repo_id TEXT NOT NULL REFERENCES repos(id),
  learner_kind TEXT NOT NULL,
  learner_id TEXT NOT NULL,
  lesson_id TEXT NOT NULL,
  lesson_version TEXT NOT NULL,
  mode TEXT NOT NULL,
  language TEXT NOT NULL DEFAULT 'en',
  step INTEGER NOT NULL DEFAULT 1,
  status TEXT NOT NULL DEFAULT 'active',
  attempts INTEGER NOT NULL DEFAULT 0,
  step_hints INTEGER NOT NULL DEFAULT 0,
  hints_used INTEGER NOT NULL DEFAULT 0,
  evidence TEXT NOT NULL DEFAULT '{}',
  last_event_seq INTEGER NOT NULL DEFAULT 0,
  started_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  completed_at TEXT
);
CREATE INDEX IF NOT EXISTS lesson_runs_repo ON lesson_runs(repo_id, status);
CREATE INDEX IF NOT EXISTS lesson_runs_learner ON lesson_runs(learner_kind, learner_id, lesson_id, status);
CREATE TABLE IF NOT EXISTS lesson_progress (
  org_id TEXT NOT NULL,
  learner_kind TEXT NOT NULL,
  learner_id TEXT NOT NULL,
  lesson_id TEXT NOT NULL,
  status TEXT NOT NULL,
  run_id TEXT NOT NULL,
  lesson_version TEXT NOT NULL,
  completed_at TEXT,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (org_id, learner_kind, learner_id, lesson_id)
);
