import { useEffect, useState } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { KeyRound } from 'lucide-react'
import { Button, Callout, Field, Input } from '../design'
import { ApiError } from '../lib/api'
import { actions, qk } from '../lib/queries'
import { Brand } from './AppShell'
import { useDocumentTitle } from './common'

export function Login() {
  useDocumentTitle('Sign in')
  const { next, token: tokenParam } = useSearch({ from: '/login' })
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const go = () => {
    const dest = next && next.startsWith('/') && !next.startsWith('//') ? next : '/'
    void navigate({ to: dest as '/', replace: true })
  }

  const submit = async (t: string) => {
    setBusy(true)
    setError(null)
    try {
      const me = await actions.signIn(t.trim())
      // Seed the session cache before navigating so the shell does not bounce
      // back here while the refetch is in flight.
      qc.setQueryData(qk.session, me)
      await qc.invalidateQueries({ queryKey: qk.session })
      go()
    } catch (e) {
      setError(e instanceof ApiError && e.status === 401 ? 'That token is not valid.' : e instanceof Error ? e.message : 'Sign-in failed.')
    } finally {
      setBusy(false)
    }
  }

  // `ore serve` prints /login?token=…; the app accepts the same and removes it from the address bar.
  useEffect(() => {
    if (tokenParam) {
      void submit(tokenParam)
      void navigate({ to: '/login', search: { next, token: undefined }, replace: true })
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tokenParam])

  return (
    <div className="login-page">
      <div className="card login-card">
        <div className="card-body">
          <Brand big />
          <h1 style={{ fontSize: 'var(--fs-5)', marginBottom: 'var(--sp-2)' }}>Sign in</h1>
          <p className="muted">Paste an access token. The admin token is printed by `ore serve`; personal tokens come from Settings → Tokens.</p>
          <form
            className="stack"
            style={{ marginTop: 'var(--sp-4)' }}
            onSubmit={(e) => {
              e.preventDefault()
              if (token.trim()) void submit(token)
            }}
          >
            <Field label="Access token" htmlFor="token" error={error ?? undefined}>
              <Input id="token" type="password" autoComplete="current-password" autoFocus value={token} onChange={(e) => setToken(e.target.value)} placeholder="ore_…" aria-invalid={!!error} />
            </Field>
            <Button type="submit" variant="primary" loading={busy} disabled={!token.trim()}>
              <KeyRound /> Sign in
            </Button>
          </form>
          <div style={{ marginTop: 'var(--sp-4)' }}>
            <Callout>
              Tokens are stored in an HttpOnly cookie shared with the server-rendered pages. Nothing is sent anywhere but this server.
            </Callout>
          </div>
        </div>
      </div>
    </div>
  )
}
