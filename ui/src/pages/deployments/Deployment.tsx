import { useState } from 'react'
import { Link, useParams } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Check, Clock, ExternalLink, X } from 'lucide-react'
import { ActorChip, Button, Callout, Card, Pill, StatusPill, useToast } from '../../design'
import { fmtDate, fmtSeconds } from '../../lib/format'
import { actions, qk, useDeployment, useDeployments } from '../../lib/queries'
import { QueryState, TimeAgo, useDocumentTitle } from '../../shell/common'
import { NotFound } from '../../shell/NotFound'

export function DeploymentPage() {
  const { org, repo, id } = useParams({ from: '/$org/$repo/deployments/$id' })
  const view = useDeployment(org, repo, id)
  const list = useDeployments(org, repo)
  const qc = useQueryClient()
  const toast = useToast()
  const [busy, setBusy] = useState(false)
  useDocumentTitle(`Deployment ${id.slice(0, 12)}`)
  const act = async (fn: () => Promise<unknown>, ok: string) => {
    setBusy(true)
    try {
      await fn()
      await qc.invalidateQueries({ queryKey: qk.r(org, repo, 'deployments') })
      toast.push({ tone: 'ok', title: ok })
    } catch {
      /* api */
    } finally {
      setBusy(false)
    }
  }
  // Without the delivery slice the detail route 404s; fall back to the list entry.
  const fallback = list.data?.find((d) => d.id === id)
  const merged = { ...view, data: view.data ?? (fallback ? { ...fallback } : undefined), isPending: view.isPending && list.isPending }

  return (
    <QueryState query={merged as typeof view} notFound={<NotFound what="deployment" />}>
      {(d) => {
        if (!d) return <NotFound what="deployment" />
        const ro = d.rollout
        return (
          <div className="stack">
            <div className="page-head">
              <div>
                <h1>
                  <StatusPill status={d.status} /> {d.environment} ← <code>{d.commit_oid.slice(0, 7)}</code>
                </h1>
                <div className="row sub">
                  <span>
                    requested by <ActorChip actor={d.actor} size="sm" />
                  </span>
                  <span>
                    <TimeAgo iso={d.created_at} />
                  </span>
                  {d.finished_at ? <span>· finished {fmtDate(d.finished_at)}</span> : null}
                  {d.rollback_of ? (
                    <span>
                      · rollback of{' '}
                      <Link to="/$org/$repo/deployments/$id" params={{ org, repo, id: d.rollback_of }}>
                        {d.rollback_of.slice(0, 12)}
                      </Link>
                    </span>
                  ) : null}
                  {d.previous_id ? (
                    <span>
                      · replaced{' '}
                      <Link to="/$org/$repo/deployments/$id" params={{ org, repo, id: d.previous_id }}>
                        {d.previous_id.slice(0, 12)}
                      </Link>
                    </span>
                  ) : null}
                </div>
              </div>
              <div className="row">
                {d.url ? (
                  <a className="btn" href={d.url} target="_blank" rel="noreferrer">
                    <ExternalLink /> Open {d.url}
                  </a>
                ) : null}
                {d.status === 'awaiting_approval' ? (
                  <>
                    <Button variant="primary" loading={busy} onClick={() => void act(() => actions.approve(org, repo, d.id), 'Approved')}>
                      <Check /> Approve
                    </Button>
                    <Button variant="danger" loading={busy} onClick={() => void act(() => actions.reject(org, repo, d.id), 'Rejected')}>
                      <X /> Reject
                    </Button>
                  </>
                ) : null}
              </div>
            </div>
            <div className="grid-2">
              <Card title="Policy decision">
                {ro?.decision ? (
                  <div className="decision">
                    <p className="small">
                      Outcome: <b>{String(ro.decision.outcome ?? '—')}</b>
                      {ro.decision.bypass ? <Pill small tone="warn" title="Rollbacks waive approvals and timers">bypass</Pill> : null}
                    </p>
                    {(ro.decision.checks ?? []).map((c, i) => (
                      <div key={i} className={`check ${c.pass ? 'pass' : 'fail'}`}>
                        {c.pass ? <Check /> : <X />}
                        <span>
                          <b>{c.rule.replace(/_/g, ' ')}</b>
                          {c.detail ? <span className="muted"> — {c.detail}</span> : null}
                        </span>
                      </div>
                    ))}
                    {ro.decision.wait_until ? (
                      <p className="small muted">
                        <Clock size={12} /> wait timer until {fmtDate(ro.decision.wait_until)}
                      </p>
                    ) : null}
                  </div>
                ) : (
                  <p className="muted small">No policy record: this environment runs with v0 defaults (no approvals, local Docker).</p>
                )}
              </Card>
              <Card title="Rollout">
                <dl className="dl">
                  <dt>Image</dt>
                  <dd>{d.image || '—'}</dd>
                  <dt>Container</dt>
                  <dd>{d.container_id ? <code>{d.container_id.slice(0, 12)}</code> : '—'}</dd>
                  {ro?.source_ref ? (
                    <>
                      <dt>Source ref</dt>
                      <dd>
                        <code>{ro.source_ref}</code>
                      </dd>
                    </>
                  ) : null}
                  {ro?.health ? (
                    <>
                      <dt>Health</dt>
                      <dd>
                        {ro.health.ok ? <Pill small tone="ok">healthy</Pill> : <Pill small tone="danger">unhealthy</Pill>} {ro.health.status ? `HTTP ${ro.health.status}` : ''} {ro.health.detail ?? ''}
                      </dd>
                    </>
                  ) : null}
                  {ro?.recovery_seconds !== undefined ? (
                    <>
                      <dt>Recovery</dt>
                      <dd>{fmtSeconds(ro.recovery_seconds)} from failure to a healthy rollback</dd>
                    </>
                  ) : null}
                  {d.ttl ? (
                    <>
                      <dt>Preview expires</dt>
                      <dd>
                        {fmtDate(d.ttl.expires_at)} ({d.ttl.extensions} extension{d.ttl.extensions === 1 ? '' : 's'}){' '}
                        <Button size="sm" onClick={() => void act(() => actions.extend(org, repo, d.id, 4), 'Extended by 4 hours (clamped to 24 h)')}>
                          Extend 4 h
                        </Button>
                      </dd>
                    </>
                  ) : null}
                </dl>
                {ro?.failure ? (
                  <Callout tone="danger" icon={<X />}>
                    <pre style={{ whiteSpace: 'pre-wrap', margin: 0, fontFamily: 'var(--font-mono)', fontSize: 'var(--fs-1)' }}>{ro.failure}</pre>
                  </Callout>
                ) : null}
              </Card>
            </div>
            {d.approvals?.length ? (
              <Card title="Approvals">
                <ul className="timeline" style={{ listStyle: 'none', padding: '0 0 0 22px', margin: 0 }}>
                  {d.approvals.map((a) => (
                    <li key={a.id} className={`timeline-item ${a.decision === 'approve' ? 'ok' : 'danger'}`}>
                      <b>{a.approver}</b> {a.decision === 'approve' ? 'approved' : 'rejected'} {a.comment ? <span className="muted">— {a.comment}</span> : null}
                      <div className="when">{fmtDate(a.at)}</div>
                    </li>
                  ))}
                </ul>
              </Card>
            ) : null}
          </div>
        )
      }}
    </QueryState>
  )
}
