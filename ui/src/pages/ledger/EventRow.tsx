import type { ReactElement } from 'react'
import { Link } from '@tanstack/react-router'
import { Activity, Bot, CircleDot, GitBranch, GitMerge, GitPullRequest, KeyRound, Rocket, Shield, Workflow } from 'lucide-react'
import { ActorChip, cx } from '../../design'
import type { LedgerEvent } from '../../lib/types'
import { TimeAgo } from '../../shell/common'

/** Turns a CloudEvent into a sentence and the page it points at. */
export function describeEvent(e: LedgerEvent): { text: string; href: string; icon: ReactElement; tone?: 'ok' | 'danger' | 'warn' | 'agent' } {
  const parts = e.subject.split('/')
  const [org = '', repo = '', kind = '', id = ''] = parts
  const repoHref = org && repo ? `/${org}/${repo}` : '/'
  const d = (e.data ?? {}) as Record<string, unknown>
  const verb = e.type.replace(/^io\.ore\./, '')
  const human = verb.replace(/\./g, ' ').replace(/_/g, ' ')
  const title = typeof d.title === 'string' ? `: ${d.title}` : ''
  let href = repoHref
  let icon = <Activity />
  let tone: 'ok' | 'danger' | 'warn' | 'agent' | undefined
  if (verb.startsWith('issue') || verb.startsWith('comment')) {
    icon = <CircleDot />
    href = kind === 'issues' ? `${repoHref}/issues/${id}` : kind === 'changes' ? `${repoHref}/changes/${id}` : `${repoHref}/issues`
  } else if (verb.startsWith('change') || verb.startsWith('patchset') || verb.startsWith('review') || verb.startsWith('landing') || verb.startsWith('stack')) {
    icon = verb.includes('merged') || verb.includes('landed') ? <GitMerge /> : <GitPullRequest />
    href = kind === 'changes' ? `${repoHref}/changes/${id}` : `${repoHref}/changes`
    if (verb.includes('merged') || verb.includes('landed')) tone = 'ok'
    if (verb.includes('reverted') || verb.includes('failed')) tone = 'danger'
  } else if (verb.startsWith('pipeline') || verb.startsWith('artifact')) {
    icon = <Workflow />
    href = kind === 'runs' ? `${repoHref}/pipelines/${id}` : `${repoHref}/pipelines`
    const st = typeof d.status === 'string' ? d.status : ''
    if (verb.endsWith('finished')) tone = st === 'succeeded' ? 'ok' : 'danger'
  } else if (verb.startsWith('deployment') || verb.startsWith('environment')) {
    icon = <Rocket />
    href = kind === 'deployments' ? `${repoHref}/deployments/${id}` : `${repoHref}/deployments`
    if (verb.endsWith('succeeded')) tone = 'ok'
    if (verb.endsWith('failed') || verb.endsWith('rolled_back') || verb.endsWith('refused') || verb.endsWith('rejected')) tone = 'danger'
    if (verb.endsWith('awaiting_approval')) tone = 'warn'
  } else if (verb.startsWith('agent')) {
    icon = <Bot />
    href = kind === 'agent' ? `${repoHref}/agents/${id}` : `${repoHref}/agents`
    tone = verb.endsWith('failed') ? 'danger' : verb.endsWith('completed') ? 'ok' : 'agent'
  } else if (verb.startsWith('ref')) {
    icon = <GitBranch />
    href = `${repoHref}/commits/${encodeURIComponent(parts.slice(2).join('/'))}`
  } else if (verb.startsWith('identity') || verb.startsWith('secret')) {
    icon = <KeyRound />
    href = verb.startsWith('secret') ? `${repoHref}/settings/secrets` : '/settings'
  } else if (verb.startsWith('repository')) {
    icon = <Shield />
    href = `${repoHref}/settings`
  } else if (verb.startsWith('github')) {
    href = `${repoHref}/settings/github`
  }
  let subject = e.subject
  if (kind === 'refs') subject = `${org}/${repo} ${parts.slice(3).join('/')}`
  else if (kind && id) subject = `${org}/${repo} ${kind === 'agent' ? 'agent run' : kind.replace(/s$/, '')} ${kind === 'runs' || kind === 'agent' || kind === 'deployments' ? id.slice(0, 12) : `#${id}`}`
  const step = typeof d.step === 'string' && verb === 'agent.session.step' ? ` → ${d.step}` : ''
  return { text: `${human}${step} · ${subject}${title}`, href, icon, tone }
}

export function EventRow({ event, fresh, showRaw }: { event: LedgerEvent; fresh?: boolean; showRaw?: boolean }) {
  const d = describeEvent(event)
  return (
    <div className={cx('event-row', fresh && 'fresh')} data-seq={event.seq}>
      <span className={cx('avatar sm', d.tone === 'agent' && 'agent')} style={{ color: d.tone === 'ok' ? 'var(--ok)' : d.tone === 'danger' ? 'var(--danger)' : d.tone === 'warn' ? 'var(--warn)' : undefined }} aria-hidden>
        {d.icon}
      </span>
      <div className="what">
        <Link to={d.href as '/'} className="quiet truncate" title={d.text}>
          {d.text}
        </Link>
        <span className="type">
          {event.type}
          {showRaw ? ` · ${event.id}` : ''}
        </span>
      </div>
      <div className="when" style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: 2 }}>
        <TimeAgo iso={event.time} />
        <ActorChip actor={event.oreactor} size="sm" />
      </div>
    </div>
  )
}
