import { useMemo, useState } from 'react'
import { Link, useParams } from '@tanstack/react-router'
import { Archive, Database, Info } from 'lucide-react'
import { Callout, Card, CopyBlock, Pill, StatusPill, cx } from '../../design'
import { repoBase } from '../../lib/api'
import { durationBetween, fmtBytes, fmtDuration, tryJSON } from '../../lib/format'
import { useArtifacts, useChanges, useRun, useRunPlan } from '../../lib/queries'
import type { Divergence, Task } from '../../lib/types'
import { Markdown, QueryState, TimeAgo, useDocumentTitle } from '../../shell/common'
import { NotFound } from '../../shell/NotFound'
import { LogViewer } from './LogViewer'

/** Groups tasks into stages by their `needs` dependencies (longest-path layering). */
export function layerTasks(tasks: Task[]): Task[][] {
  const byId = new Map(tasks.map((t) => [t.id, t]))
  const depth = new Map<string, number>()
  const visit = (t: Task, stack: Set<string>): number => {
    const d = depth.get(t.id)
    if (d !== undefined) return d
    if (stack.has(t.id)) return 0
    stack.add(t.id)
    let best = 0
    for (const n of t.needs ?? []) {
      const dep = byId.get(n)
      if (dep) best = Math.max(best, visit(dep, stack) + 1)
    }
    stack.delete(t.id)
    depth.set(t.id, best)
    return best
  }
  for (const t of tasks) visit(t, new Set())
  const layers: Task[][] = []
  for (const t of tasks) {
    const d = depth.get(t.id) ?? 0
    ;(layers[d] ??= []).push(t)
  }
  return layers.filter(Boolean)
}

const DIVERGENCE_HELP: Record<string, string> = {
  'trigger.unsupported-event': 'The workflow listens to an event Ore does not raise (release, issues, pull_request_target…); it will not run for it.',
  'trigger.schedule-unscheduled': 'Scheduled triggers are recorded but v0 does not fire them.',
  'job.reusable-workflow': 'Reusable workflows (jobs.<id>.uses) are skipped loudly; their dependents see them as skipped.',
  'job.services-unsupported': 'Service containers are not started.',
  'runs-on.unsupported-os': 'macOS and Windows runners are not available; the task is skipped.',
  'runs-on.labels-mapped': 'Self-hosted or custom labels were mapped to the default Linux image.',
  'uses.unsupported-action': 'A marketplace action without a native implementation; the step is skipped loudly at run time.',
  'uses.local-action': 'Local (./path) actions are skipped.',
  'uses.docker-action': 'docker:// actions are skipped.',
  'uses.unresolved': 'The action reference could not be resolved to a SHA because the resolver was unreachable; recorded as written.',
  'uses.input-unsupported': 'An input that changes semantics Ore cannot honour was ignored.',
  'cache.path-unsupported': 'A cache path with globs or escaping the workspace was skipped.',
  'secrets.github-token': 'secrets.GITHUB_TOKEN is injected empty.',
  'matrix.dynamic': 'A matrix built from an expression could not be expanded at plan time.',
  'job.concurrency-ignored': 'Concurrency groups are ignored; runs are sequential in v0.',
}

