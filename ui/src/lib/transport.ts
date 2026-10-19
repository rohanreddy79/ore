// What the web app says about getting a repository onto a workstation, for
// each git transport mode of its organization (ADR 0016 §3). Every page that
// shows a clone or push hint takes its text from here, so the three states
// cannot drift apart and no page teaches a git command the bridge refuses.

import type { GitTransport, OrgSettings } from './types'

export type TransportState = { mode: GitTransport; reason: string }

/** The mode from the settings endpoint; absent settings (403, 404, no identity extension) mean "on". */
export function transportState(st: OrgSettings | null | undefined): TransportState {
  const mode = st?.git_transport
  return { mode: mode === 'off' || mode === 'read-only' ? mode : 'on', reason: st?.git_transport_reason ?? '' }
}

/** True when git clients may push: the empty-repository hint offers `git push` only then. */
export const gitPushAllowed = (s: TransportState) => s.mode === 'on'
/** True when git clients may clone and fetch. */
export const gitCloneAllowed = (s: TransportState) => s.mode !== 'off'

export const transportText = {
  /** The repository header button and dialog title. */
  button: (s: TransportState) => (gitCloneAllowed(s) ? 'Clone' : 'Open'),
  dialogTitle: (s: TransportState) => (gitCloneAllowed(s) ? 'Clone this repository' : 'Open this repository'),

  httpLabel: 'HTTPS (token as password)',
  httpHint: 'Any user name works with your access token as the password.',
  sshLabel: 'SSH',
  sshHint: 'Add your public key under Settings → SSH keys first.',
  sshDisabled: 'SSH is not enabled on this server (identity extension off or ORE_SSH_ADDR=off).',
  gitClone: (url: string) => `git clone ${url}`,

  /** read-only: clone works, pushes are refused. */
  readOnlyTitle: (org: string) => `Git pushes are off for ${org}`,
  readOnlyBody: 'Clone and fetch still work. Save your work with the Ore client instead of git push: open the repository, then ore save records your edits as a patch set of your change.',
  readOnlyFlow: (org: string, repo: string) => `ore open ${org}/${repo}\nore save -m "message"`,

  /** off: no git at all. */
  offTitle: (org: string) => `Git is off for ${org}`,
  offBody: 'Work with the Ore client: it talks to the server API and needs no git on your machine.',
  oreFlowLabel: 'Ore client',
  oreFlow: (base: string, org: string, repo: string) => `ore login ${base}\nore open ${org}/${repo}`,
  oreFlowHint: 'Then ore save records your edits, ore sync brings in the latest, and ore land lands your change.',

  /** The recorded reason, shown under the mode's title. */
  reason: (reason: string) => (reason ? `Reason: ${reason}` : ''),

  noCloneLabel: 'No clone at all',
  noCloneHint: 'Server-side commits through the API: the CLI and the Edit button on any file write without a working copy.',
  oreCommit: (org: string, repo: string) => `ore commit ${org}/${repo} -m "message" path/to/file`,

  /** The empty-repository page. */
  emptyCommand: (s: TransportState, base: string, org: string, repo: string, branch: string) =>
    gitPushAllowed(s)
      ? `cd your-project\ngit remote add ore ${base}/${org}/${repo}.git\ngit push ore ${branch}\n\n# or, without git:\nore commit ${org}/${repo} -m "import" path/to/file...`
      : `cd your-project\nore commit ${org}/${repo} -m "import" path/to/file...`,
  emptyBody: (s: TransportState) =>
    gitPushAllowed(s)
      ? 'Push an existing project over HTTP (any user name, your token as the password) or SSH, or commit files through the API. The first push creates the default branch'
      : 'Commit the files through the API with the Ore client; git pushes are refused for this organization. The first commit creates the default branch',
}
