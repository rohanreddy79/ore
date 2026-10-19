import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { Activity, ChevronDown, ChevronRight, Pause, Play, Search } from 'lucide-react'
import { Button, Card, EmptyState, Input, Pill, Select, SkeletonRows, cx } from '../../design'
import { useLedgerLive } from '../../lib/live'
import { useEvents } from '../../lib/queries'
import type { LedgerEvent } from '../../lib/types'
import { useDocumentTitle } from '../../shell/common'
import { EventRow } from './EventRow'

const TYPE_GROUPS = ['', 'io.ore.issue.', 'io.ore.change.', 'io.ore.comment.', 'io.ore.review.', 'io.ore.landing.', 'io.ore.pipeline.', 'io.ore.deployment.', 'io.ore.environment.', 'io.ore.agent.', 'io.ore.ref.', 'io.ore.repository.', 'io.ore.identity.', 'io.ore.github.', 'io.ore.secret.']

/** The event explorer: filters, a replay cursor, live tail, raw CloudEvents JSON. */
export function LedgerPage() {
  const params = useParams({ strict: false }) as { org?: string; repo?: string }
  const search = useSearch({ strict: false }) as { type?: string; subject?: string; after?: number }
  const navigate = useNavigate()
  const scoped = params.org && params.repo ? `${params.org}/${params.repo}` : undefined
  useDocumentTitle(scoped ? `Ledger · ${scoped}` : 'Ledger')
  const [type, setType] = useState(search.type ?? '')
  const [subject, setSubject] = useState(search.subject ?? '')
  const [after, setAfter] = useState<number>(search.after ?? 0)
  const [live, setLive] = useState(true)
  const [liveEvents, setLiveEvents] = useState<LedgerEvent[]>([])
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [q, setQ] = useState('')
  const seen = useRef(new Set<number>())

  useEffect(() => {
    void navigate({ to: '.', search: { type: type || undefined, subject: subject || undefined, after: after || undefined } as never, replace: true })
  }, [type, subject, after, navigate])

  const query = useEvents({ type: type || undefined, subject: subject || undefined, after, limit: 1000 })
  useLedgerLive({
    subjectPrefix: scoped ? scoped + '/' : undefined,
    type: type || undefined,
    enabled: live,
    onEvent: (e) => {
      if (subject && e.subject !== subject) return
      if (e.seq && seen.current.has(e.seq)) return
      if (e.seq) seen.current.add(e.seq)
      setLiveEvents((l) => [...l, e].slice(-500))
    },
  })

  const rows = useMemo(() => {
    const base = (query.data ?? []).filter((e) => (!scoped || e.subject === scoped || e.subject.startsWith(scoped + '/')) && (!subject || e.subject === subject))
    const ids = new Set(base.map((e) => e.id))
    const merged = [...base, ...liveEvents.filter((e) => !ids.has(e.id))]
    const needle = q.trim().toLowerCase()
    const filtered = needle ? merged.filter((e) => e.type.includes(needle) || e.subject.toLowerCase().includes(needle) || JSON.stringify(e.data).toLowerCase().includes(needle)) : merged
    return filtered.slice().reverse()
  }, [query.data, liveEvents, scoped, subject, q])

  const toggle = (id: string) =>
    setExpanded((s) => {
      const n = new Set(s)
      if (n.has(id)) n.delete(id)
      else n.add(id)
      return n
    })

  const lastSeq = rows[0]?.seq ?? 0

  return (
    <div className={scoped ? '' : 'container page'}>
      <div className="page-head">
        <div>
          <h1>
            <Activity aria-hidden /> Ledger
          </h1>
          <p className="sub">Every entity change is a CloudEvent written in the same transaction. Filter, replay from a cursor, inspect the raw JSON.</p>
        </div>
        <div className="row">
          <Button size="sm" aria-pressed={live} onClick={() => setLive((l) => !l)} title={live ? 'Pause the live tail' : 'Resume the live tail'}>
            {live ? <Pause /> : <Play />} {live ? 'Live' : 'Paused'}
          </Button>
        </div>
      </div>
      <Card tight>
        <div className="filterbar">
          <label className="sr-only" htmlFor="ledger-type">
            Event type
          </label>
          <Select id="ledger-type" small value={type} onChange={(e) => setType(e.target.value)} style={{ width: 200 }}>
            {TYPE_GROUPS.map((t) => (
              <option key={t} value={t}>
                {t ? t.replace('io.ore.', '').replace(/\.$/, '') + '.*' : 'All types'}
              </option>
            ))}
          </Select>
          <label className="sr-only" htmlFor="ledger-subject">
            Subject
          </label>
          <Input id="ledger-subject" small placeholder={scoped ? `${scoped}/issues/1` : 'subject (org/repo/…)'} value={subject} onChange={(e) => setSubject(e.target.value)} style={{ width: 240 }} />
          <label className="sr-only" htmlFor="ledger-after">
            Replay after sequence
          </label>
          <div className="row nowrap" title="Replay cursor: show events after this sequence number">
            <span className="small muted">after seq</span>
            <Input id="ledger-after" small type="number" min={0} value={after} onChange={(e) => setAfter(Number(e.target.value) || 0)} style={{ width: 96 }} />
          </div>
          <div className="input-with-icon">
            <Search aria-hidden />
            <Input small placeholder="Filter loaded events" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Filter loaded events" />
          </div>
          <Pill small>
            {rows.length} shown · head {lastSeq}
          </Pill>
        </div>
        {query.isPending ? (
          <SkeletonRows rows={8} />
        ) : rows.length === 0 ? (
          <EmptyState icon={<Activity />} title="No events match" compact>
            {after ? 'Nothing after this cursor yet. New events appear here live.' : 'Nothing on the ledger for this filter yet.'}
          </EmptyState>
        ) : (
          <div className="list" aria-live={live ? 'polite' : 'off'}>
            {rows.map((e) => (
              <div key={e.id}>
                <div className="row nowrap" style={{ gap: 0 }}>
                  <button type="button" className="btn ghost icon sm" aria-expanded={expanded.has(e.id)} aria-label={expanded.has(e.id) ? 'Hide raw event' : 'Show raw event'} onClick={() => toggle(e.id)} style={{ marginLeft: 6 }}>
                    {expanded.has(e.id) ? <ChevronDown /> : <ChevronRight />}
                  </button>
                  <div className="grow">
                    <EventRow event={e} fresh={liveEvents.some((l) => l.id === e.id)} showRaw />
                  </div>
                  <span className="subtle small num" style={{ paddingRight: 'var(--sp-3)' }}>
                    #{e.seq}
                  </span>
                </div>
                {expanded.has(e.id) ? (
                  <pre className={cx('codeblock')} style={{ margin: '0 var(--sp-4) var(--sp-3) 44px', maxHeight: 360 }}>
                    {JSON.stringify(e, null, 2)}
                  </pre>
                ) : null}
              </div>
            ))}
          </div>
        )}
      </Card>
    </div>
  )
}
