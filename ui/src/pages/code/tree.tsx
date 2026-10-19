import { useEffect, useMemo, useRef, useState, type ReactElement } from 'react'
import { useQueries } from '@tanstack/react-query'
import { useVirtualizer } from '@tanstack/react-virtual'
import { ChevronRight, File, FileCode2, Folder, FolderOpen } from 'lucide-react'
import { Button, Spinner, cx } from '../../design'
import { ApiError } from '../../lib/api'
import { TREE_PAGE, defaultQueryOptions, fetchTree, treeDirKey, useTreeDir, type TreePage } from '../../lib/queries'

// Router-free building blocks of the Code page: a tree pane that loads one
// directory at a time and a directory listing. Both take callbacks for
// navigation so they can be rendered and measured without a router, and both
// virtualize their rows so a 20,000-entry directory costs what a screenful does.

export type TreeEntry = { name: string; path: string; dir: boolean }

/** Turns the depth=1 listing (full paths, directories end in "/") into sorted children. */
const collator = typeof Intl !== 'undefined' ? new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' }) : null
const compareNames = (a: string, b: string) => (collator ? collator.compare(a, b) : a < b ? -1 : a > b ? 1 : 0)

export function parseChildren(entries: string[]): TreeEntry[] {
  const out: TreeEntry[] = []
  for (const e of entries) {
    const dir = e.endsWith('/')
    const path = dir ? e.slice(0, -1) : e
    const name = path.slice(path.lastIndexOf('/') + 1)
    if (!name) continue
    out.push({ name, path, dir })
  }
  return out.sort((a, b) => (a.dir === b.dir ? compareNames(a.name, b.name) : a.dir ? -1 : 1))
}

/** Fuzzy subsequence scoring for the file finder; lower is better. */
export function fuzzyScore(q: string, s: string): number | null {
  const lq = q.toLowerCase()
  const ls = s.toLowerCase()
  let qi = 0
  let score = 0
  let last = -1
  for (let i = 0; i < ls.length && qi < lq.length; i++) {
    if (ls[i] === lq[qi]) {
      score += last >= 0 ? i - last - 1 : i
      last = i
      qi++
    }
  }
  if (qi < lq.length) return null
  return score + (ls.length - last)
}

export function ancestorsOf(path: string): string[] {
  const parts = path.split('/').filter(Boolean)
  const out: string[] = []
  for (let i = 1; i < parts.length; i++) out.push(parts.slice(0, i).join('/'))
  return out
}

type TreeRow = { kind: 'entry'; entry: TreeEntry; depth: number } | { kind: 'loading' | 'error' | 'truncated'; path: string; depth: number; text: string }

/** Flattens the open directories into rows, exported for tests. */
export function flattenTree(open: ReadonlySet<string>, pages: ReadonlyMap<string, { page?: TreePage; error?: unknown; pending: boolean }>): TreeRow[] {
  const rows: TreeRow[] = []
  const walk = (path: string, depth: number) => {
    const st = pages.get(path)
    if (!st || st.pending) {
      rows.push({ kind: 'loading', path, depth, text: `Loading ${path || 'files'}` })
      return
    }
    if (st.error) {
      rows.push({ kind: 'error', path, depth, text: st.error instanceof ApiError && st.error.status === 404 ? 'empty' : 'could not load' })
      return
    }
    const children = parseChildren(st.page?.entries ?? [])
    for (const c of children) {
      rows.push({ kind: 'entry', entry: c, depth })
      if (c.dir && open.has(c.path)) walk(c.path, depth + 1)
    }
    if (st.page?.truncated) rows.push({ kind: 'truncated', path, depth, text: `showing ${children.length.toLocaleString()} of ${st.page.total.toLocaleString()} entries` })
  }
  walk('', 0)
  return rows
}

export type TreeNavProps = {
  org: string
  repo: string
  refFull: string
  /** The path shown on the right (highlighted). */
  current: string
  /** The directory to keep open; defaults to `current`. Pass the parent directory when `current` is a file. */
  expand?: string
  onOpenDir: (path: string) => void
  onOpenFile: (path: string) => void
  /** Pixel height of the scroll box; defaults to the available viewport. */
  height?: number
}

const TREE_ROW = 26

