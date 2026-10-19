# Web app: Ore's primary user interface

Branch `slice/webapp` (worktree branch `rohan/slice-webapp-223932`). Registers as `webapp`. No migration. Owns `ui/` and `internal/webapp/`; one blank-import line in `cmd/ore/main.go`, as every other slice.

A GitHub-class single-page application served by the binary under `/app/`: a design system, the information architecture developers know (top bar with an org/repo switcher, global search and a ⌘K palette; repository tabs Code, Issues, Changes, Pipelines, Deployments, Agents, Ledger, Settings), and pages for everything the API exposes, live over server-sent events and honest about what agents did.

## What landed

### `internal/webapp` (Go)

| File | What |
|---|---|
| `webapp.go` | `//go:embed all:dist`; serves the shell for client routes, hashed assets with a one-year cache, `404` for missing assets (never HTML as a script); on a checkout without Node (`dist/.gitkeep` only) `/app/` is a 503 page that says how to build the UI. |
| `register.go` | `registry.Register("webapp")`, `s.Services["webapp"] = *Info{Built, Prefix}`; routes below. |
| `session.go` | `GET/POST/DELETE /api/v0/webapp/session`: sign in by posting a token (validated through the server's own authenticator, so the identity extension's users and agents work), stored in the same `ore_token` HttpOnly cookie the server-rendered pages use; sign out clears it. Tokens never appear in URLs. |
| `stream.go` | `GET /api/v0/webapp/events/stream`: the ledger as SSE, `id` = sequence, `Last-Event-ID`/`?after=` resume without gaps or duplicates, filters `type` (exact or prefix), `subject`, `subject_prefix`, heartbeats; `GET /api/v0/webapp/runs/{id}/log/stream` and `.../agent/{id}/log/stream`: log tails as `lines` batches (id = line count, resumable), `status` on change, `done` when the run finished and the file drained. |
| `extras.go` | `features` (which extensions run, base URL, SSH address, level names), `commits/{oid}` (metadata + patch + stat, root and merge commits handled), `blob` (size, lines, binary detection, truncation at 2 MB, last commit touching the path), `compare` (merge base, commits, diff), `search` (repositories, issues, changes by title or `#n`). Revision strings are validated so nothing reaches git as an option. |
| `webapp_test.go` | httptest end to end with the same slices `ore serve` loads: the shell or the build page, SPA fallback, missing asset 404, `/app` → `/app/`, host-qualified 4-segment routes, `/` → `/app/` when built; the cookie flow (bad token 401, empty 400, cookie authenticates the REST API, sign-out expires it); features; the event stream is live and resumes after `Last-Event-ID` with no duplicate; the log stream batches, reports status, finishes, resumes after N lines, 404 on an unknown run; commit/blob/compare/search including traversal and option-injection refusals. |

**Routing without touching `internal/web`.** Go's mux refuses patterns that overlap without one being more specific, and `web.go` owns host-less wildcards such as `GET /{org}/{name}` and `GET /{org}/{name}/changes/{n}`. A host-less `GET /app/{path...}` conflicts with them. So the app registers host-less patterns for every path shape that cannot collide (`/app/{$}`, `/app/{a}`, `/app/{a}/{b}`, `/app/{a}/{b}/{c}/{rest...}`) and, for the remaining four-segment paths (`/app/org/repo/issues`), host-qualified catch-alls for the hosts the server is reached as: the base-URL host, `localhost`, `127.0.0.1`, `::1` and `ORE_WEBAPP_HOSTS` (comma-separated, for proxies that rewrite `Host`). A pattern with a host never conflicts and always wins, which is also how `/` redirects to `/app/` on those hosts once the app is built, without editing the index handler. A one-line hook in `web.go` (`Server.Index http.Handler`, or a middleware list) would make both universal; it belongs to the UI v1 slice and is noted as a follow-up.

### `ui/` (TypeScript)

Vite 7, React 19, TypeScript strict, TanStack Router (code-based routes, `basepath: /app`) and Query, `@tanstack/react-virtual`, CodeMirror 6 (`@codemirror/merge`, `@codemirror/language-data` for lazy grammars, `@lezer/highlight`), `marked` + `DOMPurify`, `lucide-react`. No CSS framework: one token sheet and plain CSS. The shell (what `index.html` loads up front) is **about 150 KB gzipped**; grammars and routes load on demand; `npm run size` enforces the 350 KB budget.

