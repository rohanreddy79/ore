# Ore web app

The primary human interface to Ore: a Vite + React 19 + TypeScript single-page application served by the `ore` binary under `/app/`. It talks to the same REST API the CLI and the agents use, so everything it shows is a view over the ledger.

## Dev loop

```bash
# 1. a server to talk to (any data dir; the host executor needs no Docker)
CGO_ENABLED=0 go build -o bin/ore ./cmd/ore
./bin/ore serve --executor host          # prints the admin token

# 2. the app with hot reload, proxying /api, /login and /healthz to the server
cd ui && npm install && npm run dev      # http://localhost:5173/app/
```

`npm run dev` proxies to `http://localhost:7777` by default; set `ORE_URL` to point elsewhere. Sign in with the token the server printed: the app stores it in the same `ore_token` cookie the server-rendered pages use, through `POST /api/v0/webapp/session`, so the proxy carries it on every request.

## Build and embed

```bash
cd ui && npm run build                  # typecheck + Vite build into ../internal/webapp/dist
cd .. && CGO_ENABLED=0 go build -o bin/ore ./cmd/ore
```

`internal/webapp` embeds `dist/` (`//go:embed all:dist`). A checkout without Node still builds: `dist/.gitkeep` is committed, the rest is ignored, and the binary serves a "build the UI" page under `/app/` until the app is built. `scripts/e2e.sh` builds the UI whenever `npm` is on the path and fails if the build fails.

## Tests

```bash
npm test                 # Vitest: diff parsing and windowing, the palette, Markdown sanitization, the SSE client, log helpers
npm run e2e              # Playwright against a real `ore serve` (builds bin/ore if missing); needs `npx playwright install chromium` once
npm run size             # gzipped sizes and the 350 KB shell budget
```

The Playwright suite starts `ore serve --executor host` with the scripted demo agent, seeds a repository through the API, signs in once and then exercises the main flows, keyboard navigation, edge cases, the 20,000-line render budget, accessibility checks and screenshots (written to `e2e/screenshots/`). `ORE_UI_E2E=1 scripts/e2e.sh` includes it in the gate.

## Layout

| Path | What |
|---|---|
| `src/design/` | tokens, base and component CSS, the component library, `README.md` with the design decisions |
| `src/lib/` | API client, typed query hooks, SSE client, live-ledger hook, Markdown, keyboard, formatting, theme |
| `src/shell/` | app shell (top bar, palette, theme, account), repository layout and tabs, sign-in, 404, shared widgets |
| `src/diff/` | unified-diff parser, windowing model, word diff, lezer highlighting, the virtualized diff viewer and the CodeMirror merge view |
| `src/pages/` | one folder per tab: code, issues, changes, pipelines, deployments, agents, ledger, settings |
| `e2e/` | Playwright suite, global setup (server + seed + sign-in), screenshots |

URLs mirror GitHub's: `/app/{org}/{repo}` (code), `/tree/{ref}/{path}`, `/blob/{ref}/{path}`, `/commits/{ref}`, `/commit/{sha}`, `/compare/{base}...{head}`, `/issues/{n}`, `/changes/{n}?tab=files&ps=2&from=1`, `/pipelines/{run}`, `/deployments/{id}`, `/agents/{run}`, `/ledger?type=io.ore.change.&after=120`, `/settings/{section}`.

## Server endpoints the app adds (`internal/webapp`)

| Endpoint | Purpose |
|---|---|
| `GET /app/`, `GET /app/*` | the shell with client-side routing; hashed assets cached for a year |
| `GET/POST/DELETE /api/v0/webapp/session` | who am I; sign in with a token (sets the cookie); sign out |
| `GET /api/v0/webapp/features` | which extensions run, base URL, SSH address, level names |
| `GET /api/v0/webapp/events/stream` | the ledger as server-sent events (`id` = sequence, `Last-Event-ID` resumes) with `type`, `subject`, `subject_prefix` filters |
| `GET /api/v0/webapp/runs/{id}/log/stream`, `.../agent/{id}/log/stream` | log tails as batches of lines, `status` and `done` events |
| `GET /api/v0/webapp/repos/{org}/{name}/commits/{oid}` | a commit with its patch and stat |
| `GET .../blob?ref=&path=` | a file with size, line count, binary flag and the last commit touching it |
| `GET .../compare?base=&head=` | commits and the diff between two refs |
| `GET /api/v0/webapp/search?q=` | repositories, issues and changes for the command palette |

The `/` redirect to `/app/` and the catch-all for four-segment app paths are registered as host-qualified patterns (the server's base-URL host, `localhost`, `127.0.0.1`, `::1`, plus `ORE_WEBAPP_HOSTS`); see `internal/webapp/register.go` for why.
