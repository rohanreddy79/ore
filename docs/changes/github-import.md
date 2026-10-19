# GitHub importer with fidelity report (slice/github-import)

Wave 2 follow-up of the GitHub adapter (`docs/changes/github-adapter.md`), built in the same session on the same branch. Owns new files in `internal/fpi/github/` (`import_*.go`, tests, `fake_import_test.go`) and migration `0011_github_import.sql`; registers through the existing `fpi.github` extension. The spec is `docs/10-migration.md` §2 (the fidelity table) and §3 (the process): plan, run, sync period, verify.

## What landed

**Plan** — `POST /api/v0/repos/{org}/{name}/import/plan {"source": "github.com/owner/name"}` produces the fidelity report and changes nothing (one `ls-remote`, API reads only; the report is stored on a job in status `planned` for later retrieval). Each line is `exact | high | best-effort | not-migrated` with the method and the reason:

| Object | Count from | Fidelity as implemented | Why |
|---|---|---|---|
| branches, tags | `git ls-remote` | exact | the adapter's mirror: every ref, signatures preserved, recorded on the ledger via `github` |
| LFS objects | `.gitattributes` `filter=lfs` patterns | not-migrated | Ore has no LFS store in v0; pointer files are mirrored as-is (the count is patterns, not objects) |
| issues | issues list minus pull requests | exact when the Ore repository has no issues or changes, else high | numbers preserved (see below), state, author, timestamps |
| issue comments | counted during the run | exact | original author and timestamp, through the gateway's comments store |
| pull requests | pulls list | high | one change per PR with provenance `{"source":"github","pr":N}`, one patch set per observed head, reviews with verdicts; heads before a force push only when GitHub still serves the objects |
| releases, assets | releases list | exact | recorded with tag, notes, flags and author; assets downloaded and checksummed |
| labels, milestones | labels and milestones lists | best-effort | Ore v0 has no label or milestone entity; recorded in the mapping, each issue keeps its labels on its mapping |
| wiki | `has_wiki` | exact | the wiki repository is cloned next to the mirror; an empty wiki is recorded as absent at run time |
| Actions workflows | `.github/workflows` | exact / high / best-effort | each file compiled through `internal/pipelines/actions`; divergences listed per workflow; a refused workflow marks the line best-effort |
| branch protection | the default branch's protection | high / not-migrated | the default branch is protected in Ore by construction; GitHub's rules are recorded in the line's details; unreadable without Administration: read |
| CODEOWNERS | three standard locations | best-effort | mirrored with the tree, not compiled to policy yet |
| webhooks | hooks list | high / not-migrated | recreated with new secrets and listed for rotation; unreadable without `admin:repo_hook` |
| identities | contributors | high | every author becomes a mannequin (`import_identities`), claimable later |

The report also carries the compiled workflows, the contributors, the number continuity note, size and time estimates (`size_kb`, `objects`, `api_calls`, `seconds`), warnings, and a score (exact 1, high 0.9, best-effort 0.5, not-migrated 0, weighted by count).

