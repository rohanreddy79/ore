import { useState } from 'react'
import { Link, useParams } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Layers, Rocket } from 'lucide-react'
import { Button, Callout, Card, Dialog, EmptyState, Field, Input, StatusPill, Textarea, useToast } from '../../design'
import { ApiError } from '../../lib/api'
import { refName } from '../../lib/format'
import { actions, qk, useFeatures, useRefs, useStacks } from '../../lib/queries'
import type { Stack } from '../../lib/types'
import { FeatureHint, QueryState, TimeAgo, useDocumentTitle } from '../../shell/common'

export function StacksPage() {
  const params = useParams({ strict: false }) as { org: string; repo: string; id?: string }
  const { org, repo } = params
  const { data: features } = useFeatures()
  const stacks = useStacks(org, repo)
  const refs = useRefs(org, repo)
  const qc = useQueryClient()
  const toast = useToast()
  const [open, setOpen] = useState(false)
  const [branches, setBranches] = useState('')
  const [target, setTarget] = useState('')
  const [busy, setBusy] = useState(false)
  const [landError, setLandError] = useState<string | null>(null)
  useDocumentTitle(`Stacks · ${org}/${repo}`)

  const land = async (s: Stack) => {
    setBusy(true)
    setLandError(null)
    try {
      await actions.landStack(org, repo, s.id)
      toast.push({ tone: 'ok', title: 'Stack landed' })
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) setLandError(((e.body as { error?: string })?.error ?? e.message) + ' Lower changes stayed merged; restack the failing branch and land again.')
    } finally {
      setBusy(false)
      await qc.invalidateQueries({ queryKey: qk.r(org, repo) })
    }
  }
  const create = async () => {
    setBusy(true)
    try {
      await actions.openStack(org, repo, branches.split(/[\n,]/).map((s) => s.trim()).filter(Boolean), target || undefined)
      await qc.invalidateQueries({ queryKey: qk.r(org, repo) })
      setOpen(false)
      setBranches('')
      toast.push({ tone: 'ok', title: 'Stack opened' })
    } catch {
      /* api */
    } finally {
      setBusy(false)
    }
  }

  if (features && !features.review_v1) return <FeatureHint feature="review.v1" what="Stacks" />
  const selected = params.id ? stacks.data?.find((s) => s.id === params.id) : undefined
  const list = selected ? [selected] : (stacks.data ?? [])
  const branchNames = Object.keys(refs.data ?? {}).filter((r) => r.startsWith('refs/heads/')).map(refName)

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>
            <Layers aria-hidden /> Stacks
          </h1>
          <p className="sub">Ordered branches, each change targeting the one below. A stack lands bottom-up with server-side rebases and stops cleanly at the first conflict.</p>
        </div>
        <Button variant="primary" size="sm" onClick={() => setOpen(true)}>
          New stack
        </Button>
      </div>
      {landError ? <Callout tone="danger">{landError}</Callout> : null}
      <QueryState query={stacks}>
        {() =>
          list.length === 0 ? (
            <Card>
              <EmptyState icon={<Layers />} title="No stacks" command={`ore api POST /api/v0/repos/${org}/${repo}/stacks '{"branches":["part-1","part-2","part-3"]}'`}>
                Push several dependent branches (each containing the previous), then open a stack naming them bottom to top.
              </EmptyState>
            </Card>
          ) : (
            list.map((s) => (
              <Card
                key={s.id}
                title={
                  <>
                    <Link to="/$org/$repo/stacks/$id" params={{ org, repo, id: s.id }} className="quiet">
                      Stack {s.id.slice(0, 12)}
                    </Link>{' '}
                    <StatusPill status={s.state} small /> <span className="muted small">→ {refName(s.target_ref)}</span>
                  </>
                }
                actions={
                  s.state === 'open' || s.state === 'aborted' ? (
                    <Button size="sm" variant="primary" loading={busy} onClick={() => void land(s)}>
                      <Rocket /> Land stack
                    </Button>
                  ) : null
                }
              >
                {s.error ? (
                  <Callout tone="danger">
                    Aborted at change #{s.failed_change_number}: {s.error}
                  </Callout>
                ) : null}
                <ol className="stack" style={{ margin: 'var(--sp-2) 0 0', paddingLeft: 'var(--sp-5)', gap: 6 }}>
                  {[...(s.changes ?? [])].reverse().map((c) => (
                    <li key={c.id}>
                      <Link to="/$org/$repo/changes/$n" params={{ org, repo, n: String(c.number) }} search={{}}>
                        #{c.number} {c.title || refName(c.source_ref)}
                      </Link>{' '}
                      <StatusPill status={c.state} small /> <span className="muted small mono">
                        {refName(c.source_ref)} → {refName(c.target_ref)}
                      </span>
                    </li>
                  ))}
                </ol>
                <p className="small muted" style={{ marginTop: 'var(--sp-2)' }}>
                  Top first; the bottom lands first. Opened by {s.created_by.id} <TimeAgo iso={s.created_at} />.
                </p>
              </Card>
            ))
          )
        }
      </QueryState>
      <Dialog open={open} onClose={() => setOpen(false)} title="Open a stack" foot={<><Button onClick={() => setOpen(false)}>Cancel</Button><Button variant="primary" loading={busy} disabled={!branches.trim()} onClick={() => void create()}>Open stack</Button></>}>
        <div className="stack">
          <Field label="Branches, bottom to top (one per line)" htmlFor="stack-branches" hint={branchNames.length ? `Known branches: ${branchNames.join(', ')}` : undefined}>
            <Textarea id="stack-branches" mono value={branches} onChange={(e) => setBranches(e.target.value)} rows={4} placeholder={'part-1\npart-2\npart-3'} />
          </Field>
          <Field label="Target ref (optional)" htmlFor="stack-target">
            <Input id="stack-target" value={target} onChange={(e) => setTarget(e.target.value)} placeholder="refs/heads/main" />
          </Field>
        </div>
      </Dialog>
    </div>
  )
}
