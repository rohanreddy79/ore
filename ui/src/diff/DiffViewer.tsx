import { memo, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useVirtualizer, useWindowVirtualizer } from '@tanstack/react-virtual'
import { Bot, ChevronDown, ChevronRight, Columns2, FileCode2, FoldVertical, Maximize2, Rows3, UnfoldVertical } from 'lucide-react'
import { Button, CopyButton, Dialog, Pill, Segmented, cx } from '../design'
import { useHotkey } from '../lib/keyboard'
import { highlightFile, type Span } from './highlight'
import { diffStats, parseUnifiedDiff, sideTexts, type DiffFile, type DiffLine } from './parse'
import { ROW_HEIGHTS, anchorKey, buildRows, fileRowIndices, isLarge, type Row, type View } from './window'
import { wordDiff, type Segment } from './wordDiff'
import { CodeMirrorMerge } from './CodeMirrorMerge'
import './diff.css'

export type Provenance = { kind: string; id: string; model?: string; session?: string; adapter?: string; prompt_hash?: string } | null

export type LineRef = { path: string; side: 'old' | 'new'; line: number }

export type DiffViewerProps = {
  diff: string
  view?: View
  onViewChange?: (v: View) => void
  collapseOver?: number
  /** Anchor keys that have threads (see anchorKey). */
  threads?: ReadonlySet<string>
  renderThread?: (key: string) => ReactNode
  /** Called when a gutter number is clicked (start a comment). */
  onLineClick?: (ref: LineRef) => void
  /** Who authored this diff; agent provenance shows a badge on every hunk. */
  provenance?: Provenance
  /** Per-path annotation count shown in the file header. */
  fileBadges?: Map<string, ReactNode>
  /** Loads full file contents for the CodeMirror compare. */
  blobLoader?: (path: string, side: 'old' | 'new') => Promise<string>
  showTree?: boolean
  emptyMessage?: string
  /** Scroll inside a fixed-height box instead of the window (tests, dialogs). */
  height?: number
  /** Line to focus (and scroll to) on mount. */
  focusLine?: LineRef | null
}

type FileHighlight = { oldLines: Span[][]; newLines: Span[][]; oldIndex: Map<number, number>; newIndex: Map<number, number> }

export function DiffViewer(props: DiffViewerProps) {
  const files = useMemo(() => parseUnifiedDiff(props.diff), [props.diff])
  return <DiffFilesViewer {...props} files={files} />
}

