import { useMemo, useState } from 'react'
import { Link, useParams } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Check, ExternalLink, Rocket, RotateCcw, Square, X } from 'lucide-react'
import { Button, Callout, Card, Dialog, EmptyState, Field, Input, Pill, Select, Stat, StatusPill, useToast } from '../../design'
import { fmtSeconds } from '../../lib/format'
import { actions, qk, useDeployments, useDora, useEnvironments, useFeatures, useRepo } from '../../lib/queries'
import type { Deployment, Environment } from '../../lib/types'
import { ActorChip } from '../../design'
import { FeatureHint, QueryState, TimeAgo, useDocumentTitle } from '../../shell/common'

export function DeploymentsPage() {
  const { org, repo } = useParams({ from: '/$org/$repo/deployments' })
  const deployments = useDeployments(org, repo)
  const envs = useEnvironments(org, repo)
  const dora = useDora(org, repo)
  const repoQ = useRepo(org, repo)
  const { data: features } = useFeatures()
  const qc = useQueryClient()
  const toast = useToast()
  const [deployOpen, setDeployOpen] = useState<string | null>(null)
  const [ref, setRef] = useState('')
  const [busy, setBusy] = useState(false)
  const [newEnv, setNewEnv] = useState(false)
  useDocumentTitle(`Deployments · ${org}/${repo}`)

  const invalidate = () => Promise.all([qc.invalidateQueries({ queryKey: qk.r(org, repo, 'deployments') }), qc.invalidateQueries({ queryKey: qk.r(org, repo, 'environments') }), qc.invalidateQueries({ queryKey: qk.r(org, repo, 'dora') })])

  // Environments: explicit records when the delivery slice is present, else the names seen in deployments.
  const envList: Environment[] = useMemo(() => {
    if (envs.data && envs.data.length > 0) return envs.data
    const names = new Set<string>(['preview', 'production'])
    for (const d of deployments.data ?? []) names.add(d.environment)
    return [...names].map((name) => ({ id: name, org_id: '', repo_id: '', name, kind: name === 'production' ? 'production' : name === 'staging' ? 'staging' : 'preview', target: { kind: 'local-docker' }, policy: { allowed_refs: [], required_approvals: 0, approvers: [], wait_seconds: 0, require_green_pipeline: false, allow_agent_deploys: name !== 'production', health_path: '/', health_timeout_seconds: 60, auto_rollback: name !== 'preview' }, implicit: true }))
  }, [envs.data, deployments.data])

  const current = (env: string) => (deployments.data ?? []).find((d) => d.environment === env && d.status === 'succeeded')
  const awaiting = (deployments.data ?? []).filter((d) => d.status === 'awaiting_approval')

  const act = async (fn: () => Promise<unknown>, ok: string) => {
    setBusy(true)
    try {
      await fn()
      await invalidate()
      toast.push({ tone: 'ok', title: ok })
      setDeployOpen(null)
    } catch {
      /* api toast */
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="stack" style={{ gap: 'var(--sp-5)' }}>
      <div className="page-head">
        <div>
          <h1>
            <Rocket aria-hidden /> Deployments
          </h1>
          <p className="sub">Environments with policy, approvals and health-gated rollouts. Every deploy is on the ledger with its decision.</p>
        </div>
        {features?.environments ? (
          <Button size="sm" onClick={() => setNewEnv(true)}>
            New environment
          </Button>
        ) : null}
      </div>

      {awaiting.length ? (
        <Card title={`Approvals queue · ${awaiting.length}`}>
          <div className="stack">
            {awaiting.map((d) => (
              <div key={d.id} className="row between" style={{ gap: 'var(--sp-3)' }}>
                <div>
                  <Link to="/$org/$repo/deployments/$id" params={{ org, repo, id: d.id }}>
                    <b>{d.environment}</b> ← <code>{d.commit_oid.slice(0, 7)}</code>
                  </Link>
                  <div className="small muted">
                    requested by <ActorChip actor={d.actor} size="sm" /> <TimeAgo iso={d.created_at} />
                  </div>
                </div>
                <div className="row">
                  <Button size="sm" variant="primary" onClick={() => void act(() => actions.approve(org, repo, d.id), 'Approved')}>
                    <Check /> Approve
                  </Button>
                  <Button size="sm" variant="danger" onClick={() => void act(() => actions.reject(org, repo, d.id), 'Rejected')}>
                    <X /> Reject
                  </Button>
                </div>
              </div>
            ))}
          </div>
        </Card>
      ) : null}

      <div className="grid-2">
        {envList.map((env) => {
          const cur = current(env.name)
          return (
            <Card
              key={env.name}
              className="env-card"
              title={
                <span className="env-name">
                  {env.name} <Pill small tone={env.kind === 'production' ? 'danger' : env.kind === 'staging' ? 'warn' : 'info'}>{env.kind}</Pill>
                  {env.implicit ? <Pill small outline title="No environment record: v0 defaults apply">implicit</Pill> : null}
                </span>
              }
              actions={
                <>
                  <Button size="sm" variant="primary" onClick={() => { setRef(''); setDeployOpen(env.name) }}>
                    <Rocket /> Deploy
                  </Button>
                  {cur ? (
                    <>
                      <Button size="sm" onClick={() => void act(() => (env.implicit ? actions.rollback(org, repo, env.name) : actions.rollbackEnv(org, repo, env.name)), `Rolling back ${env.name}`)} title="Redeploy the previous release">
                        <RotateCcw /> Roll back
                      </Button>
                      <Button size="sm" variant="ghost" icon aria-label={`Stop ${env.name}`} title="Stop the container" onClick={() => void act(() => actions.stop(org, repo, env.name), `Stopped ${env.name}`)}>
                        <Square />
                      </Button>
                    </>
                  ) : null}
                </>
              }
            >
              <div className="stack" style={{ gap: 'var(--sp-3)' }}>
                {cur ? (
                  <div className="row between">
                    <span>
                      <StatusPill status={cur.status} small /> <code>{cur.commit_oid.slice(0, 7)}</code> <span className="muted small">
                        <TimeAgo iso={cur.finished_at ?? cur.created_at} />
                      </span>
                    </span>
                    {cur.url && /^https?:/.test(cur.url) ? (
                      <a href={cur.url} target="_blank" rel="noreferrer" className="btn sm ghost">
                        <ExternalLink /> {cur.url.replace(/^https?:\/\//, '')}
                      </a>
                    ) : cur.url ? (
                      <code className="small muted" title="Recorded only: this executor does not start containers">{cur.url}</code>
                    ) : null}
                  </div>
                ) : (
                  <p className="muted small">Nothing deployed yet.</p>
                )}
                <dl className="policy-list">
                  <dt>Approvals</dt>
                  <dd>{env.policy.required_approvals || 'none'}</dd>
                  <dt>Allowed refs</dt>
                  <dd>{env.policy.allowed_refs?.length ? env.policy.allowed_refs.join(', ') : 'any'}</dd>
                  <dt>Agents may deploy</dt>
                  <dd>{env.policy.allow_agent_deploys ? 'yes' : 'no'}</dd>
                  <dt>Green pipeline</dt>
                  <dd>{env.policy.require_green_pipeline ? 'required' : 'not required'}</dd>
                  <dt>Health</dt>
                  <dd>
                    {env.policy.health_path} · {env.policy.health_timeout_seconds}s
                  </dd>
                  <dt>Auto-rollback</dt>
                  <dd>{env.policy.auto_rollback ? 'on' : 'off'}</dd>
                  {env.kind === 'preview' || env.kind === 'ephemeral' ? (
                    <>
                      <dt>Preview TTL</dt>
                      <dd>{fmtSeconds(env.policy.preview_ttl_seconds ?? 7200)}</dd>
                    </>
                  ) : null}
                  <dt>Target</dt>
                  <dd>{env.target.kind}{env.target.host ? ` ${env.target.user ?? ''}@${env.target.host}` : ''}</dd>
                </dl>
              </div>
            </Card>
          )
        })}
      </div>

      <Card title="DORA · last 30 days">
        {dora.data ? (
          <div className="grid-3">
            <Stat label="Deployment frequency" value={`${(dora.data.deployment_frequency?.per_week ?? 0).toFixed(1)}/wk`} delta={`${dora.data.deployment_frequency?.succeeded ?? 0} succeeded · ${dora.data.deployment_frequency?.rollbacks ?? 0} rollbacks`} />
            <Stat label="Lead time (median)" value={dora.data.lead_time?.samples ? fmtSeconds(dora.data.lead_time.median_seconds) : '—'} delta={`p90 ${dora.data.lead_time?.samples ? fmtSeconds(dora.data.lead_time.p90_seconds) : '—'} · ${dora.data.lead_time?.samples ?? 0} changes`} />
            <Stat label="Change failure rate" value={dora.data.change_failure_rate?.total ? `${Math.round((dora.data.change_failure_rate.rate ?? 0) * 100)}%` : '—'} delta={`${dora.data.change_failure_rate?.failed ?? 0} of ${dora.data.change_failure_rate?.total ?? 0} production deploys`} />
            <Stat label="Failed-deploy recovery" value={dora.data.failed_deployment_recovery_time?.samples ? fmtSeconds(dora.data.failed_deployment_recovery_time.median_seconds) : '—'} delta={`p90 ${dora.data.failed_deployment_recovery_time?.samples ? fmtSeconds(dora.data.failed_deployment_recovery_time.p90_seconds) : '—'}`} />
          </div>
        ) : dora.isPending ? (
          <p className="muted small">Loading…</p>
        ) : (
          <FeatureHint feature="delivery.environments" what="The DORA view" />
        )}
      </Card>

      <Card tight title="History">
        <QueryState query={deployments}>
          {(rows) =>
            rows.length === 0 ? (
              <EmptyState icon={<Rocket />} title="No deployments yet" command={`ore deploy start ${org}/${repo} --env preview`}>
                Deploy the default branch to a preview, or let an agent at L3+ do it after verification. Production deploys at L4 go through the production policy.
              </EmptyState>
            ) : (
              <ul className="timeline" style={{ margin: 'var(--sp-3) var(--sp-4)', listStyle: 'none', padding: '0 0 0 22px' }}>
                {rows.map((d) => (
                  <DeploymentItem key={d.id} d={d} org={org} repo={repo} />
                ))}
              </ul>
            )
          }
        </QueryState>
      </Card>

      <Dialog
        open={!!deployOpen}
        onClose={() => setDeployOpen(null)}
        title={`Deploy to ${deployOpen ?? ''}`}
        foot={
          <>
            <Button onClick={() => setDeployOpen(null)}>Cancel</Button>
            <Button variant="primary" loading={busy} onClick={() => void act(() => {
              const env = envList.find((e) => e.name === deployOpen)
              const r = ref || repoQ.data?.default_branch || 'main'
              return env && !env.implicit ? actions.requestDeployment(org, repo, env.name, r) : actions.deploy(org, repo, { ref: r, environment: deployOpen ?? 'preview' })
            }, `Deployment of ${ref || repoQ.data?.default_branch || 'main'} requested`)}>
              <Rocket /> Deploy
            </Button>
          </>
        }
      >
        <div className="stack">
          <Field label="Ref" htmlFor="deploy-ref" hint="A branch, tag or commit. Policy may restrict which refs an environment accepts.">
            <Input id="deploy-ref" value={ref} onChange={(e) => setRef(e.target.value)} placeholder={repoQ.data?.default_branch ?? 'main'} />
          </Field>
          {deployOpen === 'production' ? <Callout tone="warn">Production: the policy's approvals, wait timer and green-pipeline rule apply. If approvals are required the deployment parks in the queue above.</Callout> : null}
        </div>
      </Dialog>
      <NewEnvironmentDialog open={newEnv} onClose={() => setNewEnv(false)} org={org} repo={repo} onCreated={() => void invalidate()} />
    </div>
  )
}

function DeploymentItem({ d, org, repo }: { d: Deployment; org: string; repo: string }) {
  const tone = d.status === 'succeeded' ? 'ok' : d.status === 'failed' || d.status === 'refused' || d.status === 'rejected' ? 'danger' : d.status === 'awaiting_approval' || d.status === 'started' || d.status === 'requested' ? 'warn' : ''
  return (
    <li className={`timeline-item ${tone}`}>
      <div className="row">
        <Link to="/$org/$repo/deployments/$id" params={{ org, repo, id: d.id }} className="quiet">
          <b>{d.environment}</b> ← <code>{d.commit_oid.slice(0, 7)}</code>
        </Link>
        <StatusPill status={d.status} small />
        {d.rollback_of ? <Pill small tone="warn">rollback</Pill> : null}
        {d.url && d.status === 'succeeded' ? (
          /^https?:/.test(d.url) ? (
            <a href={d.url} target="_blank" rel="noreferrer" className="small">
              {d.url}
            </a>
          ) : (
            <code className="small muted">{d.url}</code>
          )
        ) : null}
      </div>
      <div className="when">
        <ActorChip actor={d.actor} size="sm" /> · <TimeAgo iso={d.created_at} />
      </div>
    </li>
  )
}

function NewEnvironmentDialog({ open, onClose, org, repo, onCreated }: { open: boolean; onClose: () => void; org: string; repo: string; onCreated: () => void }) {
  const [name, setName] = useState('')
  const [kind, setKind] = useState('staging')
  const [approvals, setApprovals] = useState(0)
  const [refs, setRefs] = useState('')
  const [busy, setBusy] = useState(false)
  const toast = useToast()
  const submit = async () => {
    setBusy(true)
    try {
      await actions.createEnvironment(org, repo, { name, kind, policy: { required_approvals: approvals, allowed_refs: refs ? refs.split(',').map((s) => s.trim()).filter(Boolean) : [] } })
      toast.push({ tone: 'ok', title: `Environment ${name} created` })
      onCreated()
      onClose()
      setName('')
    } catch {
      /* api */
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open={open} onClose={onClose} title="New environment" foot={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" loading={busy} disabled={!name} onClick={() => void submit()}>Create</Button></>}>
      <div className="stack">
        <Field label="Name" htmlFor="env-name">
          <Input id="env-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="staging" />
        </Field>
        <Field label="Kind" htmlFor="env-kind" hint="Sets the policy defaults: production requires care, previews expire.">
          <Select id="env-kind" value={kind} onChange={(e) => setKind(e.target.value)}>
            <option value="production">production</option>
            <option value="staging">staging</option>
            <option value="preview">preview</option>
            <option value="ephemeral">ephemeral</option>
          </Select>
        </Field>
        <Field label="Required approvals" htmlFor="env-appr">
          <Input id="env-appr" type="number" min={0} value={approvals} onChange={(e) => setApprovals(Number(e.target.value))} />
        </Field>
        <Field label="Allowed refs" htmlFor="env-refs" hint="Comma-separated globs over full refs, e.g. refs/heads/main, refs/tags/v*. Empty allows any.">
          <Input id="env-refs" value={refs} onChange={(e) => setRefs(e.target.value)} />
        </Field>
      </div>
    </Dialog>
  )
}
