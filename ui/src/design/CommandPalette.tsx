import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Search } from 'lucide-react'
import { Dialog } from './overlay'
import { Kbd } from './primitives'

export type Command = {
  id: string
  label: string
  group: string
  icon?: ReactNode
  hint?: string
  keywords?: string
  run: () => void
}

/** Ranks commands against a query: prefix matches first, then substring, then fuzzy subsequence. Pure, for tests. */
export function rankCommands(cmds: Command[], query: string): Command[] {
  const q = query.trim().toLowerCase()
  if (!q) return cmds
  const scored: Array<{ c: Command; s: number }> = []
  for (const c of cmds) {
    const hay = `${c.label} ${c.keywords ?? ''} ${c.group}`.toLowerCase()
    const label = c.label.toLowerCase()
    let s = -1
    if (label.startsWith(q)) s = 0
    else if (label.includes(q)) s = 1
    else if (hay.includes(q)) s = 2
    else if (isSubsequence(q, label)) s = 3
    if (s >= 0) scored.push({ c, s })
  }
  scored.sort((a, b) => a.s - b.s || a.c.label.localeCompare(b.c.label))
  return scored.map((x) => x.c)
}

function isSubsequence(q: string, s: string): boolean {
  let i = 0
  for (const ch of s) if (ch === q[i]) i++
  return i === q.length
}

/** Reducer-style keyboard model, exported so it can be unit-tested without a DOM. */
export function paletteKey(key: string, index: number, count: number): { index: number; action?: 'run' | 'close' } {
  if (count === 0) return { index: 0, action: key === 'Escape' ? 'close' : key === 'Enter' ? undefined : undefined }
  switch (key) {
    case 'ArrowDown':
      return { index: (index + 1) % count }
    case 'ArrowUp':
      return { index: (index - 1 + count) % count }
    case 'Home':
      return { index: 0 }
    case 'End':
      return { index: count - 1 }
    case 'Enter':
      return { index, action: 'run' }
    case 'Escape':
      return { index, action: 'close' }
    default:
      return { index }
  }
}

export function CommandPalette({ open, onClose, commands, search, placeholder = 'Search or jump to…' }: { open: boolean; onClose: () => void; commands: Command[]; search?: (q: string) => Promise<Command[]>; placeholder?: string }) {
  const [q, setQ] = useState('')
  const [index, setIndex] = useState(0)
  const [remote, setRemote] = useState<Command[]>([])
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (open) {
      setQ('')
      setIndex(0)
      setRemote([])
      setTimeout(() => inputRef.current?.focus(), 0)
    }
  }, [open])

  useEffect(() => {
    if (!open || !search || q.trim().length < 2) {
      setRemote([])
      return
    }
    let alive = true
    const t = setTimeout(() => {
      search(q)
        .then((r) => alive && setRemote(r))
        .catch(() => alive && setRemote([]))
    }, 120)
    return () => {
      alive = false
      clearTimeout(t)
    }
  }, [q, open, search])

  const items = useMemo(() => {
    const local = rankCommands(commands, q)
    const seen = new Set(local.map((c) => c.id))
    return [...local, ...remote.filter((c) => !seen.has(c.id))]
  }, [commands, remote, q])

  useEffect(() => setIndex(0), [q])
  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>(`[data-index="${index}"]`)
    el?.scrollIntoView?.({ block: 'nearest' })
  }, [index])

  const groups = useMemo(() => {
    const out: Array<{ group: string; items: Array<{ c: Command; i: number }> }> = []
    items.forEach((c, i) => {
      let g = out.find((x) => x.group === c.group)
      if (!g) {
        g = { group: c.group, items: [] }
        out.push(g)
      }
      g.items.push({ c, i })
    })
    return out
  }, [items])

  const onKeyDown = (e: React.KeyboardEvent) => {
    const r = paletteKey(e.key, index, items.length)
    if (r.index !== index) {
      e.preventDefault()
      setIndex(r.index)
    }
    if (r.action === 'run') {
      e.preventDefault()
      const c = items[index]
      if (c) {
        onClose()
        c.run()
      }
    } else if (r.action === 'close') {
      e.preventDefault()
      onClose()
    }
  }

  return (
    <Dialog open={open} onClose={onClose} title="Command palette" className="palette">
      <div style={{ margin: 'calc(-1 * var(--sp-2)) calc(-1 * var(--sp-4)) calc(-1 * var(--sp-4))' }}>
        <div className="palette-input">
          <Search aria-hidden />
          <input ref={inputRef} value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={onKeyDown} placeholder={placeholder} aria-label="Search or jump to" role="combobox" aria-expanded aria-controls="palette-list" aria-activedescendant={items[index] ? `palette-${items[index].id}` : undefined} autoComplete="off" spellCheck={false} />
          <Kbd>esc</Kbd>
        </div>
        <div className="palette-list" id="palette-list" role="listbox" ref={listRef}>
          {items.length === 0 ? <div className="group-label">No matches</div> : null}
          {groups.map((g) => (
            <div key={g.group}>
              <div className="group-label">{g.group}</div>
              {g.items.map(({ c, i }) => (
                <div
                  key={c.id}
                  id={`palette-${c.id}`}
                  className="item"
                  role="option"
                  aria-selected={i === index}
                  data-index={i}
                  onMouseEnter={() => setIndex(i)}
                  onClick={() => {
                    onClose()
                    c.run()
                  }}
                >
                  {c.icon}
                  <span className="label truncate">{c.label}</span>
                  {c.hint ? <span className="hint">{c.hint}</span> : null}
                </div>
              ))}
            </div>
          ))}
        </div>
        <div className="palette-foot">
          <span>
            <Kbd>↑</Kbd>
            <Kbd>↓</Kbd> navigate
          </span>
          <span>
            <Kbd>↵</Kbd> open
          </span>
          <span>
            <Kbd>esc</Kbd> close
          </span>
        </div>
      </div>
    </Dialog>
  )
}
