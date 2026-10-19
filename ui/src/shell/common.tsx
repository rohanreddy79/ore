import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { Link } from '@tanstack/react-router'
import { AlertTriangle, Check, ChevronDown, CircleDot, Clock, GitBranch, Tag, X } from 'lucide-react'
import { Button, Callout, EmptyState, Menu, Pill, SkeletonRows, Spinner } from '../design'
import { ApiError } from '../lib/api'
import { LEVEL_EXPLANATIONS, fmtDate, levelName, refName, timeAgo } from '../lib/format'
import { useFeatures } from '../lib/queries'
import type { AgentRun } from '../lib/types'

type MarkdownModule = typeof import('../lib/markdown')
let markdownModule: MarkdownModule | null = null
const markdownLoading = () => import('../lib/markdown').then((m) => (markdownModule = m))

/** Renders sanitized Markdown. The renderer (marked + DOMPurify) loads on first use so the shell stays small. */
export function Markdown({ source, className }: { source: string | undefined | null; className?: string }) {
  const [mod, setMod] = useState<MarkdownModule | null>(markdownModule)
  useEffect(() => {
    if (!mod) void markdownLoading().then(setMod)
  }, [mod])
  const html = useMemo(() => (mod ? mod.renderMarkdown(source) : ''), [mod, source])
  if (!source?.trim()) return <p className="muted">No description provided.</p>
  if (!mod) return <div className={`prose ${className ?? ''}`} aria-busy style={{ minHeight: 24 }} />
  return <div className={`prose ${className ?? ''}`} dangerouslySetInnerHTML={{ __html: html }} />
}

/** Relative time with the absolute time on hover; re-renders every minute. */
export function TimeAgo({ iso, prefix }: { iso: string | undefined | null; prefix?: string }) {
  const [, tick] = useState(0)
  useEffect(() => {
    const t = setInterval(() => tick((n) => n + 1), 60_000)
    return () => clearInterval(t)
  }, [])
  if (!iso) return null
  return (
    <time dateTime={iso} title={fmtDate(iso)}>
      {prefix ? `${prefix} ` : ''}
      {timeAgo(iso)}
    </time>
  )
}

/** Autonomy level badge with the level's meaning on hover. */
export function LevelBadge({ level, compact }: { level: number; compact?: boolean }) {
  const { data: f } = useFeatures()
  const name = levelName(f?.levels, level)
  const ex = LEVEL_EXPLANATIONS[level]
  return (
    <span className={`level l${level}`} title={ex ? `L${level} ${ex.title}: agents ${ex.agents}; humans ${ex.humans}.` : undefined}>
      <span className="n">{level}</span>
      {!compact ? <span style={{ textTransform: 'capitalize' }}>{name}</span> : null}
    </span>
  )
}

export function LevelExplainer({ level }: { level: number }) {
  const ex = LEVEL_EXPLANATIONS[level]
  if (!ex) return null
  return (
    <Callout tone={level >= 4 ? 'agent' : 'info'} icon={<CircleDot />}>
      <b>
        L{level} {ex.title}.
      </b>{' '}
      Agents may {ex.agents}. Humans {ex.humans}.
      {level >= 3 ? ' Landing happens without a click when verification is green.' : ' Nothing lands without a human.'}
    </Callout>
  )
}

export const LOOP_STEPS = ['signal', 'plan', 'change', 'verify', 'land', 'deploy', 'observe'] as const

/** The loop, visualized: done, current, failed and skipped steps. */
export function LoopSteps({ run }: { run: Pick<AgentRun, 'status' | 'step' | 'error'> }) {
  const current = run.step === 'awaiting-human' ? 'land' : run.step
  const idx = LOOP_STEPS.indexOf(current as (typeof LOOP_STEPS)[number])
  return (
    <div className="loop" aria-label={`Loop progress: ${run.step}`}>
      {LOOP_STEPS.map((s, i) => {
        let cls = ''
        if (run.status === 'completed') cls = i <= idx || idx < 0 ? 'done' : 'skipped'
        else if (run.status === 'failed') cls = i < idx ? 'done' : i === idx ? 'failed' : 'skipped'
        else if (run.status === 'blocked' || run.step === 'awaiting-human') cls = i < idx ? 'done' : i === idx ? 'current' : ''
        else cls = i < idx ? 'done' : i === idx ? 'current' : ''
        return (
          <span key={s} style={{ display: 'contents' }}>
            {i > 0 ? (
              <span className="arrow" aria-hidden>
                →
              </span>
            ) : null}
            <span className={`step ${cls}`}>
              {cls === 'done' ? <Check size={12} /> : cls === 'failed' ? <X size={12} /> : cls === 'current' ? <Clock size={12} /> : null}
              {s === 'land' && run.step === 'awaiting-human' ? 'awaiting human' : s}
            </span>
          </span>
        )
      })}
    </div>
  )
}

