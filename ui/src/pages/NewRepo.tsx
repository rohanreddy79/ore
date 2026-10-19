import { useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Button, Card, Field, Input, Select, useToast } from '../design'
import { ApiError } from '../lib/api'
import { LEVEL_EXPLANATIONS } from '../lib/format'
import { actions, useOrgs } from '../lib/queries'
import { useDocumentTitle } from '../shell/common'

export function NewRepo() {
  useDocumentTitle('New repository')
  const orgs = useOrgs()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const toast = useToast()
  const [org, setOrg] = useState('')
  const [newOrg, setNewOrg] = useState('')
  const [name, setName] = useState('')
  const [branch, setBranch] = useState('main')
  const [level, setLevel] = useState(2)
  const [port, setPort] = useState(8080)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const orgName = org === '__new' || (orgs.data ?? []).length === 0 ? newOrg : org || orgs.data?.[0]?.name || ''

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      if ((org === '__new' || (orgs.data ?? []).length === 0) && newOrg) {
        try {
          await actions.createOrg(newOrg)
        } catch (err) {
          if (!(err instanceof ApiError && err.status === 409)) throw err
        }
      }
      const r = await actions.createRepo({ org: orgName, name, default_branch: branch, autonomy_level: level, deploy_port: port })
      await qc.invalidateQueries({ queryKey: ['repos'] })
      toast.push({ tone: 'ok', title: `Created ${r.org}/${r.name}` })
      void navigate({ to: '/$org/$repo', params: { org: r.org, repo: r.name } })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not create the repository')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="container page" style={{ maxWidth: 720 }}>
      <div className="page-head">
        <div>
          <h1>New repository</h1>
          <p className="sub">Names use lowercase letters, digits, dots, dashes and underscores.</p>
        </div>
      </div>
      <Card>
        <form className="stack" onSubmit={submit}>
          <div className="grid-2">
            <Field label="Organization" htmlFor="org">
              {(orgs.data ?? []).length > 0 ? (
                <Select id="org" value={org || orgs.data?.[0]?.name} onChange={(e) => setOrg(e.target.value)}>
                  {(orgs.data ?? []).map((o) => (
                    <option key={o.id} value={o.name}>
                      {o.name}
                    </option>
                  ))}
                  <option value="__new">New organization…</option>
                </Select>
              ) : (
                <Input id="org" value={newOrg} onChange={(e) => setNewOrg(e.target.value)} placeholder="acme" required pattern="[a-z0-9._-]+" />
              )}
            </Field>
            {org === '__new' ? (
              <Field label="New organization name" htmlFor="neworg">
                <Input id="neworg" value={newOrg} onChange={(e) => setNewOrg(e.target.value)} placeholder="acme" required pattern="[a-z0-9._-]+" />
              </Field>
            ) : null}
            <Field label="Repository name" htmlFor="name">
              <Input id="name" value={name} onChange={(e) => setName(e.target.value)} placeholder="app" required pattern="[a-z0-9._-]+" autoFocus />
            </Field>
            <Field label="Default branch" htmlFor="branch">
              <Input id="branch" value={branch} onChange={(e) => setBranch(e.target.value)} />
            </Field>
            <Field label="Deploy port" htmlFor="port" hint="The port the container listens on.">
              <Input id="port" type="number" min={1} max={65535} value={port} onChange={(e) => setPort(Number(e.target.value))} />
            </Field>
          </div>
          <fieldset style={{ border: 0, padding: 0, margin: 0 }}>
            <legend className="field-label" style={{ marginBottom: 'var(--sp-2)' }}>
              Autonomy level
            </legend>
            <div className="levels">
              {[0, 1, 2, 3, 4, 5].map((l) => {
                const ex = LEVEL_EXPLANATIONS[l]!
                return (
                  <label key={l} className="level-option" data-checked={level === l}>
                    <input type="radio" name="level" value={l} checked={level === l} onChange={() => setLevel(l)} />
                    <span>
                      <span className="t">
                        L{l} {ex.title}
                        {l === 2 ? ' (default)' : ''}
                      </span>
                      <span className="d" style={{ display: 'block' }}>
                        Agents {ex.agents}. Humans {ex.humans}.
                      </span>
                    </span>
                  </label>
                )
              })}
            </div>
          </fieldset>
          {error ? (
            <p className="error" role="alert" style={{ color: 'var(--danger-fg)' }}>
              {error}
            </p>
          ) : null}
          <div className="row">
            <Button type="submit" variant="primary" loading={busy} disabled={!name || !orgName}>
              Create repository
            </Button>
            <Button type="button" onClick={() => history.back()}>
              Cancel
            </Button>
          </div>
        </form>
      </Card>
    </div>
  )
}
