import { useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { CircleCheck, CircleDot, Plus, Search } from 'lucide-react'
import { Button, Card, EmptyState, Input, Pill } from '../../design'
import { useHotkey, useListNavigation } from '../../lib/keyboard'
import { excerpt } from '../../lib/markdown'
import { useIssues, useRepo } from '../../lib/queries'
import { QueryState, StateFilter, TimeAgo, useDocumentTitle } from '../../shell/common'

export function IssuesPage() {
  const { org, repo } = useParams({ from: '/$org/$repo/issues' })
  const search = useSearch({ from: '/$org/$repo/issues' })
  const navigate = useNavigate()
  const issues = useIssues(org, repo)
  const repoQ = useRepo(org, repo)
  useDocumentTitle(`Issues · ${org}/${repo}`)
  const state = search.state ?? 'open'
  const [q, setQ] = useState(search.q ?? '')
  const listRef = useRef<HTMLDivElement>(null)

  const rows = useMemo(() => {
    const all = issues.data ?? []
    const needle = q.trim().toLowerCase()
    return all.filter((i) => (state === 'all' ? true : i.state === state) && (!needle || i.title.toLowerCase().includes(needle) || String(i.number) === needle.replace('#', '')))
  }, [issues.data, state, q])

  const open = (i: number) => {
    const row = rows[i]
    if (row) void navigate({ to: '/$org/$repo/issues/$n', params: { org, repo, n: String(row.number) } })
  }
  const [active] = useListNavigation(rows.length, open, listRef)
  useHotkey('c', () => void navigate({ to: '/$org/$repo/issues/new', params: { org, repo } }))

  const counts = { open: issues.data?.filter((i) => i.state === 'open').length ?? 0, closed: issues.data?.filter((i) => i.state !== 'open').length ?? 0 }
  const setState = (s: 'open' | 'closed' | 'all') => void navigate({ to: '.', search: { state: s === 'open' ? undefined : s, q: q || undefined }, replace: true })

  return (
    <div>
      <div className="page-head">
        <div>
          <h1>Issues</h1>
          <p className="sub">Signals that start the loop. An agent at this repository's level can be run on any open issue.</p>
        </div>
        <Link to="/$org/$repo/issues/new" params={{ org, repo }} className="btn primary" aria-keyshortcuts="c">
          <Plus /> New issue
        </Link>
      </div>
      <Card tight>
        <div className="filterbar">
          <StateFilter
            value={state}
            onChange={setState}
            options={[
              { value: 'open', label: 'Open', count: counts.open },
              { value: 'closed', label: 'Closed', count: counts.closed },
              { value: 'all', label: 'All' },
            ]}
          />
          <div className="input-with-icon">
            <Search aria-hidden />
            <Input small placeholder="Filter by title or #number" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Filter issues" />
          </div>
          <span className="subtle small hide-sm">j / k to move, ↵ to open, c to create</span>
        </div>
        <QueryState query={issues}>
          {() =>
            rows.length === 0 ? (
              <EmptyState
                icon={<CircleDot />}
                title={issues.data?.length ? 'No issues match' : 'No issues yet'}
                command={issues.data?.length ? undefined : `ore issue create ${org}/${repo} "Fix the flaky retry in the uploader"`}
                actions={
                  issues.data?.length ? undefined : (
                    <Link to="/$org/$repo/issues/new" params={{ org, repo }} className="btn primary">
                      Open the first issue
                    </Link>
                  )
                }
              >
                {issues.data?.length ? 'Try another state or clear the filter.' : `Issues are the signals that start the loop: file one, then run an agent on it at L${repoQ.data?.autonomy_level ?? 2}.`}
              </EmptyState>
            ) : (
              <div className="list" ref={listRef} role="list">
                {rows.map((i, idx) => (
                  <Link key={i.id} to="/$org/$repo/issues/$n" params={{ org, repo, n: String(i.number) }} className="list-row" data-index={idx} data-active={idx === active} role="listitem" tabIndex={-1}>
                    {i.state === 'open' ? <CircleDot className="icon" style={{ color: 'var(--ok)' }} aria-label="Open" /> : <CircleCheck className="icon" style={{ color: 'var(--agent)' }} aria-label="Closed" />}
                    <div className="grow" style={{ minWidth: 0 }}>
                      <div className="title">{i.title}</div>
                      <div className="meta">
                        <span>#{i.number}</span>
                        <span>
                          opened <TimeAgo iso={i.created_at} />
                        </span>
                        {i.body ? <span className="truncate hide-sm" style={{ maxWidth: 420 }}>{excerpt(i.body, 90)}</span> : null}
                      </div>
                    </div>
                    {i.closed_at ? (
                      <Pill small>
                        closed <TimeAgo iso={i.closed_at} />
                      </Pill>
                    ) : null}
                  </Link>
                ))}
              </div>
            )
          }
        </QueryState>
      </Card>
    </div>
  )
}
