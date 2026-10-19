# GitHub adapter (slice/github-adapter)

Ore's loop on repositories that stay hosted on GitHub: the "adopt before you migrate" wedge from `docs/03-architecture.md` §2.5 and `docs/10-migration.md` §3 (the sync period). Registered as `fpi.github`; owns `internal/fpi/fpi.go`, `internal/fpi/github/` and migration `0003_github.sql`.

## What landed

**The Forge Provider Interface, v0.x** (`internal/fpi/fpi.go`). The minimal seam the loop needs from a host: `Repos` (resolve a repository), `Refs` (list), `Issues` (read-through), `Statuses` (report a verification result against a commit) and `Hooks` (receive the host's out-of-band events), plus `Capabilities()` so Ore knows what to emulate. Two implementations:

- `fpi.Native`: Ore's own git host, a thin read wrapper over `internal/gitx` and the ledger. Statuses become `io.ore.commit.status.reported` events; it has no inbound hooks (receive-pack records ref updates in-band).
- `github.Provider`: the adapter below, over GitHub's REST API with the standard library only (no SDK).

Providers register by name (`fpi.Register`, `fpi.Lookup`, `fpi.Names`); `spec/fpi/README.md` remains the full-interface sketch that later slices fill in (objects, principals, packages, migrations).

**Linking** — `POST /api/v0/repos/{org}/{name}/github/link` with `{"owner":"o","name":"n"}` (or `{"repository":"o/n"}`, optional `secret` and `installation_id`). Ore verifies the token against `GET /repos/{owner}/{name}`, records the link (`github_links`, event `io.ore.github.linked`), adopts GitHub's default branch when it differs (the repository's `default_branch` and the bare repository's `HEAD`), and performs the initial mirror of every branch and tag. The response carries the link, its webhook secret (generated when not supplied; also shown by `GET .../github/link`), the webhook URL and the applied ref updates. `DELETE .../github/link` unlinks (`io.ore.github.unlinked`); `POST .../github/sync` reconciles now; `GET /api/v0/github/links` lists links without secrets.

**Mirroring** (`mirror.go`). GitHub's refs are fetched with `git fetch --prune` into a tracking namespace, `refs/github/heads/*`, `refs/github/tags/*` and `refs/github/pull/N/head`, authenticated with the token as an `Authorization: Basic x-access-token:<token>` header scoped to the clone URL's origin and passed through git's environment configuration, so the token never appears on a command line or in an error. The namespace is diffed before and after the fetch and every difference is applied to `refs/heads/*` / `refs/tags/*` with compare-and-swap and recorded through `ledger.RecordRefUpdate` with `via: "github"` (`io.ore.ref.updated` / `io.ore.ref.deleted`). Because only the tracking namespace is pruned, branches that exist only in Ore (the agent's `ore/agent/*`, local work) are never touched. A force push is detected from the objects (old tip not an ancestor of the new one), not from the payload. The old objects stay (`gc.auto` is off), so a change that pointed at a rewritten tip keeps its diff.

**Webhooks** — `POST /hooks/github`, `application/json` only, 1 MB cap. The link is found from `repository.full_name` (case-insensitive), `X-Hub-Signature-256` is verified in constant time against that link's secret, and `X-GitHub-Delivery` ids are stored (`github_deliveries`) so a replayed delivery is acknowledged without side effects. Processing is synchronous (GitHub's delivery log shows the real outcome) on a background context so a dropped connection does not abort a fetch. Handled:

| Event | Effect |
|---|---|
| `ping` | acknowledged, recorded |
| `push` | mirror fetch; ref updates recorded as above; `io.ore.github.push.mirrored` summarizes the delivery (`ref`, `before`, `after`, `created`, `deleted`, `forced`, `pusher`, `updates`) |
| `issues` `opened` / `edited` / `closed` / `reopened` | mirrored into Ore issues; the Ore issue keeps the GitHub number when it is free (docs/10 §2), otherwise the next free number; the mapping lives in `github_issue_map`; events `io.ore.issue.opened` / `.updated` / `.closed` with a `github` sub-object |
| `pull_request` `opened` / `reopened` / `synchronize` / `edited` / `ready_for_review` / `closed` | mirrored into changes with provenance `{"source":"github","pr":N}`, source ref `refs/github/pull/N/head`, target `refs/heads/<base>`, number preserved when free; `synchronize` records a patch set (`io.ore.change.patchset.pushed`); `closed` with `merged` marks the change merged with GitHub's merge commit (`io.ore.change.merged`, `via: "github"`), otherwise closes it; mapping in `github_pr_map` |
| anything else, and unhandled actions (`labeled`, `assigned`, …) | acknowledged and recorded as `io.ore.github.event.ignored` |

