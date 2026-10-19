import { createRootRouteWithContext, createRoute, createRouter, lazyRouteComponent, Outlet } from '@tanstack/react-router'
import type { QueryClient } from '@tanstack/react-query'
import { AppShell } from './shell/AppShell'
import { RepoLayout } from './shell/RepoLayout'
import { NotFound, RouteError } from './shell/NotFound'

type Ctx = { queryClient: QueryClient }

const rootRoute = createRootRouteWithContext<Ctx>()({
  component: AppShell,
  notFoundComponent: () => <NotFound />,
  errorComponent: RouteError,
})

const lazy = (loader: () => Promise<Record<string, unknown>>, name: string) => lazyRouteComponent(loader as never, name as never)

export const homeRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: lazy(() => import('./pages/Home'), 'Home') })
function ledgerSearch(s: Record<string, unknown>): LedgerSearch {
  const out: LedgerSearch = {}
  if (typeof s.type === 'string' && s.type) out.type = s.type
  if (typeof s.subject === 'string' && s.subject) out.subject = s.subject
  const after = typeof s.after === 'number' ? s.after : typeof s.after === 'string' ? Number(s.after) : NaN
  if (Number.isFinite(after) && after > 0) out.after = after
  return out
}
export const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  validateSearch: (s: Record<string, unknown>): LoginSearch => {
    const out: LoginSearch = {}
    if (typeof s.next === 'string' && s.next) out.next = s.next
    if (typeof s.token === 'string' && s.token) out.token = s.token
    return out
  },
  component: lazy(() => import('./shell/Login'), 'Login'),
})
export const ledgerRoute = createRoute({ getParentRoute: () => rootRoute, path: '/ledger', validateSearch: ledgerSearch, component: lazy(() => import('./pages/ledger/Ledger'), 'LedgerPage') })
export const settingsRoute = createRoute({ getParentRoute: () => rootRoute, path: '/settings', component: lazy(() => import('./pages/settings/Settings'), 'SiteSettings') })
export const settingsSectionRoute = createRoute({ getParentRoute: () => rootRoute, path: '/settings/$section', component: lazy(() => import('./pages/settings/Settings'), 'SiteSettings') })
export const newRepoRoute = createRoute({ getParentRoute: () => rootRoute, path: '/new', component: lazy(() => import('./pages/NewRepo'), 'NewRepo') })
export const designRoute = createRoute({ getParentRoute: () => rootRoute, path: '/design', component: lazy(() => import('./pages/DesignGallery'), 'DesignGallery') })

export const repoRoute = createRoute({ getParentRoute: () => rootRoute, path: '/$org/$repo', component: RepoLayout })

const str = (v: unknown) => (typeof v === 'string' && v !== '' ? v : undefined)
const num = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : typeof v === 'string' && v !== '' && Number.isFinite(Number(v)) ? Number(v) : undefined)

export type IssuesSearch = { state?: 'closed' | 'all'; q?: string }
export type ChangesSearch = { state?: string; q?: string }
export type ChangeSearch = { ps?: number; from?: number; view?: 'split' | 'unified'; tab?: string }
export type LedgerSearch = { type?: string; subject?: string; after?: number }
export type LoginSearch = { next?: string; token?: string }