export const DiffFilesViewer = memo(function DiffFilesViewer({ files, view: viewProp, onViewChange, collapseOver = 400, threads, renderThread, onLineClick, provenance, fileBadges, blobLoader, showTree = true, emptyMessage, height, focusLine }: Omit<DiffViewerProps, 'diff'> & { files: DiffFile[] }) {
  const [viewState, setViewState] = useState<View>(() => (typeof window !== 'undefined' && window.innerWidth < 900 ? 'unified' : 'split'))
  const view = viewProp ?? viewState
  const setView = (v: View) => {
    setViewState(v)
    onViewChange?.(v)
  }
  const [expanded, setExpanded] = useState<Set<number>>(() => new Set(files.map((f, i) => (isLarge(f, collapseOver) ? -1 : i)).filter((i) => i >= 0)))
  useEffect(() => {
    setExpanded(new Set(files.map((f, i) => (isLarge(f, collapseOver) ? -1 : i)).filter((i) => i >= 0)))
  }, [files, collapseOver])
  const [highlights, setHighlights] = useState<Map<number, FileHighlight>>(new Map())
  const [compare, setCompare] = useState<{ file: DiffFile; original: string; modified: string } | null>(null)
  const [treeOpen, setTreeOpen] = useState(showTree)
  const [current, setCurrent] = useState(0)

  const rows = useMemo(() => buildRows(files, { view, expanded, collapseOver, threads }), [files, view, expanded, collapseOver, threads])
  // Performance budget instrumentation: "ore:diff:data" when the parsed files
  // arrive, "ore:diff:rendered" after the first window of rows is committed.
  // The e2e suite reads performance.measure('ore:diff').
  useMemo(() => {
    if (typeof performance !== 'undefined' && files.length) performance.mark('ore:diff:data')
  }, [files])
  const fileRows = useMemo(() => fileRowIndices(rows), [rows])
  const stats = useMemo(() => diffStats(files), [files])

  const boxRef = useRef<HTMLDivElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const useWindow = height === undefined
  const estimate = useCallback((i: number) => ROW_HEIGHTS[rows[i]!.kind], [rows])
  const windowV = useWindowVirtualizer({ count: useWindow ? rows.length : 0, estimateSize: estimate, overscan: 12, scrollMargin: listRef.current?.offsetTop ?? 0, getItemKey: (i) => rows[i]!.key })
  const boxV = useVirtualizer({ count: useWindow ? 0 : rows.length, getScrollElement: () => boxRef.current, estimateSize: estimate, overscan: 12, getItemKey: (i) => rows[i]!.key })
  const v = useWindow ? windowV : boxV
  const items = v.getVirtualItems()

  const measured = useRef<DiffFile[] | null>(null)
  const hasItems = items.length > 0
  useEffect(() => {
    if (typeof performance === 'undefined' || !files.length || !hasItems || measured.current === files) return
    measured.current = files
    try {
      performance.mark('ore:diff:rendered')
      performance.measure('ore:diff', 'ore:diff:data', 'ore:diff:rendered')
    } catch {
      /* marks cleared */
    }
  }, [files, hasItems])

  // Highlight files as they become expanded and visible.
  useEffect(() => {
    const needed = new Set<number>()
    for (const it of items) {
      const r = rows[it.index]
      if (r && (r.kind === 'line' || r.kind === 'hunk') && !highlights.has(r.fileIndex)) needed.add(r.fileIndex)
    }
    if (needed.size === 0) return
    let alive = true
    for (const fi of needed) {
      const f = files[fi]!
      const { oldText, newText, oldLineNos, newLineNos } = sideTexts(f)
      Promise.all([highlightFile(f.oldPath ?? f.path, oldText), highlightFile(f.path, newText)]).then(([o, n]) => {
        if (!alive) return
        setHighlights((m) => {
          if (m.has(fi)) return m
          const next = new Map(m)
          next.set(fi, { oldLines: o, newLines: n, oldIndex: new Map(oldLineNos.map((ln, i) => [ln, i])), newIndex: new Map(newLineNos.map((ln, i) => [ln, i])) })
          return next
        })
      })
    }
    return () => {
      alive = false
    }
  }, [items, rows, files, highlights])

  const scrollToFile = useCallback(
    (fi: number) => {
      const idx = fileRows[fi]
      if (idx === undefined) return
      setCurrent(fi)
      v.scrollToIndex(idx, { align: 'start' })
    },
    [fileRows, v],
  )
  useHotkey('n', () => scrollToFile(Math.min(files.length - 1, current + 1)))
  useHotkey('p', () => scrollToFile(Math.max(0, current - 1)))

  // Track the current file from the scroll position for the tree highlight.
  useEffect(() => {
    const first = items[0]
    if (!first) return
    const r = rows[first.index]
    if (r && r.fileIndex !== current) setCurrent(r.fileIndex)
  }, [items, rows, current])

  useEffect(() => {
    if (!focusLine) return
    const idx = rows.findIndex((r) => r.kind === 'line' && r.file.path === focusLine.path && ((focusLine.side === 'new' && r.right?.newNo === focusLine.line) || (focusLine.side === 'old' && r.left?.oldNo === focusLine.line)))
    if (idx >= 0) v.scrollToIndex(idx, { align: 'center' })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusLine, rows.length])

  const toggleFile = (fi: number) =>
    setExpanded((s) => {
      const n = new Set(s)
      if (n.has(fi)) n.delete(fi)
      else n.add(fi)
      return n
    })
  const expandAll = () => setExpanded(new Set(files.map((_, i) => i)))
  const collapseAll = () => setExpanded(new Set())

  const openCompare = async (file: DiffFile) => {
    if (!blobLoader) return
    const [original, modified] = await Promise.all([file.oldPath ? blobLoader(file.oldPath, 'old').catch(() => '') : Promise.resolve(''), file.newPath ? blobLoader(file.newPath, 'new').catch(() => '') : Promise.resolve('')])
    setCompare({ file, original, modified })
  }

  if (files.length === 0) return <div className="diff-empty">{emptyMessage ?? 'No differences.'}</div>

  const totalSize = v.getTotalSize()
  const lastIndexPerFile = new Map<number, number>()
  rows.forEach((r, i) => lastIndexPerFile.set(r.fileIndex, i))

  return (
    <div className={cx('diff', treeOpen && showTree && 'with-tree')}>
      {treeOpen && showTree ? (
        <nav className="diff-tree card flat" aria-label="Changed files">
          {files.map((f, i) => (
            <button key={i} type="button" onClick={() => scrollToFile(i)} aria-current={i === current} title={f.path}>
              <span className={`glyph ${f.status}`} aria-label={f.status} />
              <span className="p">
                <bdi>{f.path}</bdi>
              </span>
              <span className="counts">
                {f.binary ? 'bin' : (
                  <>
                    <span style={{ color: 'var(--ok-fg)' }}>+{f.additions}</span> <span style={{ color: 'var(--danger-fg)' }}>−{f.deletions}</span>
                  </>
                )}
              </span>
            </button>
          ))}
        </nav>
      ) : null}
      <div>
        <div className="diff-summary">
          <span>
            <b>{stats.files}</b> {stats.files === 1 ? 'file' : 'files'} changed
          </span>
          <span className="adds">+{stats.additions}</span>
          <span className="dels">−{stats.deletions}</span>
          {provenance && provenance.kind === 'agent' ? (
            <span className="prov-badge" title={`Authored by agent ${provenance.id}${provenance.model ? ` (${provenance.model})` : ''}${provenance.session ? `, session ${provenance.session}` : ''}`}>
              <Bot /> agent-authored · {provenance.id}
            </span>
          ) : null}
          <span className="grow" />
          <span className="subtle small hide-sm">n / p to move between files</span>
          <Segmented value={view} onChange={setView} label="Diff layout" options={[{ value: 'split', label: <><Columns2 size={12} /> Split</> }, { value: 'unified', label: <><Rows3 size={12} /> Unified</> }]} />
          <Button size="sm" variant="ghost" onClick={expandAll} title="Expand all files">
            <UnfoldVertical /> Expand all
          </Button>
          <Button size="sm" variant="ghost" onClick={collapseAll} title="Collapse all files">
            <FoldVertical /> Collapse all
          </Button>
          {showTree ? (
            <Button size="sm" variant="ghost" icon aria-pressed={treeOpen} onClick={() => setTreeOpen((o) => !o)} aria-label="Toggle file tree" title="Toggle file tree">
              <FileCode2 />
            </Button>
          ) : null}
        </div>
        <div ref={boxRef} style={useWindow ? undefined : { height, overflow: 'auto' }} data-testid="diff-scroll">
          <div ref={listRef} className="diff-list" style={{ height: totalSize }}>
            {items.map((it) => {
              const r = rows[it.index]!
              const top = it.start - (useWindow ? windowV.options.scrollMargin : 0)
              const last = lastIndexPerFile.get(r.fileIndex) === it.index
              return (
                <div key={it.key} data-index={it.index} ref={r.kind === 'thread' ? v.measureElement : undefined} className={cx('diff-row', r.kind, last && 'last')} style={{ transform: `translateY(${top}px)` }}>
                  {r.kind === 'file' ? (
                    <FileHeader file={r.file} index={r.fileIndex} expanded={expanded.has(r.fileIndex)} onToggle={() => toggleFile(r.fileIndex)} onCompare={blobLoader ? () => void openCompare(r.file) : undefined} badge={fileBadges?.get(r.file.path)} />
                  ) : r.kind === 'collapsed' ? (
                    <div className="collapsed-box">
                      {r.reason === 'binary' ? (
                        <span>Binary file not shown.</span>
                      ) : r.reason === 'mode' ? (
                        <span>
                          File mode changed {r.file.oldMode} → {r.file.newMode}.
                        </span>
                      ) : r.reason === 'empty' ? (
                        <span>{r.file.status === 'renamed' ? `Renamed without changes (${r.file.similarity ?? 100}% similar).` : 'No content changes.'}</span>
                      ) : (
                        <>
                          <span>{r.reason === 'large' ? `Large diff (${r.file.lineCount.toLocaleString()} lines) collapsed by default.` : 'Collapsed.'}</span>
                          <Button size="sm" onClick={() => toggleFile(r.fileIndex)}>
                            Load diff
                          </Button>
                        </>
                      )}
                    </div>
                  ) : r.kind === 'hunk' ? (
                    <div className="hunk-head">
                      <span>{r.header.split(' @@')[0]} @@</span>
                      <span className="section">{r.section}</span>
                      <span className="grow" />
                      {provenance && provenance.kind === 'agent' ? (
                        <span className="prov-badge" title={`This hunk comes from agent ${provenance.id}${provenance.session ? ` (session ${provenance.session})` : ''}`}>
                          <Bot /> {provenance.id}
                        </span>
                      ) : null}
                    </div>
                  ) : r.kind === 'line' ? (
                    <LineRow row={r} view={view} hl={highlights.get(r.fileIndex)} onLineClick={onLineClick} focus={!!focusLine && r.file.path === focusLine.path && ((focusLine.side === 'new' && r.right?.newNo === focusLine.line) || (focusLine.side === 'old' && r.left?.oldNo === focusLine.line))} />
                  ) : (
                    <div className="thread-box">{renderThread?.(r.anchorKey)}</div>
                  )}
                </div>
              )
            })}
          </div>
        </div>
      </div>
      <Dialog open={!!compare} onClose={() => setCompare(null)} title={compare ? `Compare full files: ${compare.file.path}` : ''} wide>
        {compare ? <CodeMirrorMerge original={compare.original} modified={compare.modified} filename={compare.file.path} unified={view === 'unified'} /> : null}
      </Dialog>
    </div>
  )
})

