# Git transport off: the per-organization switch

Branch `rohan/sweet-goodall-8e1fcd` (worktree `slice-webapp-223932`). Migration `0012_org_settings.sql`. Implements ADR 0016 §3: "an organization can switch the git transport off". Additive edits to `internal/ledger` (new `orgsettings.go`), `internal/githttp`, `internal/identity` (new `orgsettings.go`, two routes) and `internal/identity/sshd`; two subcommands in `cmd/ore/main.go`; the team checklist in `docs/10` §7.

## What landed

### The setting

`org_settings` has one row per organization, written on the first change; an organization without a row has the defaults. `git_transport` is one of:

| Mode | `git clone`, `fetch`, `ls-remote` | `git push` | Everything else |
|---|---|---|---|
| `on` (default) | work | works | works |
| `read-only` | work | refused | works |
| `off` | refused | refused | works |

"Everything else" is the API, the web app, pipelines (server-side archives), deployments, agent workspaces (clones of the bare repository on the server's disk), the GitHub adapter and importer (they fetch from GitHub on the server), `ore commit`, and the workstation client (`ore open/save/sync/land/status`, which only speak the API). None of them goes through the git bridge, so the switch never reaches them; the end-to-end test checks `ore open` and the commits endpoint with the transport off.

`ledger.OrgSettings`/`ledger.DB.OrgSettings(ctx, org)` read it (two lookups per git request: the repository, then the settings), `ledger.DB.SetOrgSettings(ctx, org, input, actor)` changes it and emits `io.ore.organization.settings.changed` in the same transaction, subject `orgs/<org>/settings`, data `{org, settings, changes: {field: {from, to}}}`. A change that leaves every field as it was writes and emits nothing. A new mode clears the reason (it explained the previous mode) unless the same request carries one; the reason alone can change. The reason is at most 500 characters.

### The transports

Smart HTTP (`internal/githttp`) reads the mode after authentication, so an anonymous probe still gets the 401 and learns nothing. A refused `GET info/refs` is answered the way git expects a smart server to speak: status 200, the `# service=` header, a flush, then an `ERR` packet, which git prints as

```
fatal: remote error: the git transport is off for acme; use the Ore client: ore open acme/app (last day of git was Friday)
```

(an error status would show the user nothing but the number). Protocol v2 clients get the same packets and the same message; the test covers v0 and v2. The RPC endpoints (`POST git-upload-pack`, `POST git-receive-pack`), which a client only reaches after an advertisement, answer 403 with the message as text. Every refusal carries `X-Ore-Git-Transport: <mode>` for non-git clients. In `read-only` mode the message is

```
fatal: remote error: git pushes are off for acme; save your work with the Ore client: ore open acme/app, then ore save
```

SSH (`internal/identity/sshd`) applies the same check after the org policy and prints the same message on stderr with the `ore: ` prefix and exit status 1, which git shows before `fatal: Could not read from remote repository`. Both transports share `githttp.TransportRefusal`, so the text cannot drift. Refused pushes record nothing on the ledger (no receive-pack runs); each refusal is one `Info` log line with the mode, the actor and the path.

### API

| Route | Who | Does |
|---|---|---|
| `GET /api/v0/orgs/{org}/settings` | members of the organization, its agents, administrators | `{org_id, org, git_transport, git_transport_reason, updated_by?, updated_at?}` |
| `PATCH /api/v0/orgs/{org}/settings` | owners of the organization and administrators (`isOrgOwner`, the rule of the membership routes) | body `{git_transport?: "on"\|"read-only"\|"off", reason?}`, at least one field; answers the settings after the change |

Errors: 400 (unknown mode, unknown field, empty body, a reason over 500 characters), 401 (no credential), 403 (not a member for GET; a member, an outsider or an agent for PATCH), 404 (unknown organization). The routes are mounted by the identity extension (the owner concept lives there), so a server without it has no settings API; the transports still read the table, so `ore serve` without identity can still be switched through the ledger.

### CLI

```bash
ore org settings acme                                              # GET
ore org git-transport acme read-only --reason "pushes go through ore save"
ore org git-transport acme off --reason "last day of git was Friday"
ore org git-transport acme on
ore events --type io.ore.organization.settings.changed
```

Checked by hand against a real `ore serve` (output in the PR). The subcommands are thin wrappers over the two routes, like the rest of `ore org`.

## Tests

- `internal/ledger/orgsettings_test.go`: defaults without a row; off with a reason writes the row and one event whose envelope has the org, the subject and the actor and whose data has `changes.git_transport {on → off}` and the reason; the same input again writes and emits nothing; a new mode clears the old reason; the reason alone changes with a `changes` map that has only the reason; back on; another organization is untouched; `sometimes`, `""`, `OFF` and a 501-character reason are refused with `ErrInvalidSetting` before anything is written; an empty input is a no-op.
- `internal/githttp/githttp_test.go` `TestGitTransportSwitchRefusesWithTheOreClientMessage`: a push works on `on`; in `read-only` clone and fetch work and a push is refused with the push message; in `off` clone (default protocol, `protocol.version=2`, `protocol.version=0`), `ls-remote`, fetch and push are all refused with `fatal: remote error: <message> (<reason>)`; refusals record no `ref.updated`; the raw advertisement is a 200 with the `ERR` packet and the mode header, the RPC endpoint a 403 with the text, the anonymous probe a 401 without the header; back on, the push goes through and is recorded. `TestTransportRefusalMessages` pins the three texts.
- `internal/identity/transport_test.go` `TestE2E_GitTransportSwitch` (the identity harness: a real `web.Server` with smart HTTP and the SSH server, real `git` and `ssh`): pushes over HTTP and SSH before the switch; a member reads the defaults, an outsider gets 403, an unknown organization 404, no credential 401; a member, an outsider, a bad mode, an unknown field, an empty body and an unknown organization cannot flip it (403/400/404, nothing applied); the administrator switches it off with a reason, the event is on the ledger with the actor and `on → off`, members see the state and the reason; HTTP push, fetch and clone are refused with the message (the administrator's own token too), SSH push and clone are refused with `ore: <message>`, no ref update is recorded; `workspace.Open` (`ore open`) materializes the tree and the commits endpoint moves a ref with the transport off; `read-only` clears the old reason, fetch and SSH clone work, HTTP and SSH pushes are refused with the push message; alice made an owner of the organization switches it back on (not an administrator), both pushes go through and are recorded, three settings events in all; an agent of the organization reads the setting and cannot change it.

`CGO_ENABLED=0 go vet ./... && go test ./...` and `scripts/e2e.sh` pass; the three packages also pass on Postgres (`scripts/test-postgres.sh ./internal/ledger/ ./internal/githttp/ ./internal/identity/...`).

## Edge cases and limits

- **The administrator is refused too.** The switch is about the transport, not the principal: an organization that turned git off has no git, for anyone. An administrator who needs a one-off `git clone` sets `read-only` (or `on`) and back; both changes are on the ledger.
- **Agents.** An agent token of the organization can read the setting (it is a member) and never change it (owners are users). Agent workspaces are clones of the bare repository on the server's disk, so an agent run is unaffected by the mode.
- **Fetches in flight.** The mode is read per request. A `git push` that already passed the advertisement when the mode changed finishes (receive-pack is running); the next request is refused. A clone whose advertisement was refused never starts.
- **`read-only` and the advertisement.** `git push` asks for `info/refs?service=git-receive-pack` first, so the refusal reaches the user before any object is sent; `git fetch` asks for `git-upload-pack` and passes.
- **Only git is refused.** The repository pages, the archive endpoint, `GET .../refs`, `.../log`, `.../file`, the diff, the commits endpoint and the change endpoints answer as before, for the same users. `ore open` of a repository whose organization is off takes the same path as before.
- **The web app and the server-rendered repository page follow the mode**: see `ui-clone-hints.md`.
- **A server without the identity extension** has no settings routes (the owner check is identity's). The transports still read `org_settings`, so the switch works wherever the row is set; in practice `ore serve` always applies identity.
- **The single admin token** (no identity) authenticates every git request as `human/admin`, and the switch refuses it like any other credential.
- **Concurrent changes.** SQLite serializes writers; on Postgres two first-time changes to the same organization can race on the primary key and one of them fails with a unique violation (reported as an error to that caller; the other change holds). A retry sees the row and succeeds.
- **No per-repository override.** The mode is per organization, as ADR 0016 §3 says; a repository that must stay on git lives in an organization that stays on `on` (or `read-only`).
- **Mirrors you push to Ore.** A one-way mirroring job that pushes into Ore over git is exactly what `read-only` catches; move it to `ore commit`, the API, or the GitHub adapter's pull-based mirror before `off`.

## Not done

- A site-wide default for new organizations (every organization starts `on`); an `ore serve` flag to disable the bridge for the whole instance.
- `ore open`, `ore status` reporting the mode; `ore doctor` listing organizations still on git.