**Run** — `POST .../import/run {"source", "options": {issues, pulls, releases, webhooks, workflows, wiki, wait}}` plans again, records a job in `import_jobs` (`io.ore.import.started`) and executes it in the background (202) or synchronously with `wait` (200). Phases in order: `git` (link the repository if it is not linked yet and mirror, so the adapter's webhooks and reconciliation keep it current afterwards; clone the wiki), `labels`, `milestones`, `issues`, `comments`, `pulls`, `releases`, `webhooks`, `workflows`, `identities`, `done`. Every imported object is keyed in `import_mappings` (kind, GitHub id) and every page advances a cursor on the job, so a resume after a crash continues from the saved page and skips what is mapped: no duplicates. Rate limiting (`429`, or `403` with the primary limit exhausted) sets the job to `deferred` with `next_attempt_at` (`Retry-After`) and `io.ore.import.deferred`; the adapter's worker resumes it when the delay has passed, and `POST .../import/jobs/{id}/resume` does so by hand. Any other error fails the job (`io.ore.import.failed`) with the cursor intact; per-object problems (an asset that will not download, a timeline that cannot be read) are recorded in the job's `errors` and the run continues. Completion emits `io.ore.import.completed` with the progress counts.

What each phase writes:

- **Issues** keep their GitHub numbers. Before the first page, a closed marker issue is created at the highest GitHub number (`io.ore.import.numbers.reserved`) so native issues continue after it while the import fills the numbers below; when that number is an issue the marker is replaced in place, when it is a pull request the marker stays as a closed note. A number already taken by a native issue is replaced by one above the whole GitHub range (`issues_renumbered` in the progress, `preserved: false` in the mapping), so no later import object loses its number. Each issue row carries the original author (`human/github:<login>`, or `app/github:<login>` for bots), `created_at` and `closed_at`, writes the adapter's `github_issue_map` row, and emits `io.ore.issue.imported`.
- **Issue comments** go into the gateway's `comments` table (the gateway keeps no insert API for other packages, so this is the plain insert with original author and timestamp; `io.ore.comment.imported`).
- **Pull requests** become changes (number preserved by the same rule) with source `refs/github/pull/N/head`, target the base branch (or the default branch when the base is gone, noted in `errors`), state `open | closed | merged` with GitHub's merge commit and `merged_at`, and the adapter's `github_pr_map` row. Patch sets: the timeline's `head_ref_force_pushed` events give the earlier heads; each one GitHub still serves becomes a patch set (base = merge base against the target), the current head is the last; unreachable earlier heads are counted in `patch_sets_unreachable` and noted. Conversation comments become change-level comments; review comments are anchored to the patch set whose head is the comment's commit, at the comment's line, when GitHub still reports a position; otherwise they attach to the latest patch set with a `migrated anchor` marker (original path, line and commit) in the body and `anchor: migrated` in the mapping. Reviews map `APPROVED → approve`, `CHANGES_REQUESTED → request_changes`, `COMMENTED → comment`, `DISMISSED → comment` with a `[dismissed on GitHub]` prefix; `PENDING` reviews are skipped. Rows go into review v1's `patch_sets`, `change_comments` and `reviews` with their original timestamps and emit review v1's event names (`io.ore.patchset.recorded`, `io.ore.review.submitted`) plus `io.ore.comment.imported`, so review v1's listings, approvals and interdiffs work on imported changes.
- **Releases** are recorded in `import_releases` (`io.ore.release.imported`) and assets downloaded to `<data>/imports/<org>/<name>/releases/<tag>/<asset>` with size and SHA-256 in `import_assets`; a failed download is recorded as `status: failed` with the error and retried by the next run.
- **Webhooks** are recreated in `import_webhooks` with a fresh secret each (`io.ore.import.webhook.recreated`); `GET .../import/jobs/{id}` and `GET .../import/webhooks` list them with the new secrets for rotation. The webhooks-out slice can adopt the table.
- **Workflows** on the mirrored default branch are compiled and the conformance report (per workflow: compiled, tasks, divergences, refusal) is attached to the job (`conformance`).
- **Identities**: every login seen as author, commenter, reviewer or release author, plus the contributors list with their commit counts, becomes a row in `import_identities` (`io.ore.import.identity.recorded`) with roles and mention count. The identity slice exposes no claim route, so `claimed_user_id` stays empty; an Ore user with the same name is recorded as `suggested_user_id`. `GET .../import/identities` lists the mannequins of the organization.

**Status and verification** — `GET .../import/jobs` and `GET .../import/jobs/{id}` return the job with phase, cursors, progress (per-object counts), errors, report, conformance, webhooks and the last verification. `POST .../import/verify {"job"?}` re-checks every ref (ls-remote against the local refs), every issue (mapping present, title and state equal) and every release (recorded, tag mirrored, each asset on disk with the recorded size) and records the report on the ledger as `io.ore.import.verified`, signed: the report's canonical JSON is hashed with SHA-256 and signed with an ed25519 key the server keeps at `<data>/import-signing.key` (created on first use); the event carries the hash, the signature and the public key, and `VerifyReport.VerifySignature()` checks them.

**Schema** (`0011_github_import.sql`): `import_jobs`, `import_mappings`, `import_identities`, `import_webhooks`, `import_releases`, `import_assets`; every table carries `org_id`.

## Running an import

```bash
export ORE_GITHUB_TOKEN=...   # see docs/changes/github-adapter.md for the scopes; add admin:repo_hook (or Webhooks: read) for webhooks and Administration: read for branch protection
ore org create acme && ore repo create acme/app
ore api POST /api/v0/repos/acme/app/import/plan '{"source":"github.com/acme/app"}'      # the fidelity report; nothing changes
ore api POST /api/v0/repos/acme/app/import/run  '{"source":"github.com/acme/app"}'      # 202 with the job id
ore api GET  /api/v0/repos/acme/app/import/jobs/<id>                                   # progress, errors, webhooks to rotate
ore api POST /api/v0/repos/acme/app/import/verify '{}'                                  # the signed report
```

Import into a repository with no issues or changes for exact numbering. The run links the repository, so the sync period of `docs/10` §3 starts at once: configure the webhook from the link (`GET .../github/link`) and the adapter keeps refs, issues and pull requests current; the reconciliation worker also resumes deferred imports. Options turn phases off (`{"options":{"releases":false}}`); `wait: true` runs synchronously. A job can be resumed at any time; running the same source again is a no-op for everything already mapped.

## Edge cases (each is a named test in `internal/fpi/github/import_test.go`)

| Case | Test | Behaviour |
|---|---|---|
| Plan for a fixture repository (issues, PRs, releases, workflows, labels, milestones, hooks, protection, CODEOWNERS, LFS) | `TestImportPlanFidelityReport` | every line with the expected count and fidelity; workflows compiled (one exact, one with a divergence); nothing changed (no link, no issues, one git `info/refs` for ls-remote, only `io.ore.import.planned`) |
| Run, verify, continuity, webhook afterwards, idempotent rerun | `TestImportRunEndToEnd` | all phases complete with the expected counts; issues keep numbers, state, author and timestamps; the merged PR is a merged change with its merge commit and provenance; anchored and migrated review comments; reviews with verdicts; assets on disk; webhooks with new secrets; conformance attached; mannequins; verify OK with a valid signature (a tampered copy fails); a native issue continues after the highest number; a GitHub webhook edits the imported issue; a second run imports nothing twice |
| Resume after a simulated crash mid-issues | `TestImportResumeAfterCrashMidIssues` | the second issues page fails once: job `failed` in phase `issues` with cursor 2 and 100 issues; resume completes with 130 issues, no duplicates, the marker at #130 replaced by the real issue |
| Rate limit mid-run | `TestImportRateLimitMidRunDefersAndResumes` | 429 on the pulls list: job `deferred` with `next_attempt_at`, `io.ore.import.deferred`; the worker does not resume before `Retry-After`, resumes after it, completes without re-importing issues |
| Issue-number continuity | `TestImportNumberContinuity`, `TestImportRunEndToEnd` | with a native #1, GitHub #1 lands above the range (#6), #2 and #5 keep theirs, the next native issue is #7; the plan warns; the webhook resolves GitHub #1 to Ore #6 |
| PR comment on a vanished diff | `TestImportReviewCommentOnVanishedDiffMarkedMigrated` | marked migrated with the original anchor, the other comment anchored exactly; the unreachable pre-force-push head is reported, not fabricated |
| Asset download fails | `TestImportAssetDownloadFailureContinues` | the job completes, the error is recorded, the asset is `failed`; verify reports the mismatch; a later run downloads it and verify is clean |
| No issues and no PRs | `TestImportEmptyRepository` | zero lines, score 1, run completes, verify OK, no reservation, native numbering starts at 1 |
| Plan on a source that does not exist | `TestImportPlanSourceErrors` | 404 "github.com/owner/name does not exist, or the token cannot see it"; malformed sources 400; no job recorded |
| Hooks not readable | `TestImportWebhooksNotReadable` | plan line not-migrated naming `admin:repo_hook`; the run records the reason and completes |
| Wiki | `TestImportWikiCloned` | cloned next to the mirror; a second run fetches into it |
| Job listing and scoping | `TestImportListsJobs` | jobs are per repository; verify without an import is 404 |
| No token | `TestImportWithoutToken` | 503 naming `ORE_GITHUB_TOKEN` |
| Source parsing | `TestParseSource` | `github.com/o/n`, `https://…(.git)`, `git@github.com:o/n.git`, `o/n` accepted; the rest rejected |

The fake GitHub (`fake_import_test.go`) serves the issues list with pull requests included and sorted like GitHub, `page`/`per_page` with `Link` headers, comments, timelines, review comments, reviews, releases with downloadable assets (`Accept: application/octet-stream`), labels, milestones, hooks, contributors, the contents API from the bare repository, branch protection, and one-shot failure injection (`500`, `429` with `Retry-After`).

## Not verified without a real token

`ORE_GITHUB_TOKEN` was not set, so nothing ran against github.com. In particular, against the fake only: the exact shape of `head_ref_force_pushed` timeline events (the importer reads `before_commit`/`after_commit` when present, else `commit_id`), whether review comments on a force-pushed head still report a `position`, the asset download redirect to the CDN (the client follows redirects and drops the token across hosts), `Link` pagination on very large repositories, and the secondary rate limits that surface as `403` with `Retry-After`. GitHub Enterprise Server was not tested.

## Follow-ups

- Labels and milestones as Ore entities (today in the mapping); releases as ledger entities (today in `import_releases`).
- A claim route in the identity slice: `import_identities.claimed_user_id` and re-attribution of imported rows.
- Adoption of `import_webhooks` by the webhooks-out slice.
- Batch mode for organizations (`docs/10` §6): the job model is per repository; a loop over repositories with a rolled-up report is the next step.
