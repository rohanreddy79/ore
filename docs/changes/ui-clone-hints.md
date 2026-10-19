# Clone hints follow the git transport

Builds on `git-transport-off.md`. No migration, no new route. ADR 0016 says a page that teaches git vocabulary the server refuses is a defect; this change removes the last ones on the repository page.

## What landed

The web app reads `GET /api/v0/orgs/{org}/settings` (`useOrgSettings` in `ui/src/lib/queries.ts`). A 403 (not a member), a 404 (unknown organization, or a server without the identity extension, where the route is absent) and 401/405/501 read as no settings, which means `on`. The answer is cached for a minute and refreshed by the live ledger on `io.ore.organization.settings.changed` (subject `orgs/<org>/settings`).

All user-facing text for the three modes lives in `ui/src/lib/transport.ts`, so the clone dialog and the empty-repository page cannot drift apart.

| Mode | Clone dialog (`RepoLayout`) | Empty repository (`Code`) | Server-rendered `/{org}/{repo}` |
|---|---|---|---|
| `on` | "Clone": HTTPS and SSH `git clone`, plus `ore commit` | `git remote add … && git push`, or `ore commit` | `clone: git clone …` |
| `read-only` | "Clone": a notice that pushes are off with the reason and `ore open` + `ore save -m`, then the clone commands and `ore commit` | `ore commit` only (a push would be refused), with the reason | the clone line, then "git pushes are off … save your work with `ore save` after `ore open`", with the reason |
| `off` | "Open": a notice that git is off with the reason, `ore login <server>` + `ore open org/repo`, then `ore commit`; no `git clone` | `ore commit` only, with the reason | "git is off for org (reason); work with the Ore client: `ore login <server>` then `ore open org/repo`"; no `git clone` |

The empty-repository page waits for the settings before rendering, so an organization with git off never sees a `git push` hint flash by. The `ore commit` example there no longer uses `$(git ls-files)`.

The server-rendered page (`internal/web/ui_repo.go`, `ui_tmpl_repo.go`) reads `DB.OrgSettings` directly, so it works on any server, with or without the identity extension. A settings read error falls back to the defaults rather than failing the page.

## Tests

- `ui/src/shell/RepoLayout.test.tsx`: RepoLayout under a real router with the API mocked; `on`, `read-only` (notice, reason, `ore save`, clone kept), `off` (button "Open", `ore login` + `ore open`, reason, no `git clone`), and 403 and 404 from the settings endpoint read as `on`.
- `ui/src/lib/live.test.ts`: the settings event invalidates the organization's settings query.
- `ui/e2e/transport.spec.ts`: against a real `ore serve`, an organization switched off shows the Ore client in the empty-repository hint and the dialog with no git commands; switched to read-only, the clone commands return with the `ore save` route.
- `internal/web/ui_test.go` `TestUIRepoCloneHintFollowsGitTransport`: the server-rendered page in all three modes and back on.

## Limits

- Other pages that mention git in passing (the edit dialog's note on direct pushes, the site settings' SSH key help) are not mode-aware yet.
- A user who is not a member of the organization but can read a public repository sees the `on` hints; the bridge refuses them anyway with the Ore client message.
