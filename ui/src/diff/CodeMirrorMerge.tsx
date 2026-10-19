import { useEffect, useRef, useState } from 'react'
import { EditorState, type Extension } from '@codemirror/state'
import { EditorView, lineNumbers, highlightActiveLine, drawSelection } from '@codemirror/view'
import { MergeView, unifiedMergeView } from '@codemirror/merge'
import { syntaxHighlighting, defaultHighlightStyle, bracketMatching } from '@codemirror/language'
import { loadLanguage } from './highlight'
import { effectiveScheme } from '../lib/theme'

/**
 * A full-file comparison on @codemirror/merge: side by side (MergeView) or
 * unified (unifiedMergeView), read-only, with the file's language loaded
 * lazily. The diff viewer opens it per file; the editor virtualizes its own
 * DOM so even very large files stay responsive.
 */
export function CodeMirrorMerge({ original, modified, filename, unified, height = '70vh' }: { original: string; modified: string; filename: string; unified?: boolean; height?: string }) {
  const host = useRef<HTMLDivElement>(null)
  const [lang, setLang] = useState<Extension | null | undefined>(undefined)

  useEffect(() => {
    let alive = true
    loadLanguage(filename).then((l) => alive && setLang(l ?? null))
    return () => {
      alive = false
    }
  }, [filename])

  useEffect(() => {
    if (!host.current || lang === undefined) return
    const dark = effectiveScheme() === 'dark'
    const base: Extension[] = [
      lineNumbers(),
      highlightActiveLine(),
      drawSelection(),
      bracketMatching(),
      syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
      EditorView.editable.of(false),
      EditorState.readOnly.of(true),
      EditorView.theme({}, { dark }),
      EditorView.lineWrapping,
      ...(lang ? [lang] : []),
    ]
    let view: EditorView | null = null
    let merge: MergeView | null = null
    if (unified) {
      view = new EditorView({
        parent: host.current,
        state: EditorState.create({ doc: modified, extensions: [...base, unifiedMergeView({ original, mergeControls: false, highlightChanges: true, gutter: true, collapseUnchanged: { margin: 3, minSize: 4 } })] }),
      })
    } else {
      merge = new MergeView({
        parent: host.current,
        a: { doc: original, extensions: base },
        b: { doc: modified, extensions: base },
        highlightChanges: true,
        gutter: true,
        collapseUnchanged: { margin: 3, minSize: 4 },
        diffConfig: { scanLimit: 2000, timeout: 1500 },
      })
    }
    return () => {
      view?.destroy()
      merge?.destroy()
    }
  }, [original, modified, unified, lang])

  return <div ref={host} className="cm-merge-host" style={{ height }} data-testid="cm-merge" />
}