export function RunPage() {
  const { org, repo, id } = useParams({ from: '/$org/$repo/pipelines/$id' })
  const run = useRun(org, repo, id)
  const plan = useRunPlan(org, repo, id)
  const artifacts = useArtifacts(org, repo, id)
  const changes = useChanges(org, repo)
  const [selected, setSelected] = useState<string | null>(null)
  useDocumentTitle(`Run ${id.slice(0, 12)}`)
  const tasks = useMemo(() => tryJSON<Task[]>(run.data?.tasks, []), [run.data?.tasks])
  const layers = useMemo(() => layerTasks(tasks), [tasks])
  const sel = tasks.find((t) => t.id === selected) ?? null
  const finished = !!run.data && run.data.status !== 'running' && run.data.status !== 'queued'
  const ch = changes.data?.find((c) => c.id === run.data?.change_id)
  const divergences: Divergence[] = useMemo(() => {
    const out: Divergence[] = [...(plan.data?.divergences ?? [])]
    for (const t of tasks) for (const d of t.divergences ?? []) if (!out.some((x) => x.code === d.code && x.message === d.message)) out.push(d)
    return out
  }, [plan.data, tasks])

  return (
    <QueryState query={run} notFound={<NotFound what="pipeline run" />}>
      {(r) => (
        <div className="stack">
          <div className="page-head">
            <div>
              <h1>
                <StatusPill status={r.status} /> Run <span className="mono muted" style={{ fontWeight: 400 }}>{r.id.slice(0, 16)}</span>
              </h1>
              <div className="row sub">
                <span>
                  on <Link to="/$org/$repo/commit/$oid" params={{ org, repo, oid: r.commit_oid }} className="mono">{r.commit_oid.slice(0, 7)}</Link>
                </span>
                {ch ? (
                  <span>
                    · for change{' '}
                    <Link to="/$org/$repo/changes/$n" params={{ org, repo, n: String(ch.number) }} search={{}}>
                      #{ch.number} {ch.title}
                    </Link>
                  </span>
                ) : null}
                <span>
                  · queued <TimeAgo iso={r.created_at} />
                </span>
                {r.started_at ? <span>· ran {durationBetween(r.started_at, r.finished_at)}</span> : null}
              </div>
            </div>
          </div>

          {plan.data?.refused ? (
            <Callout tone="danger" icon={<Info />}>
              The planner refused this workflow: {String(plan.data.refused)}. Nothing ran.
            </Callout>
          ) : null}

          <Card tight title={`Plan · ${tasks.length} task${tasks.length === 1 ? '' : 's'} in ${layers.length} stage${layers.length === 1 ? '' : 's'}`} actions={<span className="small muted">{tasks.filter((t) => t.cache_hit).length} cache hits</span>}>
            {tasks.length === 0 ? (
              <p className="muted" style={{ padding: 'var(--sp-4)' }}>{r.status === 'queued' || r.status === 'running' ? 'Tasks appear here as the plan compiles.' : 'No tasks were recorded for this run.'}</p>
            ) : (
              <div className="dag" role="list" aria-label="Pipeline tasks">
                {layers.map((layer, li) => (
                  <div className="stage" key={li} role="listitem">
                    {layer.map((t) => {
                      const tone = t.status === 'succeeded' || t.status === 'success' ? 'ok' : t.status === 'failed' || t.status === 'failure' || t.status === 'error' || t.status === 'timeout' ? 'danger' : t.status === 'running' ? 'warn' : t.status === 'skipped' || t.status === 'skip' ? 'skip' : ''
                      return (
                        <button key={t.id} type="button" className={cx('task', tone)} aria-pressed={selected === t.id} onClick={() => setSelected(selected === t.id ? null : t.id)}>
                          <span className="row between nowrap">
                            <b className="truncate">{t.name ?? t.id}</b>
                            <StatusPill status={t.status ?? 'queued'} small />
                          </span>
                          <span className="meta">
                            {t.image ? <span className="truncate" style={{ maxWidth: 140 }}>{t.image}</span> : null}
                            {t.duration_ms !== undefined ? <span>{fmtDuration(t.duration_ms)}</span> : null}
                            {t.cache_hit ? <Pill small tone="info"><Database /> cache</Pill> : null}
                            {t.reason ? <span>{t.reason}</span> : null}
                            {t.matrix_key ? <span>{t.matrix_key}</span> : null}
                          </span>
                        </button>
                      )
                    })}
                  </div>
                ))}
              </div>
            )}
          </Card>

          {sel ? (
            <Card title={<>Task <code>{sel.name ?? sel.id}</code></>} actions={<StatusPill status={sel.status ?? 'queued'} small />}>
              <dl className="dl">
                {sel.content_hash ? (
                  <>
                    <dt>Content hash</dt>
                    <dd>
                      <code>{sel.content_hash.slice(0, 20)}…</code> <span className="muted small">— the same inputs, steps and image always hash the same; a future run with this hash can reuse the result.</span>
                    </dd>
                  </>
                ) : null}
                {sel.cache_hit !== undefined ? (
                  <>
                    <dt>Cache</dt>
                    <dd>{sel.cache_hit ? 'hit: restored from a trusted scope' : 'miss'}</dd>
                  </>
                ) : null}
                {sel.exit_code !== undefined ? (
                  <>
                    <dt>Exit code</dt>
                    <dd>{sel.exit_code}</dd>
                  </>
                ) : null}
                {sel.needs?.length ? (
                  <>
                    <dt>Needs</dt>
                    <dd>{sel.needs.join(', ')}</dd>
                  </>
                ) : null}
                {sel.reason ? (
                  <>
                    <dt>Reason</dt>
                    <dd>{sel.reason}</dd>
                  </>
                ) : null}
              </dl>
              {sel.steps?.length ? (
                <table className="table dense" style={{ marginTop: 'var(--sp-3)' }}>
                  <thead>
                    <tr>
                      <th>Step</th>
                      <th>Outcome</th>
                      <th>Conclusion</th>
                      <th className="num">Duration</th>
                      <th>Reason</th>
                    </tr>
                  </thead>
                  <tbody>
                    {sel.steps.map((s, i) => (
                      <tr key={i}>
                        <td>{s.name ?? `step ${i + 1}`}</td>
                        <td>
                          <StatusPill status={s.outcome ?? '—'} small />
                        </td>
                        <td>{s.conclusion ?? '—'}</td>
                        <td className="num">{fmtDuration(s.duration_ms)}</td>
                        <td className="muted">{s.reason ?? ''}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              ) : null}
              {sel.summary ? (
                <div style={{ marginTop: 'var(--sp-3)' }}>
                  <h4>Step summary</h4>
                  <Markdown source={sel.summary} />
                </div>
              ) : null}
            </Card>
          ) : null}

          {divergences.length ? (
            <Card title={`${divergences.length} divergence${divergences.length === 1 ? '' : 's'} from GitHub Actions`}>
              <p className="muted small">Everything the compiler could not map exactly is recorded here; nothing is skipped silently.</p>
              <ul className="stack" style={{ listStyle: 'none', margin: 'var(--sp-3) 0 0', padding: 0 }}>
                {divergences.map((d, i) => (
                  <li key={i} className="callout warn" style={{ display: 'block' }}>
                    <code>{d.code}</code> {d.message ? `— ${d.message}` : ''}
                    {DIVERGENCE_HELP[d.code] ? <div className="small" style={{ marginTop: 4 }}>{DIVERGENCE_HELP[d.code]}</div> : null}
                    {d.job || d.step || d.path ? <div className="small muted">{[d.path, d.job && `job ${d.job}`, d.step && `step ${d.step}`].filter(Boolean).join(' · ')}</div> : null}
                  </li>
                ))}
              </ul>
            </Card>
          ) : null}

          {artifacts.data && artifacts.data.length > 0 ? (
            <Card tight title={<><Archive aria-hidden /> Artifacts</>}>
              <div className="list">
                {artifacts.data.map((a) => (
                  <a key={a.name} className="list-row" href={`${repoBase(org, repo)}/runs/${encodeURIComponent(id)}/artifacts/${encodeURIComponent(a.name)}`}>
                    <Archive size={16} aria-hidden />
                    <span className="grow title">{a.name}</span>
                    <span className="meta">
                      {a.size !== undefined ? <span>{fmtBytes(a.size)}</span> : null}
                      {typeof a.files === 'number' ? <span>{a.files} files</span> : Array.isArray(a.files) ? <span>{a.files.length} files</span> : null}
                      {a.digest ? <code className="truncate" style={{ maxWidth: 140 }}>{a.digest}</code> : null}
                    </span>
                  </a>
                ))}
              </div>
            </Card>
          ) : null}

          <LogViewer source={{ streamUrl: `/api/v0/webapp/runs/${encodeURIComponent(id)}/log/stream`, rawUrl: `${repoBase(org, repo)}/runs/${encodeURIComponent(id)}/log` }} finished={finished} title="Pipeline log" />
          <details>
            <summary className="muted small">Reproduce locally</summary>
            <CopyBlock text={`ore run log ${org}/${repo} ${id}`} />
          </details>
        </div>
      )}
    </QueryState>
  )
}