export const codeIndexRoute = createRoute({ getParentRoute: () => repoRoute, path: '/', component: lazy(() => import('./pages/code/Code'), 'CodePage') })
export const treeRoute = createRoute({ getParentRoute: () => repoRoute, path: '/tree/$ref/$', component: lazy(() => import('./pages/code/Code'), 'CodePage') })
export const treeRootRoute = createRoute({ getParentRoute: () => repoRoute, path: '/tree/$ref', component: lazy(() => import('./pages/code/Code'), 'CodePage') })
export const blobRoute = createRoute({ getParentRoute: () => repoRoute, path: '/blob/$ref/$', component: lazy(() => import('./pages/code/Code'), 'CodePage') })
export const commitsRoute = createRoute({ getParentRoute: () => repoRoute, path: '/commits', component: lazy(() => import('./pages/code/Commits'), 'CommitsPage') })
export const commitsRefRoute = createRoute({ getParentRoute: () => repoRoute, path: '/commits/$ref', component: lazy(() => import('./pages/code/Commits'), 'CommitsPage') })
export const commitRoute = createRoute({ getParentRoute: () => repoRoute, path: '/commit/$oid', component: lazy(() => import('./pages/code/Commit'), 'CommitPage') })
export const compareRoute = createRoute({ getParentRoute: () => repoRoute, path: '/compare/$spec', component: lazy(() => import('./pages/code/Compare'), 'ComparePage') })
export const issuesRoute = createRoute({
  getParentRoute: () => repoRoute,
  path: '/issues',
  validateSearch: (s: Record<string, unknown>): IssuesSearch => {
    const out: IssuesSearch = {}
    if (s.state === 'closed' || s.state === 'all') out.state = s.state
    if (str(s.q)) out.q = str(s.q)
    return out
  },
  component: lazy(() => import('./pages/issues/Issues'), 'IssuesPage'),
})
export const newIssueRoute = createRoute({ getParentRoute: () => repoRoute, path: '/issues/new', component: lazy(() => import('./pages/issues/NewIssue'), 'NewIssuePage') })
export const issueRoute = createRoute({ getParentRoute: () => repoRoute, path: '/issues/$n', component: lazy(() => import('./pages/issues/Issue'), 'IssuePage') })
export const changesRoute = createRoute({
  getParentRoute: () => repoRoute,
  path: '/changes',
  validateSearch: (s: Record<string, unknown>): ChangesSearch => {
    const out: ChangesSearch = {}
    if (str(s.state)) out.state = str(s.state)
    if (str(s.q)) out.q = str(s.q)
    return out
  },
  component: lazy(() => import('./pages/changes/Changes'), 'ChangesPage'),
})
export const changeRoute = createRoute({
  getParentRoute: () => repoRoute,
  path: '/changes/$n',
  validateSearch: (s: Record<string, unknown>): ChangeSearch => {
    const out: ChangeSearch = {}
    if (num(s.ps) !== undefined) out.ps = num(s.ps)
    if (num(s.from) !== undefined) out.from = num(s.from)
    if (s.view === 'split' || s.view === 'unified') out.view = s.view
    if (str(s.tab)) out.tab = str(s.tab)
    return out
  },
  component: lazy(() => import('./pages/changes/Change'), 'ChangePage'),
})
export const stacksRoute = createRoute({ getParentRoute: () => repoRoute, path: '/stacks', component: lazy(() => import('./pages/changes/Stacks'), 'StacksPage') })
export const stackRoute = createRoute({ getParentRoute: () => repoRoute, path: '/stacks/$id', component: lazy(() => import('./pages/changes/Stacks'), 'StacksPage') })
export const pipelinesRoute = createRoute({ getParentRoute: () => repoRoute, path: '/pipelines', component: lazy(() => import('./pages/pipelines/Runs'), 'RunsPage') })
export const runRoute = createRoute({ getParentRoute: () => repoRoute, path: '/pipelines/$id', component: lazy(() => import('./pages/pipelines/Run'), 'RunPage') })
export const deploymentsRoute = createRoute({ getParentRoute: () => repoRoute, path: '/deployments', component: lazy(() => import('./pages/deployments/Deployments'), 'DeploymentsPage') })
export const deploymentRoute = createRoute({ getParentRoute: () => repoRoute, path: '/deployments/$id', component: lazy(() => import('./pages/deployments/Deployment'), 'DeploymentPage') })
export const agentsRoute = createRoute({ getParentRoute: () => repoRoute, path: '/agents', component: lazy(() => import('./pages/agents/Agents'), 'AgentsPage') })
export const agentRunRoute = createRoute({ getParentRoute: () => repoRoute, path: '/agents/$id', component: lazy(() => import('./pages/agents/AgentRun'), 'AgentRunPage') })
export const repoLedgerRoute = createRoute({ getParentRoute: () => repoRoute, path: '/ledger', validateSearch: ledgerSearch, component: lazy(() => import('./pages/ledger/Ledger'), 'LedgerPage') })
export const repoSettingsRoute = createRoute({ getParentRoute: () => repoRoute, path: '/settings', component: lazy(() => import('./pages/settings/RepoSettings'), 'RepoSettingsPage') })
export const repoSettingsSectionRoute = createRoute({ getParentRoute: () => repoRoute, path: '/settings/$section', component: lazy(() => import('./pages/settings/RepoSettings'), 'RepoSettingsPage') })

const routeTree = rootRoute.addChildren([
  homeRoute,
  loginRoute,
  ledgerRoute,
  settingsRoute,
  settingsSectionRoute,
  newRepoRoute,
  designRoute,
  repoRoute.addChildren([
    codeIndexRoute,
    treeRoute,
    treeRootRoute,
    blobRoute,
    commitsRoute,
    commitsRefRoute,
    commitRoute,
    compareRoute,
    issuesRoute,
    newIssueRoute,
    issueRoute,
    changesRoute,
    changeRoute,
    stacksRoute,
    stackRoute,
    pipelinesRoute,
    runRoute,
    deploymentsRoute,
    deploymentRoute,
    agentsRoute,
    agentRunRoute,
    repoLedgerRoute,
    repoSettingsRoute,
    repoSettingsSectionRoute,
  ]),
])

export function createAppRouter(queryClient: QueryClient) {
  return createRouter({
    routeTree,
    basepath: '/app',
    context: { queryClient },
    defaultPreload: 'intent',
    defaultPreloadStaleTime: 0,
    scrollRestoration: true,
    defaultPendingMs: 150,
    defaultPendingComponent: () => (
      <div className="container page" aria-busy>
        <span className="spinner" role="status" aria-label="Loading" />
      </div>
    ),
  })
}

declare module '@tanstack/react-router' {
  interface Register {
    router: ReturnType<typeof createAppRouter>
  }
}

export { Outlet }