function FileHeader({ file, index, expanded, onToggle, onCompare, badge }: { file: DiffFile; index: number; expanded: boolean; onToggle: () => void; onCompare?: () => void; badge?: ReactNode }) {
  const status = file.status === 'modified' ? null : file.status
  return (
    <div className="file-header" id={`file-${index}`}>
      <Button size="sm" variant="ghost" icon aria-expanded={expanded} aria-label={expanded ? `Collapse ${file.path}` : `Expand ${file.path}`} onClick={onToggle}>
        {expanded ? <ChevronDown /> : <ChevronRight />}
      </Button>
      <span className={`glyph ${file.status}`} aria-hidden />
      <span className="path" title={file.path}>
        {file.status === 'renamed' || file.status === 'copied' ? (
          <>
            <bdi>{file.oldPath}</bdi>
            <span className="arrow">→</span>
            <bdi>{file.newPath}</bdi>
          </>
        ) : (
          <bdi>{file.path}</bdi>
        )}
      </span>
      {status ? (
        <Pill small tone={status === 'added' ? 'ok' : status === 'deleted' ? 'danger' : 'info'}>
          {status}
          {file.similarity !== undefined && status === 'renamed' ? ` ${file.similarity}%` : ''}
        </Pill>
      ) : null}
      {file.binary ? <Pill small>binary</Pill> : null}
      {badge}
      <span className="counts">
        <span className="a">+{file.additions}</span>
        <span className="d">−{file.deletions}</span>
      </span>
      <CopyButton text={file.path} label="Copy path" />
      {onCompare && !file.binary ? (
        <Button size="sm" variant="ghost" icon onClick={onCompare} aria-label="Compare full files in the editor" title="Compare full files (CodeMirror)">
          <Maximize2 />
        </Button>
      ) : null}
    </div>
  )
}

