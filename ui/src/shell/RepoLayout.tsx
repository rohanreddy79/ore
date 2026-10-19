import { useState } from 'react'
import { Outlet, useParams } from '@tanstack/react-router'
import { Activity, BookOpen, Bot, CircleDot, Code2, Copy, GitPullRequest, Rocket, Settings, Workflow } from 'lucide-react'
import { Button, CopyBlock, Dialog, SkeletonRows, Tabs, type TabDef } from '../design'
import { ApiError } from '../lib/api'
import { useChanges, useFeatures, useIssues, useRepo } from '../lib/queries'
import { LevelBadge, useDocumentTitle } from './common'
import { NotFound } from './NotFound'

export function RepoLayout() {
  const { org, repo } = useParams({ from: '/$org/$repo' })
  const q = useRepo(org, repo)
  const issues = useIssues(org, repo)
  const changes = useChanges(org, repo)
  const { data: features } = useFeatures()
  const [cloneOpen, setCloneOpen] = useState(false)
  useDocumentTitle(`${org}/${repo}`)

  if (q.isError && q.error instanceof ApiError && q.error.status === 404) return <NotFound what="repository" />
  const r = q.data
  const openIssues = issues.data?.filter((i) => i.state === 'open').length
  const openChanges = changes.data?.filter((c) => c.state === 'open').length
  const p = { org, repo }
  const tabs: TabDef[] = [
    { to: '/$org/$repo', params: p, label: 'Code', icon: <Code2 />, exact: true },
    { to: '/$org/$repo/issues', params: p, label: 'Issues', icon: <CircleDot />, count: openIssues },
    { to: '/$org/$repo/changes', params: p, label: 'Changes', icon: <GitPullRequest />, count: openChanges },
    { to: '/$org/$repo/pipelines', params: p, label: 'Pipelines', icon: <Workflow /> },
    { to: '/$org/$repo/deployments', params: p, label: 'Deployments', icon: <Rocket /> },
    { to: '/$org/$repo/agents', params: p, label: 'Agents', icon: <Bot /> },
    { to: '/$org/$repo/ledger', params: p, label: 'Ledger', icon: <Activity /> },
    { to: '/$org/$repo/settings', params: p, label: 'Settings', icon: <Settings /> },
  ]
  const base = features?.base_url || window.location.origin
  const httpClone = `${base}/${org}/${repo}.git`
  const sshPort = features?.ssh_addr ? features.ssh_addr.split(':').pop() : undefined
  const host = (() => {
    try {
      return new URL(base).hostname
    } catch {
      return 'localhost'
    }
  })()
  const sshClone = sshPort ? `ssh://git@${host}:${sshPort}/${org}/${repo}.git` : null

  return (
    <>
      <div className="repo-head">
        <div className="container">
          <div className="repo-title">
            <h1>
              <BookOpen aria-hidden />
              <span>
                {org} / <b>{repo}</b>
              </span>
            </h1>
            {r ? <LevelBadge level={r.autonomy_level} /> : null}
            <div className="repo-actions">
              <Button size="sm" onClick={() => setCloneOpen(true)}>
                <Copy /> Clone
              </Button>
            </div>
          </div>
          <Tabs tabs={tabs} label="Repository sections" />
        </div>
      </div>
      <div className="container page" style={{ minHeight: '60vh' }}>
        {q.isPending ? <SkeletonRows rows={6} /> : <Outlet />}
      </div>
      <Dialog open={cloneOpen} onClose={() => setCloneOpen(false)} title="Clone this repository">
        <div className="stack">
          <div className="field">
            <span className="field-label">HTTPS (token as password)</span>
            <CopyBlock text={`git clone ${httpClone}`} />
            <span className="hint">Any user name works with your access token as the password.</span>
          </div>
          <div className="field">
            <span className="field-label">SSH</span>
            {sshClone ? <CopyBlock text={`git clone ${sshClone}`} /> : <span className="hint">SSH is not enabled on this server (identity extension off or ORE_SSH_ADDR=off).</span>}
            {sshClone ? <span className="hint">Add your public key under Settings → SSH keys first.</span> : null}
          </div>
          <div className="field">
            <span className="field-label">No clone at all</span>
            <CopyBlock text={`ore commit ${org}/${repo} -m "message" path/to/file`} />
            <span className="hint">Server-side commits through the API: the CLI and the Edit button on any file write without a working copy.</span>
          </div>
        </div>
      </Dialog>
    </>
  )
}
