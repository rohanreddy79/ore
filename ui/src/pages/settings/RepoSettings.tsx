import { useEffect, useState } from 'react'
import { Link, useParams } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Link2, KeyRound, Plug, RefreshCw, Shield, Trash2, Webhook } from 'lucide-react'
import { Button, Callout, Card, Checkbox, CopyBlock, Field, Input, Pill, Select, Table, useToast } from '../../design'
import { LEVEL_EXPLANATIONS } from '../../lib/format'
import { actions, qk, useEnvironments, useFeatures, useGitHubLink, useRepo, useReviewPolicy, useSecrets, useWebhooks } from '../../lib/queries'
import type { GitHubLink, Secret } from '../../lib/types'
import { FeatureHint, LevelBadge, LevelExplainer, TimeAgo, useDocumentTitle } from '../../shell/common'

const SECTIONS = [
  { id: 'general', label: 'General' },
  { id: 'review', label: 'Review policy' },
  { id: 'environments', label: 'Environments' },
  { id: 'secrets', label: 'Secrets' },
  { id: 'github', label: 'GitHub' },
  { id: 'webhooks', label: 'Webhooks' },
  { id: 'mcp', label: 'MCP' },
]

export function RepoSettingsPage() {
  const params = useParams({ strict: false }) as { org: string; repo: string; section?: string }
  const { org, repo } = params
  const section = params.section ?? 'general'
  useDocumentTitle(`Settings · ${org}/${repo}`)
  return (
    <div className="settings-layout">
      <nav className="tabs vertical" aria-label="Repository settings sections">
        {SECTIONS.map((s) => (
          <Link key={s.id} to="/$org/$repo/settings/$section" params={{ org, repo, section: s.id }} className="tab" aria-current={section === s.id ? 'page' : undefined}>
            {s.label}
          </Link>
        ))}
      </nav>
      <div>
        {section === 'general' ? <General org={org} repo={repo} /> : null}
        {section === 'review' ? <ReviewPolicySection org={org} repo={repo} /> : null}
        {section === 'environments' ? <EnvironmentsSection org={org} repo={repo} /> : null}
        {section === 'secrets' ? <SecretsSection org={org} repo={repo} /> : null}
        {section === 'github' ? <GitHubSection org={org} repo={repo} /> : null}
        {section === 'webhooks' ? <WebhooksSection org={org} /> : null}
        {section === 'mcp' ? <MCPSection org={org} repo={repo} /> : null}
      </div>
    </div>
  )
}

