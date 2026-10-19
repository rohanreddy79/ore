import { keepPreviousData, useMutation, useQuery, useQueryClient, type UseQueryOptions } from '@tanstack/react-query'
import { ApiError, del, get, optional, patch, post, put, repoBase, webappRepoBase } from './api'
import type * as T from './types'

// Query keys are arrays whose prefix identifies the family, so the live
// ledger stream can invalidate by family: ["repo", org, name, "issues"].
export const qk = {
  session: ['session'] as const,
  features: ['features'] as const,
  orgs: ['orgs'] as const,
  repos: (org?: string) => ['repos', org ?? ''] as const,
  repo: (org: string, name: string) => ['repo', org, name] as const,
  r: (org: string, name: string, ...rest: unknown[]) => ['repo', org, name, ...rest] as const,
  events: (filter: Record<string, string | number | undefined>) => ['events', filter] as const,
  identity: (...rest: unknown[]) => ['identity', ...rest] as const,
  search: (q: string) => ['search', q] as const,
}

function retryPolicy(failureCount: number, error: unknown): boolean {
  if (error instanceof ApiError) {
    if (error.status === 0 || error.status >= 500) return failureCount < 2
    return false
  }
  return failureCount < 1
}

type Opts<D> = Omit<UseQueryOptions<D, Error, D, readonly unknown[]>, 'queryKey' | 'queryFn'>

export const defaultQueryOptions = { retry: retryPolicy, staleTime: 5_000, refetchOnWindowFocus: true }

export function useSession(opts?: Opts<T.Me | null>) {
  return useQuery({
    queryKey: qk.session,
    queryFn: async () => {
      try {
        return await get<T.Me>('/api/v0/webapp/session', { quiet: true })
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) return null
        throw e
      }
    },
    retry: false,
    staleTime: 60_000,
    ...opts,
  })
}

export function useMe() {
  return useQuery({
    queryKey: qk.identity('me'),
    queryFn: () => optional(get<T.Me>('/api/v0/identity/me', { quiet: true })),
    retry: false,
    staleTime: 60_000,
  })
}

export function useFeatures() {
  return useQuery({ queryKey: qk.features, queryFn: () => get<T.Features>('/api/v0/webapp/features', { quiet: true }), staleTime: 5 * 60_000, retry: retryPolicy })
}

export const useOrgs = () => useQuery({ queryKey: qk.orgs, queryFn: () => get<T.Org[] | null>('/api/v0/orgs').then((r) => r ?? []), ...defaultQueryOptions })
export const useRepos = (org?: string) => useQuery({ queryKey: qk.repos(org), queryFn: () => get<T.Repo[] | null>(`/api/v0/repos${org ? `?org=${encodeURIComponent(org)}` : ''}`).then((r) => r ?? []), ...defaultQueryOptions })
export const useRepo = (org: string, name: string, opts?: Opts<T.Repo>) => useQuery({ queryKey: qk.repo(org, name), queryFn: () => get<T.Repo>(repoBase(org, name), { quiet: true }), ...defaultQueryOptions, staleTime: 30_000, ...opts })

export const useRefs = (org: string, name: string) => useQuery({ queryKey: qk.r(org, name, 'refs'), queryFn: () => get<Record<string, string>>(`${repoBase(org, name)}/refs`), ...defaultQueryOptions })
export const useTree = (org: string, name: string, ref: string) => useQuery({ queryKey: qk.r(org, name, 'tree', ref), queryFn: () => get<string[] | null>(`${repoBase(org, name)}/tree?ref=${encodeURIComponent(ref)}`, { quiet: true }).then((r) => r ?? []), ...defaultQueryOptions, staleTime: 30_000 })
export const useBlob = (org: string, name: string, ref: string, path: string, enabled = true) =>
  useQuery({ queryKey: qk.r(org, name, 'blob', ref, path), queryFn: () => get<T.Blob>(`${webappRepoBase(org, name)}/blob?ref=${encodeURIComponent(ref)}&path=${encodeURIComponent(path)}`, { quiet: true }), ...defaultQueryOptions, staleTime: 60_000, enabled })
