import { useState } from 'react'
import { useNavigate, useParams } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Button, Card, Field, Input, Segmented, Textarea, useToast } from '../../design'
import { actions, qk } from '../../lib/queries'
import { Markdown, useDocumentTitle } from '../../shell/common'

export function NewIssuePage() {
  const { org, repo } = useParams({ from: '/$org/$repo/issues/new' })
  const navigate = useNavigate()
  const qc = useQueryClient()
  const toast = useToast()
  const [title, setTitle] = useState('')
  const [body, setBody] = useState('')
  const [mode, setMode] = useState<'write' | 'preview'>('write')
  const [busy, setBusy] = useState(false)
  useDocumentTitle(`New issue · ${org}/${repo}`)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!title.trim()) return
    setBusy(true)
    try {
      const issue = await actions.createIssue(org, repo, { title: title.trim(), body })
      await qc.invalidateQueries({ queryKey: qk.r(org, repo, 'issues') })
      toast.push({ tone: 'ok', title: `Opened issue #${issue.number}` })
      void navigate({ to: '/$org/$repo/issues/$n', params: { org, repo, n: String(issue.number) } })
    } catch {
      /* toast raised by the API layer */
    } finally {
      setBusy(false)
    }
  }

  return (
    <div style={{ maxWidth: 820 }}>
      <div className="page-head">
        <div>
          <h1>New issue</h1>
          <p className="sub">Describe the signal. Agents read the title and body as their prompt.</p>
        </div>
      </div>
      <Card>
        <form className="stack" onSubmit={submit}>
          <Field label="Title" htmlFor="title">
            <Input id="title" value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Greeting should say Hello, Ore!" required autoFocus maxLength={256} />
          </Field>
          <div className="field">
            <div className="row between">
              <label htmlFor="body">Description</label>
              <Segmented value={mode} onChange={setMode} label="Editor mode" options={[{ value: 'write', label: 'Write' }, { value: 'preview', label: 'Preview' }]} />
            </div>
            {mode === 'write' ? (
              <Textarea id="body" value={body} onChange={(e) => setBody(e.target.value)} placeholder="What is wrong, how to reproduce it, what done looks like. Markdown is supported." rows={10} />
            ) : (
              <div className="card flat" style={{ padding: 'var(--sp-3) var(--sp-4)', minHeight: 120 }}>
                <Markdown source={body} />
              </div>
            )}
            <span className="hint">Tip: include the exact failing test or command; the agent runs it as verification.</span>
          </div>
          <div className="row">
            <Button type="submit" variant="primary" loading={busy} disabled={!title.trim()}>
              Submit issue
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