function General({ org, repo }: { org: string; repo: string }) {
  const r = useRepo(org, repo)
  const qc = useQueryClient()
  const toast = useToast()
  const [level, setLevel] = useState<number | null>(null)
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const current = r.data?.autonomy_level ?? 2
  const chosen = level ?? current
  const save = async () => {
    setBusy(true)
    try {
      await actions.setLevel(org, repo, chosen, reason)
      await qc.invalidateQueries({ queryKey: qk.repo(org, repo) })
      toast.push({ tone: 'ok', title: `Autonomy set to L${chosen}`, body: 'Recorded on the ledger with your reason.' })
      setLevel(null)
      setReason('')
    } catch {
      /* api */
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="stack" style={{ gap: 'var(--sp-5)' }}>
      <Card title="Repository">
        <dl className="dl">
          <dt>Name</dt>
          <dd>
            {org}/{repo}
          </dd>
          <dt>Default branch</dt>
          <dd>
            <code>{r.data?.default_branch}</code> <span className="muted small">protected: pushes go through changes</span>
          </dd>
          <dt>Deploy port</dt>
          <dd>{r.data?.deploy_port}</dd>
          <dt>Created</dt>
          <dd>{r.data ? <TimeAgo iso={r.data.created_at} /> : ''}</dd>
          <dt>Id</dt>
          <dd>
            <code>{r.data?.id}</code>
          </dd>
        </dl>
        <p className="small muted" style={{ marginTop: 'var(--sp-3)' }}>Renaming, changing the default branch and the deploy port are not exposed by the v0 API yet.</p>
      </Card>
      <Card title={<><Shield aria-hidden /> Autonomy level</>}>
        <div className="stack">
          <p className="small">
            Current: <LevelBadge level={current} />. Levels go up with evidence (acceptance, fix and rollback rates) and down automatically on defined signals. Choose what agents may do here:
          </p>
          <div className="levels">
            {[0, 1, 2, 3, 4, 5].map((l) => {
              const ex = LEVEL_EXPLANATIONS[l]!
              return (
                <label key={l} className="level-option" data-checked={chosen === l}>
                  <input type="radio" name="level" value={l} checked={chosen === l} onChange={() => setLevel(l)} />
                  <span>
                    <span className="t">
                      L{l} {ex.title}
                      {l === current ? ' (current)' : ''}
                    </span>
                    <span className="d" style={{ display: 'block' }}>
                      Agents {ex.agents}. Humans {ex.humans}.
                    </span>
                  </span>
                </label>
              )
            })}
          </div>
          {chosen !== current ? (
            <>
              <LevelExplainer level={chosen} />
              {chosen >= 4 ? <Callout tone="warn">L4+ lands optimistically and deploys to production without a click. Make sure the pipeline is a trustworthy oracle and auto-rollback is on for production.</Callout> : null}
              <Field label="Reason (recorded on the ledger)" htmlFor="lvl-reason">
                <Input id="lvl-reason" value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Tests are green for two weeks; canaries in place." />
              </Field>
              <div className="row">
                <Button variant="primary" loading={busy} onClick={() => void save()}>
                  Set level L{chosen}
                </Button>
                <Button variant="ghost" onClick={() => setLevel(null)}>
                  Cancel
                </Button>
              </div>
            </>
          ) : null}
        </div>
      </Card>
    </div>
  )
}

function ReviewPolicySection({ org, repo }: { org: string; repo: string }) {
  const { data: features } = useFeatures()
  const policy = useReviewPolicy(org, repo)
  const qc = useQueryClient()
  const toast = useToast()
  const [form, setForm] = useState({ required_approvals: 0, allow_agent_approvals: false, default_strategy: 'queue' })
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    if (policy.data) setForm(policy.data)
  }, [policy.data])
  if (features && !features.review_v1) return <FeatureHint feature="review.v1" what="The review policy" />
  return (
    <Card title="Review policy">
      <form
        className="stack"
        onSubmit={async (e) => {
          e.preventDefault()
          setBusy(true)
          try {
            await actions.setReviewPolicy(org, repo, form)
            await qc.invalidateQueries({ queryKey: qk.r(org, repo, 'review-policy') })
            toast.push({ tone: 'ok', title: 'Review policy saved' })
          } catch {
            /* api */
          } finally {
            setBusy(false)
          }
        }}
      >
        <Field label="Required approvals" htmlFor="rp-req" hint="Counted on the latest patch set only; a push resets them. 0 keeps the v0 behaviour.">
          <Input id="rp-req" type="number" min={0} max={10} value={form.required_approvals} onChange={(e) => setForm({ ...form, required_approvals: Number(e.target.value) })} style={{ width: 120 }} />
        </Field>
        <Checkbox label="Agent and bot approvals count" checked={form.allow_agent_approvals} onChange={(e) => setForm({ ...form, allow_agent_approvals: e.target.checked })} />
        <Field label="Default landing strategy" htmlFor="rp-strat">
          <Select id="rp-strat" value={form.default_strategy} onChange={(e) => setForm({ ...form, default_strategy: e.target.value })} style={{ width: 260 }}>
            <option value="queue">queue — merge now after policy</option>
            <option value="land_when_green">land when green — verify the head first</option>
            <option value="optimistic">optimistic — merge, verify, auto-revert</option>
          </Select>
        </Field>
        <div>
          <Button type="submit" variant="primary" loading={busy}>
            Save
          </Button>
        </div>
      </form>
    </Card>
  )
}

function EnvironmentsSection({ org, repo }: { org: string; repo: string }) {
  const { data: features } = useFeatures()
  const envs = useEnvironments(org, repo)
  const qc = useQueryClient()
  const toast = useToast()
  if (features && !features.environments) return <FeatureHint feature="delivery.environments" what="Environment policy" />
  return (
    <Card tight title="Environments" actions={<Link to="/$org/$repo/deployments" params={{ org, repo }} className="btn sm">Deployments</Link>}>
      <Table
        columns={[
          { key: 'n', header: 'Name', render: (e: NonNullable<typeof envs.data>[number]) => <b>{e.name}</b> },
          { key: 'k', header: 'Kind', render: (e) => <Pill small>{e.kind}</Pill> },
          { key: 't', header: 'Target', render: (e) => `${e.target.kind}${e.target.host ? ` ${e.target.host}` : ''}` },
          { key: 'a', header: 'Approvals', render: (e) => e.policy.required_approvals },
          { key: 'r', header: 'Refs', render: (e) => e.policy.allowed_refs?.join(', ') || 'any', hideSm: true },
          { key: 'x', header: '', render: (e) => <Button size="sm" variant="ghost" icon aria-label={`Delete ${e.name}`} onClick={() => void actions.deleteEnvironment(org, repo, e.name).then(() => { toast.push({ tone: 'ok', title: `Deleted ${e.name}` }); return qc.invalidateQueries({ queryKey: qk.r(org, repo, 'environments') }) })}><Trash2 /></Button> },
        ]}
        rows={envs.data ?? []}
        rowKey={(e) => e.id}
        empty={<p className="muted">No environment records: deployments use the v0 defaults. Create one from the Deployments tab.</p>}
      />
    </Card>
  )
}