export const useLog = (org: string, name: string, ref: string) => useQuery({ queryKey: qk.r(org, name, 'log', ref), queryFn: () => get<T.LogEntry[] | null>(`${repoBase(org, name)}/log?ref=${encodeURIComponent(ref)}`, { quiet: true }).then((r) => r ?? []), ...defaultQueryOptions })
export const useCommit = (org: string, name: string, oid: string) => useQuery({ queryKey: qk.r(org, name, 'commit', oid), queryFn: () => get<T.CommitDetail>(`${webappRepoBase(org, name)}/commits/${encodeURIComponent(oid)}`, { quiet: true }), ...defaultQueryOptions, staleTime: Infinity })
export const useCompare = (org: string, name: string, base: string, head: string, enabled = true) =>
  useQuery({ queryKey: qk.r(org, name, 'compare', base, head), queryFn: () => get<T.Compare>(`${webappRepoBase(org, name)}/compare?base=${encodeURIComponent(base)}&head=${encodeURIComponent(head)}`, { quiet: true }), ...defaultQueryOptions, enabled })

export const useIssues = (org: string, name: string) => useQuery({ queryKey: qk.r(org, name, 'issues'), queryFn: () => get<T.Issue[] | null>(`${repoBase(org, name)}/issues`).then((r) => r ?? []), ...defaultQueryOptions, placeholderData: keepPreviousData })
export const useIssue = (org: string, name: string, n: number) => useQuery({ queryKey: qk.r(org, name, 'issues', n), queryFn: () => get<T.Issue>(`${repoBase(org, name)}/issues/${n}`, { quiet: true }), ...defaultQueryOptions })
export const useIssueComments = (org: string, name: string, n: number) => useQuery({ queryKey: qk.r(org, name, 'issues', n, 'comments'), queryFn: () => optional(get<T.GatewayComment[] | null>(`${repoBase(org, name)}/issues/${n}/comments`, { quiet: true })).then((r) => r ?? []), ...defaultQueryOptions })

export const useChanges = (org: string, name: string) => useQuery({ queryKey: qk.r(org, name, 'changes'), queryFn: () => get<T.Change[] | null>(`${repoBase(org, name)}/changes`).then((r) => r ?? []), ...defaultQueryOptions, placeholderData: keepPreviousData })
export const useChange = (org: string, name: string, n: number) => useQuery({ queryKey: qk.r(org, name, 'changes', n), queryFn: () => get<T.Change>(`${repoBase(org, name)}/changes/${n}`, { quiet: true }), ...defaultQueryOptions })
export const useChangeDiff = (org: string, name: string, n: number, enabled = true) => useQuery({ queryKey: qk.r(org, name, 'changes', n, 'diff'), queryFn: () => get<string>(`${repoBase(org, name)}/changes/${n}/diff`, { raw: true, quiet: true }), ...defaultQueryOptions, staleTime: 30_000, enabled })
export const usePatchSets = (org: string, name: string, n: number) => useQuery({ queryKey: qk.r(org, name, 'changes', n, 'patchsets'), queryFn: () => optional(get<T.PatchSet[] | null>(`${repoBase(org, name)}/changes/${n}/patchsets`, { quiet: true })).then((r) => r ?? []), ...defaultQueryOptions })
export const useInterdiff = (org: string, name: string, n: number, from: number, to: number, enabled: boolean) =>
  useQuery({ queryKey: qk.r(org, name, 'changes', n, 'interdiff', from, to), queryFn: () => get<T.Interdiff>(`${repoBase(org, name)}/changes/${n}/interdiff?from=${from}&to=${to}`, { quiet: true }), ...defaultQueryOptions, enabled })