| Area | Files | Summary |
|---|---|---|
| Design system | `src/design/` + `README.md` | tokens (color, type scale, spacing, radius, elevation, motion), light and dark from one set, `prefers-color-scheme` plus an explicit choice applied before paint; Button, Input/Textarea/Select/Field/Checkbox, Kbd, Pill/StatusPill, Avatar/ActorChip (humans, agents and bots look different), Card/Stat, Tabs, Table (sticky header), Menu, Dialog (native `<dialog>`), Toasts, EmptyState (teaches: explanation plus a copyable command), Skeleton, CommandPalette, Callout, Segmented; a living gallery at `/app/design`. |
| Shell | `src/shell/` | top bar (brand, org/repo switcher, search, live-ledger indicator, theme, account), ⌘K palette with navigation, repositories, preferences and server-side search; shortcuts (`/`, `?`, `g h/c/i/r/p/d/a/l/s`, `j/k/↵`, `c`, `t`, `n/p`, `f`, `e`); repository layout with tabs and counts, clone dialog (HTTPS, SSH when enabled, and the no-clone API commit); sign-in; friendly 404; route error boundary; `QueryState` (loading → 404 → 5xx with retry → content). |
| Libraries | `src/lib/` | API client (same-origin, cookie, typed `ApiError`, 401 → sign-in with return, 5xx → toast with retry); typed query hooks for every route of every slice with `optional()` for absent sibling APIs; `connectSSE` over `fetch` (status codes, `Last-Event-ID`, exponential backoff with jitter, duplicate suppression, clean end); `useLedgerLive` (invalidates the query families an event touches, polling fallback when the stream is unavailable); Markdown rendering with a strict allow-list; keyboard chords; formatting; theme. |
| Diff viewer | `src/diff/` | unified-diff parser (added, deleted, renamed, copied, binary, mode, quoted paths, no-newline), windowing model with estimated row heights, word-level diff for paired lines, lezer highlighting per file side, virtualized rows (window or box), split and unified, files over 400 lines collapsed by default, file tree navigation, `n`/`p`, inline threads by anchor, gutter click to comment, provenance badges on every hunk of an agent-authored change, per-file "Compare full files" opening a `@codemirror/merge` `MergeView` (side by side) or `unifiedMergeView` on the full blobs, `performance.mark`s for the budget test. |
| Home | `pages/Home.tsx` | repositories with level badges and activity counts, "what agents did today" (runs, landed changes, deployments, the last agent events), recent activity live from the ledger, the first-repository empty state with the exact commands. |
| Code | `pages/code/` | tree pane (lazy expansion, `t` to find a file with fuzzy matching), breadcrumbs, branch/tag switcher (also the GitHub mirror namespace), last commit per directory, README rendering, file view with syntax highlighting, line anchors (`#L10`, shift-click ranges), wrap, raw, Markdown rendered/source toggle, images, binary notice, truncation notice; **Edit / New file** through the API (server-side commit to any branch, no clone); commit history grouped by day; commit page with the patch; `/compare/base...head` with "Open change". |
| Issues | `pages/issues/` | list with state filter, text filter, `j/k/↵/c`, detail with sanitized Markdown, gateway comments with their lane, "Run agent on this issue" with the autonomy level shown and explained before starting, related agent runs. |
| Changes | `pages/changes/` | list with state filter and provenance badges; detail with Conversation (description, lanes human/agent/bot collapsible, comments with resolve and jump-to-line, structured reviews with findings by severity and **one-click Apply fix** that commits the suggestion to the source branch as a new patch set), Files (patch-set selector, interdiff between any two patch sets with the rebased/identical notes, the diff viewer, inline comments), Checks (runs on this change, the approval policy evaluated); sidebar with required checks and patch sets; **Land** with a strategy chooser (queue, land when green, optimistic) explained in plain language, policy state and the landing error on 409; Review dialog (verdict + findings editor); stacks page (open, land bottom-up, abort state). |
| Pipelines | `pages/pipelines/` | runs list (tasks, failures, cache hits, duration), run detail with the plan as a DAG layered by `needs`, task panel (content hash explained, cache, exit code, steps table, step summary), divergences from GitHub Actions explained per code, artifacts with downloads, the live log viewer (SSE, search-as-you-type with match navigation, jump to failure, follow, raw; virtualized for 50,000 lines). |
| Deployments | `pages/deployments/` | environment cards with policy (explicit records, or v0 implicit ones marked as such), current deployment with URL, deploy/rollback/stop, approvals queue with approve/reject, history timeline, DORA panel, new environment dialog; deployment detail with the policy decision (every rule pass/fail), health, recovery time, TTL with extend, approvals timeline. |
| Agents | `pages/agents/` | runs timeline with the loop visualized live (signal → plan → change → verify → land → deploy → observe, done/current/failed/awaiting human), prompt box to start a run at the repository's level with the explanation, run detail with what it touched (issue, branch, change, verification run, deployment) and the streaming log; fleet section (roles, budgets, pause/resume, evidence and recommended level) when `agent.fleet` is present, a hint otherwise. |
| Ledger | `pages/ledger/` | global and per-repository event explorer: type and subject filters, replay cursor (`after` sequence), live tail with pause, text filter over loaded events, raw CloudEvents JSON per event, every event described in a sentence and linked to its page. |
| Settings | `pages/settings/` | repository: autonomy level with each level's meaning and a warning at L4+, review policy, environments, secrets by name, GitHub link/sync/unlink, webhooks (hint until the slice lands), MCP instructions; site: profile, tokens (secret shown once), SSH keys, users and organizations, agent principals (creates the agent and its first token), MCP and CLI instructions. |

