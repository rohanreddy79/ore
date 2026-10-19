import { useMemo, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { Activity, BookOpen, Bot, CircleDot, GitPullRequest, Plus, Rocket, Workflow } from 'lucide-react'
import { ActorChip, Button, Card, EmptyState, Pill, SkeletonRows, Stat } from '../design'
import { useLedgerLive } from '../lib/live'
import { useEvents, useFeatures, useLessons, useRepos } from '../lib/queries'
import type { LedgerEvent, Repo } from '../lib/types'
import { LevelBadge, TimeAgo, useDocumentTitle } from '../shell/common'
import { EventRow, describeEvent } from './ledger/EventRow'

export function Home() {
  useDocumentTitle('Home')
  const repos = useRepos()
  const events = useEvents({ limit: 1000 })
  const { data: features } = useFeatures()
  const lessons = useLessons(!!features?.learn)
  const navigate = useNavigate()
  const [freshIds, setFresh] = useState<Set<string>>(new Set())
  useLedgerLive({
    onEvent: (e) =>
      setFresh((s) => {
        const n = new Set(s)
        n.add(e.id)
        return n
      }),
  })

  const recent = useMemo(() => (events.data ?? []).slice(-40).reverse(), [events.data])
  const today = useMemo(() => summarizeAgents(events.data ?? []), [events.data])

  if (repos.isPending) return <div className="container page"><SkeletonRows rows={6} /></div>
  const list = repos.data ?? []

  return (
    <div className="container page">
      <div className="home-hero">
        <div>
          <h1>Repositories</h1>
          <p>One binary hosting git, review, pipelines, delivery and the agent loop. Every page here is a view over the ledger.</p>
        </div>
        <Button variant="primary" onClick={() => void navigate({ to: '/new' })}>
          <Plus /> New repository
        </Button>
      </div>

      {list.length === 0 ? (
        <Card>
          <EmptyState
            icon={<BookOpen />}
            title="No repositories yet"
            command={`ore org create acme && ore repo create acme/app --level 2\nore commit acme/app -m "import" $(git ls-files)`}
            actions={
              <>
                <Button variant="primary" onClick={() => void navigate({ to: '/new' })}>
                  Create in the browser
                </Button>
                {features?.learn && lessons.data && lessons.data.length > 0 ? (
                  <a className="btn" href={`/api/v0/learn/lessons`}>
                    Open the first lesson
                  </a>
                ) : null}
              </>
            }
          >
            Create an organization and a repository, then commit through the API without a clone, or push with git over HTTP or SSH. <code>ore demo</code> runs the whole loop on a sample repository.
          </EmptyState>
        </Card>
      ) : (
        <div className="home-grid">
          <div className="stack">
            <Card tight title={`${list.length} ${list.length === 1 ? 'repository' : 'repositories'}`}>
              <div className="list">
                {list.map((r) => (
                  <RepoRow key={r.id} repo={r} events={events.data ?? []} />
                ))}
              </div>
            </Card>
            <Card title={<><Bot aria-hidden /> What agents did today</>}>
              {today.runs === 0 ? (
                <p className="muted">No agent runs in the last 24 hours. Open an issue and press “Run agent” to start the loop.</p>
              ) : (
                <div className="grid-3">
                  <Stat label="Agent runs" value={today.runs} delta={`${today.completed} completed · ${today.failed} failed`} />
                  <Stat label="Changes landed" value={today.merged} delta="by any actor" />
                  <Stat label="Deployments" value={today.deploys} delta={`${today.rollbacks} rolled back`} />
                </div>
              )}
              {today.lastAgentEvents.length ? (
                <ul className="timeline" style={{ marginTop: 'var(--sp-4)' }}>
                  {today.lastAgentEvents.map((e) => (
                    <li key={e.id} className={`timeline-item ${e.type.endsWith('.failed') ? 'danger' : e.type.endsWith('.completed') || e.type.endsWith('.merged') ? 'ok' : 'agent'}`}>
                      <div>
                        <Link to={describeEvent(e).href as '/'} className="quiet">
                          {describeEvent(e).text}
                        </Link>
                      </div>
                      <div className="when">
                        <ActorChip actor={e.oreactor} size="sm" /> · <TimeAgo iso={e.time} />
                      </div>
                    </li>
                  ))}
                </ul>
              ) : null}
            </Card>
          </div>
          <div className="stack">
            <Card
              tight
              title={<><Activity aria-hidden /> Recent activity</>}
              actions={
                <Link to="/ledger" className="btn sm">
                  Ledger
                </Link>
              }
            >
              <div className="list" aria-live="polite">
                {recent.length === 0 ? <p className="muted" style={{ padding: 'var(--sp-4)' }}>Nothing on the ledger yet.</p> : null}
                {recent.map((e) => (
                  <EventRow key={e.id} event={e} fresh={freshIds.has(e.id)} />
                ))}
              </div>
            </Card>
          </div>
        </div>
      )}
    </div>
  )
}

function RepoRow({ repo, events }: { repo: Repo; events: LedgerEvent[] }) {
  const prefix = `${repo.org}/${repo.name}/`
  const last = [...events].reverse().find((e) => e.subject === `${repo.org}/${repo.name}` || e.subject.startsWith(prefix))
  const counts = useMemo(() => {
    let issues = 0
    let changes = 0
    let runs = 0
    let deploys = 0
    for (const e of events) {
      if (!e.subject.startsWith(prefix)) continue
      if (e.type === 'io.ore.issue.opened') issues++
      else if (e.type === 'io.ore.change.opened') changes++
      else if (e.type === 'io.ore.pipeline.run.finished') runs++
      else if (e.type === 'io.ore.deployment.succeeded') deploys++
    }
    return { issues, changes, runs, deploys }
  }, [events, prefix])
  return (
    <Link to="/$org/$repo" params={{ org: repo.org, repo: repo.name }} className="repo-card">
      <div className="row between">
        <span className="name">
          <BookOpen aria-hidden />
          {repo.org}/<b>{repo.name}</b>
        </span>
        <LevelBadge level={repo.autonomy_level} />
      </div>
      <div className="row small muted">
        <span title="Issues opened">
          <CircleDot size={12} aria-hidden /> {counts.issues}
        </span>
        <span title="Changes opened">
          <GitPullRequest size={12} aria-hidden /> {counts.changes}
        </span>
        <span title="Pipeline runs">
          <Workflow size={12} aria-hidden /> {counts.runs}
        </span>
        <span title="Deployments">
          <Rocket size={12} aria-hidden /> {counts.deploys}
        </span>
        <Pill small>{repo.default_branch}</Pill>
        <span className="grow" />
        {last ? (
          <span>
            updated <TimeAgo iso={last.time} />
          </span>
        ) : (
          <span>
            created <TimeAgo iso={repo.created_at} />
          </span>
        )}
      </div>
    </Link>
  )
}

export function summarizeAgents(events: LedgerEvent[]) {
  const since = Date.now() - 24 * 3600 * 1000
  let runs = 0
  let completed = 0
  let failed = 0
  let merged = 0
  let deploys = 0
  let rollbacks = 0
  const lastAgentEvents: LedgerEvent[] = []
  for (const e of events) {
    if (Date.parse(e.time) < since) continue
    if (e.type === 'io.ore.agent.session.started') runs++
    else if (e.type === 'io.ore.agent.session.completed') completed++
    else if (e.type === 'io.ore.agent.session.failed') failed++
    else if (e.type === 'io.ore.change.merged') merged++
    else if (e.type === 'io.ore.deployment.succeeded') deploys++
    else if (e.type === 'io.ore.deployment.rolled_back') rollbacks++
    if (e.oreactor?.kind === 'agent' || e.type.startsWith('io.ore.agent.session.')) lastAgentEvents.push(e)
  }
  return { runs, completed, failed, merged, deploys, rollbacks, lastAgentEvents: lastAgentEvents.slice(-8).reverse() }
}