export const useChangeComments = (org: string, name: string, n: number) => useQuery({ queryKey: qk.r(org, name, 'changes', n, 'comments'), queryFn: () => optional(get<T.Comment[] | null>(`${repoBase(org, name)}/changes/${n}/comments`, { quiet: true })).then((r) => r ?? []), ...defaultQueryOptions })
export const useReviews = (org: string, name: string, n: number) => useQuery({ queryKey: qk.r(org, name, 'changes', n, 'reviews'), queryFn: () => optional(get<T.Review[] | null>(`${repoBase(org, name)}/changes/${n}/reviews`, { quiet: true })).then((r) => r ?? []), ...defaultQueryOptions })
export const useApprovals = (org: string, name: string, n: number) => useQuery({ queryKey: qk.r(org, name, 'changes', n, 'approvals'), queryFn: () => optional(get<T.ApprovalStatus>(`${repoBase(org, name)}/changes/${n}/approvals`, { quiet: true })), ...defaultQueryOptions })
export const useLandings = (org: string, name: string, n: number) => useQuery({ queryKey: qk.r(org, name, 'changes', n, 'landings'), queryFn: () => optional(get<T.Landing[] | null>(`${repoBase(org, name)}/changes/${n}/landings`, { quiet: true })).then((r) => r ?? []), ...defaultQueryOptions })
export const useReviewPolicy = (org: string, name: string) => useQuery({ queryKey: qk.r(org, name, 'review-policy'), queryFn: () => optional(get<T.ReviewPolicy>(`${repoBase(org, name)}/review/policy`, { quiet: true })), ...defaultQueryOptions, staleTime: 60_000 })
export const useStacks = (org: string, name: string) => useQuery({ queryKey: qk.r(org, name, 'stacks'), queryFn: () => optional(get<T.Stack[] | null>(`${repoBase(org, name)}/stacks`, { quiet: true })).then((r) => r ?? []), ...defaultQueryOptions })
export const useStack = (org: string, name: string, id: string) => useQuery({ queryKey: qk.r(org, name, 'stacks', id), queryFn: () => get<T.Stack>(`${repoBase(org, name)}/stacks/${encodeURIComponent(id)}`, { quiet: true }), ...defaultQueryOptions })

export const useRuns = (org: string, name: string) => useQuery({ queryKey: qk.r(org, name, 'runs'), queryFn: () => get<T.PipelineRun[] | null>(`${repoBase(org, name)}/runs`).then((r) => r ?? []), ...defaultQueryOptions, placeholderData: keepPreviousData })
export const useRun = (org: string, name: string, id: string) => useQuery({ queryKey: qk.r(org, name, 'runs', id), queryFn: () => get<T.PipelineRun>(`${repoBase(org, name)}/runs/${encodeURIComponent(id)}`, { quiet: true }), ...defaultQueryOptions, refetchInterval: (q) => (q.state.data && (q.state.data.status === 'running' || q.state.data.status === 'queued') ? 3000 : false) })
export const useRunPlan = (org: string, name: string, id: string) => useQuery({ queryKey: qk.r(org, name, 'runs', id, 'plan'), queryFn: () => optional(get<T.Plan>(`${repoBase(org, name)}/runs/${encodeURIComponent(id)}/plan`, { quiet: true })), ...defaultQueryOptions, staleTime: 60_000 })
export const useArtifacts = (org: string, name: string, id: string) => useQuery({ queryKey: qk.r(org, name, 'runs', id, 'artifacts'), queryFn: () => optional(get<T.Artifact[] | null>(`${repoBase(org, name)}/runs/${encodeURIComponent(id)}/artifacts`, { quiet: true })).then((r) => r ?? []), ...defaultQueryOptions })
export const useRunLog = (org: string, name: string, id: string, enabled = true) => useQuery({ queryKey: qk.r(org, name, 'runs', id, 'log'), queryFn: () => get<string>(`${repoBase(org, name)}/runs/${encodeURIComponent(id)}/log`, { raw: true, quiet: true }), ...defaultQueryOptions, enabled, staleTime: 0 })

