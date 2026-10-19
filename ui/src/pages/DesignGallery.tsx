import { useState } from 'react'
import { Bot, Check, CircleDot, GitPullRequest, Search } from 'lucide-react'
import { ActorChip, Avatar, Button, Callout, Card, Checkbox, Dialog, EmptyState, Field, Input, Kbd, Menu, Pill, Segmented, Select, Skeleton, Stat, StatusPill, Table, Textarea, useToast } from '../design'
import { LevelBadge, LoopSteps, useDocumentTitle } from '../shell/common'

/** A living reference of the design system; also what the dark/light screenshots capture. */
export function DesignGallery() {
  useDocumentTitle('Design system')
  const toast = useToast()
  const [open, setOpen] = useState(false)
  const [seg, setSeg] = useState<'split' | 'unified'>('split')
  const swatches = ['bg', 'surface', 'surface-2', 'surface-3', 'border', 'fg', 'fg-muted', 'fg-subtle', 'accent', 'ok', 'danger', 'warn', 'info', 'agent', 'bot']
  return (
    <div className="container page stack" style={{ gap: 'var(--sp-5)' }}>
      <div className="page-head">
        <div>
          <h1>Design system</h1>
          <p className="sub">Tokens and components as rendered. See ui/src/design/README.md for usage rules.</p>
        </div>
      </div>
      <Card title="Color tokens">
        <div className="row" style={{ gap: 'var(--sp-3)' }}>
          {swatches.map((s) => (
            <div key={s} style={{ textAlign: 'center', fontSize: 'var(--fs-0)' }}>
              <div style={{ width: 56, height: 36, borderRadius: 6, background: `var(--${s})`, border: '1px solid var(--border)' }} />
              <code>{s}</code>
            </div>
          ))}
        </div>
      </Card>
      <Card title="Type scale">
        {[7, 6, 5, 4, 3, 2, 1, 0].map((n) => (
          <div key={n} style={{ fontSize: `var(--fs-${n})`, lineHeight: 1.3, marginBottom: 6 }}>
            fs-{n} · The forge pours copper into code
          </div>
        ))}
        <p className="mono">mono: const greeting = "Hello, Ore!"</p>
      </Card>
      <Card title="Buttons">
        <div className="row">
          <Button variant="primary">Primary</Button>
          <Button>Default</Button>
          <Button variant="danger">Danger</Button>
          <Button variant="ghost">Ghost</Button>
          <Button size="sm">Small</Button>
          <Button loading>Loading</Button>
          <Button disabled>Disabled</Button>
          <Button icon aria-label="Search">
            <Search />
          </Button>
          <div className="btn-group">
            <Button size="sm" aria-pressed>
              Open
            </Button>
            <Button size="sm">Closed</Button>
            <Button size="sm">All</Button>
          </div>
          <Segmented value={seg} onChange={setSeg} label="Diff view" options={[{ value: 'split', label: 'Split' }, { value: 'unified', label: 'Unified' }]} />
        </div>
      </Card>
      <Card title="Inputs">
        <div className="grid-2">
          <Field label="Title" htmlFor="g1" hint="A hint under the field.">
            <Input id="g1" placeholder="Placeholder" />
          </Field>
          <Field label="With an error" htmlFor="g2" error="This field is required.">
            <Input id="g2" aria-invalid />
          </Field>
          <Field label="Select" htmlFor="g3">
            <Select id="g3" defaultValue="b">
              <option value="a">Queue</option>
              <option value="b">Land when green</option>
              <option value="c">Optimistic</option>
            </Select>
          </Field>
          <Field label="Textarea" htmlFor="g4">
            <Textarea id="g4" placeholder="Markdown is supported" />
          </Field>
        </div>
        <div style={{ marginTop: 'var(--sp-3)' }}>
          <Checkbox label="Allow agent approvals" />
        </div>
      </Card>
      <Card title="Pills, status and actors">
        <div className="row">
          {['succeeded', 'running', 'failed', 'queued', 'merged', 'open', 'closed', 'awaiting_approval', 'reverted'].map((s) => (
            <StatusPill key={s} status={s} />
          ))}
          <Pill tone="agent">agent</Pill>
          <Pill tone="bot">bot</Pill>
          <Pill outline>outline</Pill>
          <Kbd>⌘</Kbd>
          <Kbd>K</Kbd>
        </div>
        <div className="row" style={{ marginTop: 'var(--sp-3)' }}>
          <ActorChip actor={{ kind: 'human', id: 'alice' }} />
          <ActorChip actor={{ kind: 'agent', id: 'claude-code' }} />
          <ActorChip actor={{ kind: 'app', id: 'github' }} />
          <Avatar actor={{ kind: 'human', id: 'bob' }} size="lg" />
          <LevelBadge level={2} />
          <LevelBadge level={4} />
        </div>
      </Card>
      <Card title="The loop">
        <div className="stack">
          <LoopSteps run={{ status: 'running', step: 'verify' }} />
          <LoopSteps run={{ status: 'completed', step: 'observe' }} />
          <LoopSteps run={{ status: 'failed', step: 'change', error: 'x' }} />
          <LoopSteps run={{ status: 'blocked', step: 'awaiting-human' }} />
        </div>
      </Card>
      <div className="grid-3">
        <Stat label="Deploys / week" value="12.5" delta="+3 vs last 30 days" />
        <Stat label="Lead time (median)" value="42 m" />
        <Stat label="Change failure rate" value="4%" />
      </div>
      <Card title="Table" tight>
        <Table
          columns={[
            { key: 'n', header: '#', render: (r: { n: number; t: string; s: string }) => `#${r.n}`, width: '60px' },
            { key: 't', header: 'Title', render: (r) => r.t },
            { key: 's', header: 'Status', render: (r) => <StatusPill status={r.s} small /> },
          ]}
          rows={[
            { n: 1, t: 'Greeting should say Hello, Ore!', s: 'merged' },
            { n: 2, t: 'Retry loop never backs off', s: 'open' },
          ]}
          rowKey={(r) => String(r.n)}
        />
      </Card>
      <Card title="Callouts and empty state">
        <div className="stack">
          <Callout tone="info" icon={<CircleDot />}>
            L3 Land: agents merge when verification is green.
          </Callout>
          <Callout tone="warn" icon={<GitPullRequest />}>
            A push created a new patch set; approvals reset.
          </Callout>
          <Callout tone="agent" icon={<Bot />}>
            This hunk was written by agent/claude-code (session abc123).
          </Callout>
          <EmptyState icon={<CircleDot />} title="No issues yet" command="ore issue create acme/app &quot;Fix the flaky retry&quot;" compact>
            Issues are the signals that start the loop.
          </EmptyState>
        </div>
      </Card>
      <Card title="Overlays and skeletons">
        <div className="row">
          <Button onClick={() => setOpen(true)}>Open dialog</Button>
          <Menu label="Example menu" items={[{ label: 'Rename' }, { label: 'Archive' }, { kind: 'sep' }, { label: 'Delete', danger: true }]} trigger={(p) => <Button onClick={p.toggle}>Menu</Button>} />
          <Button onClick={() => toast.push({ tone: 'ok', title: 'Saved', body: 'Your change is on the ledger.' })}>Toast</Button>
          <Button onClick={() => toast.push({ tone: 'danger', title: 'Server error (500)', body: 'The ledger did not answer.', action: { label: 'Retry', onClick: () => {} } })}>Error toast</Button>
          <Skeleton width={120} />
          <Skeleton width={80} />
        </div>
        <Dialog open={open} onClose={() => setOpen(false)} title="A dialog" foot={<><Button onClick={() => setOpen(false)}>Cancel</Button><Button variant="primary" onClick={() => setOpen(false)}><Check /> Confirm</Button></>}>
          Dialogs are native &lt;dialog&gt; elements: focus is trapped, Escape closes, the backdrop dims.
        </Dialog>
      </Card>
    </div>
  )
}
