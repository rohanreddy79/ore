import { useMemo } from 'react'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { GitCommitHorizontal } from 'lucide-react'
import { Avatar, Card, CopyButton, EmptyState } from '../../design'
import { refName } from '../../lib/format'
import { useLog, useRefs, useRepo } from '../../lib/queries'
import { QueryState, RefPicker, TimeAgo, useDocumentTitle } from '../../shell/common'

export function CommitsPage() {
  const params = useParams({ strict: false }) as { org: string; repo: string; ref?: string }
  const { org, repo } = params
  const repoQ = useRepo(org, repo)
  const refs = useRefs(org, repo)
  const navigate = useNavigate()
  const refParam = params.ref ? (() => { try { return params.ref!.includes('%') ? decodeURIComponent(params.ref!) : params.ref! } catch { return params.ref! } })() : ''
  const ref = refParam ? (refParam.startsWith('refs/') ? refParam : refs.data?.[`refs/tags/${refParam}`] ? `refs/tags/${refParam}` : `refs/heads/${refParam}`) : repoQ.data ? `refs/heads/${repoQ.data.default_branch}` : ''
  const log = useLog(org, repo, ref)
  useDocumentTitle(`Commits · ${org}/${repo}`)
  const groups = useMemo(() => {
    const out: Array<{ day: string; entries: NonNullable<typeof log.data> }> = []
    for (const e of log.data ?? []) {
      const day = new Date(e.date).toLocaleDateString(undefined, { dateStyle: 'long' })
      const g = out[out.length - 1]
      if (g && g.day === day) g.entries.push(e)
      else out.push({ day, entries: [e] })
    }
    return out
  }, [log.data])

  return (
    <div>
      <div className="page-head">
        <div>
          <h1>Commits</h1>
          <p className="sub">The last 50 commits reachable from the selected ref.</p>
        </div>
        <RefPicker refs={refs.data} current={ref} onPick={(r) => void navigate({ to: '/$org/$repo/commits/$ref', params: { org, repo, ref: refName(r) } })} />
      </div>
      <QueryState query={log}>
        {(entries) =>
          entries.length === 0 ? (
            <Card>
              <EmptyState icon={<GitCommitHorizontal />} title="No commits on this ref" compact />
            </Card>
          ) : (
            <div className="stack">
              {groups.map((g) => (
                <div key={g.day}>
                  <h3 className="muted" style={{ fontSize: 'var(--fs-2)', margin: 'var(--sp-2) 0' }}>
                    <GitCommitHorizontal size={14} aria-hidden /> {g.day}
                  </h3>
                  <Card tight>
                    <div className="list">
                      {g.entries.map((e) => (
                        <div key={e.oid} className="list-row">
                          <Avatar actor={{ kind: 'human', id: e.author }} />
                          <div className="grow" style={{ minWidth: 0 }}>
                            <Link to="/$org/$repo/commit/$oid" params={{ org, repo, oid: e.oid }} className="title quiet truncate" style={{ display: 'block' }}>
                              {e.subject}
                            </Link>
                            <div className="meta">
                              <span>{e.author}</span>
                              <span>
                                committed <TimeAgo iso={e.date} />
                              </span>
                            </div>
                          </div>
                          <Link to="/$org/$repo/commit/$oid" params={{ org, repo, oid: e.oid }} className="mono small btn sm">
                            {e.oid.slice(0, 7)}
                          </Link>
                          <CopyButton text={e.oid} label="Copy full SHA" />
                          <Link to="/$org/$repo/tree/$ref" params={{ org, repo, ref: e.oid }} className="btn sm ghost hide-sm" title="Browse files at this commit">
                            Browse
                          </Link>
                        </div>
                      ))}
                    </div>
                  </Card>
                </div>
              ))}
            </div>
          )
        }
      </QueryState>
    </div>
  )
}