/** The lazy tree: the root loads first; a directory's children load the first time it is opened. */
export function TreeNav({ org, repo, refFull, current, expand, onOpenDir, onOpenFile, height }: TreeNavProps) {
  const keepOpen = expand ?? current
  const [open, setOpen] = useState<Set<string>>(() => new Set(ancestorsOf(keepOpen).concat(keepOpen ? [keepOpen] : [])))
  useEffect(() => {
    setOpen((s) => {
      const n = new Set(s)
      for (const a of ancestorsOf(keepOpen)) n.add(a)
      if (keepOpen) n.add(keepOpen)
      return n
    })
  }, [keepOpen])
  const toggle = (p: string) =>
    setOpen((s) => {
      const n = new Set(s)
      if (n.has(p)) n.delete(p)
      else n.add(p)
      return n
    })
  // One query per open directory; closed directories cost nothing.
  const dirs = useMemo(() => ['', ...open].filter((d, i, a) => a.indexOf(d) === i), [open])
  const results = useQueries({
    queries: dirs.map((d) => ({ queryKey: treeDirKey(org, repo, refFull, d), queryFn: () => fetchTree(org, repo, refFull, { path: d, depth1: true }), ...defaultQueryOptions, staleTime: 60_000, enabled: !!refFull })),
  })
  const pages = useMemo(() => {
    const m = new Map<string, { page?: TreePage; error?: unknown; pending: boolean }>()
    dirs.forEach((d, i) => {
      const r = results[i]!
      m.set(d, { page: r.data, error: r.error ?? undefined, pending: r.isPending })
    })
    return m
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dirs, ...results.map((r) => r.data), ...results.map((r) => r.status)])
  // Only directories whose parents are open are listed; `open` may hold stale paths from another ref.
  const rows = useMemo(() => flattenTree(open, pages), [open, pages])
  const boxRef = useRef<HTMLDivElement>(null)
  const boxHeight = height ?? Math.min(rows.length * TREE_ROW + 8, typeof window !== 'undefined' ? Math.max(240, window.innerHeight - 160) : 600)
  const v = useVirtualizer({ count: rows.length, getScrollElement: () => boxRef.current, estimateSize: () => TREE_ROW, overscan: 20 })
  return (
    <div ref={boxRef} style={{ height: boxHeight, overflow: 'auto' }} role="tree" aria-label="Files">
      <div style={{ height: v.getTotalSize(), position: 'relative' }}>
        {v.getVirtualItems().map((it) => {
          const r = rows[it.index]!
          const style: React.CSSProperties = { position: 'absolute', top: 0, left: 0, width: '100%', transform: `translateY(${it.start}px)`, height: TREE_ROW }
          if (r.kind !== 'entry') {
            return (
              <div key={it.key} style={{ ...style, padding: `4px ${8 + r.depth * 12}px` }} className={cx('small', r.kind === 'loading' ? '' : 'subtle')} role="presentation">
                {r.kind === 'loading' ? <Spinner label={r.text} /> : r.text}
              </div>
            )
          }
          const c = r.entry
          const isOpen = c.dir && open.has(c.path)
          return (
            <div key={it.key} style={style} role="treeitem" aria-expanded={c.dir ? isOpen : undefined} aria-level={r.depth + 1} aria-selected={c.path === current}>
              {c.dir ? (
                <button
                  type="button"
                  className="node"
                  aria-expanded={isOpen}
                  aria-current={c.path === current ? 'page' : undefined}
                  onClick={() => {
                    toggle(c.path)
                    if (!isOpen) onOpenDir(c.path)
                  }}
                  style={{ paddingLeft: 8 + r.depth * 12 }}
                  title={c.path}
                >
                  <ChevronRight className="chev" aria-hidden />
                  {isOpen ? <FolderOpen aria-hidden /> : <Folder aria-hidden />}
                  <span className="truncate">{c.name}</span>
                </button>
              ) : (
                <button type="button" className="node" aria-current={c.path === current ? 'page' : undefined} onClick={() => onOpenFile(c.path)} style={{ paddingLeft: 8 + r.depth * 12 + 18 }} title={c.path}>
                  <File aria-hidden />
                  <span className="truncate">{c.name}</span>
                </button>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}

export type DirListingProps = {
  org: string
  repo: string
  refFull: string
  path: string
  onOpenDir: (path: string) => void
  onOpenFile: (path: string) => void
  /** Rendered above the rows (the last-commit bar). */
  header?: ReactElement | null
  /** Called with the README entry found in this directory, if any. */
  onReadme?: (entry: TreeEntry | null) => void
  /** Pixel height of the scroll box for tests; defaults to fit up to 70 % of the viewport. */
  height?: number
}

const LIST_ROW = 36

/** The rows of one directory, virtualized, with paging past the server's per-directory limit. */
export function DirListing({ org, repo, refFull, path, onOpenDir, onOpenFile, header, onReadme, height }: DirListingProps) {
  const q = useTreeDir(org, repo, refFull, path)
  const [extra, setExtra] = useState<string[]>([])
  const [loadingMore, setLoadingMore] = useState(false)
  useEffect(() => setExtra([]), [path, refFull, q.data])
  const entries = useMemo(() => parseChildren([...(q.data?.entries ?? []), ...extra]), [q.data, extra])
  const readme = useMemo(() => entries.find((c) => !c.dir && /^readme(\.md|\.markdown|\.txt)?$/i.test(c.name)) ?? null, [entries])
  useEffect(() => {
    onReadme?.(readme)
  }, [readme, onReadme])
  const loaded = (q.data?.entries.length ?? 0) + extra.length
  const more = !!q.data && q.data.total > loaded
  const rows = useMemo(() => (path ? [{ name: '..', path: path.split('/').slice(0, -1).join('/'), dir: true, up: true }, ...entries] : entries) as Array<TreeEntry & { up?: boolean }>, [entries, path])
  const boxRef = useRef<HTMLDivElement>(null)
  const boxHeight = height ?? Math.min(rows.length * LIST_ROW, typeof window !== 'undefined' ? Math.max(360, Math.floor(window.innerHeight * 0.7)) : 600)
  const v = useVirtualizer({ count: rows.length, getScrollElement: () => boxRef.current, estimateSize: () => LIST_ROW, overscan: 15 })
  const loadMore = async () => {
    setLoadingMore(true)
    try {
      const page = await fetchTree(org, repo, refFull, { path, depth1: true, limit: TREE_PAGE, offset: loaded })
      setExtra((x) => [...x, ...page.entries])
    } finally {
      setLoadingMore(false)
    }
  }
  if (q.isPending) {
    return (
      <div className="card file-list" aria-busy>
        <div className="skeleton-rows">
          <span className="skeleton" style={{ width: '40%' }} />
          <span className="skeleton" style={{ width: '55%' }} />
          <span className="skeleton" style={{ width: '35%' }} />
        </div>
      </div>
    )
  }
  if (q.isError) {
    return (
      <div className="card file-list">
        <div className="empty compact">
          <h3>{q.error instanceof ApiError && q.error.status === 404 ? 'This path does not exist' : 'Could not load this directory'}</h3>
          <p>{q.error instanceof ApiError && q.error.status === 404 ? 'It may have been moved or removed.' : q.error instanceof Error ? q.error.message : 'Request failed'}</p>
          <Button onClick={() => q.refetch()}>Retry</Button>
        </div>
      </div>
    )
  }
  return (
    <div className="card file-list">
      {header}
      {rows.length === 0 ? <p className="muted" style={{ padding: 'var(--sp-3) var(--sp-4)' }}>This directory is empty.</p> : null}
      <div ref={boxRef} style={{ height: boxHeight, overflow: 'auto' }} role="list" aria-label={`Contents of ${path || 'the repository root'}`}>
        <div style={{ height: v.getTotalSize(), position: 'relative' }}>
          {v.getVirtualItems().map((it) => {
            const c = rows[it.index]!
            return (
              <button
                key={it.key}
                type="button"
                className="list-row"
                role="listitem"
                onClick={() => (c.dir ? onOpenDir(c.path) : onOpenFile(c.path))}
                style={{ position: 'absolute', top: 0, left: 0, width: '100%', height: LIST_ROW, transform: `translateY(${it.start}px)`, background: 'none', border: 0, borderTop: it.index === 0 ? 0 : '1px solid var(--border)', textAlign: 'left', cursor: 'pointer', font: 'inherit' }}
              >
                {c.dir ? <Folder aria-hidden /> : <FileCode2 aria-hidden />}
                <span className={cx('truncate', !c.dir && 'mono')}>{c.name}</span>
              </button>
            )
          })}
        </div>
      </div>
      {more ? (
        <div className="row" style={{ padding: 'var(--sp-2) var(--sp-4)', borderTop: '1px solid var(--border)' }}>
          <span className="small muted">
            Showing {loaded.toLocaleString()} of {q.data.total.toLocaleString()} entries (truncated by the server).
          </span>
          <Button size="sm" loading={loadingMore} onClick={() => void loadMore()}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  )
}
