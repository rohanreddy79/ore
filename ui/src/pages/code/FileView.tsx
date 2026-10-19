import { useEffect, useMemo, useState } from 'react'
import { Code2, Download, FileText, Pencil, WrapText } from 'lucide-react'
import { Button, CopyButton, Segmented, cx } from '../../design'
import { highlightFile, type Span } from '../../diff/highlight'
import { fmtBytes, plural } from '../../lib/format'
import type { Blob } from '../../lib/types'
import { Markdown } from '../../shell/common'

const IMAGE_EXT = /\.(png|jpe?g|gif|webp|svg|ico|bmp|avif)$/i
const MD_EXT = /\.(md|markdown|mdx)$/i

/** Parses "#L12" and "#L12-L20" into a line range. */
export function parseLineHash(hash: string): [number, number] | null {
  const m = hash.match(/^#?L(\d+)(?:-L?(\d+))?$/)
  if (!m) return null
  const a = Number(m[1])
  const b = m[2] ? Number(m[2]) : a
  return [Math.min(a, b), Math.max(a, b)]
}

export function FileView({ blob, rawUrl, onEdit }: { blob: Blob; rawUrl: string; onEdit?: () => void }) {
  const isMd = MD_EXT.test(blob.path)
  const [mode, setMode] = useState<'rendered' | 'source'>(isMd ? 'rendered' : 'source')
  const [wrap, setWrap] = useState(false)
  const [spans, setSpans] = useState<Span[][] | null>(null)
  const [range, setRange] = useState<[number, number] | null>(() => parseLineHash(window.location.hash))

  useEffect(() => setMode(isMd ? 'rendered' : 'source'), [isMd, blob.path])
  useEffect(() => {
    setSpans(null)
    if (blob.binary) return
    let alive = true
    highlightFile(blob.path, blob.content).then((s) => alive && setSpans(s))
    return () => {
      alive = false
    }
  }, [blob])
  useEffect(() => {
    const on = () => setRange(parseLineHash(window.location.hash))
    window.addEventListener('hashchange', on)
    return () => window.removeEventListener('hashchange', on)
  }, [])
  useEffect(() => {
    if (!range || !spans) return
    document.getElementById(`L${range[0]}`)?.scrollIntoView?.({ block: 'center' })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [spans !== null])

  const lines = useMemo(() => blob.content.split('\n'), [blob.content])
  const lineCount = lines.length === 1 && lines[0] === '' ? 0 : lines[lines.length - 1] === '' ? lines.length - 1 : lines.length

  const clickLine = (n: number, e: React.MouseEvent) => {
    e.preventDefault()
    let next: [number, number]
    if (e.shiftKey && range) next = [Math.min(range[0], n), Math.max(range[1], n)]
    else next = [n, n]
    const hash = next[0] === next[1] ? `#L${next[0]}` : `#L${next[0]}-L${next[1]}`
    history.replaceState(null, '', hash)
    setRange(next)
  }

  return (
    <div className="card">
      <div className="file-toolbar">
        <span>
          {blob.binary ? fmtBytes(blob.size) : `${plural(lineCount, 'line')} · ${fmtBytes(blob.size)}`}
          {blob.truncated ? ' · truncated at 2 MB' : ''}
        </span>
        <div className="right">
          {isMd ? <Segmented value={mode} onChange={setMode} label="Markdown view" options={[{ value: 'rendered', label: <><FileText size={12} /> Rendered</> }, { value: 'source', label: <><Code2 size={12} /> Source</> }]} /> : null}
          {!blob.binary && mode === 'source' ? (
            <Button size="sm" variant="ghost" icon aria-pressed={wrap} onClick={() => setWrap((w) => !w)} aria-label="Toggle line wrap" title="Wrap lines">
              <WrapText />
            </Button>
          ) : null}
          {!blob.binary ? <CopyButton text={blob.content} label="Copy file contents" /> : null}
          <a className="btn ghost sm" href={rawUrl} target="_blank" rel="noreferrer" title="Raw">
            <Download /> Raw
          </a>
          {onEdit && !blob.binary ? (
            <Button size="sm" onClick={onEdit}>
              <Pencil /> Edit
            </Button>
          ) : null}
        </div>
      </div>
      {blob.binary ? (
        IMAGE_EXT.test(blob.path) ? (
          <div style={{ padding: 'var(--sp-4)', textAlign: 'center' }}>
            <img src={rawUrl} alt={blob.path} style={{ maxWidth: '100%', maxHeight: '70vh' }} />
          </div>
        ) : (
          <div className="empty compact">
            <h3>Binary file</h3>
            <p>{fmtBytes(blob.size)}. Download it with the Raw link.</p>
          </div>
        )
      ) : mode === 'rendered' ? (
        <div style={{ padding: 'var(--sp-4) var(--sp-5)' }}>
          <Markdown source={blob.content} />
        </div>
      ) : (
        <div className={cx('file-view', wrap && 'wrap')}>
          <table>
            <tbody>
              {lines.map((l, i) => {
                if (i === lines.length - 1 && l === '' && i > 0) return null
                const n = i + 1
                const inRange = range && n >= range[0] && n <= range[1]
                const sp = spans?.[i]
                return (
                  <tr key={n} id={`L${n}`} className={cx(inRange && 'hl')}>
                    <td className="ln">
                      <a href={`#L${n}`} onClick={(e) => clickLine(n, e)} style={{ color: 'inherit' }} aria-label={`Line ${n}`}>
                        {n}
                      </a>
                    </td>
                    <td className="code">
                      {sp
                        ? sp.map((s, k) => (
                            <span key={k} className={s.cls}>
                              {s.text}
                            </span>
                          ))
                        : l}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
