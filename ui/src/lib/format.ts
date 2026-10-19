const rtf = typeof Intl !== 'undefined' && 'RelativeTimeFormat' in Intl ? new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' }) : null

export function timeAgo(iso: string | undefined | null, now: number = Date.now()): string {
  if (!iso) return ''
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return iso
  const diff = (t - now) / 1000
  const abs = Math.abs(diff)
  const units: Array<[Intl.RelativeTimeFormatUnit, number]> = [
    ['year', 31536000],
    ['month', 2592000],
    ['week', 604800],
    ['day', 86400],
    ['hour', 3600],
    ['minute', 60],
  ]
  if (abs < 45) return 'just now'
  for (const [unit, secs] of units) {
    if (abs >= secs) {
      const v = Math.round(diff / secs)
      return rtf ? rtf.format(v, unit) : `${Math.abs(v)} ${unit}${Math.abs(v) === 1 ? '' : 's'} ago`
    }
  }
  const v = Math.round(diff)
  return rtf ? rtf.format(v, 'second') : `${Math.abs(v)} seconds ago`
}

export function fmtDate(iso: string | undefined | null): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

export function fmtTime(iso: string | undefined | null): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

export function shortOid(oid: string | undefined | null, n = 7): string {
  return (oid ?? '').slice(0, n)
}

export function shortId(id: string | undefined | null): string {
  if (!id) return ''
  const i = id.indexOf('_')
  return i >= 0 && id.length > i + 7 ? id.slice(0, i + 7) : id
}

export function fmtBytes(n: number | undefined): string {
  if (n === undefined || n === null) return ''
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`
}

export function fmtDuration(ms: number | undefined | null): string {
  if (ms === undefined || ms === null || Number.isNaN(ms)) return ''
  if (ms < 1000) return `${Math.round(ms)} ms`
  const s = ms / 1000
  if (s < 60) return `${s.toFixed(s < 10 ? 1 : 0)} s`
  const m = Math.floor(s / 60)
  const rs = Math.round(s % 60)
  if (m < 60) return `${m}m ${rs}s`
  const h = Math.floor(m / 60)
  return `${h}h ${m % 60}m`
}

export function fmtSeconds(s: number | undefined | null): string {
  if (s === undefined || s === null) return '—'
  return fmtDuration(s * 1000)
}

export function durationBetween(a: string | undefined | null, b: string | undefined | null): string {
  if (!a) return ''
  const t0 = Date.parse(a)
  const t1 = b ? Date.parse(b) : Date.now()
  if (Number.isNaN(t0) || Number.isNaN(t1)) return ''
  return fmtDuration(t1 - t0)
}

export function refName(ref: string | undefined | null): string {
  if (!ref) return ''
  return ref.replace(/^refs\/heads\//, '').replace(/^refs\/tags\//, '')
}

export function plural(n: number, one: string, many = one + 's'): string {
  return `${n} ${n === 1 ? one : many}`
}

export function levelName(levels: string[] | undefined, n: number): string {
  const names = levels && levels.length === 6 ? levels : ['manual', 'assist', 'author', 'land', 'ship', 'operate']
  return names[n] ?? `L${n}`
}

export const LEVEL_EXPLANATIONS: Record<number, { title: string; agents: string; humans: string }> = {
  0: { title: 'Manual', agents: 'do nothing; the forge is a tool', humans: 'do everything' },
  1: { title: 'Assist', agents: 'comment, review and suggest fixes', humans: 'author and merge' },
  2: { title: 'Author', agents: 'open changes with provenance, run pipelines and deploy previews', humans: 'review and merge' },
  3: { title: 'Land', agents: 'merge when verification is green and policy allows; deploy to non-production', humans: 'watch dashboards, set policy, handle escalations' },
  4: { title: 'Ship', agents: 'merge optimistically with post-merge verification; deploy to production through staged rollouts; auto-revert', humans: 'set budgets and SLOs; review weekly' },
  5: { title: 'Operate', agents: 'all of L4 plus open their own signals, update dependencies, fix security findings and refactor toward declared goals, within budget', humans: 'own the goals' },
}

export function tryJSON<T = unknown>(s: string | undefined | null, fallback: T): T {
  if (!s) return fallback
  try {
    return JSON.parse(s) as T
  } catch {
    return fallback
  }
}
