import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { fetchTree, scanTree, treeDirKey } from '../../lib/queries'
import { DirListing, TreeNav, fuzzyScore } from './tree'

// A synthetic 100,000-file repository served through the tree contract:
// depth=1&path= lists one directory; the recursive listing is paged with
// limit/offset and reports X-Ore-Tree-Total / X-Ore-Tree-Truncated.
const DIRS = 100
const FILES_PER_DIR = 1000

function children(path: string): string[] {
  if (path === '') return [...Array.from({ length: DIRS }, (_, d) => `pkg${String(d).padStart(3, '0')}/`), 'README.md', 'go.mod']
  const m = path.match(/^pkg(\d{3})$/)
  if (!m) return []
  return Array.from({ length: FILES_PER_DIR }, (_, f) => `${path}/file${String(f).padStart(4, '0')}.go`)
}

function* recursive(): Generator<string> {
  for (let d = 0; d < DIRS; d++) for (let f = 0; f < FILES_PER_DIR; f++) yield `pkg${String(d).padStart(3, '0')}/file${String(f).padStart(4, '0')}.go`
  yield 'README.md'
  yield 'go.mod'
}

type Call = { url: URL; recursive: boolean }
let calls: Call[] = []

function fakeFetch(input: RequestInfo | URL): Promise<Response> {
  const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, 'http://test')
  if (!url.pathname.endsWith('/tree')) return Promise.resolve(new Response('{"error":"not found"}', { status: 404, headers: { 'Content-Type': 'application/json' } }))
  const depth1 = url.searchParams.get('depth') === '1'
  const path = url.searchParams.get('path') ?? ''
  calls.push({ url, recursive: !depth1 })
  const limit = Math.min(100_000, Number(url.searchParams.get('limit')) || 20_000)
  const offset = Number(url.searchParams.get('offset')) || 0
  let all: string[]
  if (depth1) all = children(path)
  else {
    all = []
    for (const p of recursive()) if (!path || p.startsWith(path + '/')) all.push(p)
  }
  const page = all.slice(offset, offset + limit)
  return Promise.resolve(new Response(JSON.stringify(page), { status: 200, headers: { 'Content-Type': 'application/json', 'X-Ore-Tree-Total': String(all.length), 'X-Ore-Tree-Truncated': String(offset + page.length < all.length || offset > 0) } }))
}

function wrap(ui: React.ReactElement, qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

/** A client whose cache already holds the given directories, so a render is synchronous and measures only the browser's work. */
async function seeded(...dirs: string[]) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
  for (const d of dirs) qc.setQueryData(treeDirKey('acme', 'big', 'refs/heads/main', d), await fetchTree('acme', 'big', 'refs/heads/main', { path: d, depth1: true }))
  calls = []
  return qc
}

