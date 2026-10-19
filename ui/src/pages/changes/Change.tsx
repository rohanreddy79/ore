import { useCallback, useMemo, useState, type ReactElement } from 'react'
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Bot, Check, ChevronDown, ChevronRight, GitMerge, GitPullRequest, Layers, MessageSquare, Play, Rocket, ShieldCheck, User, Wrench, X } from 'lucide-react'
import { ActorChip, Button, Callout, Card, Dialog, Field, Input, Pill, Select, StatusPill, Textarea, cx, useToast } from '../../design'
import { DiffViewer, anchorKey, type LineRef } from '../../diff/DiffViewer'
import { parseUnifiedDiff } from '../../diff/parse'
import { ApiError, get, webappRepoBase } from '../../lib/api'
import { refName } from '../../lib/format'
import { actions, qk, useApprovals, useChange, useChangeComments, useChangeDiff, useFeatures, useInterdiff, useLandings, usePatchSets, useReviewPolicy, useReviews, useRuns, useStacks } from '../../lib/queries'
import type { Blob, Comment, Finding, Review } from '../../lib/types'
import { Markdown, QueryState, TimeAgo, useDocumentTitle } from '../../shell/common'
import { NotFound } from '../../shell/NotFound'
import { provenanceOf } from './Changes'

const STRATEGIES: Array<{ id: 'queue' | 'land_when_green' | 'optimistic'; title: string; desc: string; level: string }> = [
  { id: 'queue', title: 'Queue', desc: 'Check policy, then merge server-side now. Ore runs no verification itself; use it when the pipeline already ran on the head and humans decide.', level: 'L2–L3' },
  { id: 'land_when_green', title: 'Land when green', desc: 'Check policy, run the pipeline on the head, merge only if it succeeds and the branch did not move meanwhile. Conservative: the merge result itself is not tested.', level: 'L3' },
  { id: 'optimistic', title: 'Optimistic', desc: 'Merge immediately, verify the merge commit on the target, and on a red run revert server-side, mark the change reverted and open a fix issue. No merge lock; needs a pipeline you trust.', level: 'L4–L5' },
]

type Tab = 'conversation' | 'files' | 'checks'

