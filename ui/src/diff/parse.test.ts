import { describe, expect, it } from 'vitest'
import { diffStats, parseGitHeader, parseUnifiedDiff, sideTexts, syntheticDiff } from './parse'

const SAMPLE = `diff --git a/src/a.ts b/src/a.ts
index 1111111..2222222 100644
--- a/src/a.ts
+++ b/src/a.ts
@@ -1,4 +1,5 @@ export function a() {
 const x = 1
-const y = 2
+const y = 3
+const z = 4
 return x
 }
diff --git a/new.txt b/new.txt
new file mode 100644
index 0000000..e69de29
--- /dev/null
+++ b/new.txt
@@ -0,0 +1,2 @@
+hello
+world
\\ No newline at end of file
diff --git a/old.txt b/old.txt
deleted file mode 100644
index e69de29..0000000
--- a/old.txt
+++ /dev/null
@@ -1 +0,0 @@
-gone
diff --git a/docs/guide.md b/docs/moved/guide.md
similarity index 90%
rename from docs/guide.md
rename to docs/moved/guide.md
index 3333333..4444444 100644
--- a/docs/guide.md
+++ b/docs/moved/guide.md
@@ -1,2 +1,2 @@
 # Guide
-old line
+new line
diff --git a/img.png b/img.png
new file mode 100644
index 0000000..5555555
Binary files /dev/null and b/img.png differ
diff --git a/script.sh b/script.sh
old mode 100644
new mode 100755
diff --git "a/sp ace/f\\303\\274le.txt" "b/sp ace/f\\303\\274le.txt"
index 1..2 100644
--- "a/sp ace/f\\303\\274le.txt"
+++ "b/sp ace/f\\303\\274le.txt"
@@ -1 +1 @@
-a
+b
`

describe('parseUnifiedDiff', () => {
  const files = parseUnifiedDiff(SAMPLE)

  it('parses every file kind', () => {
    expect(files.map((f) => [f.path, f.status, f.binary])).toEqual([
      ['src/a.ts', 'modified', false],
      ['new.txt', 'added', false],
      ['old.txt', 'deleted', false],
      ['docs/moved/guide.md', 'renamed', false],
      ['img.png', 'added', true],
      ['script.sh', 'mode', false],
      ['sp ace/f\\303\\274le.txt', 'modified', false],
    ])
  })

  it('tracks line numbers on both sides', () => {
    const h = files[0]!.hunks[0]!
    expect(h.oldStart).toBe(1)
    expect(h.newStart).toBe(1)
    expect(h.section).toBe('export function a() {')
    expect(h.lines.map((l) => [l.type, l.oldNo, l.newNo])).toEqual([
      ['context', 1, 1],
      ['del', 2, null],
      ['add', null, 2],
      ['add', null, 3],
      ['context', 3, 4],
      ['context', 4, 5],
    ])
    expect(files[0]!.additions).toBe(2)
    expect(files[0]!.deletions).toBe(1)
  })

  it('records the missing trailing newline', () => {
    const last = files[1]!.hunks[0]!.lines.at(-1)!
    expect(last.text).toBe('world')
    expect(last.noNewline).toBe(true)
  })

  it('keeps rename metadata and similarity', () => {
    const r = files[3]!
    expect(r.oldPath).toBe('docs/guide.md')
    expect(r.newPath).toBe('docs/moved/guide.md')
    expect(r.similarity).toBe(90)
  })

  it('marks binary files and mode changes without hunks', () => {
    expect(files[4]!.hunks).toHaveLength(0)
    expect(files[5]!.oldMode).toBe('100644')
    expect(files[5]!.newMode).toBe('100755')
  })

  it('handles an empty diff and garbage', () => {
    expect(parseUnifiedDiff('')).toEqual([])
    expect(parseUnifiedDiff('not a diff\nat all\n')).toEqual([])
  })

  it('sums stats', () => {
    expect(diffStats(files)).toEqual({ files: 7, additions: 6, deletions: 4 })
  })

  it('reconstructs side texts for highlighting', () => {
    const s = sideTexts(files[0]!)
    expect(s.oldText.split('\n')).toEqual(['const x = 1', 'const y = 2', 'return x', '}'])
    expect(s.newText.split('\n')).toEqual(['const x = 1', 'const y = 3', 'const z = 4', 'return x', '}'])
    expect(s.newLineNos).toEqual([1, 2, 3, 4, 5])
  })

  it('parses headers with spaces and quotes', () => {
    expect(parseGitHeader('diff --git a/x y/z.txt b/x y/z.txt')).toEqual({ a: 'x y/z.txt', b: 'x y/z.txt' })
    expect(parseGitHeader('diff --git "a/q\\"t.txt" "b/q\\"t.txt"')).toEqual({ a: 'q"t.txt', b: 'q"t.txt' })
  })

  it('parses a synthetic 20,000-line diff quickly', () => {
    const text = syntheticDiff(50, 400)
    const t0 = performance.now()
    const parsed = parseUnifiedDiff(text)
    const ms = performance.now() - t0
    expect(parsed).toHaveLength(50)
    expect(parsed.reduce((n, f) => n + f.lineCount, 0)).toBe(20_000)
    expect(ms).toBeLessThan(200)
  })
})
