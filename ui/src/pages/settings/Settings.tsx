import { useState } from 'react'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Bot, KeyRound, Plug, Trash2, Users } from 'lucide-react'
import { ActorChip, Button, Callout, Card, CopyBlock, Dialog, Field, Input, Pill, Select, Table, useToast } from '../../design'
import { fmtDate } from '../../lib/format'
import { actions, qk, useAgents, useFeatures, useMe, useOrgs, useSSHKeys, useSession, useTokens, useUsers } from '../../lib/queries'
import type { Token } from '../../lib/types'
import { TimeAgo, useDocumentTitle } from '../../shell/common'

const SECTIONS = [
  { id: 'profile', label: 'Profile' },
  { id: 'tokens', label: 'Tokens' },
  { id: 'ssh-keys', label: 'SSH keys' },
  { id: 'users', label: 'Users and organizations' },
  { id: 'agents', label: 'Agents' },
  { id: 'mcp', label: 'MCP and CLI' },
]

/** Site-level settings: identity (users, tokens shown once, SSH keys, agents) and connection instructions. */
export function SiteSettings() {
  const params = useParams({ strict: false }) as { section?: string }
  const section = params.section ?? 'profile'
  useDocumentTitle('Settings')
  const { data: features } = useFeatures()
  return (
    <div className="container page">
      <div className="page-head">
        <div>
          <h1>Settings</h1>
          <p className="sub">Identity is site-wide: users, their tokens and keys; agents belong to an organization.</p>
        </div>
      </div>
      <div className="settings-layout">
        <nav className="tabs vertical" aria-label="Settings sections">
          {SECTIONS.map((s) => (
            <Link key={s.id} to="/settings/$section" params={{ section: s.id }} className="tab" aria-current={section === s.id ? 'page' : undefined}>
              {s.label}
            </Link>
          ))}
        </nav>
        <div>
          {!features?.identity && section !== 'mcp' && section !== 'profile' ? (
            <Callout tone="warn">This server runs without the identity extension: only the admin token exists. Users, tokens and keys need <code>identity</code>.</Callout>
          ) : null}
          {section === 'profile' ? <Profile /> : null}
          {section === 'tokens' ? <Tokens /> : null}
          {section === 'ssh-keys' ? <SSHKeys /> : null}
          {section === 'users' ? <UsersAndOrgs /> : null}
          {section === 'agents' ? <Agents /> : null}
          {section === 'mcp' ? <MCP /> : null}
        </div>
      </div>
    </div>
  )
}

function Profile() {
  const session = useSession()
  const me = useMe()
  const a = session.data?.actor
  return (
    <Card title="You">
      <dl className="dl">
        <dt>Actor</dt>
        <dd>{a ? <ActorChip actor={a} /> : '—'}</dd>
        {me.data?.user ? (
          <>
            <dt>User</dt>
            <dd>
              {me.data.user.name} · {me.data.user.role}
              {me.data.user.email ? ` · ${me.data.user.email}` : ''}
            </dd>
          </>
        ) : null}
        {me.data?.token ? (
          <>
            <dt>Signed in with</dt>
            <dd>
              token <code>{me.data.token.prefix}…</code> {Array.isArray(me.data.token.scopes) ? me.data.token.scopes.map((s) => <Pill key={s} small>{s}</Pill>) : me.data.token.scopes}
            </dd>
          </>
        ) : null}
        <dt>Administrator</dt>
        <dd>{me.data?.admin ? 'yes' : 'no'}</dd>
      </dl>
    </Card>
  )
}

