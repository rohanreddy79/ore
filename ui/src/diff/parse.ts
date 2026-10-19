// A unified-diff parser for git output (git diff, diff-tree -p, interdiff).
// Pure and allocation-light: 20,000 lines parse in a few milliseconds.

export type DiffLineType = 'context' | 'add' | 'del'

export type DiffLine = {
  type: DiffLineType
  /** 1-based line numbers; null on the side the line does not exist. */
  oldNo: number | null
  newNo: number | null
  text: string
  noNewline?: boolean
}

export type Hunk = {
  header: string
  oldStart: number
  oldLines: number
  newStart: number
  newLines: number
  section: string
  lines: DiffLine[]
}

export type FileStatus = 'added' | 'deleted' | 'modified' | 'renamed' | 'copied' | 'mode'

export type DiffFile = {
  /** The path to display (new path, or the old one for deletions). */
  path: string
  oldPath: string | null
  newPath: string | null
  status: FileStatus
  binary: boolean
  hunks: Hunk[]
  additions: number
  deletions: number
  oldMode?: string
  newMode?: string
  similarity?: number
  /** Total line rows across hunks (context + add + del). */
  lineCount: number
}

function unquote(p: string): string {
  if (p.startsWith('"') && p.endsWith('"')) {
    try {
      return JSON.parse(p) as string
    } catch {
      return p.slice(1, -1)
    }
  }
  return p
}

/** Parses the `diff --git a/x b/y` header into the two paths, handling quotes and spaces. */
export function parseGitHeader(line: string): { a: string; b: string } | null {
  const rest = line.slice('diff --git '.length)
  if (rest.startsWith('"')) {
    // "a/path with spaces" "b/path with spaces"
    const m = rest.match(/^("(?:[^"\\]|\\.)*")\s+("(?:[^"\\]|\\.)*"|\S.*)$/)
    if (!m) return null
    return { a: strip(unquote(m[1]!)), b: strip(unquote(m[2]!)) }
  }
  // Paths may contain spaces; a/ and b/ prefixes anchor the split.
  const idx = rest.indexOf(' b/')
  if (idx >= 0 && rest.startsWith('a/')) {
    return { a: rest.slice(2, idx), b: rest.slice(idx + 3) }
  }
  const parts = rest.split(' ')
  if (parts.length >= 2) return { a: strip(parts[0]!), b: strip(parts.slice(1).join(' ')) }
  return null
}

