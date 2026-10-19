import { useState } from 'react'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Bot, CircleCheck, CircleDot, Play } from 'lucide-react'
import { ActorChip, Button, Callout, Card, Dialog, Pill, StatusPill, useToast } from '../../design'
import { LEVEL_EXPLANATIONS } from '../../lib/format'
import { actions, qk, useAgentRuns, useIssue, useIssueComments, useRepo } from '../../lib/queries'
import { LevelExplainer, Markdown, QueryState, TimeAgo, useDocumentTitle } from '../../shell/common'
import { NotFound } from '../../shell/NotFound'

export function IssuePage() {
  const { org, repo, n } = useParams({ from: '/$org/$repo/issues/$n' })
  const num = Number(n)
  const issue = useIssue(org, repo, num)
  const comments = useIssueComments(org, repo, num)
  const repoQ = useRepo(org, repo)
  const runs = useAgentRuns(org, repo)
  const navigate = useNavigate()
  const qc = useQueryClient()
  const toast = useToast()
  const [runOpen, setRunOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  useDocumentTitle(issue.data ? `#${num} ${issue.data.title}` : `Issue #${num}`)

  if (!Number.isFinite(num) || num <= 0) return <NotFound what="issue" />
  const level = repoQ.data?.autonomy_level ?? 2
  const related = (runs.data ?? []).filter((r) => r.issue_id && issue.data && r.issue_id === issue.data.id)

  const runAgent = async () => {
    setBusy(true)
    try {
      const run = await actions.runAgent(org, repo, { issue: num })
      await qc.invalidateQueries({ queryKey: qk.r(org, repo, 'agent') })
      toast.push({ tone: 'ok', title: 'Agent run started', body: `Following the loop at L${level}.`, action: { label: 'Watch', onClick: () => void navigate({ to: '/$org/$repo/agents/$id', params: { org, repo, id: run.id } }) } })
      setRunOpen(false)
      void navigate({ to: '/$org/$repo/agents/$id', params: { org, repo, id: run.id } })
    } catch {
      /* toast raised by the API layer */
    } finally {
      setBusy(false)
    }
  }
  const close = async () => {
    await actions.closeIssue(org, repo, num, 'closed from the web app')
    await qc.invalidateQueries({ queryKey: qk.r(org, repo, 'issues') })
    toast.push({ tone: 'ok', title: `Closed #${num}` })
  }

  return (
    <QueryState query={issue} notFound={<NotFound what="issue" />}>
      {(i) => (
        <div>
          <div className="page-head">
            <div style={{ minWidth: 0 }}>
              <h1 className="break">
                {i.title} <span className="muted" style={{ fontWeight: 400 }}>#{i.number}</span>
              </h1>
              <div className="row sub" style={{ marginTop: 'var(--sp-2)' }}>
                <StatusPill status={i.state} />
                <span>
                  opened <TimeAgo iso={i.created_at} />
                </span>
                {i.closed_at ? (
                  <span>
                    · closed <TimeAgo iso={i.closed_at} />
                  </span>
                ) : null}
              </div>
            </div>
            <div className="row">
              {i.state === 'open' ? (
                <>
                  <Button variant="primary" onClick={() => setRunOpen(true)}>
                    <Bot /> Run agent on this issue
                  </Button>
                  <Button onClick={() => void close()}>
                    <CircleCheck /> Close issue
                  </Button>
                </>
              ) : null}
            </div>
          </div>
          <div className="split">
            <div className="stack">
              <div className="comment">
                <div className="comment-head">
                  <span className="muted">Description</span>
                </div>
                <div className="comment-body">
                  <Markdown source={i.body} />
                </div>
              </div>
              {(comments.data ?? []).map((c) => (
                <div key={c.id} className={`comment ${c.actor_kind === 'agent' ? 'agent' : c.actor_kind === 'app' || c.actor_kind === 'system' ? 'bot' : ''}`}>
                  <div className="comment-head">
                    <ActorChip actor={{ kind: c.actor_kind, id: c.actor_id }} />
                    <span className="when">
                      <TimeAgo iso={c.created_at} />
                    </span>
                    {c.session_id ? <Pill small tone="agent" title="Written through the agent gateway">gateway {c.session_id.slice(0, 12)}</Pill> : null}
                  </div>
                  <div className="comment-body">
                    <Markdown source={c.body} />
                  </div>
                </div>
              ))}
              {comments.data && comments.data.length === 0 ? <p className="muted small">No comments. Agents comment through the gateway; humans through the API or MCP.</p> : null}
            </div>
            <aside className="stack">
              <Card title="Agent runs on this issue">
                {related.length === 0 ? (
                  <p className="muted small">None yet.</p>
                ) : (
                  <ul className="stack" style={{ listStyle: 'none', margin: 0, padding: 0, gap: 'var(--sp-2)' }}>
                    {related.map((r) => (
                      <li key={r.id}>
                        <Link to="/$org/$repo/agents/$id" params={{ org, repo, id: r.id }} className="row between quiet">
                          <span className="mono small">{r.id.slice(0, 14)}</span>
                          <StatusPill status={r.status} small />
                        </Link>
                        <div className="small muted">
                          step {r.step} · <TimeAgo iso={r.created_at} />
                        </div>
                      </li>
                    ))}
                  </ul>
                )}
              </Card>
              <Card title="Autonomy">
                <LevelExplainer level={level} />
                <p className="small muted" style={{ marginTop: 'var(--sp-2)' }}>
                  Change it under{' '}
                  <Link to="/$org/$repo/settings" params={{ org, repo }}>
                    Settings
                  </Link>
                  .
                </p>
              </Card>
            </aside>
          </div>
          <Dialog
            open={runOpen}
            onClose={() => setRunOpen(false)}
            title="Run an agent on this issue"
            foot={
              <>
                <Button onClick={() => setRunOpen(false)}>Cancel</Button>
                <Button variant="primary" loading={busy} onClick={() => void runAgent()}>
                  <Play /> Start the loop
                </Button>
              </>
            }
          >
            <div className="stack">
              <p>
                The agent reads <b>#{i.number} {i.title}</b> as its prompt, works on a branch, opens a change with provenance and runs the pipeline. What happens next depends on the repository's autonomy level:
              </p>
              <LevelExplainer level={level} />
              <Callout icon={<CircleDot />}>
                <b>
                  L{level} {LEVEL_EXPLANATIONS[level]?.title}
                </b>
                : {level <= 2 ? 'the change waits for a human to review and merge.' : level === 3 ? 'the change lands when the pipeline is green; deploys stop at preview.' : 'the change lands when green and ships to production with automatic rollback on failure.'}
              </Callout>
              <p className="small muted">Every step is recorded on the ledger with the agent as the actor. You can watch it live on the Agents tab.</p>
            </div>
          </Dialog>
        </div>
      )}
    </QueryState>
  )
}
