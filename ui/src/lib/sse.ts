// A server-sent-events client over fetch. EventSource cannot tell a 404 from
// a dropped connection, cannot send Last-Event-ID on the first request and
// cannot be aborted cleanly, so this does the parsing itself. It reconnects
// with exponential backoff, resumes from the last id, and never delivers an
// id it has already delivered (so a reconnect cannot duplicate events).

export type SSEMessage = { id: string | null; event: string; data: string }

export type SSEStatus = 'connecting' | 'open' | 'reconnecting' | 'closed' | 'unavailable' | 'unauthorized'

export type SSEOptions = {
  url: string
  lastEventId?: string | null
  onMessage: (m: SSEMessage) => void
  onStatus?: (s: SSEStatus, detail?: { attempt: number; delayMs?: number; status?: number }) => void
  /** Compare ids for duplicate suppression; defaults to numeric when both parse. */
  isAfter?: (id: string, last: string | null) => boolean
  fetchImpl?: typeof fetch
  minDelayMs?: number
  maxDelayMs?: number
  maxAttempts?: number
  /** Resets the backoff after a connection stayed open this long. */
  healthyAfterMs?: number
  /** Called when the server closes the stream normally (e.g. a finished log). */
  onEnd?: () => void
}

export type SSEController = { close: () => void; readonly lastEventId: string | null; readonly status: SSEStatus }

function defaultIsAfter(id: string, last: string | null): boolean {
  if (last === null) return true
  const a = Number(id)
  const b = Number(last)
  if (Number.isFinite(a) && Number.isFinite(b)) return a > b
  return id !== last
}

/** Parses SSE framing from a chunked text stream. Exported for tests. */
export class SSEParser {
  private buf = ''
  private id: string | null = null
  private event = ''
  private data: string[] = []
  constructor(private emit: (m: SSEMessage) => void) {}

  push(chunk: string) {
    this.buf += chunk
    let idx: number
    // Lines end in \n, \r\n or \r.
    while ((idx = this.buf.search(/\r\n|\n|\r/)) >= 0) {
      const line = this.buf.slice(0, idx)
      const sepLen = this.buf.startsWith('\r\n', idx) ? 2 : 1
      this.buf = this.buf.slice(idx + sepLen)
      this.line(line)
    }
  }

  private line(line: string) {
    if (line === '') {
      if (this.data.length > 0 || this.event !== '') {
        this.emit({ id: this.id, event: this.event || 'message', data: this.data.join('\n') })
      }
      this.event = ''
      this.data = []
      return
    }
    if (line.startsWith(':')) return
    const colon = line.indexOf(':')
    const field = colon < 0 ? line : line.slice(0, colon)
    let value = colon < 0 ? '' : line.slice(colon + 1)
    if (value.startsWith(' ')) value = value.slice(1)
    switch (field) {
      case 'id':
        if (!value.includes('\0')) this.id = value
        break
      case 'event':
        this.event = value
        break
      case 'data':
        this.data.push(value)
        break
      default:
        break
    }
  }
}

/** Backoff schedule, exported for tests: 500ms, 1s, 2s, … capped. */
export function backoffDelay(attempt: number, min = 500, max = 10_000): number {
  const d = Math.min(max, min * 2 ** Math.max(0, attempt - 1))
  const jitter = d * 0.2 * (Math.random() * 2 - 1)
  return Math.round(d + jitter)
}

export function connectSSE(opts: SSEOptions): SSEController {
  const fetchImpl = opts.fetchImpl ?? ((input, init) => fetch(input, init))
  const isAfter = opts.isAfter ?? defaultIsAfter
  let lastEventId: string | null = opts.lastEventId ?? null
  let status: SSEStatus = 'connecting'
  let attempt = 0
  let closed = false
  let abort: AbortController | null = null
  let timer: ReturnType<typeof setTimeout> | null = null

  const setStatus = (s: SSEStatus, detail?: { attempt: number; delayMs?: number; status?: number }) => {
    status = s
    opts.onStatus?.(s, detail)
  }

  const schedule = () => {
    if (closed) return
    attempt++
    if (opts.maxAttempts && attempt > opts.maxAttempts) {
      setStatus('closed', { attempt })
      return
    }
    const delayMs = backoffDelay(attempt, opts.minDelayMs, opts.maxDelayMs)
    setStatus('reconnecting', { attempt, delayMs })
    timer = setTimeout(run, delayMs)
  }

  const run = async () => {
    if (closed) return
    abort = new AbortController()
    const headers: Record<string, string> = { Accept: 'text/event-stream' }
    if (lastEventId !== null) headers['Last-Event-ID'] = lastEventId
    let res: Response
    try {
      res = await fetchImpl(opts.url, { headers, credentials: 'same-origin', signal: abort.signal, cache: 'no-store' })
    } catch (e) {
      if (closed) return
      schedule()
      return
    }
    if (closed) return
    if (res.status === 404 || res.status === 501 || res.status === 405) {
      setStatus('unavailable', { attempt, status: res.status })
      return
    }
    if (res.status === 401 || res.status === 403) {
      setStatus('unauthorized', { attempt, status: res.status })
      return
    }
    if (!res.ok || !res.body) {
      schedule()
      return
    }
    const openedAt = Date.now()
    setStatus('open', { attempt })
    const parser = new SSEParser((m) => {
      if (m.id !== null) {
        if (!isAfter(m.id, lastEventId)) return // duplicate after a reconnect
        lastEventId = m.id
      }
      opts.onMessage(m)
    })
    const reader = res.body.getReader()
    const decoder = new TextDecoder()
    let endedCleanly = false
    try {
      for (;;) {
        const { value, done } = await reader.read()
        if (done) {
          endedCleanly = true
          break
        }
        parser.push(decoder.decode(value, { stream: true }))
      }
    } catch {
      /* dropped */
    }
    if (closed) return
    if (Date.now() - openedAt > (opts.healthyAfterMs ?? 5000)) attempt = 0
    if (endedCleanly && opts.onEnd) {
      setStatus('closed', { attempt })
      opts.onEnd()
      return
    }
    schedule()
  }

  void run()

  return {
    close() {
      closed = true
      if (timer) clearTimeout(timer)
      abort?.abort()
      setStatus('closed', { attempt })
    },
    get lastEventId() {
      return lastEventId
    },
    get status() {
      return status
    },
  }
}
