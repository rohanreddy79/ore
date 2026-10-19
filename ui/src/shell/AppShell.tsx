import { useCallback, useEffect, useMemo, useState, type ReactElement } from 'react'
import { Link, Outlet, useLocation, useNavigate, useParams } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Activity, BookOpen, Bot, ChevronDown, CircleDot, Code2, GitPullRequest, Home, Keyboard, LogOut, Monitor, Moon, Plus, Rocket, Search, Settings, Sun, Workflow } from 'lucide-react'
import { Button, CommandPalette, Dialog, Kbd, Menu, Spinner, type Command } from '../design'
import { get } from '../lib/api'
import { modKeyLabel, useHotkey } from '../lib/keyboard'
import { useLedgerLive } from '../lib/live'
import { actions, qk, useFeatures, useRepos, useSession } from '../lib/queries'
import { useTheme } from '../lib/theme'
import type { SearchHit } from '../lib/types'

export function Brand({ big }: { big?: boolean }) {
  return (
    <Link to="/" className="brand" aria-label="Ore home" style={big ? { fontSize: 'var(--fs-5)' } : undefined}>
      <svg viewBox="0 0 32 32" aria-hidden>
        <rect width="32" height="32" rx="7" fill="var(--fg)" />
        <path d="M16 6.5l8.5 5v9l-8.5 5-8.5-5v-9z" fill="none" stroke="var(--accent)" strokeWidth="2.4" strokeLinejoin="round" />
        <circle cx="16" cy="16" r="3.2" fill="var(--accent)" />
      </svg>
      <span>Ore</span>
    </Link>
  )
}

export function AppShell() {
  const location = useLocation()
  const navigate = useNavigate()
  const isLogin = location.pathname.startsWith('/login')
  const session = useSession()

  useEffect(() => {
    if (!isLogin && session.data === null && !session.isFetching) {
      const next = location.pathname + location.searchStr
      void navigate({ to: '/login', search: { next: next || '/' }, replace: true })
    }
  }, [isLogin, session.data, session.isFetching, location.pathname, location.searchStr, navigate])

  if (isLogin) return <Outlet />
  if (session.isPending) {
    return (
      <div className="container page" style={{ display: 'grid', placeItems: 'center' }}>
        <Spinner label="Signing in" />
      </div>
    )
  }
  if (!session.data) return null
  return <Shell />
}

