import { useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { Bot, GitMerge, GitPullRequest, GitPullRequestClosed, Layers, Search } from 'lucide-react'
import { Button, Card, EmptyState, Input, Pill } from '../../design'
import { refName, tryJSON } from '../../lib/format'
import { useListNavigation } from '../../lib/keyboard'
import { useChanges, useFeatures, useRepo } from '../../lib/queries'
import type { Change } from '../../lib/types'
import { QueryState, StateFilter, TimeAgo, useDocumentTitle } from '../../shell/common'

export function provenanceOf(c: Change): { kind: string; id: string; model?: string; session?: string } | null {
  const p = tryJSON<Record<string, unknown>>(c.provenance, {})
  const kind = (p.author_kind as string) ?? (p.kind as string) ?? (p.agent ? 'agent' : null)
  if (!kind) return null
  const id = (p.agent_id as string) ?? (p.agent as string) ?? (p.adapter as string) ?? (p.id as string) ?? 'agent'
  return { kind: kind === 'human_with_agent' ? 'agent' : kind, id, model: p.model as string | undefined, session: (p.session_id as string) ?? (p.session as string | undefined) }
}

export function ChangesPage() {
  const { org, repo } = useParams({ from: '/$org/$repo/changes' })
  const search = useSearch({ from: '/$org/$repo/changes' })
  const navigate = useNavigate()
  const changes = useChanges(org, repo)
  const repoQ = useRepo(org, repo)
  const { data: features } = useFeatures()
  const state = (search.state as 'open' | 'merged' | 'closed' | 'all' | undefined) ?? 'open'
  const [q, setQ] = useState(search.q ?? '')
  const listRef = useRef<HTMLDivElement>(null)
  useDocumentTitle(`Changes · ${org}/${repo}`)

  const rows = useMemo(() => {
    const needle = q.trim().toLowerCase()
    return (changes.data ?? []).filter((c) => (state === 'all' ? true : state === 'closed' ? c.state === 'closed' || c.state === 'reverted' : c.state === state) && (!needle || c.title.toLowerCase().includes(needle) || String(c.number) === needle.replace('#', '') || c.source_ref.toLowerCase().includes(needle)))
  }, [changes.data, state, q])
  const [active] = useListNavigation(rows.length, (i) => {
    const c = rows[i]
    if (c) void navigate({ to: '/$org/$repo/changes/$n', params: { org, repo, n: String(c.number) }, search: {} })
  }, listRef)
  const counts = {
    open: changes.data?.filter((c) => c.state === 'open').length ?? 0,
    merged: changes.data?.filter((c) => c.state === 'merged').length ?? 0,
    closed: changes.data?.filter((c) => c.state === 'closed' || c.state === 'reverted').length ?? 0,
  }
  const setState = (s: typeof state) => void navigate({ to: '.', search: { state: s === 'open' ? undefined : s, q: q || undefined }, replace: true })

  return (
    <div>
      <div className="page-head">
        <div>
          <h1>Changes</h1>
          <p className="sub">Review by change-id: patch sets, interdiffs, lanes for humans, bots and agents, and three ways to land.</p>
        </div>
        <div className="row">
          {features?.review_v1 ? (
            <Link to="/$org/$repo/stacks" params={{ org, repo }} className="btn sm">
              <Layers /> Stacks
            </Link>
          ) : null}
          <Link to="/$org/$repo/compare/$spec" params={{ org, repo, spec: `${repoQ.data?.default_branch ?? 'main'}...` }} className="btn sm primary">
            <GitPullRequest /> New change
          </Link>
        </div>
      </div>
      <Card tight>
        <div className="filterbar">
          <StateFilter
            value={state}
            onChange={setState}
            options={[
              { value: 'open', label: 'Open', count: counts.open },
              { value: 'merged', label: 'Merged', count: counts.merged },
              { value: 'closed', label: 'Closed', count: counts.closed },
              { value: 'all', label: 'All' },
            ]}
          />
          <div className="input-with-icon">
            <Search aria-hidden />
            <Input small placeholder="Filter by title, branch or #number" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Filter changes" />
          </div>
        </div>
        <QueryState query={changes}>
          {() =>
            rows.length === 0 ? (
              <EmptyState icon={<GitPullRequest />} title={changes.data?.length ? 'No changes match' : 'No changes yet'} command={changes.data?.length ? undefined : `git push ore my-branch\nore change open ${org}/${repo} --source my-branch --title "…"`}>
                {changes.data?.length ? 'Try another state or clear the filter.' : 'Push a branch and open a change from it, or let an agent open one from an issue. The default branch is protected, so every landing goes through a change.'}
              </EmptyState>
            ) : (
              <div className="list" ref={listRef}>
                {rows.map((c, idx) => {
                  const prov = provenanceOf(c)
                  return (
                    <Link key={c.id} to="/$org/$repo/changes/$n" params={{ org, repo, n: String(c.number) }} search={{}} className="list-row" data-index={idx} data-active={idx === active} tabIndex={-1}>
                      {c.state === 'merged' ? <GitMerge className="icon" style={{ color: 'var(--agent)' }} aria-label="Merged" /> : c.state === 'open' ? <GitPullRequest className="icon" style={{ color: 'var(--ok)' }} aria-label="Open" /> : <GitPullRequestClosed className="icon" style={{ color: 'var(--danger)' }} aria-label={c.state} />}
                      <div className="grow" style={{ minWidth: 0 }}>
                        <div className="title row" style={{ gap: 6 }}>
                          <span className="truncate">{c.title || `Change from ${refName(c.source_ref)}`}</span>
                          {prov?.kind === 'agent' ? (
                            <Pill small tone="agent" title={`Authored by ${prov.id}${prov.model ? ` (${prov.model})` : ''}`}>
                              <Bot /> {prov.id}
                            </Pill>
                          ) : null}
                          {c.state === 'reverted' ? <Pill small tone="danger">reverted</Pill> : null}
                        </div>
                        <div className="meta">
                          <span>#{c.number}</span>
                          <span className="mono">
                            {refName(c.source_ref)} → {refName(c.target_ref)}
                          </span>
                          <span>
                            opened <TimeAgo iso={c.created_at} />
                          </span>
                          {c.merged_at ? (
                            <span>
                              · merged <TimeAgo iso={c.merged_at} />
                            </span>
                          ) : null}
                        </div>
                      </div>
                      <code className="small muted hide-sm" title={`change-id ${c.change_id}`}>
                        {c.change_id.slice(0, 10)}
                      </code>
                    </Link>
                  )
                })}
              </div>
            )
          }
        </QueryState>
      </Card>
      <p className="subtle small" style={{ marginTop: 'var(--sp-2)' }}>
        <Button size="sm" variant="ghost" onClick={() => setQ('')} style={{ display: 'none' }} aria-hidden />
        j / k to move, ↵ to open.
      </p>
    </div>
  )
}
