import { afterEach, describe, expect, it, vi } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { RepoLayout } from './RepoLayout'

// RepoLayout under a real router, with the API mocked: the repository, its
// lists, the features and the organization settings, whose answer is varied.
type Settings = { status: number; body?: unknown }
let settings: Settings
let settingsCalls: string[] = []

const json = (status: number, body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))

function fakeFetch(input: RequestInfo | URL): Promise<Response> {
  const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, 'http://ore.test')
  const p = url.pathname
  if (p === '/api/v0/orgs/acme/settings') {
    settingsCalls.push(p)
    return json(settings.status, settings.body ?? { error: settings.status === 403 ? 'not a member of acme' : 'not found' })
  }
  if (p === '/api/v0/repos/acme/app') return json(200, { id: 'r1', org_id: 'o1', org: 'acme', name: 'app', default_branch: 'main', autonomy_level: 2, deploy_port: 0, created_at: '' })
  if (p === '/api/v0/repos/acme/app/issues' || p === '/api/v0/repos/acme/app/changes') return json(200, [])
  if (p === '/api/v0/webapp/features') return json(200, { extensions: [], webapp: { built: true, prefix: '/app/' }, identity: true, base_url: 'https://ore.example', ssh_addr: ':2222' })
  return json(404, { error: 'not found' })
}

const st = (mode: string, reason = '') => ({ status: 200, body: { org_id: 'o1', org: 'acme', git_transport: mode, git_transport_reason: reason } })

function renderRepo() {
  const root = createRootRoute({ component: () => <Outlet /> })
  const repo = createRoute({ getParentRoute: () => root, path: '/$org/$repo', component: RepoLayout })
  const index = createRoute({ getParentRoute: () => repo, path: '/', component: () => <p>code page</p> })
  const router = createRouter({ routeTree: root.addChildren([repo.addChildren([index])]), history: createMemoryHistory({ initialEntries: ['/acme/app'] }) })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  )
}

/** Waits until the settings answer has been applied, opens the dialog and returns it. */
async function openDialog(button: 'Clone' | 'Open') {
  await waitFor(() => expect(settingsCalls.length).toBeGreaterThan(0))
  const b = await screen.findByRole('button', { name: button })
  fireEvent.click(b)
  return within(await screen.findByRole('dialog'))
}

describe('RepoLayout clone dialog follows the git transport mode', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    settingsCalls = []
  })
  const setup = (s: Settings) => {
    settings = s
    vi.stubGlobal('fetch', vi.fn(fakeFetch))
    renderRepo()
  }

  it('on: git clone over HTTPS and SSH, no Ore client notice', async () => {
    setup(st('on'))
    const d = await openDialog('Clone')
    expect(d.getByRole('heading', { name: 'Clone this repository' })).toBeInTheDocument()
    expect(d.getByText('git clone https://ore.example/acme/app.git')).toBeInTheDocument()
    expect(d.getByText('git clone ssh://git@ore.example:2222/acme/app.git')).toBeInTheDocument()
    expect(d.queryByText(/pushes are off/)).toBeNull()
    expect(d.queryByText(/ore open/)).toBeNull()
  })

  it('read-only: keeps the clone commands and says pushes go through ore save, with the reason', async () => {
    setup(st('read-only', 'pushes go through ore save from Monday'))
    const d = await openDialog('Clone')
    expect(d.getByText('git clone https://ore.example/acme/app.git')).toBeInTheDocument()
    expect(await d.findByText('Git pushes are off for acme')).toBeInTheDocument()
    expect(d.getByText('Reason: pushes go through ore save from Monday')).toBeInTheDocument()
    expect(d.getByText(/ore open acme\/app\s+ore save -m "message"/)).toBeInTheDocument()
  })

  it('off: replaces the clone block with ore login and ore open, with the reason', async () => {
    setup(st('off', 'last day of git was Friday'))
    const d = await openDialog('Open')
    expect(d.getByRole('heading', { name: 'Open this repository' })).toBeInTheDocument()
    expect(d.getByText('Git is off for acme')).toBeInTheDocument()
    expect(d.getByText('Reason: last day of git was Friday')).toBeInTheDocument()
    expect(d.getByText(/ore login https:\/\/ore\.example\s+ore open acme\/app/)).toBeInTheDocument()
    expect(d.queryByText(/git clone/)).toBeNull()
    expect(d.getByText('ore commit acme/app -m "message" path/to/file')).toBeInTheDocument()
  })

  it.each([
    [403, 'a non-member'],
    [404, 'a server without the identity extension'],
  ])('treats a %i (%s) as on', async (status) => {
    setup({ status })
    const d = await openDialog('Clone')
    expect(d.getByText('git clone https://ore.example/acme/app.git')).toBeInTheDocument()
    expect(d.queryByText(/Git is off|pushes are off/)).toBeNull()
  })
})