describe('lazy file browser on a 100,000-file repository', () => {
  beforeEach(async () => {
    vi.stubGlobal('fetch', vi.fn(fakeFetch))
    // Pay the one-time costs (first React Query render, the virtualizer, icon modules) outside the measured window.
    const warm = wrap(<DirListing org="acme" repo="big" refFull="refs/heads/main" path="pkg000" onOpenDir={() => {}} onOpenFile={() => {}} height={600} />)
    await screen.findAllByRole('listitem')
    warm.unmount()
    calls = []
  })
  afterEach(() => vi.unstubAllGlobals())

  it('renders a 1,000-entry directory in under 100 ms', async () => {
    const qc = await seeded('pkg042')
    const t0 = performance.now()
    wrap(<DirListing org="acme" repo="big" refFull="refs/heads/main" path="pkg042" onOpenDir={() => {}} onOpenFile={() => {}} height={600} />, qc)
    const rows = screen.getAllByRole('listitem')
    const ms = performance.now() - t0
    // Virtualized: a screenful of rows is in the DOM, the rest is a scroll height.
    expect(rows.length).toBeGreaterThan(10)
    expect(rows.length).toBeLessThan(60)
    expect(rows[0]).toHaveTextContent('..')
    expect(rows[1]).toHaveTextContent('file0000.go')
    expect(screen.getByRole('list').firstElementChild).toHaveStyle({ height: `${(FILES_PER_DIR + 1) * 36}px` })
    expect(ms).toBeLessThan(100)
    expect(calls).toHaveLength(0)
  })

  it('asks for one directory with depth=1 and never for the recursive listing', async () => {
    wrap(<DirListing org="acme" repo="big" refFull="refs/heads/main" path="pkg042" onOpenDir={() => {}} onOpenFile={() => {}} height={600} />)
    await screen.findAllByRole('listitem')
    expect(calls).toHaveLength(1)
    expect(calls[0]!.recursive).toBe(false)
    expect(calls[0]!.url.searchParams.get('path')).toBe('pkg042')
    expect(calls[0]!.url.searchParams.get('depth')).toBe('1')
  })

  it('expands a 1,000-entry directory in the tree in under 100 ms', async () => {
    const qc = await seeded('', 'pkg001', 'pkg007')
    wrap(<TreeNav org="acme" repo="big" refFull="refs/heads/main" current="" onOpenDir={() => {}} onOpenFile={() => {}} height={600} />, qc)
    // Steady state: one expansion already happened in this tree (first-time render paths are paid).
    fireEvent.click(screen.getByRole('button', { name: 'pkg001' }))
    fireEvent.click(screen.getByRole('button', { name: 'pkg001' }))
    const t0 = performance.now()
    fireEvent.click(screen.getByRole('button', { name: 'pkg007' }))
    screen.getByRole('button', { name: 'file0000.go' })
    expect(performance.now() - t0).toBeLessThan(100)
    // The tree grew by the directory's 1,000 children (scroll height), not by DOM rows.
    const scroller = screen.getByRole('tree').firstElementChild as HTMLElement
    expect(scroller).toHaveStyle({ height: `${(DIRS + 2 + FILES_PER_DIR) * 26}px` })
    expect(screen.getAllByRole('treeitem').length).toBeLessThan(60)
    expect(calls).toHaveLength(0)
  })

  it('expands and collapses with one request per opened directory', async () => {
    const onOpenDir = vi.fn()
    wrap(<TreeNav org="acme" repo="big" refFull="refs/heads/main" current="" onOpenDir={onOpenDir} onOpenFile={() => {}} height={600} />)
    await screen.findByRole('button', { name: 'pkg007' })
    const root = screen.getAllByRole('treeitem')
    expect(root.length).toBeGreaterThan(10)
    expect(root.length).toBeLessThan(DIRS + 2)
    expect(calls).toHaveLength(1)
    fireEvent.click(screen.getByRole('button', { name: 'pkg007' }))
    await screen.findByRole('button', { name: 'file0000.go' })
    expect(onOpenDir).toHaveBeenCalledWith('pkg007')
    expect(calls).toHaveLength(2)
    expect(calls.every((c) => !c.recursive)).toBe(true)
    expect(calls[1]!.url.searchParams.get('path')).toBe('pkg007')
    // Collapsing removes the children without another request; reopening reuses the cache.
    fireEvent.click(screen.getByRole('button', { name: 'pkg007' }))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'file0000.go' })).toBeNull())
    fireEvent.click(screen.getByRole('button', { name: 'pkg007' }))
    await screen.findByRole('button', { name: 'file0000.go' })
    expect(calls).toHaveLength(2)
  })

  it('opens the ancestors of the current path, each with its own request', async () => {
    wrap(<TreeNav org="acme" repo="big" refFull="refs/heads/main" current="pkg003/file0001.go" expand="pkg003" onOpenDir={() => {}} onOpenFile={() => {}} height={600} />)
    const cur = await screen.findByRole('button', { name: 'file0001.go' })
    expect(cur).toHaveAttribute('aria-current', 'page')
    expect(calls.map((c) => c.url.searchParams.get('path') ?? '')).toEqual(['', 'pkg003'])
  })

  it('pages the recursive listing for the finder and keeps only the best matches', async () => {
    const progress: number[] = []
    const res = await scanTree('acme', 'big', 'refs/heads/main', (p) => fuzzyScore('pkg099/file0999', p), { max: 50, onProgress: (p) => progress.push(p.scanned) })
    expect(res.total).toBe(DIRS * FILES_PER_DIR + 2)
    expect(res.complete).toBe(true)
    expect(res.hits.length).toBeLessThanOrEqual(50)
    expect(res.hits[0]!.path).toBe('pkg099/file0999.go')
    // 100,002 entries in pages of 20,000: six requests, all recursive, none larger than a page.
    const rec = calls.filter((c) => c.recursive)
    expect(rec).toHaveLength(6)
    expect(rec.every((c) => Number(c.url.searchParams.get('limit')) <= 20_000)).toBe(true)
    expect(progress.at(-1)).toBe(DIRS * FILES_PER_DIR + 2)
  })

  it('shows a truncation hint and loads more when a directory exceeds the page size', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = new URL(typeof input === 'string' ? input : (input as Request).url, 'http://test')
        const limit = Number(url.searchParams.get('limit')) || 20_000
        const offset = Number(url.searchParams.get('offset')) || 0
        const total = 25_000
        const page = Array.from({ length: Math.max(0, Math.min(limit, total - offset)) }, (_, i) => `huge/f${offset + i}.txt`)
        return Promise.resolve(new Response(JSON.stringify(page), { status: 200, headers: { 'Content-Type': 'application/json', 'X-Ore-Tree-Total': String(total), 'X-Ore-Tree-Truncated': String(offset + page.length < total || offset > 0) } }))
      }),
    )
    wrap(<DirListing org="acme" repo="big" refFull="refs/heads/main" path="huge" onOpenDir={() => {}} onOpenFile={() => {}} height={600} />)
    await screen.findByText(/Showing 20,000 of 25,000 entries/)
    fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(screen.queryByText(/Showing 20,000 of/)).toBeNull())
    expect(screen.getByRole('list').firstElementChild).toHaveStyle({ height: `${(25_000 + 1) * 36}px` })
  })
})
