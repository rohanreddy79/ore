// JSON shapes of the Ore API (internal/ledger/entities.go and the slices'
// docs/changes/*.md). Optional fields mirror omitempty.

export type Actor = { kind: string; id: string }

export type Org = { id: string; name: string; created_at: string }

export type Repo = {
  id: string
  org_id: string
  org: string
  name: string
  default_branch: string
  autonomy_level: number
  deploy_port: number
  created_at: string
}

export type Issue = {
  id: string
  org_id: string
  repo_id: string
  number: number
  title: string
  body: string
  state: 'open' | 'closed' | string
  created_at: string
  closed_at?: string
}

export type Change = {
  id: string
  org_id: string
  repo_id: string
  number: number
  change_id: string
  title: string
  body: string
  source_ref: string
  target_ref: string
  state: 'open' | 'merged' | 'closed' | 'reverted' | string
  base_oid: string
  head_oid: string
  merge_oid?: string
  provenance: string
  created_at: string
  merged_at?: string
}

export type PipelineRun = {
  id: string
  org_id: string
  repo_id: string
  change_id?: string
  commit_oid: string
  status: 'queued' | 'running' | 'succeeded' | 'failed' | string
  tasks: string
  created_at: string
  started_at?: string
  finished_at?: string
}

export type TaskStep = { name?: string; outcome?: string; conclusion?: string; exit_code?: number; duration_ms?: number; reason?: string }
export type Task = {
  id: string
  name?: string
  image?: string
  status?: string
  exit_code?: number
  duration_ms?: number
  reason?: string
  pipeline?: string
  content_hash?: string
  cache_hit?: boolean
  continue_on_error?: boolean
  matrix_key?: string
  steps?: TaskStep[]
  needs?: string[]
  divergences?: Divergence[]
  summary?: string
  attributes?: Record<string, unknown>
}
export type Divergence = { code: string; message?: string; detail?: string; path?: string; job?: string; step?: string }

export type Plan = {
  ir?: { tasks?: Array<{ id: string; name?: string; needs?: string[]; depends_on?: string[]; image?: string; steps?: unknown[]; content_hash?: string; inputs?: { caches?: unknown[] }; [k: string]: unknown }>; [k: string]: unknown }
  divergences?: Divergence[]
  refused?: string | boolean
  [k: string]: unknown
}

export type Artifact = { name: string; size?: number; files?: number | string[]; digest?: string; created_at?: string }

export type Deployment = {
  id: string
  org_id: string
  repo_id: string
  environment: string
  commit_oid: string
  image: string
  container_id: string
  url: string
  status: string
  previous_id?: string
  rollback_of?: string
  actor: Actor
  created_at: string
  finished_at?: string
}

export type PolicyCheck = { rule: string; pass: boolean; detail?: string }
export type Decision = { outcome?: string; checks?: PolicyCheck[]; approvals?: number; required?: number; wait_until?: string; bypass?: boolean; [k: string]: unknown }
export type Rollout = { deployment_id: string; environment_id: string; source_ref: string; decision: Decision; health?: { ok?: boolean; status?: number; url?: string; detail?: string; [k: string]: unknown }; failure?: string; recovery_seconds?: number }
export type Approval = { id: string; deployment_id: string; approver: string; decision: 'approve' | 'reject' | string; comment?: string; at: string }
export type TTL = { deployment_id: string; expires_at: string; extensions: number }
export type DeploymentView = Deployment & { rollout?: Rollout; approvals?: Approval[]; ttl?: TTL }

export type EnvTarget = { kind: 'local-docker' | 'ssh-docker' | 'none' | string; host?: string; user?: string; port?: number; key_ref?: string }
export type EnvPolicy = {
  allowed_refs: string[]
  required_approvals: number
  approvers: string[]
  wait_seconds: number
  require_green_pipeline: boolean
  allow_agent_deploys: boolean
  health_path: string
  health_timeout_seconds: number
  auto_rollback: boolean
  preview_ttl_seconds?: number
}
export type Environment = { id: string; org_id: string; repo_id: string; name: string; kind: string; target: EnvTarget; policy: EnvPolicy; created_at?: string; updated_at?: string; implicit?: boolean }

export type DORA = {
  repo: string
  days: number
  since: string
  until: string
  deployment_frequency?: { total?: number; succeeded?: number; rollbacks?: number; per_day?: number; per_week?: number }
  lead_time?: { samples?: number; median_seconds?: number; p90_seconds?: number }
  change_failure_rate?: { rate?: number; failed?: number; total?: number }
  failed_deployment_recovery_time?: { samples?: number; median_seconds?: number; p90_seconds?: number }
}

export type AgentRun = {
  id: string
  org_id: string
  repo_id: string
  issue_id?: string
  adapter: string
  status: 'running' | 'completed' | 'failed' | 'blocked' | string
  step: string
  branch: string
  change_id?: string
  pipeline_run_id?: string
  deployment_id?: string
  error?: string
  created_at: string
  finished_at?: string
}

