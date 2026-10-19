import { Link, useRouter, type ErrorComponentProps } from '@tanstack/react-router'
import { Button } from '../design'
import { ApiError } from '../lib/api'

export function NotFound({ what }: { what?: string }) {
  return (
    <div className="container page">
      <div className="notfound">
        <div className="code">404</div>
        <h1 style={{ marginTop: 'var(--sp-3)' }}>{what ? `This ${what} does not exist` : 'There is nothing here'}</h1>
        <p className="muted" style={{ marginTop: 'var(--sp-2)' }}>
          Check the address, or it may have been removed. Repositories are listed on the home page.
        </p>
        <div className="row" style={{ justifyContent: 'center', marginTop: 'var(--sp-4)' }}>
          <Link to="/" className="btn primary">
            Go home
          </Link>
          <Button onClick={() => history.back()}>Go back</Button>
        </div>
      </div>
    </div>
  )
}

export function RouteError({ error, reset }: ErrorComponentProps) {
  const router = useRouter()
  if (error instanceof ApiError && error.status === 404) return <NotFound />
  return (
    <div className="container page">
      <div className="notfound">
        <div className="code">!</div>
        <h1 style={{ marginTop: 'var(--sp-3)' }}>This page failed to render</h1>
        <p className="muted break" style={{ marginTop: 'var(--sp-2)' }}>
          {error instanceof Error ? error.message : String(error)}
        </p>
        <div className="row" style={{ justifyContent: 'center', marginTop: 'var(--sp-4)' }}>
          <Button
            variant="primary"
            onClick={() => {
              reset()
              void router.invalidate()
            }}
          >
            Try again
          </Button>
          <Link to="/" className="btn">
            Go home
          </Link>
        </div>
      </div>
    </div>
  )
}
