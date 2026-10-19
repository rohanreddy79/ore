// The API client: same-origin fetch with the session cookie, JSON by default,
// typed errors, a single place that notices 401 (redirect to sign-in) and
// 5xx (a toast with a retry).

export class ApiError extends Error {
  status: number
  body: unknown
  retry?: () => Promise<unknown>
  constructor(status: number, message: string, body?: unknown) {
    super(message)
    this.status = status
    this.body = body
  }
  get notFound() {
    return this.status === 404
  }
}

type Listener = (e: ApiError) => void
const listeners = new Set<Listener>()

/** Subscribe to API failures (the shell shows toasts and handles 401). */
export function onApiError(fn: Listener): () => void {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

export type ApiInit = Omit<RequestInit, 'body'> & { body?: unknown; raw?: boolean; quiet?: boolean; signal?: AbortSignal }

export async function parseError(res: Response): Promise<ApiError> {
  let message = `${res.status} ${res.statusText}`
  let body: unknown
  try {
    const text = await res.text()
    try {
      body = JSON.parse(text)
      const m = (body as { error?: string })?.error
      if (m) message = m
    } catch {
      if (text) message = text.slice(0, 300)
    }
  } catch {
    /* ignore */
  }
  return new ApiError(res.status, message, body)
}

/** api fetches a JSON (or raw text) resource. Paths are absolute ("/api/v0/…"). */
export async function api<T = unknown>(path: string, init: ApiInit = {}): Promise<T> {
  const { body, raw, quiet, headers, ...rest } = init
  const h = new Headers(headers)
  let payload: BodyInit | undefined
  if (body !== undefined) {
    h.set('Content-Type', 'application/json')
    payload = JSON.stringify(body)
  }
  if (!raw) h.set('Accept', 'application/json, text/plain;q=0.9, */*;q=0.8')
  let res: Response
  try {
    res = await fetch(path, { credentials: 'same-origin', ...rest, headers: h, body: payload })
  } catch (e) {
    const err = new ApiError(0, 'Network error: the server did not answer')
    err.retry = () => api(path, init)
    if (!quiet) listeners.forEach((l) => l(err))
    throw err
  }
  if (!res.ok) {
    const err = await parseError(res)
    err.retry = () => api(path, init)
    if (!quiet) listeners.forEach((l) => l(err))
    throw err
  }
  if (res.status === 204) return undefined as T
  if (raw) return (await res.text()) as T
  const ct = res.headers.get('content-type') ?? ''
  if (ct.includes('application/json')) return (await res.json()) as T
  return (await res.text()) as T
}

export const get = <T>(path: string, init?: ApiInit) => api<T>(path, { ...init, method: 'GET' })
export const post = <T>(path: string, body?: unknown, init?: ApiInit) => api<T>(path, { ...init, method: 'POST', body: body ?? {} })
export const put = <T>(path: string, body?: unknown, init?: ApiInit) => api<T>(path, { ...init, method: 'PUT', body })
export const patch = <T>(path: string, body?: unknown, init?: ApiInit) => api<T>(path, { ...init, method: 'PATCH', body })
export const del = <T>(path: string, init?: ApiInit) => api<T>(path, { ...init, method: 'DELETE' })

/** Treats a 404 as "absent" (sibling slice not merged yet) instead of an error. */
export async function optional<T>(p: Promise<T>): Promise<T | null> {
  try {
    return await p
  } catch (e) {
    if (e instanceof ApiError && (e.status === 404 || e.status === 501 || e.status === 405)) return null
    throw e
  }
}

export const repoBase = (org: string, repo: string) => `/api/v0/repos/${encodeURIComponent(org)}/${encodeURIComponent(repo)}`
export const webappRepoBase = (org: string, repo: string) => `/api/v0/webapp/repos/${encodeURIComponent(org)}/${encodeURIComponent(repo)}`