function strip(p: string): string {
  return p.replace(/^[ab]\//, '')
}

export function parseUnifiedDiff(text: string): DiffFile[] {
  const files: DiffFile[] = []
  if (!text) return files
  const lines = text.split('\n')
  let i = 0
  let cur: DiffFile | null = null
  let hunk: Hunk | null = null
  let oldNo = 0
  let newNo = 0

  const finish = () => {
    if (cur) {
      cur.lineCount = cur.hunks.reduce((n, h) => n + h.lines.length, 0)
      if (cur.hunks.length === 0 && !cur.binary && cur.status === 'modified' && (cur.oldMode || cur.newMode)) cur.status = 'mode'
      files.push(cur)
    }
    cur = null
    hunk = null
  }

  while (i < lines.length) {
    const line = lines[i]!
    if (line.startsWith('diff --git ')) {
      finish()
      const h = parseGitHeader(line)
      cur = { path: h?.b ?? h?.a ?? '?', oldPath: h?.a ?? null, newPath: h?.b ?? null, status: 'modified', binary: false, hunks: [], additions: 0, deletions: 0, lineCount: 0 }
      i++
      continue
    }
    if (!cur) {
      // Diffs without the git header (plain `diff -u`): start at ---.
      if (line.startsWith('--- ') && lines[i + 1]?.startsWith('+++ ')) {
        const a = unquote(line.slice(4).split('\t')[0]!)
        const b = unquote(lines[i + 1]!.slice(4).split('\t')[0]!)
        cur = { path: strip(b === '/dev/null' ? a : b), oldPath: a === '/dev/null' ? null : strip(a), newPath: b === '/dev/null' ? null : strip(b), status: a === '/dev/null' ? 'added' : b === '/dev/null' ? 'deleted' : 'modified', binary: false, hunks: [], additions: 0, deletions: 0, lineCount: 0 }
        i += 2
        continue
      }
      i++
      continue
    }
    if (line.startsWith('@@')) {
      const m = line.match(/^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@ ?(.*)$/)
      if (m) {
        hunk = { header: line, oldStart: Number(m[1]), oldLines: m[2] === undefined ? 1 : Number(m[2]), newStart: Number(m[3]), newLines: m[4] === undefined ? 1 : Number(m[4]), section: m[5] ?? '', lines: [] }
        cur.hunks.push(hunk)
        oldNo = hunk.oldStart
        newNo = hunk.newStart
      }
      i++
      continue
    }
    if (hunk) {
      const c = line[0]
      if (c === ' ' || (line === '' && i < lines.length - 1)) {
        hunk.lines.push({ type: 'context', oldNo: oldNo++, newNo: newNo++, text: line.slice(1) })
        i++
        continue
      }
      if (c === '+') {
        hunk.lines.push({ type: 'add', oldNo: null, newNo: newNo++, text: line.slice(1) })
        cur.additions++
        i++
        continue
      }
      if (c === '-') {
        hunk.lines.push({ type: 'del', oldNo: oldNo++, newNo: null, text: line.slice(1) })
        cur.deletions++
        i++
        continue
      }
      if (c === '\\') {
        const last = hunk.lines[hunk.lines.length - 1]
        if (last) last.noNewline = true
        i++
        continue
      }
      // Anything else ends the hunk (next file header or garbage).
      hunk = null
      if (line === '') {
        i++
        continue
      }
      continue
    }
    // Extended headers between `diff --git` and the first hunk.
    if (line.startsWith('new file mode ')) {
      cur.status = 'added'
      cur.oldPath = null
      cur.newMode = line.slice(14)
    } else if (line.startsWith('deleted file mode ')) {
      cur.status = 'deleted'
      cur.newPath = null
      cur.path = cur.oldPath ?? cur.path
      cur.oldMode = line.slice(18)
    } else if (line.startsWith('old mode ')) cur.oldMode = line.slice(9)
    else if (line.startsWith('new mode ')) cur.newMode = line.slice(9)
    else if (line.startsWith('similarity index ')) cur.similarity = parseInt(line.slice(17), 10)
    else if (line.startsWith('rename from ')) {
      cur.status = 'renamed'
      cur.oldPath = unquote(line.slice(12))
    } else if (line.startsWith('rename to ')) {
      cur.status = 'renamed'
      cur.newPath = unquote(line.slice(10))
      cur.path = cur.newPath
    } else if (line.startsWith('copy from ')) {
      cur.status = 'copied'
      cur.oldPath = unquote(line.slice(10))
    } else if (line.startsWith('copy to ')) {
      cur.status = 'copied'
      cur.newPath = unquote(line.slice(8))
      cur.path = cur.newPath
    } else if (line.startsWith('Binary files ') || line.startsWith('GIT binary patch')) {
      cur.binary = true
    } else if (line.startsWith('--- ')) {
      const p = unquote(line.slice(4).split('\t')[0]!)
      if (p === '/dev/null') {
        cur.oldPath = null
        if (cur.status === 'modified') cur.status = 'added'
      }
    } else if (line.startsWith('+++ ')) {
      const p = unquote(line.slice(4).split('\t')[0]!)
      if (p === '/dev/null') {
        cur.newPath = null
        if (cur.status === 'modified') cur.status = 'deleted'
        cur.path = cur.oldPath ?? cur.path
      }
    }
    i++
  }
  finish()
  return files
}

export function diffStats(files: DiffFile[]): { files: number; additions: number; deletions: number } {
  let additions = 0
  let deletions = 0
  for (const f of files) {
    additions += f.additions
    deletions += f.deletions
  }
  return { files: files.length, additions, deletions }
}

/** Reconstructs the old and new text of a file from its hunks (context + one side). */
export function sideTexts(file: DiffFile): { oldText: string; newText: string; oldLineNos: number[]; newLineNos: number[] } {
  const oldL: string[] = []
  const newL: string[] = []
  const oldLineNos: number[] = []
  const newLineNos: number[] = []
  for (const h of file.hunks) {
    for (const l of h.lines) {
      if (l.type !== 'add') {
        oldL.push(l.text)
        oldLineNos.push(l.oldNo ?? 0)
      }
      if (l.type !== 'del') {
        newL.push(l.text)
        newLineNos.push(l.newNo ?? 0)
      }
    }
  }
  return { oldText: oldL.join('\n'), newText: newL.join('\n'), oldLineNos, newLineNos }
}

/** Builds a synthetic diff for tests and benchmarks: `files` files of `linesPerFile` lines. */
export function syntheticDiff(files: number, linesPerFile: number): string {
  const out: string[] = []
  for (let f = 0; f < files; f++) {
    const path = `src/module${f}/file${f}.ts`
    out.push(`diff --git a/${path} b/${path}`, `index 0000000..1111111 100644`, `--- a/${path}`, `+++ b/${path}`)
    const n = linesPerFile
    out.push(`@@ -1,${Math.ceil(n * 0.75)} +1,${Math.ceil(n * 0.75)} @@ function module${f}()`)
    for (let l = 0; l < n; l++) {
      const m = l % 4
      if (m === 0) out.push(` const x${l} = compute(${l});`)
      else if (m === 1) out.push(`-  return old(${l}, x${l});`)
      else if (m === 2) out.push(`+  return fresh(${l}, x${l}); // changed`)
      else out.push(` }`)
    }
  }
  return out.join('\n') + '\n'
}