export const useDeployments = (org: string, name: string) => useQuery({ queryKey: qk.r(org, name, 'deployments'), queryFn: () => get<T.Deployment[] | null>(`${repoBase(org, name)}/deployments`).then((r) => r ?? []), ...defaultQueryOptions, placeholderData: keepPreviousData })
export const useDeployment = (org: string, name: string, id: string) => useQuery({ queryKey: qk.r(org, name, 'deployments', id), queryFn: () => optional(get<T.DeploymentView>(`${repoBase(org, name)}/deployments/${encodeURIComponent(id)}`, { quiet: true })), ...defaultQueryOptions })
export const useEnvironments = (org: string, name: string) => useQuery({ queryKey: qk.r(org, name, 'environments'), queryFn: () => optional(get<T.Environment[] | null>(`${repoBase(org, name)}/environments`, { quiet: true })), ...defaultQueryOptions })
export const useDora = (org: string, name: string, days = 30) => useQuery({ queryKey: qk.r(org, name, 'dora', days), queryFn: () => optional(get<T.DORA>(`${repoBase(org, name)}/dora?days=${days}`, { quiet: true })), ...defaultQueryOptions, staleTime: 60_000 })

export const useAgentRuns = (org: string, name: string) => useQuery({ queryKey: qk.r(org, name, 'agent'), queryFn: () => get<T.AgentRun[] | null>(`${repoBase(org, name)}/agent/runs`).then((r) => r ?? []), ...defaultQueryOptions, placeholderData: keepPreviousData })
export const useAgentRun = (org: string, name: string, id: string) => useQuery({ queryKey: qk.r(org, name, 'agent', id), queryFn: () => get<T.AgentRun>(`${repoBase(org, name)}/agent/runs/${encodeURIComponent(id)}`, { quiet: true }), ...defaultQueryOptions, refetchInterval: (q) => (q.state.data?.status === 'running' ? 2000 : false) })
export const useAgentLog = (org: string, name: string, id: string, enabled = true) => useQuery({ queryKey: qk.r(org, name, 'agent', id, 'log'), queryFn: () => get<string>(`${repoBase(org, name)}/agent/runs/${encodeURIComponent(id)}/log`, { raw: true, quiet: true }), ...defaultQueryOptions, enabled, staleTime: 0 })

// Optional sibling APIs: hidden when absent.
export const useFleet = (org: string, name: string, enabled: boolean) => useQuery({ queryKey: qk.r(org, name, 'fleet'), queryFn: () => optional(get<{ agents?: T.FleetAgent[]; evidence?: T.FleetEvidence; paused?: boolean; [k: string]: unknown } | T.FleetAgent[]>(`/api/v0/fleet/repos/${encodeURIComponent(org)}/${encodeURIComponent(name)}`, { quiet: true })), ...defaultQueryOptions, enabled })
export const useFleetEvidence = (org: string, name: string, enabled: boolean) => useQuery({ queryKey: qk.r(org, name, 'fleet', 'evidence'), queryFn: () => optional(get<T.FleetEvidence>(`/api/v0/fleet/repos/${encodeURIComponent(org)}/${encodeURIComponent(name)}/evidence`, { quiet: true })), ...defaultQueryOptions, enabled })
export const useLessons = (enabled: boolean) => useQuery({ queryKey: ['learn', 'lessons'], queryFn: () => optional(get<T.Lesson[] | { lessons: T.Lesson[] }>('/api/v0/learn/lessons', { quiet: true })).then((r) => (Array.isArray(r) ? r : (r?.lessons ?? []))), ...defaultQueryOptions, staleTime: 5 * 60_000, enabled })
export const useWebhooks = (org: string | undefined, enabled: boolean) => useQuery({ queryKey: ['webhooks', org ?? ''], queryFn: () => optional(get<T.Webhook[] | { webhooks: T.Webhook[] }>(`/api/v0/webhooks${org ? `?org=${encodeURIComponent(org)}` : ''}`, { quiet: true })).then((r) => (Array.isArray(r) ? r : (r?.webhooks ?? null))), ...defaultQueryOptions, enabled })

