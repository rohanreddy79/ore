-- Identity slice (docs/changes/identity-ssh.md): principals, memberships, tokens, SSH keys, agents.
-- users are site-wide (memberships carry the org); tokens and ssh_keys carry org_id as
-- '' for site-wide user credentials and the agent's org for agent tokens (ADR 0006, ADR 0009).
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  email TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT 'member',
  status TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS memberships (
  org_id TEXT NOT NULL REFERENCES orgs(id),
  user_id TEXT NOT NULL REFERENCES users(id),
  role TEXT NOT NULL DEFAULT 'member',
  created_at TEXT NOT NULL,
  PRIMARY KEY (org_id, user_id)
);
CREATE TABLE IF NOT EXISTS agents (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL REFERENCES orgs(id),
  name TEXT NOT NULL,
  owner_user_id TEXT NOT NULL REFERENCES users(id),
  budgets TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL,
  UNIQUE(org_id, name)
);
CREATE TABLE IF NOT EXISTS tokens (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL DEFAULT '',
  user_id TEXT REFERENCES users(id),
  agent_id TEXT REFERENCES agents(id),
  name TEXT NOT NULL DEFAULT '',
  secret_hash TEXT NOT NULL UNIQUE,
  prefix TEXT NOT NULL DEFAULT '',
  scopes TEXT NOT NULL DEFAULT '["read"]',
  expires_at TEXT,
  revoked_at TEXT,
  last_used_at TEXT,
  created_at TEXT NOT NULL,
  CHECK ((user_id IS NULL) <> (agent_id IS NULL))
);
CREATE INDEX IF NOT EXISTS tokens_user ON tokens(user_id);
CREATE INDEX IF NOT EXISTS tokens_agent ON tokens(agent_id);
CREATE TABLE IF NOT EXISTS ssh_keys (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL DEFAULT '',
  user_id TEXT NOT NULL REFERENCES users(id),
  public_key TEXT NOT NULL,
  fingerprint TEXT NOT NULL UNIQUE,
  title TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  last_used_at TEXT
);
CREATE INDEX IF NOT EXISTS ssh_keys_user ON ssh_keys(user_id);
