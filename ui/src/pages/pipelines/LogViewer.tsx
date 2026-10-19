import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { ArrowDownToLine, ChevronDown, ChevronUp, Download, Search, TriangleAlert } from 'lucide-react'
import { Button, Input, Pill, cx } from '../../design'
import { useHotkey } from '../../lib/keyboard'
import { connectSSE, type SSEStatus } from '../../lib/sse'

const FAILURE_RE = /\b(error|fail(ed|ure)?|panic|fatal|exit (status|code) [1-9]\d*|FAIL\b|Traceback|exception)\b/i

/** Finds the first line that looks like a failure; exported for tests. */
export function findFailure(lines: string[], from = 0): number {
  for (let i = from; i < lines.length; i++) {
    const l = lines[i]!
    // The runner echoes each task's command as "[task] start: …"; that line is not a failure.
    if (/\] start: /.test(l)) continue
    if (FAILURE_RE.test(l)) return i
  }
  return -1
}

/** Lines that match a case-insensitive needle; exported for tests. */
export function searchLines(lines: string[], needle: string): number[] {
  const n = needle.trim().toLowerCase()
  if (!n) return []
  const out: number[] = []
  for (let i = 0; i < lines.length; i++) if (lines[i]!.toLowerCase().includes(n)) out.push(i)
  return out
}

export type LogSource = {
  /** SSE endpoint producing "lines" batches (internal/webapp stream). */
  streamUrl: string
  /** Plain-text fallback when the stream is unavailable. */
  rawUrl: string
}