function Shell() {
  const params = useParams({ strict: false }) as { org?: string; repo?: string }
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { data: features } = useFeatures()
  const { data: repos } = useRepos()
  const session = useSession()
  const [theme, setTheme] = useTheme()
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [helpOpen, setHelpOpen] = useState(false)
  const live = useLedgerLive()

  const org = params.org
  const repo = params.repo
  const inRepo = !!(org && repo)

  const goRepo = useCallback(
    (to: string) => {
      if (!inRepo) return
      void navigate({ to: `/$org/$repo${to}` as '/$org/$repo', params: { org: org!, repo: repo! } })
    },
    [inRepo, navigate, org, repo],
  )

  useHotkey('mod+k', () => setPaletteOpen((o) => !o), { allowInInput: true })
  useHotkey('/', () => setPaletteOpen(true))
  useHotkey('?', () => setHelpOpen((o) => !o))
  useHotkey('g h', () => void navigate({ to: '/' }))
  useHotkey('g l', () => (inRepo ? goRepo('/ledger') : void navigate({ to: '/ledger' })))
  useHotkey('g c', () => goRepo(''))
  useHotkey('g i', () => goRepo('/issues'))
  useHotkey('g r', () => goRepo('/changes'))
  useHotkey('g p', () => goRepo('/pipelines'))
  useHotkey('g d', () => goRepo('/deployments'))
  useHotkey('g a', () => goRepo('/agents'))
  useHotkey('g s', () => (inRepo ? goRepo('/settings') : void navigate({ to: '/settings' })))

  const commands = useMemo<Command[]>(() => {
    const cmds: Command[] = []
    if (inRepo) {
      const p = { org: org!, repo: repo! }
      const nav = (id: string, label: string, to: string, icon: ReactElement, hint?: string) => cmds.push({ id, label, group: `${org}/${repo}`, icon, hint, run: () => void navigate({ to: `/$org/$repo${to}` as '/$org/$repo', params: p }) })
      nav('code', 'Code', '', <Code2 />, 'g c')
      nav('issues', 'Issues', '/issues', <CircleDot />, 'g i')
      nav('new-issue', 'New issue', '/issues/new', <Plus />)
      nav('changes', 'Changes', '/changes', <GitPullRequest />, 'g r')
      nav('pipelines', 'Pipelines', '/pipelines', <Workflow />, 'g p')
      nav('deployments', 'Deployments', '/deployments', <Rocket />, 'g d')
      nav('agents', 'Agents', '/agents', <Bot />, 'g a')
      nav('ledger', 'Ledger', '/ledger', <Activity />, 'g l')
      nav('settings', 'Repository settings', '/settings', <Settings />, 'g s')
    }
    cmds.push({ id: 'home', label: 'Home', group: 'Go to', icon: <Home />, hint: 'g h', run: () => void navigate({ to: '/' }) })
    cmds.push({ id: 'global-ledger', label: 'Global ledger', group: 'Go to', icon: <Activity />, run: () => void navigate({ to: '/ledger' }) })
    cmds.push({ id: 'site-settings', label: 'Identity and site settings', group: 'Go to', icon: <Settings />, run: () => void navigate({ to: '/settings' }) })
    cmds.push({ id: 'new-repo', label: 'New repository', group: 'Create', icon: <Plus />, run: () => void navigate({ to: '/new' }) })
    for (const r of repos ?? []) {
      cmds.push({ id: `repo:${r.org}/${r.name}`, label: `${r.org}/${r.name}`, group: 'Repositories', icon: <BookOpen />, keywords: 'repository', run: () => void navigate({ to: '/$org/$repo', params: { org: r.org, repo: r.name } }) })
    }
    cmds.push({ id: 'theme-light', label: 'Theme: light', group: 'Preferences', icon: <Sun />, run: () => setTheme('light') })
    cmds.push({ id: 'theme-dark', label: 'Theme: dark', group: 'Preferences', icon: <Moon />, run: () => setTheme('dark') })
    cmds.push({ id: 'theme-auto', label: 'Theme: follow system', group: 'Preferences', icon: <Monitor />, run: () => setTheme('auto') })
    cmds.push({ id: 'shortcuts', label: 'Keyboard shortcuts', group: 'Help', icon: <Keyboard />, hint: '?', run: () => setHelpOpen(true) })
    cmds.push({ id: 'design', label: 'Design system gallery', group: 'Help', icon: <BookOpen />, run: () => void navigate({ to: '/design' }) })
    return cmds
  }, [inRepo, org, repo, repos, navigate, setTheme])

  const remoteSearch = useCallback(
    async (q: string): Promise<Command[]> => {
      const res = await get<{ hits: SearchHit[] }>(`/api/v0/webapp/search?q=${encodeURIComponent(q)}`, { quiet: true })
      return res.hits.map((h) => ({
        id: `hit:${h.kind}:${h.repo}:${h.number ?? ''}`,
        label: h.kind === 'repo' ? h.title : `#${h.number} ${h.title}`,
        group: h.kind === 'repo' ? 'Repositories' : h.kind === 'issue' ? `Issues in ${h.repo}` : `Changes in ${h.repo}`,
        icon: h.kind === 'repo' ? <BookOpen /> : h.kind === 'issue' ? <CircleDot /> : <GitPullRequest />,
        hint: h.state,
        run: () => void navigate({ to: h.url.replace(/^\/app/, '') as '/' }),
      }))
    },
    [navigate],
  )

  const signOut = async () => {
    await actions.signOut()
    qc.clear()
    await qc.invalidateQueries({ queryKey: qk.session })
    void navigate({ to: '/login', search: { next: undefined, token: undefined } })
  }

  const actor = session.data?.actor
  const repoItems = (repos ?? []).map((r) => ({ label: `${r.org}/${r.name}`, icon: <BookOpen />, onSelect: () => void navigate({ to: '/$org/$repo', params: { org: r.org, repo: r.name } }) }))

  return (
    <>
      <a href="#main" className="sr-only">
        Skip to content
      </a>
      <header className="topbar">
        <div className="container topbar-inner">
          <Brand />
          <nav className="crumbs" aria-label="Breadcrumb">
            {inRepo ? (
              <>
                <Link to="/" search={{}}>
                  {org}
                </Link>
                <span className="sep">/</span>
                <Menu
                  label="Switch repository"
                  items={[{ kind: 'label', label: 'Repositories' }, ...repoItems, { kind: 'sep' }, { label: 'New repository', icon: <Plus />, onSelect: () => void navigate({ to: '/new' }) }]}
                  trigger={(p) => (
                    <button type="button" className="crumb current" onClick={p.toggle} aria-haspopup={p['aria-haspopup']} aria-expanded={p['aria-expanded']} aria-label={`Repository ${repo}; switch repository`}>
                      <span className="truncate">{repo}</span>
                      <ChevronDown size={14} aria-hidden />
                    </button>
                  )}
                />
              </>
            ) : (
              <Menu
                label="Repositories"
                items={[{ kind: 'label', label: 'Repositories' }, ...repoItems, { kind: 'sep' }, { label: 'New repository', icon: <Plus />, onSelect: () => void navigate({ to: '/new' }) }]}
                trigger={(p) => (
                  <button type="button" className="crumb" onClick={p.toggle} aria-haspopup={p['aria-haspopup']} aria-expanded={p['aria-expanded']}>
                    Repositories <ChevronDown size={14} aria-hidden />
                  </button>
                )}
              />
            )}
          </nav>
          <div className="topbar-right">
            <button type="button" className="topbar-search" onClick={() => setPaletteOpen(true)} aria-label="Search or jump to (command palette)" aria-keyshortcuts="Meta+K">
              <Search aria-hidden />
              <span className="placeholder">Search or jump to…</span>
              <Kbd>{modKeyLabel} K</Kbd>
            </button>
            <span className={`live-dot ${live}`} title={`Ledger stream: ${live}`} aria-live="off">
              <i aria-hidden />
              <span className="hide-sm">{live === 'open' ? 'live' : live}</span>
            </span>
            <Menu
              label="Theme"
              align="right"
              items={[
                { label: 'Light', icon: <Sun />, onSelect: () => setTheme('light') },
                { label: 'Dark', icon: <Moon />, onSelect: () => setTheme('dark') },
                { label: 'Follow system', icon: <Monitor />, onSelect: () => setTheme('auto') },
              ]}
              trigger={(p) => (
                <Button variant="ghost" icon onClick={p.toggle} aria-haspopup={p['aria-haspopup']} aria-expanded={p['aria-expanded']} aria-label={`Theme: ${theme}`} title="Theme">
                  {theme === 'dark' ? <Moon /> : theme === 'light' ? <Sun /> : <Monitor />}
                </Button>
              )}
            />
            <Menu
              label="Account"
              align="right"
              items={[
                { kind: 'label', label: actor ? `${actor.kind}/${actor.id}` : 'Account' },
                { label: 'Settings', icon: <Settings />, onSelect: () => void navigate({ to: '/settings' }) },
                { label: 'Keyboard shortcuts', icon: <Keyboard />, onSelect: () => setHelpOpen(true) },
                { kind: 'sep' },
                { label: 'Sign out', icon: <LogOut />, onSelect: () => void signOut() },
              ]}
              trigger={(p) => (
                <Button variant="ghost" onClick={p.toggle} aria-haspopup={p['aria-haspopup']} aria-expanded={p['aria-expanded']} aria-label="Account menu">
                  <span className="avatar sm" aria-hidden>
                    {(actor?.id ?? '?').replace(/^[a-z]+\//, '').slice(0, 2)}
                  </span>
                  <span className="hide-sm">{actor?.id.replace(/^[a-z]+\//, '')}</span>
                  <ChevronDown />
                </Button>
              )}
            />
          </div>
        </div>
      </header>
      <main id="main" style={{ display: 'flex', flexDirection: 'column', flex: 1 }}>
        <Outlet />
      </main>
      <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} commands={commands} search={remoteSearch} />
      <Dialog open={helpOpen} onClose={() => setHelpOpen(false)} title="Keyboard shortcuts">
        <div className="grid-2">
          <ShortcutList
            title="Everywhere"
            items={[
              [`${modKeyLabel} K`, 'Command palette'],
              ['/', 'Search'],
              ['?', 'This help'],
              ['g h', 'Home'],
              ['g l', 'Ledger'],
            ]}
          />
          <ShortcutList
            title="In a repository"
            items={[
              ['g c', 'Code'],
              ['g i', 'Issues'],
              ['g r', 'Changes'],
              ['g p', 'Pipelines'],
              ['g d', 'Deployments'],
              ['g a', 'Agents'],
              ['g s', 'Settings'],
              ['t', 'Find a file (Code)'],
            ]}
          />
          <ShortcutList
            title="Lists"
            items={[
              ['j / k', 'Next / previous'],
              ['↵', 'Open'],
              ['c', 'Create (issues)'],
            ]}
          />
          <ShortcutList
            title="Diffs and logs"
            items={[
              ['n / p', 'Next / previous file'],
              ['e', 'Jump to failure (logs)'],
              ['f', 'Search log'],
            ]}
          />
        </div>
        {features?.extensions ? (
          <p className="subtle small" style={{ marginTop: 'var(--sp-4)' }}>
            Server extensions: {features.extensions.join(', ')}
          </p>
        ) : null}
      </Dialog>
    </>
  )
}

function ShortcutList({ title, items }: { title: string; items: Array<[string, string]> }) {
  return (
    <div>
      <h3 style={{ fontSize: 'var(--fs-2)', marginBottom: 'var(--sp-2)', color: 'var(--fg-muted)' }}>{title}</h3>
      <dl className="dl">
        {items.map(([k, d]) => (
          <span key={k} style={{ display: 'contents' }}>
            <dt>
              {k.split(' ').map((p, i) => (
                <span key={i}>
                  {i > 0 ? ' ' : ''}
                  <Kbd>{p}</Kbd>
                </span>
              ))}
            </dt>
            <dd>{d}</dd>
          </span>
        ))}
      </dl>
    </div>
  )
}