function spansFor(hl: FileHighlight | undefined, side: 'old' | 'new', line: DiffLine | null): Span[] | null {
  if (!hl || !line) return null
  const idx = side === 'old' ? hl.oldIndex.get(line.oldNo ?? -1) : hl.newIndex.get(line.newNo ?? -1)
  if (idx === undefined) return null
  return (side === 'old' ? hl.oldLines : hl.newLines)[idx] ?? null
}

/** Splits highlighted spans at word-diff boundaries so both decorations apply. */
export function mergeSpans(spans: Span[] | null, text: string, segs: Segment[] | null): Array<Span & { changed?: boolean }> {
  const base: Span[] = spans ?? [{ text }]
  if (!segs || !segs.some((s) => s.changed)) return base
  const out: Array<Span & { changed?: boolean }> = []
  let si = 0
  let segOff = 0
  for (const sp of base) {
    let remaining = sp.text
    while (remaining.length > 0 && si < segs.length) {
      const seg = segs[si]!
      const avail = seg.text.length - segOff
      const take = Math.min(avail, remaining.length)
      out.push({ text: remaining.slice(0, take), cls: sp.cls, changed: seg.changed })
      remaining = remaining.slice(take)
      segOff += take
      if (segOff >= seg.text.length) {
        si++
        segOff = 0
      }
    }
    if (remaining.length > 0) out.push({ text: remaining, cls: sp.cls })
  }
  return out
}

