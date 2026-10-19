# Web UI v1: pages for everything wave 1 added, live logs over SSE, ledger filters

Branch `slice/ui-v1` (worktree branch `rohan/slice-identity-ssh-f6e9a7`, wave 2). Part of `internal/web`; no migration, no registration. Owns `internal/web/templates.go`, the new `internal/web/ui_*.go` files and additive edits to `internal/web/web.go`.

## What landed

**Dependency-free.** Go templates, one inline stylesheet, one inline script (vanilla JavaScript), no fonts, no npm, nothing fetched from anywhere. Dark and light themes follow `prefers-color-scheme`, with a toggle in the header remembered in `localStorage`. Phone width works (fluid layout, horizontally scrollable tables and tabs, 2-column cards). Every form has labelled inputs, every page a skip link and `aria-current` on the active tab; status pills are text, not colour alone.

**Auth UX** (`ui_core.go`). `/login` is a form that takes a token (the admin token or an identity token); the v0 `/login?token=` flow still works. The header shows who you are (`kind/id`) and a **Log out** button. Every page needs a signed-in actor: unauthenticated requests are redirected to `/login?next=<page>` (303), never answered with raw text. The token lives in the `ore_token` HttpOnly cookie, which the identity extension's hook resolves like any other credential, so identity tokens work for the UI, the API and git alike. **CSRF**: a double-submit token (`ore_csrf` cookie plus a hidden `csrf` field on every form); a missing or stale token is a 403 error page and nothing is written.

**Live updates** (`ui_stream.go`), server-sent events:

| Route | What it streams |
|---|---|
| `GET /api/v0/events/stream?after=&type=&subject=&subject_prefix=&actor_kind=&org=` | the ledger, polled every 500 ms (`Server.StreamInterval`), one unnamed event per ledger event (`id:` the sequence number, `data:` the CloudEvent JSON); without `after` it tails from the head; `Last-Event-ID` resumes a reconnect; `: ping` every 15 s |
| `GET /api/v0/repos/{org}/{name}/runs/{id}/log/stream` | the pipeline run's log file as it grows: `data:` per line, `id:` the byte offset after it (so `Last-Event-ID` / `?offset=` resume), `event: status` on status changes, `event: done` once the run is finished and the file stopped growing |
| `GET /api/v0/repos/{org}/{name}/agent/runs/{id}/log/stream` | the same for agent runs |

A log that does not exist yet makes the stream wait (heartbeats) and deliver when it appears. Lines longer than 4 KiB are cut with a `… [line truncated: N more bytes]` marker, on pages and streams alike (UTF-8 safe); a page shows at most the first 2 MiB of a log and the stream continues from there. Pages subscribe with `EventSource` (which reconnects by itself and sends `Last-Event-ID`): the index, the repository overview and the ledger page prepend new events to their tables and show a live indicator; run and agent-run pages append log lines, update the status pill and stop on `done`.

**Pages** (`ui_routes.go`, `ui_repo.go`, `ui_ops.go`, `ui_identity.go`, `ui_ledger.go`; templates in `templates.go` and `ui_tmpl_*.go`), all reachable from the repository tabs:

| Page | Shows | Forms |
|---|---|---|
| `/` | repositories, recent events (live) | |
| `/{org}/{name}` | autonomy level, counts, review policy, environments, DORA (30 d), **agent fleet budgets and evidence when `s.Services["agent.fleet"]` implements `web.FleetSummary`** (hidden otherwise), recent runs, agent runs, repository events (live) | set level; run agent; run pipeline; deploy; roll back |
| `/{org}/{name}/issues`, `/issues/{n}` | paginated list with state filter; the issue with its comments (the gateway's `comments` table), GitHub link when mirrored, agent runs on it | file issue; comment; close; run agent on the issue |
| `/{org}/{name}/changes`, `/changes/{n}` | paginated list with state filter; the change: provenance, approval status, **patch-set selector** (`?ps=N`), **interdiff between any two patch sets** (`?from=&to=`, rebase-aware, identical/rebased/note), server-rendered diff per file with files over 400 lines collapsed behind "expand", **comment lanes** (human, agent, bot) as collapsible sections with anchors, outdated and resolved markers, **reviews with structured findings** (severity, path:range, confidence), landings, stacks containing the change, verification runs | comment (path, lines, patch set, lane); review (approve / request changes / comment, findings JSON); **land with a strategy** (queue, land-when-green, optimistic; default from the policy); run pipeline on head; close; resolve/reopen a comment |
| `/{org}/{name}/stacks`, `/stacks/{id}` | stacks with their ordered changes and states; the stack view with the failed change marked | open stack (branches bottom first); land stack |
| `/{org}/{name}/runs`, `/runs/{id}` | paginated runs; the run: **compiled plan** (tasks, needs, images, caches, divergences, refusal), tasks (status, exit, duration, cache hit, content hash, reasons, divergences, annotations), **artifact downloads**, live log with **search-as-you-type** | run pipeline |
| `/{org}/{name}/agent`, `/agent/{id}` | paginated agent runs; the run with its issue, change, pipeline run, deployment, error, live log | run agent |
| `/{org}/{name}/deployments`, `/deployments/{id}` | paginated deployments with an environment filter; the deployment: rollout **policy decision** (every rule, pass/fail, detail), health probe, failure, recovery time, approvals, **preview TTL** | **approve / reject** (approvers only); extend TTL |
| `/{org}/{name}/environments` | environments with policy summary, current deployment, deployments awaiting approval | create (kind, target incl. ssh-docker, approvals, approvers, allowed refs, wait, health, auto-rollback, agent deploys, TTL); request deployment; roll back; stop; delete; approve/reject |
| `/{org}/{name}/dora` | the four keys for 7, 30 or 90 days | |
| `/{org}/{name}/github` | link status, last sync, next sync, webhook URL and secret, GitHub events | link; sync now; unlink |
| `/{org}/{name}/gateway` | gateway sessions (writes, refusals, state), MCP connection instructions, session events | open session (token shown once); close |
| `/identity`, `/identity/tokens`, `/identity/keys`, `/identity/agents`, `/identity/users` | who you are, memberships; tokens (prefix, scopes, expiry, last use); SSH keys and the host key fingerprint; agent principals per org; users (administrators) | create token (**secret shown once**, on the response page only); revoke; add/remove key; create agent; create user |
| `/events` | the ledger newest first with **filters by type (exact or prefix), subject prefix, actor kind and org**, cursor paging (older/newer/newest), a **replay cursor** linking `GET /api/v0/events?after=N`, and a live tail toggle | |

Every list is paginated (`?page=&per=`, 50 by default; the ledger pages by sequence cursors).

**How writes happen.** Forms post to `/ui/...` handlers that call the Go services where `web` already imports them (ledger, review, delivery) and otherwise the slices' own API routes **in-process** (`Server.api`: identity, GitHub, gateway, pipelines artifacts and plans), forwarding the browser's credential; the slices' validation, policy and audit apply unchanged, so the UI, the CLI and the agents remain indistinguishable on the ledger except for their actor. Issue comments from humans are written into the gateway's `comments` table with the same `io.ore.comment.created` event the gateway emits. Features whose extension is not attached render a short note instead of a broken page.

**web.go** gained three additive things: `Server.StreamInterval`, an unexported `root` handler used for in-process dispatch, and the UI is mounted from `mountUI`/`mountStreams` (the v0 UI block moved out of `web.go` into the new files; the API routes are untouched).

## Performance and accessibility

- Diffs are rendered on the server into one HTML fragment (`ui_diff.go`): a file list, per-file sections, files over 400 lines collapsed with "expand", binary files noted and not dumped. `TestUIDiffBudget20000Lines` renders 20,000 changed lines through the renderer and through the whole change page (real git, real SQLite) and fails above 200 ms; on this machine the page renders in well under the budget (the log line of the test prints the measured time).
- Keyboard: skip link, visible focus ring, labelled inputs (`<label for>`, `aria-label` where a label would be noise), buttons that are buttons, `<details>` for collapsibles, `aria-live` on the log and flashes, `aria-current` on tabs and selected patch sets.
- Themes: CSS custom properties, `prefers-color-scheme` plus `data-theme` override; `color-scheme` meta so form controls follow.
- Phone width: no fixed widths, tables scroll horizontally, tabs scroll, cards reflow, 16 px gutters.
- No external fonts or scripts (asserted by `TestUIEveryPageRenders`).

## Tests (`internal/web/ui_test.go`, a server built like `ore serve` with every wave-1 extension applied, host executor, `none` deployer)

- `TestUIEveryPageRenders`: a repository populated through the API (commits, a change with a comment and a review with findings, an issue, a pipeline run, an implicit preview deployment, a staging environment with a parked deployment, a user with a token and an SSH key, a gateway session); every page above answers 200 with its expected content and the layout, and loads no external asset.
- `TestUIEventStreamDeliversAndClosesCleanly`: a new issue arrives on the stream, a type filter keeps other events out, cancelling the request returns the handler, `Last-Event-ID` resumes after a sequence number.
- `TestUIDiffBudget20000Lines`: the budget above.
- `TestUIFormsWriteThroughTheServices`: level, issue comment (escaped, one `io.ore.comment.created` by the human actor), change comment and review, a bad findings body becomes a flash, approval supersedes a request-changes, landing with `queue` merges and a second landing is a flash, approving a parked deployment, creating an environment and deploying to it, extending a preview TTL, identity token shown once and usable on the API, key add and bad-key flash, agent and user creation, gateway session opened from the form (token shown once) and closed, pipeline and agent runs from forms, a stack opened and landed.
- `TestUILoginLogoutAndIdentityTokens`: bad token 401, identity token signs in as `human/alice` with her own identity page and no administrator rights, open redirects refused, logout, the v0 query-string login.
- `TestUIPaginationAndFilters`: 61 issues over two pages, state filter, page clamp; ledger cursor paging and type/subject/actor/org filters.

Edge cases, each a named test:

| Case | Test | Result |
|---|---|---|
| Missing repository, issue, change, run, agent run, deployment, stack; a run reached through another repository; a form on a missing repository | `TestUIMissingRepositoryIs404Page` | a 404 page with the layout, a flash for forms, no panic |
| Change with zero patch sets (a row inserted without the review store) | `TestUIChangeWithZeroPatchSets` | renders; the read backfills patch set 1 (review v1 semantics) or says none is recorded; unknown commits give a diff error, not a crash; bogus `?ps=&from=&to=` are harmless |
| Log that does not exist yet | `TestUILogStreamWaitsForMissingFileThenDelivers` | the stream waits, delivers complete lines when the file appears, holds a partial line until its newline, ends with `done` when the run finishes; 404 for a missing run |
| Very long single log line (100 KiB) | `TestUIVeryLongLogLineIsTruncated` | cut at 4 KiB with a marker on the page and on the stream, UTF-8 safe |
| Comment body with HTML (issue and change comments, review findings) | `TestUIFormsWriteThroughTheServices`, `TestUIEveryPageRenders` | escaped everywhere |
| Diff with binary files | `TestUIDiffWithBinaryFiles` | "binary" pill and a note, text files still rendered; through the page with a NUL-bearing file committed on a branch |
| Unauthenticated access to every page | `TestUIUnauthenticatedRedirectsToLogin` | 303 to `/login?next=`; API streams answer 401 |
| Stale, missing or malformed CSRF token (forms and logout) | `TestUIStaleCSRFIsRefused` | 403 "security token" page; nothing written; the right token works |

The gate `scripts/e2e.sh` passes (gofmt, vet, every package, build, the Docker demo).

## Deferred

- A CodeMirror editor and collaborative editing of change descriptions (docs/03 §2.1); the diff is read-only and rendered server-side.
- Line-anchored commenting from the diff (clicking a line): comments take a path and line range in the form today.
- Windowed rendering: files over 400 lines are collapsed, not virtualised; a single 20,000-line file renders in one `<pre>` behind "expand".
- The ledger live tail post-filters subject prefixes and actor kinds on the server per poll; a busy org with a narrow filter does more work than an index would.
- 403 pages for policy refusals over the API hook (the hook can only say 401); UI forms calling the services directly do show the policy reason.
- Fleet budgets and evidence render whatever the fleet slice returns from `FleetSummary.RepoSummary` as key/value pairs; a dedicated layout waits for that slice.
- Learning pages (the learn slice) and webhook delivery dashboards (the webhooks slice) are not here; both slices mount under their own prefixes and the ledger page already shows their events.
