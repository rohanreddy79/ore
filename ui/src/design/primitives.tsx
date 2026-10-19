import { forwardRef, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes, type TextareaHTMLAttributes } from 'react'
import { Bot, Check, Copy, Cpu, User } from 'lucide-react'
import { useState } from 'react'

export function cx(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(' ')
}

/* ---------- Button ---------- */

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: 'default' | 'primary' | 'danger' | 'ghost'
  size?: 'sm' | 'md' | 'lg'
  icon?: boolean
  loading?: boolean
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'default', size = 'md', icon, loading, className, children, disabled, type = 'button', ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      className={cx('btn', variant !== 'default' && variant, size !== 'md' && size, icon && 'icon', className)}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      {...rest}
    >
      {loading ? <span className="spinner" aria-hidden /> : null}
      {children}
    </button>
  )
})

/* ---------- Inputs ---------- */

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement> & { size?: never; small?: boolean }>(function Input(
  { className, small, ...rest },
  ref,
) {
  return <input ref={ref} className={cx('input', small && 'sm', className)} {...rest} />
})

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaHTMLAttributes<HTMLTextAreaElement> & { mono?: boolean }>(function Textarea(
  { className, mono, ...rest },
  ref,
) {
  return <textarea ref={ref} className={cx('textarea', mono && 'mono', className)} {...rest} />
})

export const Select = forwardRef<HTMLSelectElement, SelectHTMLAttributes<HTMLSelectElement> & { small?: boolean }>(function Select(
  { className, small, ...rest },
  ref,
) {
  return <select ref={ref} className={cx('select', small && 'sm', className)} {...rest} />
})

export function Field({ label, hint, error, htmlFor, children }: { label: string; hint?: ReactNode; error?: string; htmlFor?: string; children: ReactNode }) {
  return (
    <div className="field">
      <label htmlFor={htmlFor}>{label}</label>
      {children}
      {hint ? <div className="hint">{hint}</div> : null}
      {error ? (
        <div className="error" role="alert">
          {error}
        </div>
      ) : null}
    </div>
  )
}

export function Checkbox({ label, ...rest }: InputHTMLAttributes<HTMLInputElement> & { label: ReactNode }) {
  return (
    <label className="checkbox">
      <input type="checkbox" {...rest} />
      <span>{label}</span>
    </label>
  )
}

/* ---------- Kbd ---------- */

export function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="kbd">{children}</kbd>
}

/* ---------- Pill ---------- */

export type Tone = 'neutral' | 'ok' | 'danger' | 'warn' | 'info' | 'agent' | 'bot' | 'accent'

export function Pill({ tone = 'neutral', outline, small, dot, pulse, children, title, className }: { tone?: Tone; outline?: boolean; small?: boolean; dot?: boolean; pulse?: boolean; children: ReactNode; title?: string; className?: string }) {
  return (
    <span className={cx('pill', tone !== 'neutral' && tone, outline && 'outline', small && 'sm', className)} title={title}>
      {dot ? <span className={cx('dot', pulse && 'pulse')} aria-hidden /> : null}
      {children}
    </span>
  )
}

/** Maps every status string the API produces to a tone. */
export function statusTone(status: string | undefined): Tone {
  switch (status) {
    case 'succeeded':
    case 'merged':
    case 'completed':
    case 'landed':
    case 'approve':
    case 'approved':
    case 'open':
    case 'active':
      return 'ok'
    case 'failed':
    case 'error':
    case 'stopped':
    case 'rejected':
    case 'refused':
    case 'reverted':
    case 'aborted':
    case 'request_changes':
    case 'revoked':
      return 'danger'
    case 'running':
    case 'started':
    case 'queued':
    case 'verifying':
    case 'landing':
    case 'awaiting_approval':
    case 'awaiting-human':
    case 'requested':
    case 'blocked':
      return 'warn'
    case 'closed':
    case 'rolled_back':
      return 'neutral'
    default:
      return 'neutral'
  }
}

export function StatusPill({ status, small }: { status: string | undefined; small?: boolean }) {
  const live = status === 'running' || status === 'started' || status === 'queued' || status === 'verifying' || status === 'landing'
  return (
    <Pill tone={statusTone(status)} small={small} dot pulse={live}>
      {(status ?? 'unknown').replace(/_/g, ' ')}
    </Pill>
  )
}

/* ---------- Avatar and actors ---------- */

export type ActorLike = { kind?: string; id?: string } | string | undefined | null

export function actorParts(a: ActorLike): { kind: string; id: string } {
  if (!a) return { kind: 'human', id: 'unknown' }
  if (typeof a === 'string') {
    const i = a.indexOf('/')
    return i > 0 ? { kind: a.slice(0, i), id: a.slice(i + 1) } : { kind: 'human', id: a }
  }
  let id = a.id ?? 'unknown'
  const kind = a.kind ?? 'human'
  // Identity writes actor ids as "human/alice"; strip the duplicate prefix.
  if (id.startsWith(kind + '/')) id = id.slice(kind.length + 1)
  return { kind, id }
}