function Tokens() {
  const { data: features } = useFeatures()
  const tokens = useTokens(!!features?.identity)
  const qc = useQueryClient()
  const toast = useToast()
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [scopes, setScopes] = useState('read,write')
  const [expires, setExpires] = useState('720h')
  const [created, setCreated] = useState<Token | null>(null)
  const [busy, setBusy] = useState(false)
  const create = async () => {
    setBusy(true)
    try {
      const t = await actions.createToken({ name, scopes: scopes.split(',').map((s) => s.trim()).filter(Boolean), expires_in: expires || undefined })
      setCreated(t)
      setOpen(false)
      setName('')
      await qc.invalidateQueries({ queryKey: qk.identity() })
    } catch {
      /* api */
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="stack">
      <Card tight title={<><KeyRound aria-hidden /> Access tokens</>} actions={<Button size="sm" variant="primary" onClick={() => setOpen(true)} disabled={!features?.identity}>New token</Button>}>
        <Table
          columns={[
            { key: 'name', header: 'Name', render: (t: Token) => <b>{t.name}</b> },
            { key: 'prefix', header: 'Prefix', render: (t) => <code>{t.prefix}…</code> },
            { key: 'scopes', header: 'Scopes', render: (t) => (Array.isArray(t.scopes) ? t.scopes.join(', ') : t.scopes) },
            { key: 'owner', header: 'Owner', render: (t) => t.agent ?? t.user ?? '', hideSm: true },
            { key: 'exp', header: 'Expires', render: (t) => (t.expires_at ? fmtDate(t.expires_at) : 'never'), hideSm: true },
            { key: 'last', header: 'Last used', render: (t) => (t.last_used_at ? <TimeAgo iso={t.last_used_at} /> : '—'), hideSm: true },
            { key: 'st', header: '', render: (t) => (t.revoked_at ? <Pill small tone="danger">revoked</Pill> : <Button size="sm" variant="ghost" icon aria-label={`Revoke ${t.name}`} onClick={() => void actions.revokeToken(t.id).then(() => { toast.push({ tone: 'ok', title: 'Token revoked' }); return qc.invalidateQueries({ queryKey: qk.identity() }) })}><Trash2 /></Button>) },
          ]}
          rows={tokens.data ?? []}
          rowKey={(t) => t.id}
          empty={<p className="muted">No tokens yet. The admin token printed by <code>ore serve</code> is not listed here.</p>}
        />
      </Card>
      <Dialog open={open} onClose={() => setOpen(false)} title="New access token" foot={<><Button onClick={() => setOpen(false)}>Cancel</Button><Button variant="primary" loading={busy} disabled={!name} onClick={() => void create()}>Create</Button></>}>
        <div className="stack">
          <Field label="Name" htmlFor="tk-name">
            <Input id="tk-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="laptop" />
          </Field>
          <Field label="Scopes" htmlFor="tk-scopes" hint="read ⊂ write ⊂ admin. GET needs read; everything else write.">
            <Input id="tk-scopes" value={scopes} onChange={(e) => setScopes(e.target.value)} />
          </Field>
          <Field label="Expires in" htmlFor="tk-exp" hint="A Go duration such as 720h. Empty means never.">
            <Input id="tk-exp" value={expires} onChange={(e) => setExpires(e.target.value)} />
          </Field>
        </div>
      </Dialog>
      <Dialog open={!!created} onClose={() => setCreated(null)} title="Copy your token now" foot={<Button variant="primary" onClick={() => setCreated(null)}>I saved it</Button>}>
        <div className="stack">
          <Callout tone="warn">This is the only time the secret is shown. Ore stores a hash.</Callout>
          {created?.token ? <CopyBlock text={created.token} /> : null}
          <p className="small muted">Use it as a bearer token, as the git password over HTTP, or paste it into the sign-in page.</p>
        </div>
      </Dialog>
    </div>
  )
}

function SSHKeys() {
  const { data: features } = useFeatures()
  const keys = useSSHKeys(!!features?.identity)
  const qc = useQueryClient()
  const toast = useToast()
  const [open, setOpen] = useState(false)
  const [title, setTitle] = useState('')
  const [pub, setPub] = useState('')
  const [busy, setBusy] = useState(false)
  const add = async () => {
    setBusy(true)
    try {
      await actions.addKey({ public_key: pub.trim(), title })
      toast.push({ tone: 'ok', title: 'Key added' })
      setOpen(false)
      setPub('')
      setTitle('')
      await qc.invalidateQueries({ queryKey: qk.identity() })
    } catch {
      /* api */
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="stack">
      <Card tight title="SSH keys" actions={<Button size="sm" variant="primary" onClick={() => setOpen(true)} disabled={!features?.identity}>Add key</Button>}>
        <Table
          columns={[
            { key: 't', header: 'Title', render: (k: NonNullable<typeof keys.data>[number]) => <b>{k.title}</b> },
            { key: 'f', header: 'Fingerprint', render: (k) => <code>{k.fingerprint}</code> },
            { key: 'u', header: 'Last used', render: (k) => (k.last_used_at ? <TimeAgo iso={k.last_used_at} /> : '—'), hideSm: true },
            { key: 'x', header: '', render: (k) => <Button size="sm" variant="ghost" icon aria-label={`Remove ${k.title}`} onClick={() => void actions.removeKey(k.id).then(() => qc.invalidateQueries({ queryKey: qk.identity() }))}><Trash2 /></Button> },
          ]}
          rows={keys.data ?? []}
          rowKey={(k) => k.id}
          empty={<p className="muted">No keys. Add one to push over SSH{features?.ssh_addr ? ` (port ${features.ssh_addr.split(':').pop()})` : ''}.</p>}
        />
      </Card>
      <Dialog open={open} onClose={() => setOpen(false)} title="Add an SSH key" foot={<><Button onClick={() => setOpen(false)}>Cancel</Button><Button variant="primary" loading={busy} disabled={!pub.trim()} onClick={() => void add()}>Add</Button></>}>
        <div className="stack">
          <Field label="Title" htmlFor="k-title">
            <Input id="k-title" value={title} onChange={(e) => setTitle(e.target.value)} placeholder="laptop" />
          </Field>
          <Field label="Public key" htmlFor="k-pub" hint="One authorized_keys line (ed25519, ecdsa or RSA ≥ 2048).">
            <Input id="k-pub" value={pub} onChange={(e) => setPub(e.target.value)} placeholder="ssh-ed25519 AAAA… you@laptop" className="mono" />
          </Field>
        </div>
      </Dialog>
    </div>
  )
}

function UsersAndOrgs() {
  const { data: features } = useFeatures()
  const users = useUsers(!!features?.identity)
  const orgs = useOrgs()
  const qc = useQueryClient()
  const toast = useToast()
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [role, setRole] = useState('member')
  const [org, setOrg] = useState('')
  const [busy, setBusy] = useState(false)
  const [newOrg, setNewOrg] = useState('')
  const create = async () => {
    setBusy(true)
    try {
      await actions.createUser({ name, email: email || undefined, role, memberships: org ? [{ org, role: 'member' }] : [] })
      toast.push({ tone: 'ok', title: `User ${name} created`, body: 'Create a token for them under Tokens (as an administrator, pass user=).' })
      setOpen(false)
      setName('')
      await qc.invalidateQueries({ queryKey: qk.identity() })
    } catch {
      /* api */
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="stack">
      <Card tight title={<><Users aria-hidden /> Users</>} actions={<Button size="sm" variant="primary" onClick={() => setOpen(true)} disabled={!features?.identity}>New user</Button>}>
        <Table
          columns={[
            { key: 'n', header: 'Name', render: (u: NonNullable<typeof users.data>[number]) => <b>{u.name}</b> },
            { key: 'e', header: 'Email', render: (u) => u.email ?? '', hideSm: true },
            { key: 'r', header: 'Site role', render: (u) => <Pill small>{u.role}</Pill> },
            { key: 's', header: 'Status', render: (u) => u.status ?? 'active' },
          ]}
          rows={users.data ?? []}
          rowKey={(u) => u.id}
          empty={<p className="muted">Only administrators can list users.</p>}
        />
      </Card>
      <Card title="Organizations">
        <div className="stack">
          <ul className="stack" style={{ listStyle: 'none', margin: 0, padding: 0, gap: 6 }}>
            {(orgs.data ?? []).map((o) => (
              <li key={o.id} className="row between">
                <span>
                  <b>{o.name}</b> <span className="muted small">created {fmtDate(o.created_at)}</span>
                </span>
                <Button size="sm" variant="ghost" onClick={() => void navigate({ to: '/', search: {} })}>
                  Repositories
                </Button>
              </li>
            ))}
          </ul>
          <form
            className="row"
            onSubmit={async (e) => {
              e.preventDefault()
              if (!newOrg) return
              await actions.createOrg(newOrg)
              setNewOrg('')
              await qc.invalidateQueries({ queryKey: qk.orgs })
            }}
          >
            <Input small value={newOrg} onChange={(e) => setNewOrg(e.target.value)} placeholder="new organization" aria-label="New organization name" style={{ width: 220 }} pattern="[a-z0-9._-]+" />
            <Button size="sm" type="submit" disabled={!newOrg}>
              Create organization
            </Button>
          </form>
        </div>
      </Card>
      <Dialog open={open} onClose={() => setOpen(false)} title="New user" foot={<><Button onClick={() => setOpen(false)}>Cancel</Button><Button variant="primary" loading={busy} disabled={!name} onClick={() => void create()}>Create</Button></>}>
        <div className="stack">
          <Field label="Name" htmlFor="u-name">
            <Input id="u-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="alice" />
          </Field>
          <Field label="Email" htmlFor="u-email">
            <Input id="u-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} />
          </Field>
          <Field label="Site role" htmlFor="u-role">
            <Select id="u-role" value={role} onChange={(e) => setRole(e.target.value)}>
              <option value="owner">owner (administrator)</option>
              <option value="member">member</option>
              <option value="guest">guest</option>
            </Select>
          </Field>
          <Field label="Member of" htmlFor="u-org">
            <Select id="u-org" value={org} onChange={(e) => setOrg(e.target.value)}>
              <option value="">no organization yet</option>
              {(orgs.data ?? []).map((o) => (
                <option key={o.id} value={o.name}>
                  {o.name}
                </option>
              ))}
            </Select>
          </Field>
        </div>
      </Dialog>
    </div>
  )
}

function Agents() {
  const { data: features } = useFeatures()
  const orgs = useOrgs()
  const [org, setOrg] = useState('')
  const effectiveOrg = org || orgs.data?.[0]?.name
  const agents = useAgents(effectiveOrg, !!features?.identity && !!effectiveOrg)
  const qc = useQueryClient()
  const toast = useToast()
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [created, setCreated] = useState<Token | null>(null)
  const create = async () => {
    if (!effectiveOrg) return
    setBusy(true)
    try {
      await actions.createAgent({ org: effectiveOrg, name })
      const t = await actions.createToken({ name: `${name} default`, agent: `${effectiveOrg}/${name}`, scopes: ['read', 'write'] })
      setCreated(t)
      setName('')
      await qc.invalidateQueries({ queryKey: qk.identity() })
      toast.push({ tone: 'ok', title: `Agent ${name} created` })
    } catch {
      /* api */
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="stack">
      <Card title={<><Bot aria-hidden /> Agent principals</>}>
        <p className="small muted">Agents have identity, a human owner, budgets and tokens of their own; writes from agent sessions go through the gateway. Actor ids look like <code>agent/fixer</code> on the ledger.</p>
        <div className="row" style={{ margin: 'var(--sp-3) 0' }}>
          <Select small value={effectiveOrg ?? ''} onChange={(e) => setOrg(e.target.value)} aria-label="Organization" style={{ width: 200 }}>
            {(orgs.data ?? []).map((o) => (
              <option key={o.id} value={o.name}>
                {o.name}
              </option>
            ))}
          </Select>
          <Input small value={name} onChange={(e) => setName(e.target.value)} placeholder="agent name, e.g. fixer" aria-label="Agent name" style={{ width: 220 }} pattern="[a-z0-9._-]+" />
          <Button size="sm" variant="primary" loading={busy} disabled={!name || !effectiveOrg || !features?.identity} onClick={() => void create()}>
            Create agent and token
          </Button>
        </div>
        <Table
          columns={[
            { key: 'n', header: 'Name', render: (a: NonNullable<typeof agents.data>[number]) => <ActorChip actor={{ kind: 'agent', id: a.name }} /> },
            { key: 'o', header: 'Owner', render: (a) => a.owner },
            { key: 's', header: 'Status', render: (a) => a.status ?? 'active' },
            { key: 'b', header: 'Budgets', render: (a) => <code className="small">{typeof a.budgets === 'string' ? a.budgets : JSON.stringify(a.budgets ?? {})}</code>, hideSm: true },
          ]}
          rows={agents.data ?? []}
          rowKey={(a) => a.id}
          empty={<p className="muted">No agents in {effectiveOrg ?? 'this organization'}.</p>}
        />
      </Card>
      <Dialog open={!!created} onClose={() => setCreated(null)} title="Agent token (shown once)" foot={<Button variant="primary" onClick={() => setCreated(null)}>Done</Button>}>
        <div className="stack">
          {created?.token ? <CopyBlock text={created.token} /> : null}
          <p className="small muted">Give it to the agent runtime as <code>ORE_TOKEN</code>; or mint a gateway session instead for MCP writes.</p>
        </div>
      </Dialog>
    </div>
  )
}

function MCP() {
  const { data: features } = useFeatures()
  const base = features?.base_url || window.location.origin
  return (
    <div className="stack">
      <Card title={<><Plug aria-hidden /> Connect an agent over MCP</>}>
        {features?.mcp ? (
          <div className="stack">
            <p className="small">Ore's MCP server is stateless Streamable HTTP at <code>/mcp</code>. Reads work with any token; writes need a gateway session so every agent write is policy-checked and on the ledger.</p>
            <Field label="1. Register the server in Claude Code (reads)" htmlFor="mcp1">
              <CopyBlock text={`claude mcp add --transport http ore ${base}/mcp --header "Authorization: Bearer <your token>"`} />
            </Field>
            <Field label="2. Mint a gateway session for writes (as an administrator)" htmlFor="mcp2">
              <CopyBlock text={`ore api POST /api/v0/gateway/sessions '{"agent_id":"claude-code","repo":"<org>/<repo>"}'`} />
            </Field>
            <p className="small muted">Then connect with the returned <code>gws_…</code> token instead. Tools: repos_*, changes_*, issues_*, pipelines_*, deployments_list, ledger_events; writes: issues_open, changes_open, changes_request_review, deployments_request, issues_comment.</p>
          </div>
        ) : (
          <Callout tone="warn">The MCP extension is not running on this server.</Callout>
        )}
      </Card>
      <Card title="The CLI">
        <div className="stack">
          <CopyBlock text={`export ORE_URL=${base}\nexport ORE_TOKEN=<your token>\nore repo list`} />
          <p className="small muted">The CLI, the UI and agents speak the same API and are indistinguishable on the ledger except for their actor.</p>
        </div>
      </Card>
    </div>
  )
}