Every accepted delivery emits `io.ore.github.delivery.received` (or `.event.ignored`) with the delivery id, event, action and outcome. The actor on everything a delivery causes is `human/github:<sender login>`; the worker's actor is `app/github`. An App delivery's `installation.id` is stored on the link.

**Commit statuses** (`status.go`). A reporter tails the ledger's `io.ore.pipeline.run.*` events from a stored cursor (`github_cursors`) and, for runs of linked repositories whose commit is reachable from a GitHub ref, posts `POST /repos/{owner}/{repo}/statuses/{sha}` under context `ore/pipeline`: `pending` when the run starts, `success` / `failure` / `error` when it finishes, with the Ore run URL as `target_url`. Within one pass only the latest state per run is posted; posted states are recorded (`github_statuses`, `io.ore.github.status.posted`) so nothing is sent twice; a 422 (GitHub no longer has the commit) is recorded and not retried. Runs on commits GitHub does not know (agent branches, local commits) and runs of unlinked repositories are skipped. The cursor starts at the ledger's head when the adapter is first attached, so historical runs are not reported.

**Reconciliation worker** (`worker.go`). Every `ORE_GITHUB_SYNC_INTERVAL` (default `5m`) each link is re-synced: `GET /repos/{owner}/{name}` (surfaces rate limiting and renames), a mirror fetch of branches and tags, and a fetch of every open pull request's head, with changes recorded exactly as a webhook would (`io.ore.github.sync.completed` when something moved, `io.ore.github.sync.failed` on errors). A `429` (or `403` with the primary limit exhausted) defers that link until `Retry-After` (or `X-RateLimit-Reset`), records `io.ore.github.sync.deferred` with the retry delay, and the worker does not call GitHub for it before then. The status reporter runs every `ORE_GITHUB_STATUS_INTERVAL` (default `5s`) and backs off the same way.

**Schema** (`internal/ledger/migrations/0003_github.sql`): `github_links` (repo id, owner, name, GitHub id, secret, installation id, clone URL, default branch, last sync time/status/error, next sync time, actor), `github_issue_map`, `github_pr_map`, `github_deliveries`, `github_statuses`, `github_cursors`. Every table carries `org_id`.

## Configuring a real GitHub repository

1. **Credential.** Either a personal access token or a GitHub App installation token in `ORE_GITHUB_TOKEN` (or a file path in `ORE_GITHUB_TOKEN_FILE`, re-read on every request, for installation tokens that a sidecar rotates hourly).
   - Fine-grained PAT, repository permissions: *Contents: Read*, *Metadata: Read*, *Issues: Read*, *Pull requests: Read*, *Commit statuses: Read and write*.
   - Classic PAT: `repo` (private repositories) or `public_repo` + `repo:status`.
   - GitHub App: the same five permissions (*Commit statuses: Write*), subscribed to the *Push*, *Issues* and *Pull request* events. Mint the installation token with the App's JWT (`POST /app/installations/{id}/access_tokens`) and write it to the token file; minting inside Ore is a follow-up (see below).
2. **Start Ore** with the token in the environment and a `--base-url` GitHub can reach (the webhook URL is `<base-url>/hooks/github`; put a TLS terminator in front of it). `ORE_GITHUB_API` only changes for GitHub Enterprise Server (`https://ghes.example/api/v3`) and tests.
3. **Link**: `ore api POST /api/v0/repos/acme/app/github/link '{"repository":"acme/app"}'`. The response includes `link.secret` and `webhook_url`; the initial mirror runs before it returns.
4. **Webhook** on the GitHub repository (Settings → Webhooks → Add): payload URL = `webhook_url`, content type `application/json`, secret = `link.secret`, events: *Pushes*, *Issues*, *Pull requests*. GitHub's `ping` shows up in the ledger as a received delivery. Link first, then create the webhook: deliveries for an unlinked repository are answered 404 (redeliver them from GitHub's *Recent Deliveries* after linking).
5. From here the loop runs on the mirror: `ore issue list acme/app` shows GitHub's issues with their numbers, `ore agent run acme/app --issue 42` works on the mirrored branch, pipeline results on GitHub commits appear as the `ore/pipeline` status on GitHub, and anything a webhook misses is caught by the worker within `ORE_GITHUB_SYNC_INTERVAL`.

Writes stay one-directional in this slice: GitHub is the source of truth for refs, issues and pull requests; Ore's own changes on a linked repository (the agent's branches, server-side merges into the mirror) are not pushed back to GitHub yet (follow-up).

## Edge cases (each is a named test in `internal/fpi/github`)