## Design system decisions

Written up in `ui/src/design/README.md`. In short: a forge aesthetic (warm paper and graphite, one copper accent) at GitHub density; one token set with light and dark values; system font stacks (no download, no layout shift); AA contrast for text; a visible two-tone focus ring on every control; three motion durations that collapse under reduced motion; agents get their own hue so provenance is one glance away everywhere (actor chips, lanes, hunk badges, level badges). Lists that the user navigates are rows, not tables; empty states teach with the exact command; relative times carry the absolute time.

## Tests

**Vitest (70 tests, `npm test`).** Diff parser (every file kind, line numbers, no-newline, renames, binary, quoted paths, a 20,000-line synthetic diff parsed under 200 ms); windowing (collapse threshold, split pairing, thread insertion, visible range, span merging; the 20,000-line model built under 200 ms; only a window of rows in the DOM; binary, rename and a very long name render); word diff; command palette (ranking, keyboard model, filter + Enter runs, remote merge without duplicates, Escape); Markdown sanitization (fourteen XSS payloads rendered inert; GFM kept; external links hardened; unsafe image sources dropped); SSE client (framing across chunks, backoff schedule, 404 → unavailable without retry, 401 → unauthorized, drop → reconnect with `Last-Event-ID` and no duplicate, backoff growth and stop after close, clean end); live-ledger invalidation map; formatting; log helpers on 50,000 lines; DAG layering with cycles; file tree and fuzzy finder; primitives.

**Playwright (`npm run e2e`, `ORE_UI_E2E=1 scripts/e2e.sh`)** against a real `ore serve --executor host` with the scripted demo agent, seeded through the API, signed in once:

- `auth.spec.ts`: unauthenticated deep link → sign-in → back to the link; a bad token is refused; `/login?token=` works and strips the token; sign-out.
- `flow.spec.ts`: home; browse code (README sanitized, syntax highlighting, line anchors, `t` finder, history, commit diff); edit a file through the API; issues with `j/k/↵`, the run-agent dialog explaining L3, the loop completing live, the change landing (L3), the ledger tab updating without a reload, the issue closed by the loop; review a change (inline comment, request-changes review with a finding, Apply fix creates patch set 2, Land with the queue strategy merges); a run detail with a 50,000-line log (jump to failure, search); deployments (cards, deploy request, DORA); settings (level with explanation, tokens shown once, MCP instructions).
- `keyboard.spec.ts`: ⌘K palette, `g` chords for every tab, `?` help, Tab order with a visible focus ring, `/` and `c`.
- `edge.spec.ts`: friendly 404 for repository, change, issue and file; API 5xx → toast with Retry and no blank screen; stream disconnect → reconnect without duplicate events; a Markdown comment with script tags inert; a renamed file in a diff and a very long file name; a change with zero patch sets; fleet/learn/webhooks absent → hints, no console errors; 375 px width without horizontal overflow.
- `perf.spec.ts`: the seeded 20,050-line change (50 files × 401 lines): `performance.measure('ore:diff')` under 200 ms after data arrives (measured 13 ms in headless Chromium on this machine), collapsed large files load on demand, fewer than 150 rows in the DOM, cumulative layout shift under 0.1 on the main pages.
- `a11y.spec.ts`: every control named, every input labelled, images with alt, body and muted text contrast ≥ 4.5:1, in light and dark.
- `visual.spec.ts`: screenshots of sixteen pages in light and dark plus two mobile shots, written to `ui/e2e/screenshots/`.

## Screenshots, described

Stored under `ui/e2e/screenshots/` as `<page>-<light|dark>.png`:

