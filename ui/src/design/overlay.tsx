import { createContext, useCallback, useContext, useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react'
import { X } from 'lucide-react'
import { Button, cx } from './primitives'

/* ---------- Menu (dropdown) ---------- */

export type MenuItem =
  | { kind?: 'item'; label: ReactNode; desc?: ReactNode; icon?: ReactNode; onSelect?: () => void; href?: string; disabled?: boolean; danger?: boolean }
  | { kind: 'sep' }
  | { kind: 'label'; label: ReactNode }

export function Menu({ trigger, items, align = 'left', label }: { trigger: (props: { open: boolean; toggle: () => void; 'aria-haspopup': 'menu'; 'aria-expanded': boolean }) => ReactNode; items: MenuItem[]; align?: 'left' | 'right'; label: string }) {
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)
  const ref = useRef<HTMLDivElement>(null)
  const selectable = useMemo(() => items.map((it, i) => ({ it, i })).filter(({ it }) => !it.kind || it.kind === 'item'), [items])

  useEffect(() => {
    if (!open) return
    const onDoc = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', onDoc)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDoc)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setActive((a) => Math.min(a + 1, selectable.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActive((a) => Math.max(a - 1, 0))
    } else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      const sel = selectable[active]
      if (sel && sel.it.kind !== 'sep' && sel.it.kind !== 'label' && !sel.it.disabled) {
        sel.it.onSelect?.()
        setOpen(false)
      }
    }
  }

  return (
    <div className="menu-anchor" ref={ref} onKeyDown={onKeyDown}>
      {trigger({ open, toggle: () => setOpen((o) => !o), 'aria-haspopup': 'menu', 'aria-expanded': open })}
      {open ? (
        <div className={cx('menu', align === 'right' && 'right')} role="menu" aria-label={label}>
          {items.map((it, i) => {
            if (it.kind === 'sep') return <div key={i} className="menu-sep" role="separator" />
            if (it.kind === 'label')
              return (
                <div key={i} className="menu-label">
                  {it.label}
                </div>
              )
            const idx = selectable.findIndex((s) => s.i === i)
            const common = {
              role: 'menuitem' as const,
              className: 'menu-item',
              'data-active': idx === active || undefined,
              'aria-disabled': it.disabled || undefined,
              onMouseEnter: () => setActive(idx),
              style: it.danger ? { color: 'var(--danger-fg)' } : undefined,
            }
            if (it.href) {
              return (
                <a key={i} href={it.href} {...common} onClick={() => setOpen(false)}>
                  {it.icon}
                  <span>
                    {it.label}
                    {it.desc ? <span className="desc">{it.desc}</span> : null}
                  </span>
                </a>
              )
            }
            return (
              <button
                key={i}
                type="button"
                {...common}
                onClick={() => {
                  if (it.disabled) return
                  it.onSelect?.()
                  setOpen(false)
                }}
              >
                {it.icon}
                <span>
                  {it.label}
                  {it.desc ? <span className="desc">{it.desc}</span> : null}
                </span>
              </button>
            )
          })}
        </div>
      ) : null}
    </div>
  )
}

/* ---------- Dialog ---------- */

export function Dialog({ open, onClose, title, children, foot, wide, className }: { open: boolean; onClose: () => void; title: ReactNode; children: ReactNode; foot?: ReactNode; wide?: boolean; className?: string }) {
  const ref = useRef<HTMLDialogElement>(null)
  const titleId = useId()
  useEffect(() => {
    const el = ref.current
    if (!el) return
    if (open && !el.open) {
      try {
        el.showModal()
      } catch {
        el.setAttribute('open', '')
      }
    } else if (!open && el.open) {
      el.close()
    }
  }, [open])
  return (
    <dialog
      ref={ref}
      className={cx('dialog', wide && 'wide', className)}
      aria-labelledby={titleId}
      onClose={onClose}
      onCancel={(e) => {
        e.preventDefault()
        onClose()
      }}
      onClick={(e) => {
        if (e.target === ref.current) onClose()
      }}
    >
      {open ? (
        <>
          <div className="dialog-head">
            <h2 id={titleId}>{title}</h2>
            <Button variant="ghost" icon size="sm" aria-label="Close" onClick={onClose}>
              <X />
            </Button>
          </div>
          <div className="dialog-body">{children}</div>
          {foot ? <div className="dialog-foot">{foot}</div> : null}
        </>
      ) : null}
    </dialog>
  )
}

/* ---------- Toasts ---------- */

export type Toast = { id: number; title: string; body?: string; tone?: 'ok' | 'danger' | 'warn' | 'info'; action?: { label: string; onClick: () => void }; sticky?: boolean }

type ToastApi = {
  push: (t: Omit<Toast, 'id'>) => number
  dismiss: (id: number) => void
}

const ToastCtx = createContext<ToastApi | null>(null)

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])
  const seq = useRef(0)
  const dismiss = useCallback((id: number) => setToasts((ts) => ts.filter((t) => t.id !== id)), [])
  const push = useCallback(
    (t: Omit<Toast, 'id'>) => {
      const id = ++seq.current
      setToasts((ts) => [...ts.slice(-4), { ...t, id }])
      if (!t.sticky) setTimeout(() => dismiss(id), t.tone === 'danger' ? 8000 : 4500)
      return id
    },
    [dismiss],
  )
  const api = useMemo(() => ({ push, dismiss }), [push, dismiss])
  return (
    <ToastCtx.Provider value={api}>
      {children}
      <div className="toasts" aria-live="polite" aria-atomic="false">
        {toasts.map((t) => (
          <div key={t.id} className={cx('toast', t.tone)} role={t.tone === 'danger' ? 'alert' : 'status'}>
            <div className="grow">
              <div className="title">{t.title}</div>
              {t.body ? <div className="body">{t.body}</div> : null}
              {t.action ? (
                <div className="actions">
                  <Button
                    size="sm"
                    onClick={() => {
                      t.action?.onClick()
                      dismiss(t.id)
                    }}
                  >
                    {t.action.label}
                  </Button>
                </div>
              ) : null}
            </div>
            <Button variant="ghost" icon size="sm" aria-label="Dismiss" onClick={() => dismiss(t.id)}>
              <X />
            </Button>
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  )
}

export function useToast(): ToastApi {
  const ctx = useContext(ToastCtx)
  if (!ctx) throw new Error('useToast outside ToastProvider')
  return ctx
}

/** A module-level hook so non-React code (the API client) can raise toasts. */
let globalToast: ToastApi | null = null
export function ToastBridge() {
  const api = useToast()
  useEffect(() => {
    globalToast = api
    return () => {
      globalToast = null
    }
  }, [api])
  return null
}
export function toast(t: Omit<Toast, 'id'>): void {
  globalToast?.push(t)
}