export function Avatar({ actor, size }: { actor: ActorLike; size?: 'sm' | 'lg' }) {
  const { kind, id } = actorParts(actor)
  const isAgent = kind === 'agent'
  const isBot = kind === 'app' || kind === 'system' || kind === 'bot'
  const initials = id.replace(/^github:/, '').slice(0, 2)
  return (
    <span className={cx('avatar', size, isAgent && 'agent', isBot && 'bot')} aria-hidden title={`${kind}/${id}`}>
      {isAgent ? <Cpu /> : isBot ? <Bot /> : initials || <User />}
    </span>
  )
}

export function ActorChip({ actor, size }: { actor: ActorLike; size?: 'sm' }) {
  const { kind, id } = actorParts(actor)
  const label = kind === 'human' ? id : `${id}`
  return (
    <span className="actor" title={`${kind}/${id}`}>
      <Avatar actor={actor} size={size} />
      <span className="name">{label}</span>
      {kind !== 'human' ? (
        <Pill tone={kind === 'agent' ? 'agent' : 'bot'} small>
          {kind}
        </Pill>
      ) : null}
    </span>
  )
}

/* ---------- Card ---------- */

export function Card({ title, actions, children, flat, className, tight, foot }: { title?: ReactNode; actions?: ReactNode; children: ReactNode; flat?: boolean; className?: string; tight?: boolean; foot?: ReactNode }) {
  return (
    <section className={cx('card', flat && 'flat', className)}>
      {title || actions ? (
        <header className="card-head">
          <h3>{title}</h3>
          {actions ? <div className="row">{actions}</div> : null}
        </header>
      ) : null}
      <div className={cx('card-body', tight && 'tight')}>{children}</div>
      {foot ? <footer className="card-foot">{foot}</footer> : null}
    </section>
  )
}

export function Stat({ label, value, delta }: { label: string; value: ReactNode; delta?: ReactNode }) {
  return (
    <div className="card stat">
      <div className="label">{label}</div>
      <div className="value">{value}</div>
      {delta ? <div className="delta">{delta}</div> : null}
    </div>
  )
}

/* ---------- Empty state ---------- */

export function EmptyState({ icon, title, children, command, actions, compact }: { icon?: ReactNode; title: string; children?: ReactNode; command?: string; actions?: ReactNode; compact?: boolean }) {
  return (
    <div className={cx('empty', compact && 'compact')} role="status">
      {icon ? <span className="art">{icon}</span> : null}
      <h3>{title}</h3>
      {children ? <p>{children}</p> : null}
      {command ? <CopyBlock text={command} /> : null}
      {actions ? <div className="row" style={{ marginTop: 'var(--sp-3)' }}>{actions}</div> : null}
    </div>
  )
}

/* ---------- Skeleton ---------- */

export function Skeleton({ width, height, style }: { width?: string | number; height?: string | number; style?: React.CSSProperties }) {
  return <span className="skeleton" style={{ width, height, ...style }} aria-hidden />
}

export function SkeletonRows({ rows = 4 }: { rows?: number }) {
  return (
    <div className="skeleton-rows" aria-busy aria-label="Loading">
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} width={`${60 + ((i * 17) % 35)}%`} />
      ))}
    </div>
  )
}

export function Spinner({ label = 'Loading' }: { label?: string }) {
  return <span className="spinner" role="status" aria-label={label} />
}

/* ---------- Copy ---------- */

export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [done, setDone] = useState(false)
  return (
    <Button
      size="sm"
      variant="ghost"
      icon
      aria-label={done ? 'Copied' : label}
      title={done ? 'Copied' : label}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text)
          setDone(true)
          setTimeout(() => setDone(false), 1500)
        } catch {
          /* clipboard unavailable: nothing to do */
        }
      }}
    >
      {done ? <Check /> : <Copy />}
    </Button>
  )
}

export function CopyBlock({ text, mono = true }: { text: string; mono?: boolean }) {
  return (
    <div className={cx('copybox', !mono && 'sans')}>
      <code>{text}</code>
      <CopyButton text={text} />
    </div>
  )
}

/* ---------- Callout ---------- */

export function Callout({ tone = 'neutral', icon, children }: { tone?: 'neutral' | 'info' | 'warn' | 'danger' | 'agent'; icon?: ReactNode; children: ReactNode }) {
  return (
    <div className={cx('callout', tone !== 'neutral' && tone)} role={tone === 'danger' ? 'alert' : undefined}>
      {icon}
      <div className="grow">{children}</div>
    </div>
  )
}

/* ---------- Segmented control ---------- */

export function Segmented<T extends string>({ value, onChange, options, label }: { value: T; onChange: (v: T) => void; options: Array<{ value: T; label: ReactNode }>; label: string }) {
  return (
    <div className="segmented" role="group" aria-label={label}>
      {options.map((o) => (
        <button key={o.value} type="button" aria-pressed={o.value === value} onClick={() => onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </div>
  )
}
