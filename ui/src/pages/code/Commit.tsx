import { Link, useParams } from '@tanstack/react-router'
import { ActorChip, Card, CopyBlock } from '../../design'
import { DiffViewer } from '../../diff/DiffViewer'
import { get, webappRepoBase } from '../../lib/api'
import { fmtDate } from '../../lib/format'
import { useCommit } from '../../lib/queries'
import type { Blob } from '../../lib/types'
import { QueryState, TimeAgo, useDocumentTitle } from '../../shell/common'
import { NotFound } from '../../shell/NotFound'

export function CommitPage() {
  const { org, repo, oid } = useParams({ from: '/$org/$repo/commit/$oid' })
  const q = useCommit(org, repo, oid)
  useDocumentTitle(q.data ? q.data.subject : `Commit ${oid.slice(0, 7)}`)
  const blobLoader = (path: string, side: 'old' | 'new') => {
    const ref = side === 'new' ? oid : (q.data?.parents[0] ?? oid)
    return get<Blob>(`${webappRepoBase(org, repo)}/blob?ref=${encodeURIComponent(ref)}&path=${encodeURIComponent(path)}`, { quiet: true }).then((b) => b.content)
  }
  return (
    <QueryState query={q} notFound={<NotFound what="commit" />}>
      {(c) => (
        <div className="stack">
          <Card>
            <div className="page-head" style={{ marginBottom: 0 }}>
              <div style={{ minWidth: 0 }}>
                <h1 className="break" style={{ fontSize: 'var(--fs-5)' }}>
                  {c.subject}
                </h1>
                {c.body ? <pre className="muted" style={{ whiteSpace: 'pre-wrap', fontFamily: 'inherit', marginTop: 'var(--sp-2)' }}>{c.body}</pre> : null}
                <div className="row sub" style={{ marginTop: 'var(--sp-2)' }}>
                  <ActorChip actor={{ kind: 'human', id: c.author }} />
                  <span title={fmtDate(c.date)}>
                    committed <TimeAgo iso={c.date} />
                  </span>
                  {c.parents.length ? (
                    <span>
                      · parent{c.parents.length > 1 ? 's' : ''}{' '}
                      {c.parents.map((p) => (
                        <Link key={p} to="/$org/$repo/commit/$oid" params={{ org, repo, oid: p }} className="mono" style={{ marginRight: 6 }}>
                          {p.slice(0, 7)}
                        </Link>
                      ))}
                    </span>
                  ) : (
                    <span>· root commit</span>
                  )}
                </div>
              </div>
              <div className="stack" style={{ gap: 'var(--sp-2)', minWidth: 260 }}>
                <CopyBlock text={c.oid} />
                <Link to="/$org/$repo/tree/$ref" params={{ org, repo, ref: c.oid }} className="btn sm">
                  Browse files at this commit
                </Link>
              </div>
            </div>
          </Card>
          {c.stat ? <pre className="codeblock small" style={{ maxHeight: 160 }}>{c.stat.trim()}</pre> : null}
          <DiffViewer diff={c.diff} blobLoader={blobLoader} emptyMessage="This commit changes no files." />
        </div>
      )}
    </QueryState>
  )
}