/** Data-state wrapper: loading, friendly 404, 5xx with retry, then content. */
export function QueryState<T>({ query, children, notFound, skeleton, loadingRows = 4 }: { query: { data: T | undefined; isPending: boolean; isError: boolean; error: unknown; refetch: () => unknown }; children: (data: T) => ReactNode; notFound?: ReactNode; skeleton?: ReactNode; loadingRows?: number }) {
  if (query.isPending) return <>{skeleton ?? <SkeletonRows rows={loadingRows} />}</>
  if (query.isError) {
    const e = query.error
    if (e instanceof ApiError && e.status === 404) {
      return <>{notFound ?? <EmptyState icon={<AlertTriangle />} title="Not found" compact>This does not exist or you cannot see it.</EmptyState>}</>
    }
    return (
      <EmptyState icon={<AlertTriangle />} title="Something went wrong" compact actions={<Button onClick={() => query.refetch()}>Retry</Button>}>
        {e instanceof Error ? e.message : 'The request failed.'}
      </EmptyState>
    )
  }
  if (query.data === undefined) return <Spinner />
  return <>{children(query.data as T)}</>
}

/** A ref switcher listing branches and tags from GET /refs. */
export function RefPicker({ refs, current, onPick, label = 'Switch branch or tag' }: { refs: Record<string, string> | undefined; current: string; onPick: (ref: string) => void; label?: string }) {
  const branches = Object.keys(refs ?? {})
    .filter((r) => r.startsWith('refs/heads/'))
    .sort()
  const tags = Object.keys(refs ?? {})
    .filter((r) => r.startsWith('refs/tags/'))
    .sort()
  const other = Object.keys(refs ?? {})
    .filter((r) => r.startsWith('refs/github/'))
    .sort()
  const isTag = current.startsWith('refs/tags/')
  const items = [
    { kind: 'label' as const, label: `Branches (${branches.length})` },
    ...branches.map((r) => ({ label: refName(r), icon: <GitBranch />, onSelect: () => onPick(r) })),
    ...(tags.length ? [{ kind: 'label' as const, label: `Tags (${tags.length})` }, ...tags.map((r) => ({ label: refName(r), icon: <Tag />, onSelect: () => onPick(r) }))] : []),
    ...(other.length ? [{ kind: 'label' as const, label: 'GitHub mirror' }, ...other.slice(0, 20).map((r) => ({ label: r.replace('refs/github/', ''), icon: <GitBranch />, onSelect: () => onPick(r) }))] : []),
  ]
  return (
    <Menu
      label={label}
      items={items}
      trigger={(p) => (
        <Button size="sm" onClick={p.toggle} aria-haspopup={p['aria-haspopup']} aria-expanded={p['aria-expanded']} aria-label={label}>
          {isTag ? <Tag /> : <GitBranch />}
          <span className="truncate" style={{ maxWidth: 160 }}>
            {refName(current) || current.slice(0, 7)}
          </span>
          <ChevronDown />
        </Button>
      )}
    />
  )
}

export function StateFilter<T extends string>({ value, options, onChange }: { value: T; options: Array<{ value: T; label: string; count?: number }>; onChange: (v: T) => void }) {
  return (
    <div className="btn-group" role="group" aria-label="Filter by state">
      {options.map((o) => (
        <Button key={o.value} size="sm" aria-pressed={o.value === value} onClick={() => onChange(o.value)}>
          {o.label}
          {typeof o.count === 'number' ? <Pill small>{o.count}</Pill> : null}
        </Button>
      ))}
    </div>
  )
}

export function RepoLink({ org, repo, children }: { org: string; repo: string; children?: ReactNode }) {
  return (
    <Link to="/$org/$repo" params={{ org, repo }} className="quiet">
      {children ?? `${org}/${repo}`}
    </Link>
  )
}

/** Hints shown where a sibling slice's API is absent. */
export function FeatureHint({ feature, what }: { feature: string; what: string }) {
  return (
    <Callout icon={<AlertTriangle />}>
      {what} needs the <code>{feature}</code> extension, which this server does not run. Build the binary with that slice merged to enable it.
    </Callout>
  )
}

export function useDocumentTitle(title: string) {
  useEffect(() => {
    const prev = document.title
    document.title = title ? `${title} · Ore` : 'Ore'
    return () => {
      document.title = prev
    }
  }, [title])
}
