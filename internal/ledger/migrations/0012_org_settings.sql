-- Organization settings (ADR 0016 §3, docs/changes/git-transport-off.md): one row per
-- organization, written on the first change; an organization without a row has the
-- defaults (git_transport 'on'). org_id is the tenant key (ADR 0006).
CREATE TABLE IF NOT EXISTS org_settings (
  org_id TEXT PRIMARY KEY REFERENCES orgs(id),
  git_transport TEXT NOT NULL DEFAULT 'on',
  git_transport_reason TEXT NOT NULL DEFAULT '',
  updated_by_kind TEXT NOT NULL DEFAULT '',
  updated_by_id TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);
