// Syntax highlighting without an editor: the lezer parser for the file's
// language (loaded lazily from @codemirror/language-data, one chunk per
// language) produces a tree; highlightTree maps it to class names per line.
// Used by the diff rows and the file viewer. Results are cached per text.

import { LanguageDescription, type Language } from '@codemirror/language'
import { languages } from '@codemirror/language-data'
import { highlightTree, tags, Tag, tagHighlighter } from '@lezer/highlight'

export type Span = { text: string; cls?: string }

const highlighter = tagHighlighter([
  { tag: [tags.keyword, tags.modifier, tags.controlKeyword, tags.operatorKeyword, tags.definitionKeyword, tags.moduleKeyword], class: 'tok-keyword' },
  { tag: [tags.string, tags.special(tags.string), tags.character, tags.docString, tags.attributeValue], class: 'tok-string' },
  { tag: [tags.number, tags.integer, tags.float, tags.bool, tags.atom, tags.null], class: 'tok-number' },
  { tag: [tags.comment, tags.lineComment, tags.blockComment], class: 'tok-comment' },
  { tag: [tags.definition(tags.variableName), tags.function(tags.variableName), tags.function(tags.propertyName), tags.definition(tags.propertyName)], class: 'tok-def' },
  { tag: [tags.typeName, tags.className, tags.namespace, tags.tagName, tags.standard(tags.typeName)], class: 'tok-type' },
  { tag: [tags.propertyName, tags.attributeName, tags.labelName, tags.macroName], class: 'tok-prop' },
  { tag: [tags.operator, tags.punctuation, tags.bracket, tags.derefOperator], class: 'tok-op' },
  { tag: [tags.meta, tags.processingInstruction, tags.annotation, tags.heading, tags.url, tags.link], class: 'tok-meta' },
  { tag: [tags.invalid], class: 'tok-invalid' },
])

const langCache = new Map<string, Promise<Language | null>>()

export function languageFor(filename: string): LanguageDescription | null {
  const name = filename.split('/').pop() ?? filename
  return LanguageDescription.matchFilename(languages, name) ?? null
}

export function loadLanguage(filename: string): Promise<Language | null> {
  const desc = languageFor(filename)
  if (!desc) return Promise.resolve(null)
  let p = langCache.get(desc.name)
  if (!p) {
    p = desc
      .load()
      .then((s) => s.language)
      .catch(() => null)
    langCache.set(desc.name, p)
  }
  return p
}

const MAX_HIGHLIGHT_BYTES = 1_500_000

/** Highlights `text` into per-line spans. Lines beyond the limit come back unstyled. */
export function highlightToLines(language: Language | null, text: string): Span[][] {
  const lines = text.split('\n')
  if (!language || text.length > MAX_HIGHLIGHT_BYTES) return lines.map((l) => [{ text: l }])
  const tree = language.parser.parse(text)
  const out: Span[][] = lines.map(() => [])
  // Map absolute offsets to (line, column) incrementally.
  let lineIdx = 0
  let lineStart = 0
  let pos = 0
  const advanceTo = (to: number) => {
    while (lineIdx < lines.length && to > lineStart + lines[lineIdx]!.length) {
      lineStart += lines[lineIdx]!.length + 1
      lineIdx++
    }
  }
  const emit = (from: number, to: number, cls?: string) => {
    while (from < to) {
      advanceTo(from)
      if (lineIdx >= lines.length) return
      const lineEnd = lineStart + lines[lineIdx]!.length
      const segEnd = Math.min(to, lineEnd)
      if (segEnd > from) out[lineIdx]!.push({ text: text.slice(from, segEnd), cls })
      if (to > lineEnd) {
        // cross the newline
        from = lineEnd + 1
        lineStart = lineEnd + 1
        lineIdx++
      } else {
        from = segEnd
      }
    }
  }
  highlightTree(tree, highlighter, (from, to, classes) => {
    if (from > pos) emit(pos, from)
    emit(from, to, classes)
    pos = to
  })
  if (pos < text.length) emit(pos, text.length)
  // Preserve empty lines as a single empty span so rows keep height.
  for (let i = 0; i < out.length; i++) if (out[i]!.length === 0) out[i] = [{ text: lines[i] ?? '' }]
  return out
}

const resultCache = new Map<string, Span[][]>()
let cacheBytes = 0

/** Async, cached highlighting keyed by filename and content. */
export async function highlightFile(filename: string, text: string): Promise<Span[][]> {
  const key = `${filename}\u0000${text.length}\u0000${hash(text)}`
  const hit = resultCache.get(key)
  if (hit) return hit
  const lang = await loadLanguage(filename)
  const res = highlightToLines(lang, text)
  if (cacheBytes > 20_000_000) {
    resultCache.clear()
    cacheBytes = 0
  }
  resultCache.set(key, res)
  cacheBytes += text.length
  return res
}

function hash(s: string): number {
  let h = 2166136261
  const step = Math.max(1, Math.floor(s.length / 4096))
  for (let i = 0; i < s.length; i += step) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 16777619)
  }
  return h >>> 0
}

export { Tag }