export function LogViewer({ source, finished, height = 520, title }: { source: LogSource; finished: boolean; height?: number; title?: string }) {
  const [lines, setLines] = useState<string[]>([])
  const [status, setStatus] = useState<SSEStatus | 'raw'>('connecting')
  const [follow, setFollow] = useState(true)
  const [q, setQ] = useState('')
  const [matchIdx, setMatchIdx] = useState(0)
  const [current, setCurrent] = useState<number | null>(null)
  const boxRef = useRef<HTMLDivElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)
  const linesRef = useRef<string[]>([])
  const doneRef = useRef(false)

  // Stream, with raw fallback and dedupe by line count.
  useEffect(() => {
    linesRef.current = []
    setLines([])
    doneRef.current = false
    let pending: string[] = []
    let raf: number | null = null
    const flush = () => {
      raf = null
      if (pending.length === 0) return
      linesRef.current = linesRef.current.concat(pending)
      pending = []
      setLines(linesRef.current)
    }
    const schedule = () => {
      if (raf === null) raf = requestAnimationFrame(flush)
    }
    const loadRaw = async () => {
      try {
        const res = await fetch(source.rawUrl, { credentials: 'same-origin' })
        const text = await res.text()
        const ls = text.split('\n')
        if (ls[ls.length - 1] === '') ls.pop()
        linesRef.current = ls
        setLines(ls)
        setStatus('raw')
      } catch {
        setStatus('closed')
      }
    }
    const ctl = connectSSE({
      url: source.streamUrl,
      lastEventId: null,
      isAfter: (id, last) => Number(id) > Number(last ?? -1),
      onMessage: (m) => {
        if (m.event === 'lines') {
          try {
            const batch = JSON.parse(m.data) as string[]
            pending.push(...batch)
            schedule()
          } catch {
            /* ignore */
          }
        } else if (m.event === 'done') {
          doneRef.current = true
          flush()
        }
      },
      onStatus: (s) => {
        if (s === 'unavailable') void loadRaw()
        else setStatus(s)
      },
      onEnd: () => setStatus('closed'),
      maxDelayMs: 5000,
    })
    return () => {
      ctl.close()
      if (raf !== null) cancelAnimationFrame(raf)
    }
  }, [source.streamUrl, source.rawUrl])

  // When a finished run's stream was opened before it finished, reconnects resume via Last-Event-ID (line count).
  const matches = useMemo(() => searchLines(lines, q), [lines, q])
  useEffect(() => setMatchIdx(0), [q])

  const rowV = useVirtualizer({ count: lines.length, getScrollElement: () => boxRef.current, estimateSize: () => 20, overscan: 30 })
  useEffect(() => {
    if (follow && lines.length > 0 && !finished) rowV.scrollToIndex(lines.length - 1, { align: 'end' })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [lines.length, follow, finished])

  const jumpTo = useCallback(
    (i: number) => {
      if (i < 0 || i >= lines.length) return
      setCurrent(i)
      setFollow(false)
      rowV.scrollToIndex(i, { align: 'center' })
    },
    [lines.length, rowV],
  )
  // Search as you type: scroll to the first match of a new query.
  const lastQuery = useRef('')
  useEffect(() => {
    if (q.trim() && q !== lastQuery.current && matches.length > 0) {
      lastQuery.current = q
      jumpTo(matches[0]!)
    }
    if (!q.trim()) lastQuery.current = ''
  }, [q, matches, jumpTo])

  const nextMatch = () => {
    if (matches.length === 0) return
    const n = (matchIdx + 1) % matches.length
    setMatchIdx(n)
    jumpTo(matches[n]!)
  }
  const prevMatch = () => {
    if (matches.length === 0) return
    const n = (matchIdx - 1 + matches.length) % matches.length
    setMatchIdx(n)
    jumpTo(matches[n]!)
  }
  const jumpFailure = () => {
    const i = findFailure(lines, current !== null ? current + 1 : 0)
    if (i >= 0) jumpTo(i)
    else {
      const first = findFailure(lines, 0)
      if (first >= 0) jumpTo(first)
    }
  }
  useHotkey('f', () => searchRef.current?.focus())
  useHotkey('e', jumpFailure)
  const failureAt = useMemo(() => findFailure(lines), [lines])

  const onScroll = () => {
    const el = boxRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40
    if (!atBottom && follow) setFollow(false)
    if (atBottom && !follow && !finished) setFollow(true)
  }

  return (
    <div className="logview" aria-label={title ?? 'Log'}>
      <div className="log-toolbar">
        <div className="input-with-icon" style={{ width: 260 }}>
          <Search aria-hidden />
          <Input ref={searchRef} small value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search log (f)" aria-label="Search log" onKeyDown={(e) => {
            if (e.key === 'Enter') { e.preventDefault(); if (e.shiftKey) prevMatch(); else nextMatch() }
            if (e.key === 'Escape') { setQ(''); (e.target as HTMLInputElement).blur() }
          }} />
        </div>
        {q ? (
          <span className="small muted num" aria-live="polite">
            {matches.length === 0 ? 'no matches' : `${matchIdx + 1} / ${matches.length}`}
          </span>
        ) : null}
        <Button size="sm" variant="ghost" icon onClick={prevMatch} aria-label="Previous match" disabled={matches.length === 0}>
          <ChevronUp />
        </Button>
        <Button size="sm" variant="ghost" icon onClick={nextMatch} aria-label="Next match" disabled={matches.length === 0}>
          <ChevronDown />
        </Button>
        <Button size="sm" onClick={jumpFailure} disabled={failureAt < 0} title="Jump to the first failure (e)">
          <TriangleAlert /> Jump to failure
        </Button>
        <span className="grow" />
        <Pill small>{lines.length.toLocaleString()} lines</Pill>
        <Pill small tone={status === 'open' ? 'ok' : status === 'reconnecting' ? 'warn' : 'neutral'} dot={status === 'open' && !finished} pulse={status === 'open' && !finished}>
          {finished ? 'complete' : status === 'open' ? 'streaming' : status}
        </Pill>
        <Button size="sm" variant="ghost" aria-pressed={follow} onClick={() => { setFollow((f) => !f); if (!follow) rowV.scrollToIndex(lines.length - 1) }} title="Follow the tail">
          <ArrowDownToLine /> Follow
        </Button>
        <a className="btn ghost sm" href={source.rawUrl} target="_blank" rel="noreferrer">
          <Download /> Raw
        </a>
      </div>
      <div className="log-body" ref={boxRef} style={{ height }} onScroll={onScroll} role="log" aria-live="off" tabIndex={0}>
        {lines.length === 0 ? <div className="muted" style={{ padding: 'var(--sp-3)' }}>{finished ? 'The log is empty.' : 'Waiting for output…'}</div> : null}
        <div style={{ height: rowV.getTotalSize(), position: 'relative' }}>
          {rowV.getVirtualItems().map((it) => {
            const text = lines[it.index]!
            const isMatch = q && matches.length > 0 && text.toLowerCase().includes(q.trim().toLowerCase())
            const isErr = failureAt === it.index
            return (
              <div key={it.key} className={cx('log-line', isMatch && 'match', isErr && 'err', current === it.index && 'current')} style={{ position: 'absolute', top: 0, left: 0, width: '100%', transform: `translateY(${it.start}px)` }} data-line={it.index + 1}>
                <span className="n">{it.index + 1}</span>
                <span>{isMatch ? highlight(text, q.trim()) : text}</span>
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}

function highlight(text: string, needle: string) {
  const idx = text.toLowerCase().indexOf(needle.toLowerCase())
  if (idx < 0) return text
  return (
    <>
      {text.slice(0, idx)}
      <mark>{text.slice(idx, idx + needle.length)}</mark>
      {text.slice(idx + needle.length)}
    </>
  )
}