export const useEvents = (filter: { type?: string; subject?: string; after?: number; limit?: number }, opts?: { enabled?: boolean; refetchInterval?: number | false }) =>
  useQuery({
    queryKey: qk.events(filter),
    queryFn: () => {
      const p = new URLSearchParams()
      if (filter.type) p.set('type', filter.type)
      if (filter.subject) p.set('subject', filter.subject)
      if (filter.after) p.set('after', String(filter.after))
      p.set('limit', String(filter.limit ?? 200))
      return get<T.LedgerEvent[] | null>(`/api/v0/events?${p}`).then((r) => r ?? [])
    },
    ...defaultQueryOptions,
    placeholderData: keepPreviousData,
    ...opts,
  })

export const useSearch = (q: string) => useQuery({ queryKey: qk.search(q), queryFn: () => get<{ hits: T.SearchHit[] }>(`/api/v0/webapp/search?q=${encodeURIComponent(q)}`, { quiet: true }).then((r) => r.hits), enabled: q.trim().length >= 2, staleTime: 10_000, retry: false })

// identity
export const useUsers = (enabled: boolean) => useQuery({ queryKey: qk.identity('users'), queryFn: () => optional(get<T.User[] | null>('/api/v0/identity/users', { quiet: true })).then((r) => r ?? []), ...defaultQueryOptions, enabled })
export const useTokens = (enabled: boolean, q = '') => useQuery({ queryKey: qk.identity('tokens', q), queryFn: () => optional(get<T.Token[] | null>(`/api/v0/identity/tokens${q}`, { quiet: true })).then((r) => r ?? []), ...defaultQueryOptions, enabled })
export const useSSHKeys = (enabled: boolean) => useQuery({ queryKey: qk.identity('ssh-keys'), queryFn: () => optional(get<T.SSHKey[] | null>('/api/v0/identity/ssh-keys', { quiet: true })).then((r) => r ?? []), ...defaultQueryOptions, enabled })
export const useAgents = (org: string | undefined, enabled: boolean) => useQuery({ queryKey: qk.identity('agents', org ?? ''), queryFn: () => optional(get<T.Agent[] | null>(`/api/v0/identity/agents${org ? `?org=${encodeURIComponent(org)}` : ''}`, { quiet: true })).then((r) => r ?? []), ...defaultQueryOptions, enabled })
export const useMembers = (org: string, enabled: boolean) => useQuery({ queryKey: qk.identity('members', org), queryFn: () => optional(get<Array<{ user: string; role: string }> | null>(`/api/v0/identity/orgs/${encodeURIComponent(org)}/members`, { quiet: true })).then((r) => r ?? []), ...defaultQueryOptions, enabled })
export const useSecrets = (org: string, name: string, enabled: boolean) => useQuery({ queryKey: qk.r(org, name, 'secrets'), queryFn: () => optional(get<T.Secret[] | null>(`${repoBase(org, name)}/secrets`, { quiet: true })).then((r) => r ?? []), ...defaultQueryOptions, enabled })
export const useGitHubLink = (org: string, name: string, enabled: boolean) => useQuery({ queryKey: qk.r(org, name, 'github'), queryFn: () => optional(get<{ link?: T.GitHubLink; webhook_url?: string } | T.GitHubLink>(`${repoBase(org, name)}/github/link`, { quiet: true })), ...defaultQueryOptions, enabled, retry: false })

