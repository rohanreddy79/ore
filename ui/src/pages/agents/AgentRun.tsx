import { Link, useParams } from '@tanstack/react-router'
import { Bot } from 'lucide-react'
import { Callout, Card, Pill, StatusPill } from '../../design'
import { repoBase } from '../../lib/api'
import { durationBetween } from '../../lib/format'
import { useAgentRun, useChanges, useDeployments, useIssues, useRuns } from '../../lib/queries'
import { LoopSteps, QueryState, TimeAgo, useDocumentTitle } from '../../shell/common'
import { NotFound } from '../../shell/NotFound'
import { LogViewer } from '../pipelines/LogViewer'

export function AgentRunPage() {
  const { org, repo, id } = useParams({ from: '/$org/$repo/agents/$id' })
  const run = useAgentRun(org, repo, id)
  const issues = useIssues(org, repo)
  const changes = useChanges(org, repo)
  const runs = useRuns(org, repo)
  const deployments = useDeployments(org, repo)
  useDocumentTitle(`Agent run ${id.slice(0, 12)}`)
  return (
    <QueryState query={run} notFound={<NotFound what="agent run" />}>
      {(r) => {
        const issue = issues.data?.find((i) => i.id === r.issue_id)
        const change = changes.data?.find((c) => c.id === r.change_id)
        const prun = runs.data?.find((p) => p.id === r.pipeline_run_id)
        const dep = deployments.data?.find((d) => d.id === r.deployment_id)
        return (
          <div className="stack">
            <div className="page-head">
              <div>
                <h1>
                  <Bot aria-hidden /> Agent run <span className="mono muted" style={{ fontWeight: 400 }}>{r.id.slice(0, 16)}</span>
                </h1>
                <div className="row sub">
                  <StatusPill status={r.status} />
                  <Pill tone="agent">{r.adapter}</Pill>
                  <span>
                    started <TimeAgo iso={r.created_at} />
                  </span>
                  <span>· {durationBetween(r.created_at, r.finished_at)}</span>
                </div>
              </div>
            </div>
            <Card title="The loop">
              <LoopSteps run={r} />
              {r.step === 'awaiting-human' ? <Callout tone="info" icon={<Bot />}>At this autonomy level the agent stops after verification. A human reviews and lands the change.</Callout> : null}
              {r.error ? (
                <Callout tone="danger" icon={<Bot />}>
                  <b>Failed at {r.step}:</b> {r.error}
                </Callout>
              ) : null}
            </Card>
            <div className="grid-2">
              <Card title="What it touched">
                <dl className="dl">
                  <dt>Signal</dt>
                  <dd>
                    {issue ? (
                      <Link to="/$org/$repo/issues/$n" params={{ org, repo, n: String(issue.number) }}>
                        #{issue.number} {issue.title}
                      </Link>
                    ) : (
                      <span className="muted">a prompt</span>
                    )}
                  </dd>
                  <dt>Branch</dt>
                  <dd>{r.branch ? <code>{r.branch}</code> : '—'}</dd>
                  <dt>Change</dt>
                  <dd>
                    {change ? (
                      <Link to="/$org/$repo/changes/$n" params={{ org, repo, n: String(change.number) }} search={{}}>
                        #{change.number} {change.title} <StatusPill status={change.state} small />
                      </Link>
                    ) : (
                      '—'
                    )}
                  </dd>
                  <dt>Verification</dt>
                  <dd>
                    {prun ? (
                      <Link to="/$org/$repo/pipelines/$id" params={{ org, repo, id: prun.id }}>
                        run {prun.id.slice(0, 12)} <StatusPill status={prun.status} small />
                      </Link>
                    ) : (
                      '—'
                    )}
                  </dd>
                  <dt>Deployment</dt>
                  <dd>
                    {dep ? (
                      <Link to="/$org/$repo/deployments/$id" params={{ org, repo, id: dep.id }}>
                        {dep.environment} {dep.id.slice(0, 12)} <StatusPill status={dep.status} small />
                      </Link>
                    ) : (
                      '—'
                    )}
                  </dd>
                </dl>
              </Card>
              <Card title="Provenance">
                <p className="small">
                  Everything this run wrote is on the ledger with actor <code>agent/{r.adapter}</code>. Hunks in the change carry the adapter and session, so blame and audit know an agent wrote them.
                </p>
                <Link to="/$org/$repo/ledger" params={{ org, repo }} search={{ subject: `${org}/${repo}/agent/${r.id}` }} className="btn sm" style={{ marginTop: 'var(--sp-2)' }}>
                  Events for this run
                </Link>
              </Card>
            </div>
            <LogViewer source={{ streamUrl: `/api/v0/webapp/agent/${encodeURIComponent(id)}/log/stream`, rawUrl: `${repoBase(org, repo)}/agent/runs/${encodeURIComponent(id)}/log` }} finished={r.status !== 'running'} title="Agent log" />
          </div>
        )
      }}
    </QueryState>
  )
}