- **home**: a serif-free, warm page: "Repositories" with a copper "New repository" button; a card listing `acme/app` with its L3 "Land" badge and counts; "What agents did today" with three stat tiles and the last agent events on a timeline; "Recent activity" on the right with icons per event kind, actors as chips, agent actors in violet.
- **code**: the file tree on the left with "Find file", the last commit bar, directories first, README rendered below with the script tag gone and the checkboxes disabled.
- **file**: `main.go` with line numbers, Go keywords in magenta, strings teal, the line-anchor highlight, the wrap/copy/raw/edit toolbar.
- **issues / issue**: the state filter group with counts, rows with open/closed glyphs; the issue page with the sanitized body, the "Run agent on this issue" primary action and the "Autonomy" card explaining L3.
- **changes / change-conversation / change-files**: provenance pills (violet "agent-authored · demo-script" on the loop's change, grey "human-authored" on the seeded one); the conversation with "HUMAN LANE · 2" and the bot lane carrying a request-changes review with a medium finding and its Apply fix button; the files tab with the patch-set selector, the split diff with green/red gutters, word-level highlights, the renamed file with its arrow and the "Compare full files" icon.
- **pipelines / run**: status pills, task and cache counts; the run page with the DAG card, the task panel and the log viewer with search, "Jump to failure" and the streaming pill.
- **deployments**: production and preview cards with their policy lists, the DORA tiles, the history timeline.
- **agents / agent-run**: the loop pills (green done, copper current), the Fleet hint card; the run page with the seven steps done, the "What it touched" links to the issue, change, verification run and deployment, and the agent log.
- **ledger**: the filter bar with the replay cursor, rows with sequence numbers and expanders for raw JSON, fresh rows flashing copper.
- **settings**: the vertical section tabs, the repository card and the six level options with their explanations, L3 marked current.
- **design**: the gallery of tokens, type scale, buttons, inputs, pills, actors, the loop, stat tiles, table, callouts, empty state, dialog and toasts.
- **mobile-issues / mobile-change**: at 375 px the top bar collapses to icons, tabs scroll horizontally, the diff switches to unified by default and nothing overflows.

Dark variants use graphite surfaces with the same copper and violet; contrast checks pass in both.

## Edge cases (each a named test)

| Case | Where |
|---|---|
| Unauthenticated deep link redirects to login then returns | `auth.spec.ts` |
| Repository, change, issue or file that does not exist | `edge.spec.ts` friendly 404; `webapp_test.go` 404s for blob, commit, compare |
| API 5xx: toast and retry, no blank screen | `edge.spec.ts` |
| SSE disconnect and reconnect without duplicate events | `sse.test.ts` (drop → `Last-Event-ID`, replayed id suppressed), `edge.spec.ts` (aborted first connection), `webapp_test.go` (resume after id) |
| Markdown comment with script tags | `markdown.test.ts` (14 payloads), `edge.spec.ts`, `flow.spec.ts` (README) |
| Diff with binary and renamed files | `parse.test.ts`, `window.test.tsx`, `edge.spec.ts` |
| Very long file name | `window.test.tsx`, `edge.spec.ts` (tree, breadcrumb) |
| A log of 50,000 lines | `pipelines.test.ts` (search and failure scan), `flow.spec.ts` (streamed, searched, jumped) |
| A change with zero patch sets | `edge.spec.ts` |
| Fleet or learn API absent | `edge.spec.ts` (hints, no errors); every optional hook treats 404 as absent |
| 20,000-line change interactive under 200 ms | `window.test.tsx` (model under 200 ms in jsdom), `perf.spec.ts` (13 ms in Chromium) |
| Works at 375 px | `edge.spec.ts`, mobile screenshots |
| Missing asset is a 404, never the shell; unbuilt app explains itself | `webapp_test.go` |
| Option injection and path traversal in revision and path parameters | `webapp_test.go` |

## Deferred

- **Blame** (named in the brief as later): no blame endpoint exists yet.
- **`/` redirect and the app catch-all for arbitrary hosts** need a hook in `internal/web` (owned by the UI v1 slice); until then they cover the base-URL host, loopback names and `ORE_WEBAPP_HOSTS`.
- **The sibling SSE endpoints** (`/api/v0/events/stream`, `.../runs/{id}/log/stream` from the UI v1 slice) were not merged while this was built; the app uses its own `/api/v0/webapp/...` streams and can switch to the shared ones with a one-line change in `src/lib/live.ts` and `LogViewer.tsx`.
- **Fleet, learn and webhooks** pages are built against the documented intent (`/api/v0/fleet/`, `/api/v0/learn/lessons`, `/api/v0/webhooks`) and hide themselves with a hint until those routes exist; their exact JSON will need a pass when the slices land.
- **Binary files in diffs** show a notice; image previews side by side are not built. The commits API cannot post binary content, so binary diffs are covered by unit tests, not the live suite.
- **Expand context** between hunks needs the blobs; the per-file CodeMirror compare covers "show me the whole file" instead.
- **Inline comments on the old side**: the review API has no side field; comments anchor to new-side lines. (The API's `anchor` carries the patch set and lines; the path stays on the comment itself, which is what the viewer keys threads by.)
- **Renaming a repository, changing the default branch or deploy port**: not in the v0 API.
- **Issue comments from humans**: the comments route for issues is read-only outside the gateway; the UI shows them and points at the API/MCP.
- **Offline/PWA**, localization and the Loro CRDT for shared descriptions (docs/03 §2.1) are not started.
