import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SSEParser, backoffDelay, connectSSE, type SSEMessage } from './sse'

function streamOf(chunks: string[], opts: { hang?: boolean; abortSignal?: AbortSignal } = {}): ReadableStream<Uint8Array> {
  const enc = new TextEncoder()
  return new ReadableStream({
    start(controller) {
      for (const c of chunks) controller.enqueue(enc.encode(c))
      if (!opts.hang) controller.close()
      else opts.abortSignal?.addEventListener('abort', () => controller.error(new Error('aborted')))
    },
  })
}

describe('SSEParser', () => {
  it('parses multi-line data, ids, events and comments across chunks', () => {
    const out: SSEMessage[] = []
    const p = new SSEParser((m) => out.push(m))
    p.push(': hello\n\nid: 1\nevent: lines\ndata: a\nda')
    p.push('ta: b\n\nid: 2\r\ndata: {"x":1}\r\n\r\ndata: no id\n\n')
    expect(out).toEqual([
      { id: '1', event: 'lines', data: 'a\nb' },
      { id: '2', event: 'message', data: '{"x":1}' },
      { id: '2', event: 'message', data: 'no id' },
    ])
  })
  it('ignores empty frames and unknown fields', () => {
    const out: SSEMessage[] = []
    const p = new SSEParser((m) => out.push(m))
    p.push('\n\nfoo: bar\n\nretry: 1000\n\ndata\n\n')
    expect(out).toEqual([{ id: null, event: 'message', data: '' }])
  })
})

describe('backoffDelay', () => {
  it('doubles from the minimum and caps', () => {
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
    expect(backoffDelay(1, 500, 10_000)).toBe(500)
    expect(backoffDelay(2, 500, 10_000)).toBe(1000)
    expect(backoffDelay(3, 500, 10_000)).toBe(2000)
    expect(backoffDelay(10, 500, 10_000)).toBe(10_000)
    vi.restoreAllMocks()
  })
})

describe('connectSSE', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('reports unavailable on 404 and does not retry', async () => {
    const fetchImpl = vi.fn(async () => new Response('nope', { status: 404 }))
    const statuses: string[] = []
    connectSSE({ url: '/s', onMessage: () => {}, onStatus: (s) => statuses.push(s), fetchImpl: fetchImpl as unknown as typeof fetch })
    await vi.advanceTimersByTimeAsync(10)
    expect(statuses).toEqual(['unavailable'])
    await vi.advanceTimersByTimeAsync(60_000)
    expect(fetchImpl).toHaveBeenCalledTimes(1)
  })

  it('reports unauthorized on 401', async () => {
    const fetchImpl = vi.fn(async () => new Response('', { status: 401 }))
    const statuses: string[] = []
    connectSSE({ url: '/s', onMessage: () => {}, onStatus: (s) => statuses.push(s), fetchImpl: fetchImpl as unknown as typeof fetch })
    await vi.advanceTimersByTimeAsync(10)
    expect(statuses).toEqual(['unauthorized'])
  })

  it('reconnects after a drop, resumes from the last id and never duplicates', async () => {
    const calls: Array<Record<string, string>> = []
    let n = 0
    const fetchImpl = vi.fn(async (_url: string, init: RequestInit) => {
      calls.push(Object.fromEntries(Object.entries((init.headers ?? {}) as Record<string, string>)))
      n++
      if (n === 1) {
        // first connection delivers 1 and 2 then drops (stream errors)
        const body = new ReadableStream<Uint8Array>({
          start(c) {
            c.enqueue(new TextEncoder().encode('id: 1\ndata: one\n\nid: 2\ndata: two\n\n'))
            setTimeout(() => c.error(new Error('drop')), 5)
          },
        })
        return new Response(body, { status: 200 })
      }
      // the server replays 2 (a race) and then 3
      return new Response(streamOf(['id: 2\ndata: two-again\n\nid: 3\ndata: three\n\n'], { hang: true, abortSignal: init.signal as AbortSignal }), { status: 200 })
    })
    const got: string[] = []
    const statuses: string[] = []
    const ctl = connectSSE({ url: '/s', onMessage: (m) => got.push(m.data), onStatus: (s) => statuses.push(s), fetchImpl: fetchImpl as unknown as typeof fetch, minDelayMs: 100, maxDelayMs: 100 })
    await vi.advanceTimersByTimeAsync(20)
    expect(got).toEqual(['one', 'two'])
    expect(statuses).toContain('reconnecting')
    await vi.advanceTimersByTimeAsync(200)
    expect(fetchImpl).toHaveBeenCalledTimes(2)
    expect(calls[1]!['Last-Event-ID']).toBe('2')
    await vi.advanceTimersByTimeAsync(20)
    expect(got).toEqual(['one', 'two', 'three'])
    expect(ctl.lastEventId).toBe('3')
    ctl.close()
    expect(ctl.status).toBe('closed')
  })

  it('backs off exponentially on repeated failures and stops after close', async () => {
    const fetchImpl = vi.fn(async () => {
      throw new Error('network')
    })
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
    const delays: number[] = []
    const ctl = connectSSE({ url: '/s', onMessage: () => {}, onStatus: (s, d) => s === 'reconnecting' && delays.push(d?.delayMs ?? 0), fetchImpl: fetchImpl as unknown as typeof fetch, minDelayMs: 100, maxDelayMs: 1000 })
    await vi.advanceTimersByTimeAsync(5)
    await vi.advanceTimersByTimeAsync(100)
    await vi.advanceTimersByTimeAsync(200)
    await vi.advanceTimersByTimeAsync(400)
    expect(delays.slice(0, 4)).toEqual([100, 200, 400, 800])
    const before = fetchImpl.mock.calls.length
    ctl.close()
    await vi.advanceTimersByTimeAsync(10_000)
    expect(fetchImpl.mock.calls.length).toBe(before)
  })

  it('calls onEnd when the server closes the stream cleanly', async () => {
    const fetchImpl = vi.fn(async () => new Response(streamOf(['event: done\ndata: {}\n\n']), { status: 200 }))
    const onEnd = vi.fn()
    const events: string[] = []
    connectSSE({ url: '/s', onMessage: (m) => events.push(m.event), onEnd, fetchImpl: fetchImpl as unknown as typeof fetch })
    await vi.advanceTimersByTimeAsync(20)
    expect(events).toEqual(['done'])
    expect(onEnd).toHaveBeenCalledTimes(1)
    expect(fetchImpl).toHaveBeenCalledTimes(1)
  })
})
