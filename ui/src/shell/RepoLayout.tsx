import { useState } from 'react'
import { Outlet, useParams } from '@tanstack/react-router'
import { Activity, BookOpen, Bot, CircleDot, Code2, Copy, GitPullRequest, Rocket, Settings, Workflow } from 'lucide-react'
import { Button, Callout, CopyBlock, Dialog, SkeletonRows, Tabs, type TabDef } from '../design'
import { ApiError } from '../lib/api'
import { useChanges, useFeatures, useIssues, useOrgSettings, useRepo } from '../lib/queries'
import { gitCloneAllowed, transportState, transportText as tt, type TransportState } from '../lib/transport'
import { LevelBadge, useDocumentTitle } from './common'
import { NotFound } from './NotFound'

export function RepoLayout() {
  const { org, repo } = useParams({ from: '/$org/$repo' })
  const q = useRepo(org, repo)
  const issues = useIssues(org, repo)
  const changes = useChanges(org, repo)
  const { data: features } = useFeatures()
  const settings = useOrgSettings(org)
  const transport = transportState(settings.data)
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
                <Copy /> {tt.button(transport)}
              </Button>
            </div>
          </div>
          <Tabs tabs={tabs} label="Repository sections" />
        </div>
      </div>
      <div className="container page" style={{ minHeight: '60vh' }}>
        {q.isPending ? <SkeletonRows rows={6} /> : <Outlet />}
      </div>
      <Dialog open={cloneOpen} onClose={() => setCloneOpen(false)} title={tt.dialogTitle(transport)}>
        <ClonePanel transport={transport} base={base} org={org} repo={repo} httpClone={httpClone} sshClone={sshClone} />
      </Dialog>
    </>
  )
}

/** How to get the repository onto a workstation, for the organization's git transport mode. */
export function ClonePanel({ transport, base, org, repo, httpClone, sshClone }: { transport: TransportState; base: string; org: string; repo: string; httpClone: string; sshClone: string | null }) {
  const reason = tt.reason(transport.reason)
  return (
    <div className="stack">
      {transport.mode === 'off' ? (
        <>
          <Callout tone="info">
            <b>{tt.offTitle(org)}</b>
            <p>{tt.offBody}</p>
            {reason ? <p className="small">{reason}</p> : null}
          </Callout>
          <div className="field">
            <span className="field-label">{tt.oreFlowLabel}</span>
            <CopyBlock text={tt.oreFlow(base, org, repo)} />
            <span className="hint">{tt.oreFlowHint}</span>
          </div>
        </>
      ) : null}
      {gitCloneAllowed(transport) ? (
        <>
          {transport.mode === 'read-only' ? (
            <Callout tone="warn">
              <b>{tt.readOnlyTitle(org)}</b>
              <p>{tt.readOnlyBody}</p>
              {reason ? <p className="small">{reason}</p> : null}
              <CopyBlock text={tt.readOnlyFlow(org, repo)} />
            </Callout>
          ) : null}
          <div className="field">
            <span className="field-label">{tt.httpLabel}</span>
            <CopyBlock text={tt.gitClone(httpClone)} />
            <span className="hint">{tt.httpHint}</span>
          </div>
          <div className="field">
            <span className="field-label">{tt.sshLabel}</span>
            {sshClone ? <CopyBlock text={tt.gitClone(sshClone)} /> : <span className="hint">{tt.sshDisabled}</span>}
            {sshClone ? <span className="hint">{tt.sshHint}</span> : null}
          </div>
        </>
      ) : null}
      <div className="field">
        <span className="field-label">{tt.noCloneLabel}</span>
        <CopyBlock text={tt.oreCommit(org, repo)} />
        <span className="hint">{tt.noCloneHint}</span>
      </div>
    </div>
  )
}