function SecretsSection({ org, repo }: { org: string; repo: string }) {
  const { data: features } = useFeatures()
  const secrets = useSecrets(org, repo, features?.pipelines_actions !== false)
  const qc = useQueryClient()
  const toast = useToast()
  const [name, setName] = useState('')
  const [value, setValue] = useState('')
  const [env, setEnv] = useState('')
  const [busy, setBusy] = useState(false)
  if (features && !features.pipelines_actions) return <FeatureHint feature="pipelines.actions" what="Secrets" />
  return (
    <div className="stack">
      <Card title={<><KeyRound aria-hidden /> Secrets</>}>
        <p className="small muted">Encrypted at rest, injected only into the step that references <code>secrets.NAME</code>, masked in logs. Values are never shown again.</p>
        <form
          className="row"
          style={{ marginTop: 'var(--sp-3)' }}
          onSubmit={async (e) => {
            e.preventDefault()
            setBusy(true)
            try {
              await actions.setSecret(org, repo, { name, value, environment: env || undefined })
              toast.push({ tone: 'ok', title: `Secret ${name} saved` })
              setName('')
              setValue('')
              await qc.invalidateQueries({ queryKey: qk.r(org, repo, 'secrets') })
            } catch {
              /* api */
            } finally {
              setBusy(false)
            }
          }}
        >
          <Input small value={name} onChange={(e) => setName(e.target.value.toUpperCase())} placeholder="NAME" aria-label="Secret name" pattern="[A-Za-z_][A-Za-z0-9_]*" style={{ width: 180 }} />
          <Input small type="password" value={value} onChange={(e) => setValue(e.target.value)} placeholder="value" aria-label="Secret value" style={{ width: 220 }} autoComplete="off" />
          <Input small value={env} onChange={(e) => setEnv(e.target.value)} placeholder="environment (optional)" aria-label="Environment" style={{ width: 180 }} />
          <Button size="sm" type="submit" variant="primary" loading={busy} disabled={!name || !value}>
            Save secret
          </Button>
        </form>
      </Card>
      <Card tight>
        <Table
          columns={[
            { key: 'n', header: 'Name', render: (s: Secret) => <code>{s.name}</code> },
            { key: 'e', header: 'Environment', render: (s) => s.environment || 'repository-wide' },
            { key: 'u', header: 'Updated', render: (s) => <TimeAgo iso={s.updated_at ?? s.created_at} /> },
            { key: 'x', header: '', render: (s) => <Button size="sm" variant="ghost" icon aria-label={`Delete ${s.name}`} onClick={() => void actions.deleteSecret(org, repo, s.name, s.environment).then(() => qc.invalidateQueries({ queryKey: qk.r(org, repo, 'secrets') }))}><Trash2 /></Button> },
          ]}
          rows={secrets.data ?? []}
          rowKey={(s) => `${s.name}:${s.environment ?? ''}`}
          empty={<p className="muted">No secrets.</p>}
        />
      </Card>
    </div>
  )
}