const LineRow = memo(function LineRow({ row, view, hl, onLineClick, focus }: { row: Extract<Row, { kind: 'line' }>; view: View; hl: FileHighlight | undefined; onLineClick?: (ref: LineRef) => void; focus?: boolean }) {
  const { left, right, file } = row
  const paired = left && right && left !== right && left.type === 'del' && right.type === 'add'
  const wd = useMemo(() => (paired ? wordDiff(left.text, right.text) : null), [paired, left, right])
  const leftSpans = mergeSpans(spansFor(hl, 'old', left), left?.text ?? '', wd?.left ?? null)
  const rightSpans = mergeSpans(spansFor(hl, 'new', right), right?.text ?? '', wd?.right ?? null)
  const clickable = !!onLineClick
  const num = (side: 'old' | 'new', n: number | null, cls: string) => (
    <span
      className={cx('cell num', cls, clickable && n !== null && 'clickable')}
      role={clickable && n !== null ? 'button' : undefined}
      tabIndex={-1}
      aria-label={n !== null ? `${side === 'old' ? 'Old' : 'New'} line ${n}${clickable ? ', comment' : ''}` : undefined}
      onClick={n !== null && onLineClick ? () => onLineClick({ path: side === 'old' ? (file.oldPath ?? file.path) : file.path, side, line: n }) : undefined}
    >
      {clickable && n !== null ? (
        <span className="add-btn" aria-hidden>
          +
        </span>
      ) : null}
      {n ?? ''}
    </span>
  )
  const code = (line: DiffLine | null, spans: Array<Span & { changed?: boolean }>) => {
    if (!line) return <span className="cell code empty" />
    const cls = line.type === 'add' ? 'add' : line.type === 'del' ? 'del' : ''
    return (
      <span className={cx('cell code', cls)} data-sign={line.type === 'add' ? '+' : line.type === 'del' ? '−' : ''}>
        {spans.map((s, i) => (
          <span key={i} className={cx(s.cls, s.changed && 'w')}>
            {s.text}
          </span>
        ))}
        {line.noNewline ? (
          <span className="nonl" title="No newline at end of file">
            ⏎̸
          </span>
        ) : null}
      </span>
    )
  }
  if (view === 'split') {
    const lc = left ? (left.type === 'del' ? 'del' : '') : 'empty'
    const rc = right ? (right.type === 'add' ? 'add' : '') : 'empty'
    return (
      <div className={cx('line-grid split', focus && 'focus')}>
        {num('old', left?.oldNo ?? null, lc)}
        {code(left, leftSpans)}
        {num('new', right?.newNo ?? null, rc)}
        {code(right, rightSpans)}
      </div>
    )
  }
  const line = (right ?? left)!
  const cls = line.type === 'add' ? 'add' : line.type === 'del' ? 'del' : ''
  return (
    <div className={cx('line-grid unified', focus && 'focus')}>
      {num('old', line.oldNo, cls)}
      {num('new', line.newNo, cls)}
      {code(line, line.type === 'del' ? leftSpans : rightSpans)}
    </div>
  )
})

export { anchorKey }