| Case | Test | Behaviour |
|---|---|---|
| Bad, missing or malformed signature | `TestWebhookBadSignature` | 401; nothing mirrored, no event recorded |
| Replayed delivery id | `TestWebhookReplayedDeliveryIsIdempotent` | 200 `duplicate`; no new events; a redelivery with a new id converges without duplicate ref updates |
| Push with a deleted branch | `TestPushDeletedBranch` | the mirrored branch is deleted and `io.ore.ref.deleted` recorded; an Ore-only `ore/agent/*` branch survives |
| Force push (old tip not an ancestor) | `TestPushForced` | ref updated, `io.ore.github.push.mirrored` has `forced: true` and the update's `forced` flag; old objects kept |
| `pull_request` for an already mirrored PR (`opened` redelivered, `synchronize`) | `TestPullRequestAlreadyMirroredNoDuplicate`, `TestPullRequestMirrored` | head updated as a patch set, no second change; close-without-merge closes; number collision takes the next free number; a PR closed before Ore saw it is acknowledged and ignored |
| `issues` event for an unknown repository | `TestWebhookUnknownRepository404` | 404, nothing recorded |
| Webhook larger than 1 MB | `TestWebhookTooLarge413` | 413 (by `Content-Length`, and by reading when absent), mirror untouched |
| Wrong webhook content type | `TestWebhookWrongContentType415` | 415 with the fix in the message |
| Token missing | `TestLinkWithoutToken` | link answers 503 naming `ORE_GITHUB_TOKEN`; the FPI provider returns `ErrNoToken`; nothing recorded |
| Token rejected, repository missing, double link | `TestLinkErrors` | 502 with GitHub's message / 404 / 409 |
| API rate-limit response (429 + `Retry-After`) during sync | `TestSyncRateLimitedDefers` | link deferred until `Retry-After`, `io.ore.github.sync.deferred` recorded, no API calls before then, resumes and catches up afterwards; the manual sync endpoint answers 503 with `Retry-After` |
| Rate limit while posting statuses | `TestStatusRateLimitDefers` | pass stops without advancing the cursor, `sync.deferred` recorded, posts after the delay |
| Reconciliation finds a branch, a tag, a PR head moved and a branch deleted without a webhook | `TestReconcileFindsMovedBranch` | all recorded via `github` with the `app/github` actor; quiet passes record nothing |
| Repository gone from GitHub during sync | `TestSyncFailureRecorded` | link marked failed, `io.ore.github.sync.failed` |
| Status states and the mirrored-only rule | `TestStatusPendingThenFinal`, `TestStatusOnlyForMirroredCommits`, `TestStatusRejectedShaNotRetried` | pending → final; local and unlinked commits never reported; a 422 is not retried |
| Issue mirroring, number preservation and collision | `TestIssuesMirrored`, `TestIssueNumberCollisionFallsBack` | `#42` stays `#42`; native issues continue after it; a collision falls back to the next free number; an issue first seen through `closed` is mirrored closed |
| Default branch differs from Ore's | `TestLinkAdoptsGitHubDefaultBranch` | the repository and `HEAD` follow GitHub |
| Worker loop | `TestWorkerLoop` | reconciles and posts statuses on its own with short intervals |
| Signature vector from GitHub's docs | `TestSignKnownVector` | matches |
| Event translation of recorded payloads | `TestTranslateFixtures` | `testdata/*.json` (push, deleted, forced, issues opened/edited/closed/labeled, PR opened/synchronize/closed, ping, star, installation) |

The fake GitHub used by the tests (`helpers_test.go`) serves the REST endpoints behind the bearer token, answers 429 on demand, records status calls, and serves each repository's git data with `git-http-backend` over smart HTTP behind the token as basic credentials, so every mirror fetch in the tests authenticates the way a fetch from github.com does.

## Not verified without a real token

`ORE_GITHUB_TOKEN` was not set in the build environment, so `TestRealGitHub` (link and mirror a real repository named by `ORE_GITHUB_TEST_REPO`, then reconcile) was skipped. Against the real service the following were exercised only through the fake and GitHub's documented shapes: the exact `clone_url` and `default_branch` fields of `GET /repos`, `Retry-After` / `X-RateLimit-*` on 429 and 403, the statuses endpoint's 201 and 422 answers, the `x-access-token` basic-auth form for installation tokens over git, and real webhook headers (`X-Hub-Signature-256`, `X-GitHub-Delivery`, installation ids). GitHub Enterprise Server was not tested at all (`ORE_GITHUB_API` is the only switch).

## Follow-ups

- Mint GitHub App installation tokens inside Ore from an App id and private key (today a token or a rotated token file).
- Push back: Ore's agent branches and server-side merges to GitHub, pull requests opened from Ore changes, and the checks API (richer than statuses) for pipeline results.
- Issue comments and review comments; labels, milestones, assignees.
- Deliveries dashboard and redelivery from Ore (the `github_deliveries` table already records outcomes).
- A UI page for links and sync state; the `refs/github/*` namespace appears in `GET .../refs` today.