function GitHubSection({ org, repo }: { org: string; repo: string }) {
  const { data: features } = useFeatures()
  const link = useGitHubLink(org, repo, features?.github !== false)
  const qc = useQueryClient()
  const toast = useToast()
  const [repository, setRepository] = useState('')
  const [busy, setBusy] = useState(false)
  if (features && !features.github) return <FeatureHint feature="fpi.github" what="The GitHub adapter" />
  const raw = link.data as ({ link?: GitHubLink; webhook_url?: string } & GitHubLink) | null | undefined
  const l: GitHubLink | undefined = raw?.link ?? (raw && 'owner' in raw ? (raw as GitHubLink) : undefined)
  const run = async (fn: () => Promise<unknown>, ok: string) => {
    setBusy(true)
    try {
      await fn()
      toast.push({ tone: 'ok', title: ok })
      await qc.invalidateQueries({ queryKey: qk.r(org, repo, 'github') })
    } catch {
      /* api */
    } finally {
      setBusy(false)
    }
  }
  return (
    <Card title={<><Link2 aria-hidden /> GitHub</>}>
      {l ? (
        <div className="stack">
          <dl className="dl">
            <dt>Linked to</dt>
            <dd>
              <a href={`https://github.com/${l.owner}/${l.name}`} target="_blank" rel="noreferrer">
                {l.owner}/{l.name}
              </a>
            </dd>
            <dt>Last sync</dt>
            <dd>
              {l.last_sync_at ? <TimeAgo iso={l.last_sync_at} /> : 'never'} {l.last_sync_status ? <Pill small tone={l.last_sync_status === 'ok' || l.last_sync_status === 'completed' ? 'ok' : 'warn'}>{l.last_sync_status}</Pill> : null}
              {l.last_sync_error ? <div className="small" style={{ color: 'var(--danger-fg)' }}>{l.last_sync_error}</div> : null}
            </dd>
            {raw?.webhook_url ? (
              <>
                <dt>Webhook URL</dt>
                <dd>
                  <CopyBlock text={raw.webhook_url} />
                </dd>
              </>
            ) : null}
          </dl>
          <div className="row">
            <Button loading={busy} onClick={() => void run(() => actions.syncGitHub(org, repo), 'Sync requested')}>
              <RefreshCw /> Sync now
            </Button>
            <Button variant="danger" loading={busy} onClick={() => void run(() => actions.unlinkGitHub(org, repo), 'Unlinked')}>
              Unlink
            </Button>
          </div>
          <p className="small muted">GitHub stays the source of truth for refs, issues and pull requests; mirrored refs live under <code>refs/github/*</code>. Pipeline results are posted back as the <code>ore/pipeline</code> status.</p>
        </div>
      ) : (
        <form
          className="stack"
          onSubmit={(e) => {
            e.preventDefault()
            void run(() => actions.linkGitHub(org, repo, repository), 'Linked and mirrored')
          }}
        >
          <p className="small">Run the loop on a repository that stays on GitHub. The server needs <code>ORE_GITHUB_TOKEN</code>; then link, and add the webhook GitHub-side with the secret the link returns.</p>
          <Field label="GitHub repository" htmlFor="gh-repo">
            <Input id="gh-repo" value={repository} onChange={(e) => setRepository(e.target.value)} placeholder="owner/name" style={{ width: 300 }} />
          </Field>
          <div>
            <Button type="submit" variant="primary" loading={busy} disabled={!repository.includes('/')}>
              Link
            </Button>
          </div>
        </form>
      )}
    </Card>
  )
}

function WebhooksSection({ org }: { org: string }) {
  const { data: features } = useFeatures()
  const hooks = useWebhooks(org, !!features?.webhooks)
  if (features && !features.webhooks) return <FeatureHint feature="connect.webhooks" what="Outbound webhooks (CloudEvents over Standard Webhooks, with retry and replay)" />
  return (
    <Card tight title={<><Webhook aria-hidden /> Webhooks</>}>
      <Table
        columns={[
          { key: 'u', header: 'URL', render: (h: NonNullable<typeof hooks.data>[number]) => <code>{h.url}</code> },
          { key: 't', header: 'Types', render: (h) => h.types?.join(', ') ?? 'all' },
          { key: 'a', header: 'Active', render: (h) => (h.active === false ? <Pill small tone="danger">paused</Pill> : <Pill small tone="ok">active</Pill>) },
          { key: 'l', header: 'Last delivery', render: (h) => (h.last_delivery ? <TimeAgo iso={h.last_delivery} /> : '—') },
        ]}
        rows={hooks.data ?? []}
        rowKey={(h) => h.id}
        empty={<p className="muted">No webhooks configured.</p>}
      />
    </Card>
  )
}

function MCPSection({ org, repo }: { org: string; repo: string }) {
  const { data: features } = useFeatures()
  const base = features?.base_url || window.location.origin
  return (
    <Card title={<><Plug aria-hidden /> Connect an agent to this repository</>}>
      <div className="stack">
        <Field label="Gateway session (writes are policy-checked and on the ledger)" htmlFor="m1">
          <CopyBlock text={`ore api POST /api/v0/gateway/sessions '{"agent_id":"claude-code","repo":"${org}/${repo}"}'`} />
        </Field>
        <Field label="Register in Claude Code with the session token" htmlFor="m2">
          <CopyBlock text={`claude mcp add --transport http ore ${base}/mcp --header "Authorization: Bearer gws_…"`} />
        </Field>
        <p className="small muted">
          Rate limit: 20 writes per 10 minutes per session by default. Everything refused is recorded as <code>io.ore.agent.session.write.gated</code>. More under <Link to="/settings/$section" params={{ section: 'mcp' }}>site settings</Link>.
        </p>
      </div>
    </Card>
  )
}