export function ChangePage() {
  const { org, repo, n } = useParams({ from: '/$org/$repo/changes/$n' })
  const search = useSearch({ from: '/$org/$repo/changes/$n' })
  const navigate = useNavigate()
  const qc = useQueryClient()
  const toast = useToast()
  const num = Number(n)
  const change = useChange(org, repo, num)
  const { data: features } = useFeatures()
  const rv1 = features?.review_v1 !== false
  const patchsets = usePatchSets(org, repo, num)
  const comments = useChangeComments(org, repo, num)
  const reviews = useReviews(org, repo, num)
  const approvals = useApprovals(org, repo, num)
  const landings = useLandings(org, repo, num)
  const policy = useReviewPolicy(org, repo)
  const runs = useRuns(org, repo)
  const stacks = useStacks(org, repo)
  const latestPs = patchsets.data?.length ? Math.max(...patchsets.data.map((p) => p.number)) : 0
  const ps = search.ps ?? latestPs
  const from = search.from
  const interdiffMode = !!from && !!ps && from !== ps
  const diff = useChangeDiff(org, repo, num, !interdiffMode && (ps === latestPs || !latestPs))
  const interdiff = useInterdiff(org, repo, num, from ?? 0, ps, interdiffMode)
  const psDiff = useInterdiff(org, repo, num, 0, ps, !interdiffMode && !!latestPs && ps !== latestPs && ps > 0)
  const tab: Tab = (search.tab as Tab) ?? 'conversation'
  const [landOpen, setLandOpen] = useState(false)
  const [reviewOpen, setReviewOpen] = useState(false)
  const [draft, setDraft] = useState<LineRef | null>(null)
  const [focus, setFocus] = useState<LineRef | null>(null)
  const [collapsedLanes, setCollapsedLanes] = useState<Set<string>>(new Set())
  useDocumentTitle(change.data ? `#${num} ${change.data.title}` : `Change #${num}`)

  const setSearch = (patch: Partial<typeof search>) => void navigate({ to: '.', search: { ...search, ...patch }, replace: true })
  const changeRuns = useMemo(() => (runs.data ?? []).filter((r) => r.change_id === change.data?.id), [runs.data, change.data?.id])
  const latestRun = changeRuns[0]
  const inStack = useMemo(() => (stacks.data ?? []).find((s) => change.data && s.change_ids.includes(change.data.id)), [stacks.data, change.data])

  const diffText = interdiffMode ? (interdiff.data?.diff ?? '') : ps && latestPs && ps !== latestPs ? (psDiff.data?.diff ?? '') : (diff.data ?? '')
  const diffLoading = interdiffMode ? interdiff.isPending : ps && latestPs && ps !== latestPs ? psDiff.isPending : diff.isPending
  const files = useMemo(() => parseUnifiedDiff(diffText), [diffText])

  const threads = useMemo(() => {
    const m = new Map<string, Comment[]>()
    for (const c of comments.data ?? []) {
      const a = c.anchor
      if (!a || !a.path) continue
      const k = anchorKey(a.path, 'new', a.end_line || a.start_line)
      ;(m.get(k) ?? m.set(k, []).get(k)!).push(c)
    }
    return m
  }, [comments.data])
  const threadKeys = useMemo(() => {
    const s = new Set(threads.keys())
    if (draft) s.add(anchorKey(draft.path, draft.side, draft.line))
    return s
  }, [threads, draft])
  const findingsByPath = useMemo(() => {
    const m = new Map<string, Finding[]>()
    for (const r of reviews.data ?? []) for (const f of r.findings ?? []) (m.get(f.path) ?? m.set(f.path, []).get(f.path)!).push(f)
    return m
  }, [reviews.data])
  const fileBadges = useMemo(() => {
    const m = new Map<string, ReactElement>()
    for (const [p, fs] of findingsByPath) m.set(p, <Pill small tone={fs.some((f) => f.severity === 'high' || f.severity === 'critical') ? 'danger' : 'warn'}>{fs.length} finding{fs.length === 1 ? '' : 's'}</Pill>)
    return m
  }, [findingsByPath])

  const invalidate = useCallback(() => qc.invalidateQueries({ queryKey: qk.r(org, repo) }), [qc, org, repo])
  const blobLoader = useCallback(
    (path: string, side: 'old' | 'new') => {
      const c = change.data
      if (!c) return Promise.resolve('')
      const ref = side === 'new' ? c.head_oid : c.base_oid
      return get<Blob>(`${webappRepoBase(org, repo)}/blob?ref=${encodeURIComponent(ref)}&path=${encodeURIComponent(path)}`, { quiet: true }).then((b) => b.content)
    },
    [change.data, org, repo],
  )

  const post = async (fn: () => Promise<unknown>, ok: string) => {
    try {
      await fn()
      await invalidate()
      toast.push({ tone: 'ok', title: ok })
      return true
    } catch {
      return false
    }
  }

  if (!Number.isFinite(num) || num <= 0) return <NotFound what="change" />

  return (
    <QueryState query={change} notFound={<NotFound what="change" />}>
      {(c) => {
        const prov = provenanceOf(c)
        const isOpen = c.state === 'open'
        return (
          <div className="stack">
            <div className="page-head">
              <div style={{ minWidth: 0 }}>
                <h1 className="break">
                  {c.title || `Change from ${refName(c.source_ref)}`} <span className="muted" style={{ fontWeight: 400 }}>#{c.number}</span>
                </h1>
                <div className="row sub" style={{ marginTop: 'var(--sp-2)' }}>
                  <StatusPill status={c.state} />
                  <span className="mono">
                    {refName(c.source_ref)} → {refName(c.target_ref)}
                  </span>
                  {prov?.kind === 'agent' ? (
                    <Pill tone="agent" title={`Provenance: ${c.provenance}`}>
                      <Bot /> agent-authored · {prov.id}
                      {prov.model ? ` · ${prov.model}` : ''}
                    </Pill>
                  ) : (
                    <Pill title={`Provenance: ${c.provenance}`}>
                      <User /> human-authored
                    </Pill>
                  )}
                  <span>
                    opened <TimeAgo iso={c.created_at} />
                  </span>
                  {c.merged_at ? (
                    <span>
                      · merged <TimeAgo iso={c.merged_at} /> as{' '}
                      <Link to="/$org/$repo/commit/$oid" params={{ org, repo, oid: c.merge_oid ?? '' }} className="mono">
                        {c.merge_oid?.slice(0, 7)}
                      </Link>
                    </span>
                  ) : null}
                  <code className="small muted" title="Stable change-id across rebases">
                    {c.change_id.slice(0, 12)}
                  </code>
                  {inStack ? (
                    <Link to="/$org/$repo/stacks/$id" params={{ org, repo, id: inStack.id }} className="pill info">
                      <Layers size={12} /> in a stack
                    </Link>
                  ) : null}
                </div>
              </div>
              <div className="row">
                {isOpen ? (
                  <>
                    <Button onClick={() => setReviewOpen(true)}>
                      <ShieldCheck /> Review
                    </Button>
                    <Button onClick={() => void post(() => actions.startRun(org, repo, { change: num }), 'Pipeline started on the head')}>
                      <Play /> Verify
                    </Button>
                    {rv1 ? (
                      <Button variant="primary" onClick={() => setLandOpen(true)}>
                        <Rocket /> Land
                      </Button>
                    ) : (
                      <Button variant="primary" onClick={() => void post(() => actions.mergeChange(org, repo, num), 'Merged server-side')}>
                        <GitMerge /> Merge
                      </Button>
                    )}
                    <Button variant="ghost" onClick={() => void post(() => actions.closeChange(org, repo, num, 'closed from the web app'), 'Closed')}>
                      <X /> Close
                    </Button>
                  </>
                ) : null}
              </div>
            </div>

            {c.state === 'reverted' ? (
              <Callout tone="danger" icon={<X />}>
                This change landed optimistically and was reverted because post-merge verification failed. Rebase the fix onto the target (the original commits are already ancestors) and push again to reopen it.
              </Callout>
            ) : null}

            <nav className="tabs" aria-label="Change sections">
              {(
                [
                  ['conversation', 'Conversation', (comments.data?.length ?? 0) + (reviews.data?.length ?? 0)],
                  ['files', 'Files', files.length],
                  ['checks', 'Checks', changeRuns.length],
                ] as Array<[Tab, string, number]>
              ).map(([id, label, count]) => (
                <button key={id} type="button" className="tab" aria-current={tab === id ? 'page' : undefined} onClick={() => setSearch({ tab: id === 'conversation' ? undefined : id })} style={{ background: 'none', border: 0, borderBottom: '2px solid transparent', cursor: 'pointer' }}>
                  {label}
                  <span className="count">{count}</span>
                </button>
              ))}
            </nav>

            {tab === 'conversation' ? (
              <div className="split">
                <div className="stack">
                  <div className="comment">
                    <div className="comment-head">
                      {prov?.kind === 'agent' ? <ActorChip actor={{ kind: 'agent', id: prov.id }} /> : <span className="muted">Description</span>}
                      <span className="when">
                        <TimeAgo iso={c.created_at} />
                      </span>
                    </div>
                    <div className="comment-body">
                      <Markdown source={c.body} />
                    </div>
                  </div>
                  {(['human', 'agent', 'bot'] as const).map((lane) => {
                    const items = [...(comments.data ?? []).filter((x) => (x.lane ?? 'human') === lane), ...(reviews.data ?? []).filter((r) => (r.lane ?? 'human') === lane)].sort((a, b) => a.created_at.localeCompare(b.created_at))
                    if (items.length === 0) return null
                    const collapsed = collapsedLanes.has(lane)
                    return (
                      <section key={lane} aria-label={`${lane} lane`}>
                        <button type="button" className="lane-head" aria-expanded={!collapsed} onClick={() => setCollapsedLanes((s) => { const n2 = new Set(s); if (n2.has(lane)) n2.delete(lane); else n2.add(lane); return n2 })}>
                          {collapsed ? <ChevronRight /> : <ChevronDown />}
                          {lane === 'human' ? <User /> : lane === 'agent' ? <Bot /> : <Wrench />}
                          {lane} lane · {items.length}
                          <span className="line" />
                        </button>
                        {!collapsed ? (
                          <div className="thread">
                            {items.map((it) =>
                              'verdict' in it ? (
                                <ReviewCard key={it.id} review={it} org={org} repo={repo} num={num} onFocus={(f) => { setSearch({ tab: 'files' }); setFocus(f) }} onApply={(f) => void applyFix(org, repo, c.source_ref, c.head_oid, f, toast, invalidate)} canApply={isOpen} />
                              ) : (
                                <CommentCard key={it.id} comment={it} onResolve={(resolved) => void post(() => actions.resolveComment(org, repo, num, it.id, resolved), resolved ? 'Resolved' : 'Reopened')} onJump={it.anchor ? () => { setSearch({ tab: 'files' }); setFocus({ path: it.anchor!.path, side: 'new', line: it.anchor!.end_line || it.anchor!.start_line }) } : undefined} />
                              ),
                            )}
                          </div>
                        ) : null}
                      </section>
                    )
                  })}
                  {rv1 ? <CommentComposer onSubmit={(body) => post(() => actions.comment(org, repo, num, { body }), 'Comment posted')} label="Comment on the change" /> : null}
                  {landings.data && landings.data.length ? (
                    <Card title="Landings">
                      <ul className="timeline" style={{ listStyle: 'none', padding: '0 0 0 22px', margin: 0 }}>
                        {landings.data.map((l) => (
                          <li key={l.id} className={`timeline-item ${l.status === 'landed' ? 'ok' : l.status === 'failed' || l.status === 'reverted' ? 'danger' : 'warn'}`}>
                            <b>{l.strategy.replace(/_/g, ' ')}</b> <StatusPill status={l.status} small /> on patch set {l.patch_set} by <ActorChip actor={l.actor} size="sm" />
                            {l.error ? <div className="small" style={{ color: 'var(--danger-fg)' }}>{l.error}</div> : null}
                            <div className="when row">
                              <TimeAgo iso={l.created_at} />
                              {l.verification_run_id ? (
                                <Link to="/$org/$repo/pipelines/$id" params={{ org, repo, id: l.verification_run_id }}>
                                  verification run
                                </Link>
                              ) : null}
                              {l.revert_oid ? (
                                <Link to="/$org/$repo/commit/$oid" params={{ org, repo, oid: l.revert_oid }} className="mono">
                                  revert {l.revert_oid.slice(0, 7)}
                                </Link>
                              ) : null}
                              {l.issue_number ? (
                                <Link to="/$org/$repo/issues/$n" params={{ org, repo, n: String(l.issue_number) }}>
                                  fix issue #{l.issue_number}
                                </Link>
                              ) : null}
                            </div>
                          </li>
                        ))}
                      </ul>
                    </Card>
                  ) : null}
                </div>
                <Sidebar org={org} repo={repo} num={num} patchsets={patchsets.data ?? []} approvals={approvals.data ?? null} latestRun={latestRun} policy={policy.data ?? null} onPickPs={(p) => setSearch({ ps: p === latestPs ? undefined : p, from: undefined, tab: 'files' })} currentPs={ps} />
              </div>
            ) : null}

            {tab === 'files' ? (
              <div className="stack">
                {rv1 && (patchsets.data?.length ?? 0) > 0 ? (
                  <div className="row card flat" style={{ padding: 'var(--sp-2) var(--sp-3)' }}>
                    <label className="small muted" htmlFor="ps-select">
                      Patch set
                    </label>
                    <Select id="ps-select" small value={ps} onChange={(e) => setSearch({ ps: Number(e.target.value) === latestPs ? undefined : Number(e.target.value) })} style={{ width: 200 }}>
                      {patchsets.data!.map((p) => (
                        <option key={p.id} value={p.number}>
                          PS{p.number} · {p.head_oid.slice(0, 7)} · {p.pushed_by.id}
                        </option>
                      ))}
                    </Select>
                    <label className="small muted" htmlFor="from-select">
                      compared with
                    </label>
                    <Select id="from-select" small value={from ?? 0} onChange={(e) => setSearch({ from: Number(e.target.value) || undefined })} style={{ width: 200 }}>
                      <option value={0}>the base (full diff)</option>
                      {patchsets.data!.filter((p) => p.number !== ps).map((p) => (
                        <option key={p.id} value={p.number}>
                          PS{p.number} (interdiff)
                        </option>
                      ))}
                    </Select>
                    {interdiffMode && interdiff.data ? (
                      <span className="small muted">
                        {interdiff.data.identical ? 'Identical patch sets.' : interdiff.data.rebased ? 'Rebased: the base changed; only the patch’s own delta is shown.' : ''} {interdiff.data.note ?? ''}
                      </span>
                    ) : null}
                    <span className="grow" />
                    <span className="small subtle">Click a line number to comment.</span>
                  </div>
                ) : null}
                {diffLoading ? (
                  <div className="skeleton-rows">
                    <span className="skeleton" style={{ width: '40%' }} />
                    <span className="skeleton" style={{ width: '90%', height: 120 }} />
                  </div>
                ) : (
                  <DiffViewer
                    diff={diffText}
                    view={search.view}
                    onViewChange={(v) => setSearch({ view: v })}
                    threads={threadKeys}
                    renderThread={(key) => (
                      <InlineThread
                        comments={threads.get(key) ?? []}
                        isDraft={!!draft && anchorKey(draft.path, draft.side, draft.line) === key}
                        onCancel={() => setDraft(null)}
                        onSubmit={(body) => {
                          const target = draft ?? parseKey(key)
                          return post(() => actions.comment(org, repo, num, { body, path: target.path, start_line: target.line, end_line: target.line, patch_set: ps || undefined }), 'Comment posted').then((ok) => {
                            if (ok) setDraft(null)
                            return ok
                          })
                        }}
                        onResolve={(id, resolved) => void post(() => actions.resolveComment(org, repo, num, id, resolved), resolved ? 'Resolved' : 'Reopened')}
                        canComment={rv1}
                      />
                    )}
                    onLineClick={rv1 ? (ref) => setDraft(ref) : undefined}
                    provenance={prov}
                    fileBadges={fileBadges}
                    blobLoader={blobLoader}
                    focusLine={focus}
                    emptyMessage={(patchsets.data?.length ?? 0) === 0 && !diffText ? 'This change has no patch sets yet: nothing has been pushed to its branch.' : 'No differences.'}
                  />
                )}
              </div>
            ) : null}

            {tab === 'checks' ? (
              <div className="split">
                <div className="stack">
                  <Card tight title="Pipeline runs on this change" actions={isOpen ? <Button size="sm" onClick={() => void post(() => actions.startRun(org, repo, { change: num }), 'Pipeline started')}><Play /> Run on head</Button> : undefined}>
                    {changeRuns.length === 0 ? (
                      <p className="muted" style={{ padding: 'var(--sp-4)' }}>No runs yet. Verification runs on every patch set when an agent works; press Run on head to start one.</p>
                    ) : (
                      <div className="list">
                        {changeRuns.map((r) => (
                          <Link key={r.id} to="/$org/$repo/pipelines/$id" params={{ org, repo, id: r.id }} className="list-row">
                            <StatusPill status={r.status} />
                            <span className="grow mono small">{r.id.slice(0, 16)}</span>
                            <span className="mono small muted">{r.commit_oid.slice(0, 7)}</span>
                            <span className="small muted">
                              <TimeAgo iso={r.created_at} />
                            </span>
                          </Link>
                        ))}
                      </div>
                    )}
                  </Card>
                  <ApprovalsCard approvals={approvals.data ?? null} policy={policy.data ?? null} />
                </div>
                <Sidebar org={org} repo={repo} num={num} patchsets={patchsets.data ?? []} approvals={approvals.data ?? null} latestRun={latestRun} policy={policy.data ?? null} onPickPs={(p) => setSearch({ ps: p === latestPs ? undefined : p, from: undefined, tab: 'files' })} currentPs={ps} />
              </div>
            ) : null}

            <LandDialog open={landOpen} onClose={() => setLandOpen(false)} defaultStrategy={policy.data?.default_strategy ?? 'queue'} approvals={approvals.data ?? null} latestRunStatus={latestRun?.status} onLand={async (strategy) => {
              try {
                const res = await actions.land(org, repo, num, strategy)
                await invalidate()
                toast.push({ tone: 'ok', title: `Landed with ${strategy.replace(/_/g, ' ')}`, body: res.change.state === 'merged' ? `Merged as ${res.landing.merge_oid?.slice(0, 7)}` : `Landing ${res.landing.status}` })
                setLandOpen(false)
                return null
              } catch (e) {
                await invalidate()
                if (e instanceof ApiError && e.status === 409) {
                  const b = e.body as { error?: string; landing?: { error?: string; status?: string } } | undefined
                  return b?.landing?.error ? `${b.error ?? 'Did not land'}: ${b.landing.error}` : (b?.error ?? e.message)
                }
                return e instanceof Error ? e.message : 'Landing failed'
              }
            }} />
            <ReviewDialog open={reviewOpen} onClose={() => setReviewOpen(false)} paths={files.map((f) => f.path)} onSubmit={async (body) => {
              const ok = await post(() => actions.review(org, repo, num, body), 'Review submitted')
              if (ok) setReviewOpen(false)
            }} />
          </div>
        )
      }}
    </QueryState>
  )
}