/** A mutation that invalidates query families on success. */
export function useAction<TVars, TData = unknown>(fn: (vars: TVars) => Promise<TData>, invalidate: (vars: TVars, data: TData) => readonly unknown[][], opts?: { onSuccess?: (data: TData, vars: TVars) => void }) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onSuccess: (data, vars) => {
      for (const key of invalidate(vars, data)) void qc.invalidateQueries({ queryKey: key })
      opts?.onSuccess?.(data, vars)
    },
  })
}

export const actions = {
  createOrg: (name: string) => post<T.Org>('/api/v0/orgs', { name }),
  createRepo: (body: { org: string; name: string; default_branch?: string; autonomy_level?: number; deploy_port?: number }) => post<T.Repo>('/api/v0/repos', body),
  setLevel: (org: string, name: string, level: number, reason: string) => patch<T.Repo>(repoBase(org, name), { autonomy_level: level, reason }),
  commit: (org: string, name: string, body: { branch?: string; message: string; files: Array<{ path: string; content?: string; delete?: boolean; mode?: string }> }) => post<{ commit: string; ref: string }>(`${repoBase(org, name)}/commits`, body),
  createIssue: (org: string, name: string, body: { title: string; body: string }) => post<T.Issue>(`${repoBase(org, name)}/issues`, body),
  closeIssue: (org: string, name: string, n: number, reason = '') => post<T.Issue>(`${repoBase(org, name)}/issues/${n}/close`, { reason }),
  openChange: (org: string, name: string, body: { source_ref: string; target_ref?: string; title?: string; body?: string }) => post<T.Change>(`${repoBase(org, name)}/changes`, body),
  mergeChange: (org: string, name: string, n: number) => post<{ merge_commit: string; change: T.Change }>(`${repoBase(org, name)}/changes/${n}/merge`),
  closeChange: (org: string, name: string, n: number, reason = '') => post(`${repoBase(org, name)}/changes/${n}/close`, { reason }),
  land: (org: string, name: string, n: number, strategy?: string) => post<{ landing: T.Landing; change: T.Change }>(`${repoBase(org, name)}/changes/${n}/land`, strategy ? { strategy } : {}),
  comment: (org: string, name: string, n: number, body: { body: string; path?: string; start_line?: number; end_line?: number; patch_set?: number; lane?: string }) => post<T.Comment>(`${repoBase(org, name)}/changes/${n}/comments`, body),
  resolveComment: (org: string, name: string, n: number, id: string, resolved: boolean) => post<T.Comment>(`${repoBase(org, name)}/changes/${n}/comments/${encodeURIComponent(id)}/resolve`, { resolved }),
  review: (org: string, name: string, n: number, body: { verdict: string; findings: T.Finding[]; body?: string; patch_set?: number }) => post<T.Review>(`${repoBase(org, name)}/changes/${n}/reviews`, body),
  setReviewPolicy: (org: string, name: string, body: T.ReviewPolicy) => put<T.ReviewPolicy>(`${repoBase(org, name)}/review/policy`, body),
  openStack: (org: string, name: string, branches: string[], target_ref?: string) => post<T.Stack>(`${repoBase(org, name)}/stacks`, target_ref ? { branches, target_ref } : { branches }),
  landStack: (org: string, name: string, id: string, strategy?: string) => post<{ stack: T.Stack; landings: T.Landing[] }>(`${repoBase(org, name)}/stacks/${encodeURIComponent(id)}/land`, strategy ? { strategy } : {}),
  startRun: (org: string, name: string, body: { ref?: string; change?: number }) => post<T.PipelineRun>(`${repoBase(org, name)}/runs`, body),
  deploy: (org: string, name: string, body: { ref?: string; environment?: string }) => post<T.Deployment>(`${repoBase(org, name)}/deployments`, body),
  requestDeployment: (org: string, name: string, env: string, ref: string) => post<T.DeploymentView>(`${repoBase(org, name)}/environments/${encodeURIComponent(env)}/deployments`, { ref }),
  rollback: (org: string, name: string, environment: string) => post<T.Deployment>(`${repoBase(org, name)}/deployments/rollback`, { environment }),
  rollbackEnv: (org: string, name: string, env: string) => post<T.Deployment>(`${repoBase(org, name)}/environments/${encodeURIComponent(env)}/rollback`),
  stop: (org: string, name: string, environment: string) => post(`${repoBase(org, name)}/deployments/stop`, { environment }),
  approve: (org: string, name: string, id: string, comment = '') => post<T.DeploymentView>(`${repoBase(org, name)}/deployments/${encodeURIComponent(id)}/approve`, { comment }),
  reject: (org: string, name: string, id: string, comment = '') => post<T.DeploymentView>(`${repoBase(org, name)}/deployments/${encodeURIComponent(id)}/reject`, { comment }),
  extend: (org: string, name: string, id: string, hours: number) => post(`${repoBase(org, name)}/deployments/${encodeURIComponent(id)}/extend`, { hours }),
  createEnvironment: (org: string, name: string, body: { name: string; kind?: string; target?: T.EnvTarget; policy?: Partial<T.EnvPolicy> }) => post<T.Environment>(`${repoBase(org, name)}/environments`, body),
  updateEnvironment: (org: string, name: string, env: string, body: Partial<T.Environment>) => put<T.Environment>(`${repoBase(org, name)}/environments/${encodeURIComponent(env)}`, body),
  deleteEnvironment: (org: string, name: string, env: string) => del(`${repoBase(org, name)}/environments/${encodeURIComponent(env)}`),
  runAgent: (org: string, name: string, body: { issue?: number; prompt?: string }) => post<T.AgentRun>(`${repoBase(org, name)}/agent/runs`, body),
  fleetPause: (org: string, name: string, paused: boolean) => post(`/api/v0/fleet/repos/${encodeURIComponent(org)}/${encodeURIComponent(name)}/${paused ? 'pause' : 'resume'}`),
  createUser: (body: { name: string; email?: string; role?: string; memberships?: Array<{ org: string; role: string }> }) => post<T.User>('/api/v0/identity/users', body),
  createToken: (body: { name: string; scopes?: string[]; expires_in?: string; user?: string; agent?: string }) => post<T.Token>('/api/v0/identity/tokens', body),
  revokeToken: (id: string) => del(`/api/v0/identity/tokens/${encodeURIComponent(id)}`),
  addKey: (body: { public_key: string; title: string }) => post<T.SSHKey>('/api/v0/identity/ssh-keys', body),
  removeKey: (id: string) => del(`/api/v0/identity/ssh-keys/${encodeURIComponent(id)}`),
  createAgent: (body: { org: string; name: string; owner?: string; budgets?: Record<string, unknown> }) => post<T.Agent>('/api/v0/identity/agents', body),
  addMember: (org: string, body: { user: string; role: string }) => post(`/api/v0/identity/orgs/${encodeURIComponent(org)}/members`, body),
  removeMember: (org: string, user: string) => del(`/api/v0/identity/orgs/${encodeURIComponent(org)}/members/${encodeURIComponent(user)}`),
  setSecret: (org: string, name: string, body: { name: string; value: string; environment?: string }) => post(`${repoBase(org, name)}/secrets`, body),
  deleteSecret: (org: string, name: string, secret: string, environment?: string) => del(`${repoBase(org, name)}/secrets/${encodeURIComponent(secret)}${environment ? `?environment=${encodeURIComponent(environment)}` : ''}`),
  linkGitHub: (org: string, name: string, repository: string) => post(`${repoBase(org, name)}/github/link`, { repository }),
  unlinkGitHub: (org: string, name: string) => del(`${repoBase(org, name)}/github/link`),
  syncGitHub: (org: string, name: string) => post(`${repoBase(org, name)}/github/sync`),
  signIn: (token: string) => post<T.Me>('/api/v0/webapp/session', { token }, { quiet: true }),
  signOut: () => del('/api/v0/webapp/session', { quiet: true }),
}
