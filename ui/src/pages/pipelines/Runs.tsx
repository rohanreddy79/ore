import { useState } from 'react'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Play, Workflow } from 'lucide-react'
import { Button, Card, EmptyState, Input, StatusPill, useToast } from '../../design'
import { durationBetween, tryJSON } from '../../lib/format'
import { actions, qk, useChanges, useRepo, useRuns } from '../../lib/queries'
import type { Task } from '../../lib/types'
import { QueryState, TimeAgo, useDocumentTitle } from '../../shell/common'

export function RunsPage() {
  const { org, repo } = useParams({ from: '/$org/$repo/pipelines' })
  const runs = useRuns(org, repo)
  const changes = useChanges(org, repo)
  const repoQ = useRepo(org, repo)
  const qc = useQueryClient()
  const toast = useToast()
  const navigate = useNavigate()
  const [ref, setRef] = useState('')
  const [busy, setBusy] = useState(false)
  useDocumentTitle(`Pipelines · ${org}/${repo}`)

  const start = async () => {
    setBusy(true)
    try {
      const run = await actions.startRun(org, repo, { ref: ref || undefined })
      await qc.invalidateQueries({ queryKey: qk.r(org, repo, 'runs') })
      toast.push({ tone: 'ok', title: 'Pipeline queued', action: { label: 'Open', onClick: () => void navigate({ to: '/$org/$repo/pipelines/$id', params: { org, repo, id: run.id } }) } })
    } catch {
      /* api toast */
    } finally {
      setBusy(false)
    }
  }
  const changeFor = (id: string | undefined) => changes.data?.find((c) => c.id === id)

  return (
    <div>
      <div className="page-head">
        <div>
          <h1>Pipelines</h1>
          <p className="sub">Runs of <code>.ore/pipeline.yaml</code> or <code>.github/workflows/*.yml</code>, compiled to a content-addressed plan.</p>
        </div>
        <form
          className="row nowrap"
          onSubmit={(e) => {
            e.preventDefault()
            void start()
          }}
        >
          <Input small value={ref} onChange={(e) => setRef(e.target.value)} placeholder={repoQ.data?.default_branch ?? 'main'} aria-label="Ref to run" style={{ width: 160 }} />
          <Button type="submit" size="sm" variant="primary" loading={busy}>
            <Play /> Run pipeline
          </Button>
        </form>
      </div>
      <Card tight>
        <QueryState query={runs}>
          {(rows) =>
            rows.length === 0 ? (
              <EmptyState icon={<Workflow />} title="No pipeline runs yet" command={`cat > .ore/pipeline.yaml <<'EOF'\nversion: 0\ntasks:\n  - id: test\n    image: golang:1.25-alpine\n    run: go test ./...\nEOF\nore run start ${org}/${repo} --wait`}>
                Commit a <code>.ore/pipeline.yaml</code> or a GitHub Actions workflow, then start a run here or let a change trigger it.
              </EmptyState>
            ) : (
              <div className="list">
                {rows.map((r) => {
                  const tasks = tryJSON<Task[]>(r.tasks, [])
                  const ch = changeFor(r.change_id)
                  const failed = tasks.filter((t) => t.status === 'failed' || t.status === 'failure').length
                  const cache = tasks.filter((t) => t.cache_hit).length
                  return (
                    <Link key={r.id} to="/$org/$repo/pipelines/$id" params={{ org, repo, id: r.id }} className="list-row">
                      <StatusPill status={r.status} />
                      <div className="grow" style={{ minWidth: 0 }}>
                        <div className="title">
                          {ch ? `#${ch.number} ${ch.title}` : `Run on ${r.commit_oid.slice(0, 7)}`}
                        </div>
                        <div className="meta">
                          <span className="mono">{r.id.slice(0, 16)}</span>
                          <span className="mono">{r.commit_oid.slice(0, 7)}</span>
                          <span>{tasks.length} task{tasks.length === 1 ? '' : 's'}</span>
                          {failed ? <span style={{ color: 'var(--danger-fg)' }}>{failed} failed</span> : null}
                          {cache ? <span>{cache} cache hit{cache === 1 ? '' : 's'}</span> : null}
                        </div>
                      </div>
                      <div className="meta" style={{ textAlign: 'right', flexDirection: 'column', alignItems: 'flex-end' }}>
                        <TimeAgo iso={r.created_at} />
                        <span>{r.started_at ? durationBetween(r.started_at, r.finished_at) : 'queued'}</span>
                      </div>
                    </Link>
                  )
                })}
              </div>
            )
          }
        </QueryState>
      </Card>
    </div>
  )
}