export type LedgerEvent = {
  seq?: number
  specversion: string
  id: string
  source: string
  type: string
  subject: string
  time: string
  datacontenttype: string
  oreactor: Actor
  oreorg: string
  data: unknown
}

export type LogEntry = { oid: string; author: string; email: string; date: string; subject: string }
export type CommitDetail = { oid: string; author: string; email: string; date: string; parents: string[]; subject: string; body: string; stat: string; diff: string }
export type Blob = { path: string; ref: string; size: number; lines: number; binary: boolean; truncated: boolean; content: string; last_commit?: LogEntry }
export type Compare = { base: string; head: string; merge_base: string; diff: string; stat: string; commits: LogEntry[] }

// review v1
export type PatchSet = { id: string; number: number; base_oid: string; head_oid: string; pushed_by: Actor; created_at: string }
export type Interdiff = { from: PatchSet; to: PatchSet; identical: boolean; rebased: boolean; diff: string; range_diff?: string; note?: string }
export type Anchor = { patch_set: number; path: string; start_line: number; end_line: number; side?: string }
export type Comment = { id: string; change_id: string; patch_set: number; path?: string; start_line?: number; end_line?: number; body: string; author: Actor; lane: 'human' | 'bot' | 'agent' | string; resolved: boolean; created_at: string; anchor?: Anchor; outdated: boolean }
export type Finding = { severity: 'info' | 'low' | 'medium' | 'high' | 'critical' | string; confidence: number; category?: string; path: string; range?: { start_line: number; end_line: number }; message: string; suggested_fix?: string; fingerprint?: string }
export type Review = { id: string; patch_set: number; reviewer: Actor; lane: string; verdict: 'comment' | 'approve' | 'request_changes' | string; findings: Finding[]; body?: string; created_at: string; counts: boolean }
export type ReviewPolicy = { required_approvals: number; allow_agent_approvals: boolean; default_strategy: 'queue' | 'land_when_green' | 'optimistic' | string }
export type ApprovalStatus = { patch_set: number; required: number; approvals: number; approvers: Actor[]; ignored?: Array<{ reviewer: Actor; reason: string }>; changes_requested_by?: Actor[]; landable: boolean; reason?: string }
export type Landing = { id: string; change_id: string; change_number: number; patch_set: number; strategy: string; status: string; merge_oid?: string; revert_oid?: string; verification_run_id?: string; issue_number?: number; error?: string; actor: Actor; created_at: string; finished_at?: string }
export type Stack = { id: string; target_ref: string; change_ids: string[]; state: 'open' | 'landing' | 'landed' | 'aborted' | string; failed_change_id?: string; failed_change_number?: number; error?: string; created_by: Actor; created_at: string; updated_at: string; changes?: Change[] }

// gateway comments (issues)
export type GatewayComment = { id: string; target_kind: string; target_id: string; target_number: number; body: string; actor_kind: string; actor_id: string; session_id?: string; created_at: string }

// identity
export type User = { id: string; name: string; email?: string; role: string; status?: string; created_at?: string }
export type Membership = { user?: string; org: string; role: string }
export type Token = { id: string; name: string; prefix: string; scopes: string[] | string; user?: string; agent?: string; expires_at?: string; revoked_at?: string; last_used_at?: string; created_at: string; token?: string }
export type SSHKey = { id: string; title: string; fingerprint: string; user?: string; last_used_at?: string; created_at: string }
export type Agent = { id: string; org_id?: string; org?: string; name: string; owner: string; budgets?: Record<string, unknown> | string; status?: string; created_at?: string }
export type Me = { actor: Actor; user?: User; agent?: Agent; token?: Token; admin?: boolean }

export type Secret = { name: string; environment?: string; created_at?: string; updated_at?: string }
export type GitHubLink = { owner: string; name: string; default_branch?: string; last_sync_at?: string; last_sync_status?: string; last_sync_error?: string; next_sync_at?: string; installation_id?: number; [k: string]: unknown }

export type Features = {
  extensions: string[]
  webapp: { built: boolean; prefix: string }
  identity: boolean
  review_v1: boolean
  environments: boolean
  pipelines_actions: boolean
  gateway: boolean
  mcp: boolean
  github: boolean
  fleet: boolean
  learn: boolean
  webhooks: boolean
  base_url: string
  ssh_addr?: string
  levels: string[]
}

export type SearchHit = { kind: 'repo' | 'issue' | 'change'; repo: string; number?: number; title: string; state?: string; url: string }

// fleet (docs/12 §4; API from the agent-fleet slice when present)
export type FleetAgent = { id?: string; name: string; role?: string; status?: string; budgets?: Record<string, unknown>; spent?: Record<string, unknown>; [k: string]: unknown }
export type FleetEvidence = { acceptance_rate?: number; fix_rate?: number; rollback_rate?: number; recovery_seconds?: number; recommended_level?: number; samples?: number; [k: string]: unknown }

// learn
export type Lesson = { id: string; slug?: string; title: string; summary?: string; track?: string; [k: string]: unknown }

// webhooks
export type Webhook = { id: string; url: string; types?: string[]; active?: boolean; last_delivery?: string; failures?: number; [k: string]: unknown }
