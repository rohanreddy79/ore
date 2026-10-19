// The windowing model: turns parsed files into a flat list of rows with
// estimated heights so a virtualizer renders only the visible slice. Pure,
// so the 20,000-line budget test runs without a DOM.

import type { DiffFile, DiffLine } from './parse'

export type View = 'unified' | 'split'

export type Row =
  | { kind: 'file'; fileIndex: number; file: DiffFile; key: string }
  | { kind: 'collapsed'; fileIndex: number; file: DiffFile; key: string; reason: 'large' | 'binary' | 'mode' | 'empty' | 'user' }
  | { kind: 'hunk'; fileIndex: number; file: DiffFile; hunkIndex: number; header: string; section: string; key: string }
  | { kind: 'line'; fileIndex: number; file: DiffFile; hunkIndex: number; left: DiffLine | null; right: DiffLine | null; key: string }
  | { kind: 'thread'; fileIndex: number; file: DiffFile; key: string; anchorKey: string }

export const ROW_HEIGHTS: Record<Row['kind'], number> = { file: 44, collapsed: 52, hunk: 28, line: 22, thread: 160 }

export type WindowOptions = {
  view: View
  /** Files whose rows render; others show as collapsed. */
  expanded: ReadonlySet<number> | 'all'
  /** Files over this many rows are collapsed unless explicitly expanded. */
  collapseOver?: number
  /** Thread anchors to insert after a line: keys like "<path>:<side>:<line>". */
  threads?: ReadonlySet<string>
}

export function anchorKey(path: string, side: 'old' | 'new', line: number): string {
  return `${path}:${side}:${line}`
}

export function isLarge(file: DiffFile, collapseOver = 400): boolean {
  return file.lineCount > collapseOver
}

/** Pairs deletions with the additions that follow them, GitHub style. */
export function pairLines(lines: DiffLine[]): Array<[DiffLine | null, DiffLine | null]> {
  const out: Array<[DiffLine | null, DiffLine | null]> = []
  let i = 0
  while (i < lines.length) {
    const l = lines[i]!
    if (l.type === 'context') {
      out.push([l, l])
      i++
      continue
    }
    const dels: DiffLine[] = []
    const adds: DiffLine[] = []
    while (i < lines.length && lines[i]!.type === 'del') dels.push(lines[i++]!)
    while (i < lines.length && lines[i]!.type === 'add') adds.push(lines[i++]!)
    const n = Math.max(dels.length, adds.length)
    for (let k = 0; k < n; k++) out.push([dels[k] ?? null, adds[k] ?? null])
    if (dels.length === 0 && adds.length === 0) i++ // safety
  }
  return out
}

export function buildRows(files: DiffFile[], opts: WindowOptions): Row[] {
  const rows: Row[] = []
  const collapseOver = opts.collapseOver ?? 400
  for (let fi = 0; fi < files.length; fi++) {
    const file = files[fi]!
    rows.push({ kind: 'file', fileIndex: fi, file, key: `f${fi}` })
    const userExpanded = opts.expanded === 'all' || opts.expanded.has(fi)
    if (file.binary) {
      rows.push({ kind: 'collapsed', fileIndex: fi, file, key: `c${fi}`, reason: 'binary' })
      continue
    }
    if (file.hunks.length === 0) {
      rows.push({ kind: 'collapsed', fileIndex: fi, file, key: `c${fi}`, reason: file.status === 'mode' || file.oldMode || file.newMode ? 'mode' : 'empty' })
      continue
    }
    if (!userExpanded) {
      rows.push({ kind: 'collapsed', fileIndex: fi, file, key: `c${fi}`, reason: isLarge(file, collapseOver) ? 'large' : 'user' })
      continue
    }
    for (let hi = 0; hi < file.hunks.length; hi++) {
      const h = file.hunks[hi]!
      rows.push({ kind: 'hunk', fileIndex: fi, file, hunkIndex: hi, header: h.header, section: h.section, key: `h${fi}.${hi}` })
      if (opts.view === 'split') {
        const pairs = pairLines(h.lines)
        for (let pi = 0; pi < pairs.length; pi++) {
          const [l, r] = pairs[pi]!
          rows.push({ kind: 'line', fileIndex: fi, file, hunkIndex: hi, left: l, right: r, key: `l${fi}.${hi}.${pi}` })
          pushThreads(rows, opts, file, fi, l, r, `l${fi}.${hi}.${pi}`)
        }
      } else {
        for (let li = 0; li < h.lines.length; li++) {
          const l = h.lines[li]!
          rows.push({ kind: 'line', fileIndex: fi, file, hunkIndex: hi, left: l.type === 'add' ? null : l, right: l.type === 'del' ? null : l, key: `l${fi}.${hi}.${li}` })
          pushThreads(rows, opts, file, fi, l.type === 'add' ? null : l, l.type === 'del' ? null : l, `l${fi}.${hi}.${li}`)
        }
      }
    }
  }
  return rows
}

function pushThreads(rows: Row[], opts: WindowOptions, file: DiffFile, fi: number, l: DiffLine | null, r: DiffLine | null, base: string) {
  if (!opts.threads || opts.threads.size === 0) return
  const seen = new Set<string>()
  if (r?.newNo) {
    const k = anchorKey(file.path, 'new', r.newNo)
    if (opts.threads.has(k)) {
      rows.push({ kind: 'thread', fileIndex: fi, file, key: `t${base}n`, anchorKey: k })
      seen.add(k)
    }
  }
  if (l?.oldNo) {
    const k = anchorKey(file.oldPath ?? file.path, 'old', l.oldNo)
    if (opts.threads.has(k) && !seen.has(k)) rows.push({ kind: 'thread', fileIndex: fi, file, key: `t${base}o`, anchorKey: k })
  }
}

/** Which rows fall inside [scrollTop, scrollTop + height) given estimated heights; for tests of the model without a DOM. */
export function visibleRange(rows: Row[], scrollTop: number, height: number, overscan = 10): { start: number; end: number; total: number } {
  let y = 0
  let start = -1
  let end = rows.length
  let total = 0
  for (let i = 0; i < rows.length; i++) {
    const h = ROW_HEIGHTS[rows[i]!.kind]
    if (start < 0 && y + h > scrollTop) start = i
    if (start >= 0 && end === rows.length && y > scrollTop + height) end = i
    y += h
  }
  total = y
  if (start < 0) start = 0
  return { start: Math.max(0, start - overscan), end: Math.min(rows.length, end + overscan), total }
}

/** Indices of the file header rows, for n/p navigation. */
export function fileRowIndices(rows: Row[]): number[] {
  const out: number[] = []
  rows.forEach((r, i) => {
    if (r.kind === 'file') out.push(i)
  })
  return out
}