function parseKey(key: string): LineRef {
  const parts = key.split(':')
  const line = Number(parts.pop())
  const side = parts.pop() as 'old' | 'new'
  return { path: parts.join(':'), side, line }
}

function Sidebar({ org, repo, patchsets, approvals, latestRun, policy, onPickPs, currentPs }: { org: string; repo: string; num: number; patchsets: Array<{ id: string; number: number; head_oid: string; base_oid: string; pushed_by: { kind: string; id: string }; created_at: string }>; approvals: import('../../lib/types').ApprovalStatus | null; latestRun: import('../../lib/types').PipelineRun | undefined; policy: import('../../lib/types').ReviewPolicy | null; onPickPs: (n: number) => void; currentPs: number }) {
  return (
    <aside className="stack" style={{ gap: 'var(--sp-3)' }}>
      <Card title="Required checks">
        <div className="stack" style={{ gap: 6 }}>
          <div className="row between">
            <span className="small">Pipeline on head</span>
            {latestRun ? (
              <Link to="/$org/$repo/pipelines/$id" params={{ org, repo, id: latestRun.id }}>
                <StatusPill status={latestRun.status} small />
              </Link>
            ) : (
              <Pill small>not run</Pill>
            )}
          </div>
          <div className="row between">
            <span className="small">Approvals</span>
            {approvals ? <Pill small tone={approvals.landable ? 'ok' : approvals.required ? 'warn' : 'neutral'}>{approvals.approvals}/{approvals.required}</Pill> : <Pill small>n/a</Pill>}
          </div>
          {approvals && !approvals.landable ? <p className="small muted">{approvals.reason}</p> : null}
          {policy ? <p className="small subtle">Default landing: {policy.default_strategy.replace(/_/g, ' ')}.</p> : null}
        </div>
      </Card>
      <Card title={`Patch sets · ${patchsets.length}`}>
        {patchsets.length === 0 ? (
          <p className="muted small">None recorded. Patch sets appear when the branch is pushed; a rebase gets a new base.</p>
        ) : (
          <ul className="stack" style={{ listStyle: 'none', margin: 0, padding: 0, gap: 6 }}>
            {[...patchsets].reverse().map((p) => (
              <li key={p.id} className="row between">
                <button type="button" className={cx('btn sm', p.number === currentPs ? 'primary' : 'ghost')} onClick={() => onPickPs(p.number)} aria-pressed={p.number === currentPs}>
                  PS{p.number}
                </button>
                <span className="small muted grow">
                  <code>{p.head_oid.slice(0, 7)}</code> by {p.pushed_by.id}
                </span>
                <span className="small subtle">
                  <TimeAgo iso={p.created_at} />
                </span>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </aside>
  )
}

function ApprovalsCard({ approvals, policy }: { approvals: import('../../lib/types').ApprovalStatus | null; policy: import('../../lib/types').ReviewPolicy | null }) {
  if (!approvals) return null
  return (
    <Card title="Approval policy">
      <dl className="dl">
        <dt>Evaluated on</dt>
        <dd>patch set {approvals.patch_set} (approvals reset when a new patch set is pushed)</dd>
        <dt>Required</dt>
        <dd>{approvals.required} human approval{approvals.required === 1 ? '' : 's'}{policy?.allow_agent_approvals ? ' (agent approvals count)' : ''}</dd>
        <dt>Approved by</dt>
        <dd>{approvals.approvers.length ? approvals.approvers.map((a) => <ActorChip key={a.id} actor={a} size="sm" />) : '—'}</dd>
        {approvals.ignored?.length ? (
          <>
            <dt>Ignored</dt>
            <dd>{approvals.ignored.map((i, k) => <div key={k}><ActorChip actor={i.reviewer} size="sm" /> <span className="muted small">{i.reason}</span></div>)}</dd>
          </>
        ) : null}
        {approvals.changes_requested_by?.length ? (
          <>
            <dt>Changes requested by</dt>
            <dd>{approvals.changes_requested_by.map((a) => <ActorChip key={a.id} actor={a} size="sm" />)}</dd>
          </>
        ) : null}
        <dt>Landable</dt>
        <dd>{approvals.landable ? <Pill small tone="ok"><Check /> yes</Pill> : <Pill small tone="warn">{approvals.reason || 'no'}</Pill>}</dd>
      </dl>
    </Card>
  )
}

function CommentCard({ comment, onResolve, onJump }: { comment: Comment; onResolve: (resolved: boolean) => void; onJump?: () => void }) {
  return (
    <div className={cx('comment', comment.lane)}>
      <div className="comment-head">
        <ActorChip actor={comment.author} />
        <span className="when">
          <TimeAgo iso={comment.created_at} />
        </span>
        {comment.path ? (
          <button type="button" className="btn ghost sm" onClick={onJump} disabled={!onJump} title={comment.outdated ? 'The line changed in a later patch set' : 'Go to the line'}>
            <code>
              {comment.path}:{comment.start_line}
            </code>
            {comment.outdated ? <Pill small tone="warn">outdated</Pill> : null}
          </button>
        ) : null}
        <span className="grow" />
        {comment.resolved ? <Pill small tone="ok">resolved</Pill> : null}
        <Button size="sm" variant="ghost" onClick={() => onResolve(!comment.resolved)}>
          {comment.resolved ? 'Reopen' : 'Resolve'}
        </Button>
      </div>
      <div className={cx('comment-body', comment.outdated && 'outdated')}>
        <Markdown source={comment.body} />
      </div>
    </div>
  )
}

function ReviewCard({ review, onFocus, onApply, canApply }: { review: Review; org: string; repo: string; num: number; onFocus: (f: LineRef) => void; onApply: (f: Finding) => void; canApply: boolean }) {
  const tone = review.verdict === 'approve' ? 'ok' : review.verdict === 'request_changes' ? 'danger' : 'neutral'
  return (
    <div className={cx('comment', review.lane)}>
      <div className="comment-head">
        <ActorChip actor={review.reviewer} />
        <Pill tone={tone} small>
          {review.verdict === 'approve' ? <Check /> : review.verdict === 'request_changes' ? <X /> : <MessageSquare />}
          {review.verdict.replace(/_/g, ' ')}
        </Pill>
        <span className="when">
          on PS{review.patch_set} · <TimeAgo iso={review.created_at} />
        </span>
        {!review.counts && review.verdict !== 'comment' ? <Pill small outline title="Not on the latest patch set, or a lane that does not count">does not count</Pill> : null}
      </div>
      <div className="comment-body">
        {review.body ? <Markdown source={review.body} /> : null}
        {review.findings?.length ? (
          <ul className="stack" style={{ listStyle: 'none', padding: 0, margin: review.body ? 'var(--sp-3) 0 0' : 0, gap: 'var(--sp-2)' }}>
            {review.findings.map((f, i) => (
              <li key={f.fingerprint ?? i} className="card flat" style={{ padding: 'var(--sp-2) var(--sp-3)' }}>
                <div className="row">
                  <span className={`finding-sev ${f.severity}`}>{f.severity}</span>
                  {f.category ? <Pill small>{f.category}</Pill> : null}
                  <button type="button" className="btn ghost sm" onClick={() => f.range && onFocus({ path: f.path, side: 'new', line: f.range.end_line || f.range.start_line })} disabled={!f.range}>
                    <code>
                      {f.path}
                      {f.range ? `:${f.range.start_line}${f.range.end_line !== f.range.start_line ? `-${f.range.end_line}` : ''}` : ''}
                    </code>
                  </button>
                  <span className="small subtle">confidence {Math.round(f.confidence * 100)}%</span>
                  <span className="grow" />
                  {f.suggested_fix && f.range && canApply ? (
                    <Button size="sm" onClick={() => onApply(f)} title="Commit the suggested fix to the change's branch">
                      <Wrench /> Apply fix
                    </Button>
                  ) : null}
                </div>
                <p style={{ margin: '6px 0 0' }}>{f.message}</p>
                {f.suggested_fix ? <pre className="codeblock small" style={{ marginTop: 6 }}>{f.suggested_fix}</pre> : null}
              </li>
            ))}
          </ul>
        ) : null}
      </div>
    </div>
  )
}

/** Applies a suggested fix: replace the finding's line range on the source branch with the suggestion, as a server-side commit. */
async function applyFix(org: string, repo: string, sourceRef: string, headOid: string, f: Finding, toast: ReturnType<typeof useToast>, invalidate: () => Promise<unknown>) {
  if (!f.range) return
  try {
    const blob = await get<Blob>(`${webappRepoBase(org, repo)}/blob?ref=${encodeURIComponent(headOid)}&path=${encodeURIComponent(f.path)}`, { quiet: true })
    const lines = blob.content.split('\n')
    const start = Math.max(1, f.range.start_line)
    const end = Math.max(start, f.range.end_line || start)
    const fix = (f.suggested_fix ?? '').replace(/\n$/, '').split('\n')
    lines.splice(start - 1, end - start + 1, ...fix)
    await actions.commit(org, repo, { branch: refName(sourceRef), message: `Apply suggested fix: ${f.message.slice(0, 60)}`, files: [{ path: f.path, content: lines.join('\n') }] })
    await invalidate()
    toast.push({ tone: 'ok', title: 'Fix applied', body: `Committed to ${refName(sourceRef)}; a new patch set was recorded.` })
  } catch (e) {
    toast.push({ tone: 'danger', title: 'Could not apply the fix', body: e instanceof Error ? e.message : String(e) })
  }
}

function CommentComposer({ onSubmit, label, compact, onCancel }: { onSubmit: (body: string) => Promise<boolean>; label: string; compact?: boolean; onCancel?: () => void }) {
  const [body, setBody] = useState('')
  const [busy, setBusy] = useState(false)
  return (
    <form
      className="stack"
      style={{ gap: 'var(--sp-2)' }}
      onSubmit={async (e) => {
        e.preventDefault()
        if (!body.trim()) return
        setBusy(true)
        const ok = await onSubmit(body)
        setBusy(false)
        if (ok) setBody('')
      }}
    >
      <Textarea value={body} onChange={(e) => setBody(e.target.value)} placeholder="Leave a comment (Markdown)" rows={compact ? 3 : 4} aria-label={label} autoFocus={compact} onKeyDown={(e) => {
        if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') (e.currentTarget.form as HTMLFormElement | null)?.requestSubmit()
      }} />
      <div className="row">
        <Button type="submit" variant="primary" size="sm" loading={busy} disabled={!body.trim()}>
          <MessageSquare /> Comment
        </Button>
        {onCancel ? (
          <Button type="button" size="sm" variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
        ) : null}
        <span className="small subtle">⌘↵ to submit</span>
      </div>
    </form>
  )
}

function InlineThread({ comments, isDraft, onCancel, onSubmit, onResolve, canComment }: { comments: Comment[]; isDraft: boolean; onCancel: () => void; onSubmit: (body: string) => Promise<boolean>; onResolve: (id: string, resolved: boolean) => void; canComment: boolean }) {
  const [replying, setReplying] = useState(false)
  return (
    <div>
      {comments.map((c) => (
        <div key={c.id} className={cx('comment', c.lane)}>
          <div className="comment-head">
            <ActorChip actor={c.author} size="sm" />
            <span className="when">
              <TimeAgo iso={c.created_at} />
            </span>
            {c.outdated ? <Pill small tone="warn">outdated</Pill> : null}
            {c.resolved ? <Pill small tone="ok">resolved</Pill> : null}
            <span className="grow" />
            <Button size="sm" variant="ghost" onClick={() => onResolve(c.id, !c.resolved)}>
              {c.resolved ? 'Reopen' : 'Resolve'}
            </Button>
          </div>
          <div className="comment-body">
            <Markdown source={c.body} />
          </div>
        </div>
      ))}
      {isDraft || replying ? (
        <CommentComposer compact label="Inline comment" onSubmit={async (b) => { const ok = await onSubmit(b); if (ok) setReplying(false); return ok }} onCancel={() => { setReplying(false); onCancel() }} />
      ) : canComment ? (
        <Button size="sm" variant="ghost" onClick={() => setReplying(true)}>
          <MessageSquare /> Reply
        </Button>
      ) : null}
    </div>
  )
}

function LandDialog({ open, onClose, defaultStrategy, approvals, latestRunStatus, onLand }: { open: boolean; onClose: () => void; defaultStrategy: string; approvals: import('../../lib/types').ApprovalStatus | null; latestRunStatus?: string; onLand: (s: string) => Promise<string | null> }) {
  const [strategy, setStrategy] = useState(defaultStrategy)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  return (
    <Dialog open={open} onClose={onClose} title="Land this change" foot={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" loading={busy} onClick={async () => { setBusy(true); setError(null); const err = await onLand(strategy); setBusy(false); if (err) setError(err) }}><Rocket /> Land with {STRATEGIES.find((s) => s.id === strategy)?.title.toLowerCase()}</Button></>}>
      <div className="stack">
        {approvals && !approvals.landable ? <Callout tone="warn">Policy says not landable yet: {approvals.reason}. Landing will be refused until it is.</Callout> : null}
        {latestRunStatus && latestRunStatus !== 'succeeded' ? <Callout tone="warn">The latest pipeline run on the head is {latestRunStatus}.</Callout> : null}
        <div className="strategy" role="radiogroup" aria-label="Landing strategy">
          {STRATEGIES.map((s) => (
            <label key={s.id} data-checked={strategy === s.id}>
              <input type="radio" name="strategy" value={s.id} checked={strategy === s.id} onChange={() => setStrategy(s.id)} />
              <span>
                <b>{s.title}</b> <span className="subtle small">{s.level}{s.id === defaultStrategy ? ' · repository default' : ''}</span>
                <span className="d" style={{ display: 'block' }}>{s.desc}</span>
              </span>
            </label>
          ))}
        </div>
        {error ? <Callout tone="danger">{error}</Callout> : null}
      </div>
    </Dialog>
  )
}

function ReviewDialog({ open, onClose, paths, onSubmit }: { open: boolean; onClose: () => void; paths: string[]; onSubmit: (body: { verdict: string; findings: Finding[]; body?: string }) => Promise<void> }) {
  const [verdict, setVerdict] = useState('comment')
  const [body, setBody] = useState('')
  const [findings, setFindings] = useState<Finding[]>([])
  const [busy, setBusy] = useState(false)
  const update = (i: number, patch: Partial<Finding>) => setFindings((fs) => fs.map((f, k) => (k === i ? { ...f, ...patch } : f)))
  return (
    <Dialog open={open} onClose={onClose} title="Submit a review" wide foot={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" loading={busy} onClick={async () => { setBusy(true); await onSubmit({ verdict, findings: findings.filter((f) => f.path && f.message), body: body || undefined }); setBusy(false) }}>Submit review</Button></>}>
      <div className="stack">
        <div className="strategy" role="radiogroup" aria-label="Verdict">
          {[
            ['comment', 'Comment', 'Feedback without a verdict.'],
            ['approve', 'Approve', 'Counts toward the required approvals (human lane; agent approvals only if the policy allows).'],
            ['request_changes', 'Request changes', 'Blocks landing until you submit a later verdict.'],
          ].map(([v, t, d]) => (
            <label key={v} data-checked={verdict === v}>
              <input type="radio" name="verdict" value={v} checked={verdict === v} onChange={() => setVerdict(v!)} />
              <span>
                <b>{t}</b>
                <span className="d" style={{ display: 'block' }}>{d}</span>
              </span>
            </label>
          ))}
        </div>
        <Field label="Summary" htmlFor="rv-body">
          <Textarea id="rv-body" value={body} onChange={(e) => setBody(e.target.value)} rows={3} placeholder="Overall notes (Markdown)" />
        </Field>
        <div className="row between">
          <span className="field-label">Findings</span>
          <Button size="sm" onClick={() => setFindings((fs) => [...fs, { severity: 'medium', confidence: 0.8, path: paths[0] ?? '', message: '', range: { start_line: 1, end_line: 1 } }])}>
            Add finding
          </Button>
        </div>
        {findings.map((f, i) => (
          <div key={i} className="card flat" style={{ padding: 'var(--sp-3)' }}>
            <div className="grid-3" style={{ gap: 'var(--sp-2)' }}>
              <Field label="Severity" htmlFor={`f-sev-${i}`}>
                <Select id={`f-sev-${i}`} small value={f.severity} onChange={(e) => update(i, { severity: e.target.value })}>
                  {['info', 'low', 'medium', 'high', 'critical'].map((s) => (
                    <option key={s}>{s}</option>
                  ))}
                </Select>
              </Field>
              <Field label="Path" htmlFor={`f-path-${i}`}>
                <Input id={`f-path-${i}`} small list="rv-paths" value={f.path} onChange={(e) => update(i, { path: e.target.value })} />
              </Field>
              <Field label="Lines" htmlFor={`f-l1-${i}`}>
                <div className="row nowrap">
                  <Input id={`f-l1-${i}`} small type="number" min={1} value={f.range?.start_line ?? 1} onChange={(e) => update(i, { range: { start_line: Number(e.target.value), end_line: Math.max(Number(e.target.value), f.range?.end_line ?? 1) } })} />
                  <Input small type="number" min={1} aria-label="End line" value={f.range?.end_line ?? 1} onChange={(e) => update(i, { range: { start_line: f.range?.start_line ?? 1, end_line: Number(e.target.value) } })} />
                </div>
              </Field>
            </div>
            <Field label="Message" htmlFor={`f-msg-${i}`}>
              <Input id={`f-msg-${i}`} value={f.message} onChange={(e) => update(i, { message: e.target.value })} />
            </Field>
            <Field label="Suggested fix (replaces the lines)" htmlFor={`f-fix-${i}`}>
              <Textarea id={`f-fix-${i}`} mono rows={3} value={f.suggested_fix ?? ''} onChange={(e) => update(i, { suggested_fix: e.target.value })} />
            </Field>
            <Button size="sm" variant="ghost" onClick={() => setFindings((fs) => fs.filter((_, k) => k !== i))}>
              Remove
            </Button>
          </div>
        ))}
        <datalist id="rv-paths">
          {paths.map((p) => (
            <option key={p} value={p} />
          ))}
        </datalist>
      </div>
    </Dialog>
  )
}

export { GitPullRequest }
