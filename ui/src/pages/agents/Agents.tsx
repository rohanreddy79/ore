import { useState } from 'react'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Bot, Pause, Play, Wallet } from 'lucide-react'
import { Button, Callout, Card, EmptyState, Pill, Stat, StatusPill, Textarea, useToast } from '../../design'
import { fmtSeconds } from '../../lib/format'
import { actions, qk, useAgentRuns, useFeatures, useFleet, useFleetEvidence, useIssues, useRepo } from '../../lib/queries'
import type { FleetAgent } from '../../lib/types'
import { LevelBadge, LevelExplainer, LoopSteps, QueryState, TimeAgo, useDocumentTitle } from '../../shell/common'

export function AgentsPage() {
  const { org, repo } = useParams({ from: '/$org/$repo/agents' })
  const runs = useAgentRuns(org, repo)
  const issues = useIssues(org, repo)
  const repoQ = useRepo(org, repo)
  const { data: features } = useFeatures()
  const fleet = useFleet(org, repo, !!features?.fleet)
  const evidence = useFleetEvidence(org, repo, !!features?.fleet)
  const qc = useQueryClient()
  const toast = useToast()
  const navigate = useNavigate()
  const [prompt, setPrompt] = useState('')
  const [busy, setBusy] = useState(false)
  useDocumentTitle(`Agents · ${org}/${repo}`)
  const level = repoQ.data?.autonomy_level ?? 2

  const start = async () => {
    if (!prompt.trim()) return
    setBusy(true)
    try {
      const run = await actions.runAgent(org, repo, { prompt: prompt.trim() })
      await qc.invalidateQueries({ queryKey: qk.r(org, repo, 'agent') })
      setPrompt('')
      toast.push({ tone: 'ok', title: 'Agent run started' })
      void navigate({ to: '/$org/$repo/agents/$id', params: { org, repo, id: run.id } })
    } catch {
      /* api toast */
    } finally {
      setBusy(false)
    }
  }

  const fleetData = fleet.data
  const fleetAgents: FleetAgent[] = Array.isArray(fleetData) ? fleetData : (fleetData?.agents ?? [])
  const paused = !Array.isArray(fleetData) && fleetData?.paused === true
  const issueFor = (id: string | undefined) => issues.data?.find((i) => i.id === id)

  return (
    <div className="stack" style={{ gap: 'var(--sp-5)' }}>
      <div className="page-head">
        <div>
          <h1>
            <Bot aria-hidden /> Agents
          </h1>
          <p className="sub">Every run follows the loop: signal → plan → change → verify → land → deploy → observe. How far it goes is set by the autonomy level.</p>
        </div>
        <div className="row">
          <LevelBadge level={level} />
          <Link to="/$org/$repo/settings" params={{ org, repo }} className="btn sm">
            Change level
          </Link>
        </div>
      </div>

      <div className="split">
        <div className="stack" style={{ gap: 'var(--sp-5)' }}>
          <Card title="Run an agent">
            <div className="stack">
              <Textarea value={prompt} onChange={(e) => setPrompt(e.target.value)} placeholder="Describe the work, e.g. “Add a /healthz endpoint that returns ok”. Or open an issue and run the agent from there." rows={3} aria-label="Prompt for the agent" />
              <div className="row">
                <Button variant="primary" loading={busy} disabled={!prompt.trim()} onClick={() => void start()}>
                  <Play /> Start the loop at L{level}
                </Button>
                <span className="small muted">Adapter: {runs.data?.[0]?.adapter ?? 'the server’s --agent-cmd'}</span>
              </div>
              <LevelExplainer level={level} />
            </div>
          </Card>

          <Card tight title="Runs">
            <QueryState query={runs}>
              {(rows) =>
                rows.length === 0 ? (
                  <EmptyState icon={<Bot />} title="No agent runs yet" command={`ore issue create ${org}/${repo} "Fix the failing test"\nore agent run ${org}/${repo} --issue 1 --wait`}>
                    File an issue and run the agent on it, or start one with a prompt above. The scripted demo adapter needs no API key; Claude Code works with <code>ore serve --agent-cmd</code>.
                  </EmptyState>
                ) : (
                  <div className="list">
                    {rows.map((r) => {
                      const iss = issueFor(r.issue_id)
                      return (
                        <Link key={r.id} to="/$org/$repo/agents/$id" params={{ org, repo, id: r.id }} className="list-row" style={{ alignItems: 'flex-start', padding: 'var(--sp-3) var(--sp-4)' }}>
                          <StatusPill status={r.status} />
                          <div className="grow stack" style={{ gap: 6, minWidth: 0 }}>
                            <div className="title truncate">{iss ? `#${iss.number} ${iss.title}` : r.id}</div>
                            <LoopSteps run={r} />
                            <div className="meta">
                              <Pill small tone="agent">{r.adapter}</Pill>
                              {r.branch ? <span className="mono">{r.branch}</span> : null}
                              <span>
                                started <TimeAgo iso={r.created_at} />
                              </span>
                              {r.error ? <span style={{ color: 'var(--danger-fg)' }} className="truncate">{r.error}</span> : null}
                            </div>
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

        <aside className="stack" style={{ gap: 'var(--sp-4)' }}>
          {features?.fleet ? (
            <>
              <Card
                title={<><Wallet aria-hidden /> Fleet</>}
                actions={
                  <Button size="sm" onClick={() => void actions.fleetPause(org, repo, !paused).then(() => qc.invalidateQueries({ queryKey: qk.r(org, repo, 'fleet') }))}>
                    {paused ? <><Play /> Resume</> : <><Pause /> Pause</>}
                  </Button>
                }
              >
                {paused ? (
                  <Callout tone="warn">The fleet is paused: no agent starts new work in this repository.</Callout>
                ) : null}
                {fleetAgents.length === 0 ? (
                  <p className="muted small">No fleet roles configured.</p>
                ) : (
                  <ul className="stack" style={{ listStyle: 'none', padding: 0, margin: 0, gap: 'var(--sp-2)' }}>
                    {fleetAgents.map((a, i) => (
                      <li key={a.id ?? i} className="row between">
                        <span>
                          <b>{a.name}</b> {a.role ? <span className="muted small">{a.role}</span> : null}
                        </span>
                        <span className="row">
                          {a.status ? <StatusPill status={a.status} small /> : null}
                          {a.budgets ? <Pill small title={JSON.stringify(a.budgets)}>budget</Pill> : null}
                        </span>
                      </li>
                    ))}
                  </ul>
                )}
              </Card>
              <Card title="Evidence">
                {evidence.data ? (
                  <div className="grid-2" style={{ gap: 'var(--sp-2)' }}>
                    <Stat label="Acceptance" value={pct(evidence.data.acceptance_rate)} />
                    <Stat label="Post-merge fixes" value={pct(evidence.data.fix_rate)} />
                    <Stat label="Rollbacks" value={pct(evidence.data.rollback_rate)} />
                    <Stat label="Recovery" value={fmtSeconds(evidence.data.recovery_seconds)} />
                  </div>
                ) : (
                  <p className="muted small">No evidence recorded yet.</p>
                )}
                {evidence.data?.recommended_level !== undefined ? (
                  <p className="small" style={{ marginTop: 'var(--sp-3)' }}>
                    Recommended level: <LevelBadge level={evidence.data.recommended_level} compact /> based on {evidence.data.samples ?? 0} samples.
                  </p>
                ) : null}
              </Card>
            </>
          ) : (
            <Card title="Fleet">
              <p className="muted small">Budgets, roles, watchers and evidence-based level recommendations arrive with the <code>agent.fleet</code> extension. This server does not run it yet; autonomy is set by hand under Settings.</p>
            </Card>
          )}
        </aside>
      </div>
    </div>
  )
}

function pct(v: number | undefined): string {
  return v === undefined || v === null ? '—' : `${Math.round(v * 100)}%`
}
