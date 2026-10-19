import { useState } from 'react'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { GitPullRequest } from 'lucide-react'
import { Button, Card, Field, Input, Textarea, useToast } from '../../design'
import { DiffViewer } from '../../diff/DiffViewer'
import { get, webappRepoBase } from '../../lib/api'
import { refName } from '../../lib/format'
import { actions, qk, useCompare, useRepo } from '../../lib/queries'
import type { Blob } from '../../lib/types'
import { QueryState, TimeAgo, useDocumentTitle } from '../../shell/common'

/** /compare/base...head : the diff between two refs and the button that opens a change from it. */
export function ComparePage() {
  const { org, repo, spec } = useParams({ from: '/$org/$repo/compare/$spec' })
  const [baseRaw, headRaw] = decodeURIComponent(spec).split('...')
  const repoQ = useRepo(org, repo)
  const base = baseRaw || repoQ.data?.default_branch || 'main'
  const head = headRaw || base
  const q = useCompare(org, repo, base, head, !!headRaw)
  const navigate = useNavigate()
  const qc = useQueryClient()
  const toast = useToast()
  const [title, setTitle] = useState('')
  const [body, setBody] = useState('')
  const [busy, setBusy] = useState(false)
  useDocumentTitle(`Compare ${base}...${head}`)

  const blobLoader = (path: string, side: 'old' | 'new') => get<Blob>(`${webappRepoBase(org, repo)}/blob?ref=${encodeURIComponent(side === 'new' ? head : base)}&path=${encodeURIComponent(path)}`, { quiet: true }).then((b) => b.content)

  const open = async () => {
    setBusy(true)
    try {
      const ch = await actions.openChange(org, repo, { source_ref: head.startsWith('refs/') ? head : `refs/heads/${head}`, target_ref: base.startsWith('refs/') ? base : `refs/heads/${base}`, title: title || undefined, body: body || undefined })
      await qc.invalidateQueries({ queryKey: qk.r(org, repo, 'changes') })
      toast.push({ tone: 'ok', title: `Opened change #${ch.number}` })
      void navigate({ to: '/$org/$repo/changes/$n', params: { org, repo, n: String(ch.number) }, search: {} })
    } catch {
      /* toast raised by api */
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>
            Comparing <code>{refName(base)}</code> … <code>{refName(head)}</code>
          </h1>
          <p className="sub">Changes on {refName(head)} since it diverged from {refName(base)}.</p>
        </div>
      </div>
      {!headRaw ? (
        <Card>
          <p className="muted">
            Use the address <code>/compare/base...head</code>, for example <code>/compare/{base}...feature</code>.
          </p>
        </Card>
      ) : (
        <QueryState query={q}>
          {(c) => (
            <>
              <div className="split">
                <Card tight title={`${c.commits.length} commit${c.commits.length === 1 ? '' : 's'}`}>
                  <div className="list">
                    {c.commits.map((e) => (
                      <Link key={e.oid} to="/$org/$repo/commit/$oid" params={{ org, repo, oid: e.oid }} className="list-row quiet">
                        <span className="mono small">{e.oid.slice(0, 7)}</span>
                        <span className="grow truncate">{e.subject}</span>
                        <span className="muted small">
                          {e.author} · <TimeAgo iso={e.date} />
                        </span>
                      </Link>
                    ))}
                    {c.commits.length === 0 ? <p className="muted" style={{ padding: 'var(--sp-3)' }}>No commits: the head is not ahead of the base.</p> : null}
                  </div>
                </Card>
                <Card title={<><GitPullRequest aria-hidden /> Open a change</>}>
                  <div className="stack">
                    <Field label="Title" htmlFor="cmp-title">
                      <Input id="cmp-title" value={title} onChange={(e) => setTitle(e.target.value)} placeholder={c.commits[0]?.subject ?? 'Title'} />
                    </Field>
                    <Field label="Description" htmlFor="cmp-body">
                      <Textarea id="cmp-body" value={body} onChange={(e) => setBody(e.target.value)} rows={4} />
                    </Field>
                    <Button variant="primary" loading={busy} disabled={c.commits.length === 0} onClick={() => void open()}>
                      Open change from {refName(head)}
                    </Button>
                  </div>
                </Card>
              </div>
              <DiffViewer diff={c.diff} blobLoader={blobLoader} emptyMessage="No differences between these refs." />
            </>
          )}
        </QueryState>
      )}
    </div>
  )
}
