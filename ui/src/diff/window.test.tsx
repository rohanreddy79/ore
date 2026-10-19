import { describe, expect, it } from 'vitest'
import { render } from '@testing-library/react'
import { parseUnifiedDiff, syntheticDiff } from './parse'
import { anchorKey, buildRows, fileRowIndices, pairLines, visibleRange } from './window'
import { DiffViewer, mergeSpans } from './DiffViewer'

describe('windowing model', () => {
  it('collapses files over the threshold and expands small ones', () => {
    const files = parseUnifiedDiff(syntheticDiff(3, 500) + syntheticDiff(1, 40).replace('module0', 'small'))
    const rows = buildRows(files, { view: 'unified', expanded: new Set([3]), collapseOver: 400 })
    const kinds = rows.map((r) => r.kind)
    // three big files: header + collapsed each
    expect(kinds.slice(0, 6)).toEqual(['file', 'collapsed', 'file', 'collapsed', 'file', 'collapsed'])
    expect(rows.filter((r) => r.kind === 'line')).toHaveLength(40)
    expect(fileRowIndices(rows)).toEqual([0, 2, 4, 6])
  })

  it('pairs deletions with additions in split view', () => {
    const files = parseUnifiedDiff(`diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1,3 +1,3 @@\n ctx\n-a\n-b\n+c\n ctx2\n`)
    const pairs = pairLines(files[0]!.hunks[0]!.lines)
    expect(pairs.map(([l, r]) => [l?.text ?? null, r?.text ?? null])).toEqual([
      ['ctx', 'ctx'],
      ['a', 'c'],
      ['b', null],
      ['ctx2', 'ctx2'],
    ])
    const rows = buildRows(files, { view: 'split', expanded: 'all' })
    expect(rows.filter((r) => r.kind === 'line')).toHaveLength(4)
    const unified = buildRows(files, { view: 'unified', expanded: 'all' })
    expect(unified.filter((r) => r.kind === 'line')).toHaveLength(5)
  })

  it('inserts thread rows after anchored lines', () => {
    const files = parseUnifiedDiff(`diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1,2 +1,2 @@\n ctx\n-a\n+c\n`)
    const rows = buildRows(files, { view: 'unified', expanded: 'all', threads: new Set([anchorKey('f', 'new', 2), anchorKey('f', 'old', 2)]) })
    const kinds = rows.map((r) => r.kind)
    expect(kinds).toEqual(['file', 'hunk', 'line', 'line', 'thread', 'line', 'thread'])
  })

  it('computes a visible window from estimated heights', () => {
    const files = parseUnifiedDiff(syntheticDiff(2, 200))
    const rows = buildRows(files, { view: 'unified', expanded: 'all' })
    const w = visibleRange(rows, 0, 600, 0)
    expect(w.start).toBe(0)
    expect(w.end).toBeLessThan(40)
    const w2 = visibleRange(rows, 2000, 600, 0)
    expect(w2.start).toBeGreaterThan(60)
    expect(w2.end - w2.start).toBeLessThan(40)
  })

  it('merges syntax spans with word-diff segments', () => {
    const out = mergeSpans([{ text: 'return ', cls: 'tok-keyword' }, { text: 'old(1)' }], 'return old(1)', [
      { text: 'return ', changed: false },
      { text: 'old', changed: true },
      { text: '(1)', changed: false },
    ])
    expect(out).toEqual([
      { text: 'return ', cls: 'tok-keyword', changed: false },
      { text: 'old', cls: undefined, changed: true },
      { text: '(1)', cls: undefined, changed: false },
    ])
  })
})

describe('20,000-line change budget', () => {
  it('builds the interactive model in under 200 ms', () => {
    const text = syntheticDiff(50, 400)
    const t0 = performance.now()
    const files = parseUnifiedDiff(text)
    const rows = buildRows(files, { view: 'unified', expanded: new Set(files.map((_, i) => i)), collapseOver: 400 })
    const split = buildRows(files, { view: 'split', expanded: 'all', collapseOver: 400 })
    const win = visibleRange(rows, 0, 900)
    const ms = performance.now() - t0
    expect(rows.length).toBeGreaterThan(20_000)
    expect(split.length).toBeLessThan(rows.length)
    expect(win.end - win.start).toBeLessThan(80)
    expect(ms).toBeLessThan(200)
  })

  it('renders only the visible window of rows', () => {
    const text = syntheticDiff(50, 400)
    const t0 = performance.now()
    const { container } = render(<DiffViewer diff={text} height={800} showTree={false} />)
    const ms = performance.now() - t0
    const rendered = container.querySelectorAll('.diff-row').length
    expect(rendered).toBeGreaterThan(0)
    expect(rendered).toBeLessThan(120)
    expect(container.querySelector('.diff-summary')?.textContent).toContain('50 files changed')
    // jsdom has no layout engine; this bounds the React work, not paint.
    expect(ms).toBeLessThan(1500)
  })

  it('shows a binary file, a rename and a very long name without crashing', () => {
    const long = 'a/'.repeat(60) + 'very-long-file-name.ts'
    const text = `diff --git a/${long} b/${long}\n--- a/${long}\n+++ b/${long}\n@@ -1 +1 @@\n-a\n+b\ndiff --git a/x.png b/x.png\nBinary files a/x.png and b/x.png differ\ndiff --git a/one.md b/two.md\nsimilarity index 100%\nrename from one.md\nrename to two.md\n`
    const { container } = render(<DiffViewer diff={text} height={600} />)
    expect(container.textContent).toContain('Binary file not shown')
    expect(container.textContent).toContain('Renamed without changes')
    expect(container.querySelector('.file-header .path')?.getAttribute('title')).toBe(long)
  })

  it('renders an empty message for a diff with no files', () => {
    const { container } = render(<DiffViewer diff="" height={300} emptyMessage="Nothing here" />)
    expect(container.textContent).toContain('Nothing here')
  })
})
