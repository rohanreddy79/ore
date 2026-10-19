import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { connectSSE, type SSEStatus } from './sse'
import type { LedgerEvent } from './types'

export type LiveStatus = SSEStatus | 'polling'

const STREAM = '/api/v0/webapp/events/stream'

/**
 * Subscribes to the ledger. Every event invalidates the query families it
 * touches, so lists and details refresh without the page polling. When the
 * stream is unavailable the hook falls back to polling the REST endpoint.
 */
export function useLedgerLive(opts: { subjectPrefix?: string; type?: string; onEvent?: (e: LedgerEvent) => void; enabled?: boolean } = {}): LiveStatus {
  const qc = useQueryClient()
  const [status, setStatus] = useState<LiveStatus>('connecting')
  const onEvent = useRef(opts.onEvent)
  onEvent.current = opts.onEvent
  const enabled = opts.enabled ?? true
  const { subjectPrefix, type } = opts

  useEffect(() => {
    if (!enabled) return
    const p = new URLSearchParams()
    if (subjectPrefix) p.set('subject_prefix', subjectPrefix)
    if (type) p.set('type', type)
    let poll: ReturnType<typeof setInterval> | null = null
    let lastSeq = 0
    const handle = (e: LedgerEvent) => {
      if (e.seq && e.seq <= lastSeq) return
      if (e.seq) lastSeq = e.seq
      invalidateFor(qc, e)
      onEvent.current?.(e)
    }
    const startPolling = () => {
      if (poll) return
      setStatus('polling')
      const tick = async () => {
        try {
          const res = await fetch(`/api/v0/events?after=${lastSeq}&limit=200${type ? `&type=${encodeURIComponent(type)}` : ''}`, { credentials: 'same-origin' })
          if (!res.ok) return
          const evs = (await res.json()) as LedgerEvent[] | null
          for (const e of evs ?? []) {
            if (subjectPrefix && !e.subject.startsWith(subjectPrefix)) {
              if (e.seq) lastSeq = Math.max(lastSeq, e.seq)
              continue
            }
            handle(e)
          }
        } catch {
          /* next tick */
        }
      }
      // Seed the cursor so polling starts "from now".
      fetch('/api/v0/events?limit=1000', { credentials: 'same-origin' })
        .then((r) => (r.ok ? r.json() : []))
        .then((evs: LedgerEvent[] | null) => {
          for (const e of evs ?? []) if (e.seq) lastSeq = Math.max(lastSeq, e.seq)
        })
        .finally(() => {
          poll = setInterval(tick, 3000)
        })
    }
    const ctl = connectSSE({
      url: `${STREAM}?${p}`,
      onMessage: (m) => {
        if (m.event !== 'message') return
        try {
          handle(JSON.parse(m.data) as LedgerEvent)
        } catch {
          /* malformed */
        }
      },
      onStatus: (s) => {
        if (s === 'unavailable') startPolling()
        else if (s !== 'closed') setStatus(s)
      },
    })
    return () => {
      ctl.close()
      if (poll) clearInterval(poll)
    }
  }, [qc, subjectPrefix, type, enabled])

  return status
}

/** Maps a CloudEvent to the query families that must refetch. */
export function invalidateFor(qc: { invalidateQueries: (o: { queryKey: readonly unknown[] }) => unknown }, e: LedgerEvent) {
  const parts = e.subject.split('/')
  const org = parts[0] ?? ''
  const repo = parts[1] ?? ''
  const t = e.type
  const inv = (...key: unknown[]) => void qc.invalidateQueries({ queryKey: key })
  inv('events')
  if (t.startsWith('io.ore.repository.') || t.startsWith('io.ore.org.')) {
    inv('repos')
    inv('orgs')
    if (org && repo) inv('repo', org, repo)
    return
  }
  if (!org || !repo) {
    if (t.startsWith('io.ore.identity.')) inv('identity')
    return
  }
  if (t.startsWith('io.ore.issue.') || t.startsWith('io.ore.comment.')) inv('repo', org, repo, 'issues')
  if (t.startsWith('io.ore.change.') || t.startsWith('io.ore.patchset.') || t.startsWith('io.ore.comment.') || t.startsWith('io.ore.review.') || t.startsWith('io.ore.landing.') || t.startsWith('io.ore.stack.')) {
    inv('repo', org, repo, 'changes')
    inv('repo', org, repo, 'stacks')
  }
  if (t.startsWith('io.ore.ref.')) {
    inv('repo', org, repo, 'refs')
    inv('repo', org, repo, 'log')
    inv('repo', org, repo, 'tree')
  }
  if (t.startsWith('io.ore.pipeline.') || t.startsWith('io.ore.artifact.')) inv('repo', org, repo, 'runs')
  if (t.startsWith('io.ore.deployment.') || t.startsWith('io.ore.environment.')) {
    inv('repo', org, repo, 'deployments')
    inv('repo', org, repo, 'environments')
    inv('repo', org, repo, 'dora')
  }
  if (t.startsWith('io.ore.agent.')) {
    inv('repo', org, repo, 'agent')
    inv('repo', org, repo, 'fleet')
  }
  if (t.startsWith('io.ore.secret.')) inv('repo', org, repo, 'secrets')
  if (t.startsWith('io.ore.github.')) inv('repo', org, repo, 'github')
  if (t.startsWith('io.ore.identity.')) inv('identity')
}
