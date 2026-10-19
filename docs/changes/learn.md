# Learning engine: lessons as pipelines, lesson runs, learn at the error, track 1

Branch `slice/learn` (worktree `rohan/slice-pipelines-actions-ac2a0d`, wave 2). Registers as `learn`. Migration `0009_learn.sql`. Owns `internal/learn/` and `lessons/`.

A lesson is a versioned package (a manifest with one assert query per step, localized step text, an optional template for a scratch repository) loaded from the curriculum embedded in the binary and from `ORE_LESSONS_DIR`. A lesson run is a ledger entity that advances when the learner's real repository satisfies the current step, detected from the same ledger events every pipeline reacts to. Every error state in the product maps to the lesson that explains it. Track 1 ships with five lessons in English, the first two also in Hindi and Brazilian Portuguese, and every lesson is run end to end by `internal/learn/curriculum_test.go` driving git and the API through its steps on a real server.

## What landed

| File | What |
|---|---|
| `lessons/embed.go` | `lessons.FS`: the curriculum embedded with `embed` so `ore learn` works offline and air-gapped. |
| `internal/learn/manifest.go` | The finalized `lesson.yaml` format (below), parsed from `yaml.Node` so an invalid assert is reported with its line; `steps/<lang>.md` parsed into a title, one section per `## Step N — …` heading (also `Passo`, `Etapa`, `चरण`) and the `> Hint:` / `> Dica:` / `> संकेत:` blockquotes as hints. |
| `internal/learn/assert.go` | The assert language: a parser with column-numbered errors and an evaluator against the ledger and git (`AssertEnv`), returning per-call results (`checks[]` with a human detail) so the learner sees why a step did not pass. |
| `internal/learn/catalog.go` | `LoadCatalog`: the embedded curriculum then every directory in `ORE_LESSONS_DIR` (a lesson with the same id overrides a built-in); `en` is required, the step count of every language must match the manifest, prerequisites must exist. |
| `internal/learn/runs.go` | `Service`: start (prerequisites, resume, language fallback, in_place with a read check, scratch repositories under the learner's org with the template's files, staged commits and review policy), check (evaluate the current step and the following ones while they pass; attempts; `io.ore.lesson.run.step.completed`, `io.ore.lesson.run.completed`), hints (`io.ore.lesson.run.hint.used`), progress per learner, the learner's view of a run. |
| `internal/learn/worker.go` | `Worker`: follows the event stream by sequence number (the transactional outbox), batches the relevant events (`io.ore.ref.*`, `io.ore.change.*`, `io.ore.pipeline.run.finished`, `io.ore.review.submitted`, `io.ore.landing.landed`) per repository and re-evaluates that repository's active runs once per batch; runs record the last sequence they saw so nothing is evaluated twice and events before a run started are not attempts. Interval `ORE_LEARN_INTERVAL` (default 1s). |
| `internal/learn/explain.go` | The learn-at-the-error catalog (`States`): 13 states mapped to lessons, event classification (failed pipeline runs are classified from the run's task record and log: test, dependency, timeout, cache, permissions, a missing secret, a refused action ref; deployment refusals; gated agent writes and budgets; conflicts; oversized review requests). |
| `internal/learn/routes.go`, `register.go` | The routes below; the extension loads the catalog, attaches `s.Services["learn"]`, takes identity from `s.Services["identity"]` when registered, mounts the routes and starts the worker. |
| `internal/ledger/migrations/0009_learn.sql` | `lesson_runs` (org, repo, learner, lesson id and version, mode, language, step, status, attempts, hints, evidence, last event seq, timestamps) and `lesson_progress` per learner and lesson. |
| `lessons/tracks/01-git-and-forge/*` | Five lessons (below) and `lessons/README.md`, the index. |

### The manifest (`lesson.yaml`)

```yaml
id: git-and-forge/your-first-change     # <track slug>/<lesson slug>
version: 0.2.0
track: 1-git-and-forge
title_key: title                        # key of the steps front matter that holds the title
estimated_minutes: 30
prerequisites: []                       # lesson ids the learner must have completed (in any org)
artifact: a merged change in your own repository
exam_objectives:                        # external currency for the credential
  - github-foundations: "Introduction to Git; ..."
mode: scratch_repository                # in_place | scratch_repository
languages: [en, hi, pt-BR]              # shipped = steps/<lang>.md exists
quota_class: learning
template:                               # scratch_repository only
  files: [{path: README.md, content: "..."}]   # the first commit on the default branch
  commits:                              # further commits, in order, on main or on a named branch
    - {message: "...", branch: "", files: [...]}
  review_policy: {required_approvals: 1}       # applied through the review service
steps:                                  # four or fewer
  - id: 1-clone-and-branch
    assert:
      query: ref.exists('learn/first-change-*')
    hint_after_failures: 2              # the UI surfaces the first hint after N failed checks
on_complete:
  credential: track-1-progress
  cleanup: ...                          # recorded; cleanup is deferred
```

A scratch run creates `<org>/learn-<lesson slug>-<learner>` (a repository name cannot contain `/`); the org is the request's `org`, else the learner's first membership, else the first org the learner may write to. An in-place run names the repository (`repo: org/name`) and requires read access to it when identity is registered. Starting a lesson evaluates its first step at once, so a repository that already satisfies early steps picks up where it is; those evaluations are not attempts.

### The assert language

```
expr  := or          or := and ('||' and)*        and := not ('&&' not)*
not   := '!' not | cmp
cmp   := call (('>=' | '>' | '==' | '!=' | '<=' | '<') NUMBER)?     -- only after a counting function
call  := NAME '(' args ')' | '(' expr ')' | 'true' | 'false'
arg   := 'string' | NUMBER | learner | default
```

| Function | Returns | Meaning |
|---|---|---|
| `ref.exists(pattern)` | bool | a ref matches the glob (`refs/heads/` is implied; `*` within a segment, `**` across) |
| `ref.commits_by(actor, pattern)` | count | commits beyond the default branch on matching refs whose last recorded push was by `actor` (`learner` or `'name'`) |
| `change.open(pattern)` / `change.merged(pattern)` | bool | a change with a matching source ref is open / merged |
| `change.mergeable(pattern)` | bool | the latest open matching change merges into its target without conflicts (server-side merge-tree on the live source head) |
| `change.approved(pattern)` | bool | the latest matching change has an approving review |
| `pipeline.green(ref)` | bool | the latest pipeline run on the ref's commit succeeded |
| `file.exists(ref, path)` / `file.contains(ref, path, text)` | bool | the file at the ref (first match of a pattern, or `default`) exists / contains the text |

Parse errors name the column (`column 12: unknown function ref.exist (known: …)`), and a manifest with an invalid query fails to load with `lesson.yaml:LINE: step ID: invalid assert: …`. Evaluation errors (the run's repository is gone) are reported as such, never as a failed step.

### Routes

| Route | What |
|---|---|
| `GET /api/v0/learn/lessons?lang=` | The catalog with languages, exam objectives and the learner's status per lesson (`completed`, `in_progress`, `available`, `locked`). |
| `GET /api/v0/learn/lessons/{id}?lang=` | Manifest, localized steps (with hints), the error states the lesson explains; falls back to English with a note. |
| `GET /api/v0/learn/lessons/{id}/steps.md?lang=` | The raw step text (`Content-Language`; `X-Ore-Language-Fallback: en` and a leading comment when falling back). |
| `POST /api/v0/learn/runs` `{lesson, repo?, org?, lang?}` | Start (201) or resume (200, `resumed: true`); 409 with `prerequisite` when one is missing; 403 when the learner cannot read the repository; 400 for an in_place lesson without `repo`. |
| `POST /api/v0/repos/{org}/{name}/learn/runs` `{lesson, lang?}` | Start in place on that repository. |
| `GET /api/v0/learn/runs?status=` and `GET /api/v0/repos/{org}/{name}/learn/runs` | The learner's runs; the runs on a repository. |
| `GET /api/v0/learn/runs/{id}?lang=` | The run with the current step's text, the hints shown so far, `hint_unlocked`, `auto_hint` after `hint_after_failures` failed checks. Runs are private to their learner (403). |
| `POST /api/v0/learn/runs/{id}/hint` | The next hint of the current step; 409 `hints exhausted` when none is left. |
| `POST /api/v0/learn/runs/{id}/check` | Evaluate now; returns `passed`, `advanced[]`, `completed`, the `checks[]` with details; 422 when the assert cannot be evaluated. |
| `GET /api/v0/learn/progress` | Progress rows plus the annotated catalog, `completed`/`total`. |
| `GET /api/v0/learn/explain?event=<id>` or `?state=<state>` | The lesson for an error state; 404 with `state`, `nearest_track` and `nearest_lessons` when no shipped lesson covers it; without parameters, the state catalog. |

Events: `io.ore.lesson.run.started`, `.step.completed` (with the checks as evidence), `.completed` (credential, artifact, evidence), `.hint.used`.

### Track 1

| Lesson | Steps | Languages |
|---|---|---|
| `git-and-forge/your-first-change` | branch → signed commit of `learn/hello.md` pushed by the learner → change open → merged | en, hi, pt-BR |
| `git-and-forge/branches-and-protection` | see the protected-ref refusal, then branch → `learn/branches.md` says "protected" → change → merged into the protected branch | en, hi, pt-BR |
| `git-and-forge/resolving-a-conflict` | branch from `HEAD~1` of a two-commit history → an open change that conflicts → resolved (mergeable) → merged | en |
| `git-and-forge/a-change-with-a-review` | branch with `learn/review.md` → change (policy: one approval) → approved by a reviewer → landed through the review service | en |
| `git-and-forge/a-green-pipeline` | branch making the template's red `.ore/pipeline.yaml` pass → a green run on the branch → change → merged | en |

Each has exam objectives, hints on every step, and `credential: track-1-progress`.

## Edge cases (named tests)

| Test | What it proves |
|---|---|
| `TestEdgeAssertMissingRepository` | An assert without a repository, or on a run whose repository directory is gone, is an evaluation error (`422`, "missing on disk"), not a failed step; a deleted run cannot be checked. |
| `TestEdgePrerequisiteNotCompleted` | Starting a lesson before its prerequisite is a 409 naming the prerequisite id and title; the catalog shows it `locked`; completing the prerequisite unlocks it. |
| `TestEdgeHintsExhausted` | Two hints then a 409; the view counts them; hint events; the automatic hint unlocks after `hint_after_failures` failed explicit checks. |
| `TestEdgeSameLessonStartedTwice` | The second start resumes the active run (200, same id, no second scratch repository); another learner gets their own run; runs are private. |
| `TestEdgeInvalidAssertInManifest` | A broken query in `ORE_LESSONS_DIR` fails the load with `lesson.yaml:7` and the unknown function; `TestCatalogLoadsTheCurriculum` covers the same for parse errors and mismatched step counts. |
| `TestEdgeLanguageNotAvailable` | `zh-Hans`/`ja` fall back to English with `language_fallback`, a note listing the available languages, and the `X-Ore-Language-Fallback` header on `steps.md`; Hindi serves Hindi. |
| `TestEdgeWorkerNoActiveRuns` | Events on a repository with no active runs evaluate nothing and emit no lesson events. |
| `TestEdgeCompletedRunReceivesLateEvents` | Late pushes on a completed run's repository evaluate nothing, change nothing, emit nothing; an explicit check reports completion. |
| `TestEdgeExplainWithoutLesson` | An event type without a lesson is a 404 with the nearest track and its lessons; a known state whose lesson is unshipped names the lesson, its track and the nearest shipped track; a missing event is 404; run classification by task record and log. |
| `TestEdgeInPlaceRunOnUnreadableRepository` | A user outside the org gets 403 for an in_place run and for a scratch repository in that org; a member runs in place; an in_place lesson without `repo` is a 400. |
| `TestCurriculum` | All five lessons end to end with real git over HTTP and the API, the worker advancing each step; the Hindi and Portuguese views; the red pipeline run's event explained to the green-pipeline lesson; progress and the newcomer's locked catalog. |
| `TestParseManifest`, `TestParseSteps`, `TestParseAssert` | Format and language errors with positions. |

## Deferred

- **Tutor and Q&A** (docs/06 §6): no model is called; hints are the authored ones. `POST .../hint` is the surface a tutor would plug into.
- **Credentials** (Open Badges 3.0): `on_complete.credential` is recorded on the run and in the completion event with the evidence; no badge is issued or signed.
- **Cohorts, role paths, the organization dashboard** (docs/06 §4): progress is per learner only.
- **Ephemeral fork mode** and **cleanup** on completion: `cleanup` is recorded in the manifest and not executed; scratch repositories stay.
- **Protected-ref refusal has no ledger event**: the pre-receive hook refuses inside git; the explain endpoint answers for it through `?state=ref.protected` until the push handler records refusals.
- **Service containers in lesson pipelines, quota accounting** (`quota_class` is recorded).
- **Replay after downtime**: the worker starts at the end of the event stream, so events that happened while the server was down are not replayed; a learner's explicit check (or their next push) catches the run up.
- **Tracks 2 to 6**: referenced by the explain catalog (`pipelines/debugging-a-failed-task`, `delivery/environments-and-approvals`, …) and reported as unshipped with the nearest track.
- **Translations** beyond Hindi and Brazilian Portuguese for the first two lessons; Weblate is not wired.

## Verification

`CGO_ENABLED=0 scripts/e2e.sh` passes on the branch (gofmt, vet, every package including `internal/learn`, build, `ore demo --exit` on Docker). The curriculum test needs `git-http-backend` (skipped without it) and takes about ten seconds.
