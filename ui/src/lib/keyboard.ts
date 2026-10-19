import { useEffect, useRef, useState, type RefObject } from 'react'

/** True when the key event targets something that takes typing. */
export function isEditable(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null
  if (!el || !el.tagName) return false
  const tag = el.tagName.toLowerCase()
  if (tag === 'input' || tag === 'textarea' || tag === 'select') return true
  if (el.isContentEditable) return true
  return !!el.closest?.('.cm-editor')
}

export type Combo = string // e.g. "mod+k", "g i", "j", "?"

const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)
export const modKeyLabel = isMac ? '⌘' : 'Ctrl'

function matchSingle(e: KeyboardEvent, combo: string): boolean {
  const parts = combo.toLowerCase().split('+')
  const key = parts[parts.length - 1] ?? ''
  const mod = parts.includes('mod')
  const shift = parts.includes('shift')
  const alt = parts.includes('alt')
  const wantMod = isMac ? e.metaKey : e.ctrlKey
  if (mod !== wantMod) return false
  if (shift !== e.shiftKey && key.length > 1) return false
  if (alt !== e.altKey) return false
  const k = e.key.toLowerCase()
  if (key === 'esc') return k === 'escape'
  return k === key
}

type Binding = { combo: Combo; handler: (e: KeyboardEvent) => void; allowInInput?: boolean }
const bindings = new Set<Binding>()
let pending: string | null = null
let pendingTimer: ReturnType<typeof setTimeout> | null = null
let installed = false

function install() {
  if (installed || typeof window === 'undefined') return
  installed = true
  window.addEventListener('keydown', (e) => {
    if (e.defaultPrevented) return
    const editable = isEditable(e.target)
    for (const b of bindings) {
      if (editable && !b.allowInInput && !b.combo.includes('mod+') && b.combo !== 'esc') continue
      const seq = b.combo.split(' ')
      if (seq.length === 2) {
        const [first, second] = seq as [string, string]
        if (pending === first && matchSingle(e, second)) {
          pending = null
          e.preventDefault()
          b.handler(e)
          return
        }
      } else if (matchSingle(e, b.combo)) {
        e.preventDefault()
        b.handler(e)
        return
      }
    }
    if (editable) return
    // Start a two-key sequence if any binding begins with this key.
    const k = e.key.toLowerCase()
    if (!e.metaKey && !e.ctrlKey && !e.altKey) {
      for (const b of bindings) {
        const seq = b.combo.split(' ')
        if (seq.length === 2 && seq[0] === k) {
          pending = k
          if (pendingTimer) clearTimeout(pendingTimer)
          pendingTimer = setTimeout(() => (pending = null), 1200)
          return
        }
      }
    }
  })
}

/** Registers a global shortcut for the component's lifetime. */
export function useHotkey(combo: Combo, handler: (e: KeyboardEvent) => void, opts: { allowInInput?: boolean; enabled?: boolean } = {}) {
  const ref = useRef(handler)
  ref.current = handler
  useEffect(() => {
    if (opts.enabled === false) return
    install()
    const b: Binding = { combo, handler: (e) => ref.current(e), allowInInput: opts.allowInInput }
    bindings.add(b)
    return () => {
      bindings.delete(b)
    }
  }, [combo, opts.allowInInput, opts.enabled])
}

/** j/k list navigation with Enter to open; returns the active index. */
export function useListNavigation(count: number, onOpen: (index: number) => void, containerRef?: RefObject<HTMLElement | null>): [number, (i: number) => void] {
  const [active, setActive] = useState(-1)
  useHotkey('j', () => setActive((a) => Math.min(a + 1, count - 1)))
  useHotkey('k', () => setActive((a) => Math.max(a - 1, 0)))
  useHotkey('enter', () => {
    if (active >= 0) onOpen(active)
  })
  useEffect(() => {
    if (active < 0) return
    const el = containerRef?.current?.querySelector<HTMLElement>(`[data-index="${active}"]`)
    el?.scrollIntoView?.({ block: 'nearest' })
    el?.focus?.({ preventScroll: true })
  }, [active, containerRef])
  useEffect(() => {
    if (active >= count) setActive(count - 1)
  }, [count, active])
  return [active, setActive]
}
