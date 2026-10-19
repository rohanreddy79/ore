# Pipelines slice: GitHub Actions on-ramp, caches, artifacts, secrets by reference

Branch `slice/pipelines-actions` (worktree `rohan/slice-pipelines-actions-ac2a0d`). Registers as `pipelines.actions`. Migration `0005_pipelines.sql`.

A repository with `.github/workflows/*.yml` now runs on Ore without changes for the supported subset. The compiler turns each workflow into the Pipeline IR (`spec/pipeline-ir`), every task is content-addressed, caches are scoped by trust level, artifacts move between jobs and are downloadable from the API, and secrets reach only the step process that references them, encrypted at rest and masked in logs. Everything the compiler cannot map exactly is a **divergence** stored on the run; nothing is skipped silently.

## What landed

### `internal/pipelines` (new files; existing files edited additively, exported API unchanged)

| File | What |
|---|---|
| `ir.go` | Go types mirroring `pipeline-ir.schema.json`: `IR`, `IRTask`, `Inputs`, `Step`, `Executor`, `Outputs`, `Divergence`, trust scopes and their ordering (`TrustRank`, `CanReadScope`). |
| `hash.go` | `ContentHash(task)`: SHA-256 over the canonical JSON of resolved inputs, steps, executor and matrix coordinates. Secrets contribute by path only. `IR.Hash()` stamps every task. |
| `expr.go` | The expression language of conditions and `${{ }}` interpolations: the Actions grammar (a CEL subset): literals, property access, index, `! == != < <= > >= && \|\|`, functions (`contains`, `startsWith`, `endsWith`, `format`, `join`, `toJSON`, `fromJSON`, `hashFiles`, `success`, `failure`, `always`, `cancelled`), Actions coercion rules (case-insensitive string equality, number coercion), `Analyze` for static checks, `MatchGlob`/`GlobFiles` (`**`, `!` exclusions), `HashFiles`. |
| `cache.go` | `CacheStore`: `<data>/cache/<repo id>/<scope>/<entry>.json` plus one tar blob per cached path. Exact key then `restore-keys` prefix (newest first). A task reads its own scope and the scopes above it (`default` > `protected` > `internal` > `fork`) and writes only its own. |
| `artifacts.go` | `ArtifactStore`: zip per artifact under `<data>/artifacts/<run id>/`, a row in `artifacts`, `io.ore.artifact.published`. Names are validated (`ValidArtifactName`), entries are stored relative to the least common ancestor like upload-artifact, extraction refuses traversal. |
| `secrets.go` | `SecretStore`: AES-256-GCM with a key in `<data>/secrets.key` (0600, created on first use; AAD binds the ciphertext to repo, environment and name), rows in `secrets` with environment scoping (`production` overrides the repository-wide value), events `io.ore.secret.created\|updated\|deleted` without values. `Masker` replaces values in logs with `***`. |
| `frontend.go` | The extension points: `Frontend` (compile a tree into `Plan`s), `Stores`, `RegisterNativeStep`/`StepContext` (native `uses:` implementations), `WithTrust` (tests). |
| `runir.go` | The IR executor: event and ref detection (push to a branch or tag, pull_request from a change), trust scope (commit reachable from the default branch → `default`, else `internal`; forks have no first-class representation in v0 and are injected through `WithTrust`), trigger matching with branch/tag/path filters, plans stored in `pipeline_plans`, secret preflight, one fresh workspace per task (a real `git` checkout when the task uses `actions/checkout`, an archive otherwise), one container per task on the docker executor with one `docker exec` per step, the Actions step model (`if`, `continue-on-error`, `timeout-minutes` at job and step level, `$GITHUB_OUTPUT`, `$GITHUB_ENV`, `$GITHUB_PATH`, `$GITHUB_STEP_SUMMARY`, `::add-mask::`, `::set-output::`, `::error\|warning\|notice::`, `::stop-commands::`), job outputs and the `needs` context, post hooks (cache save), per-task span attributes and the `io.ore.pipeline.run.task.started\|finished` events. |
| `pipelines.go` | Additive: `Runner.Frontend`, `Runner.Stores`, `Runner.DataDir`; `execute` consults the front-end when the tree has no `.ore/pipeline.yaml` and otherwise behaves exactly as before. |

### `internal/pipelines/actions`

| File | What |
|---|---|
| `workflow.go` | The YAML model, decoded from `yaml.Node` so job and step order survive and unknown keys are reported: `on` as string/list/map with `branches`, `branches-ignore`, `tags`, `paths`, `paths-ignore`, `types`, `schedule.cron`, `workflow_dispatch.inputs`; `jobs` with `runs-on` (string, list, `{group, labels}`), `needs`, `if`, `env`, `defaults.run`, `environment`, `outputs`, `strategy.matrix` with `include`/`exclude`, `timeout-minutes`, `continue-on-error`, `container`, `services`, `permissions`, `concurrency`, `uses`/`with`/`secrets` (reusable workflows); steps with `run`/`uses`, `shell`, `working-directory`, `with`, `env`, `if`, `continue-on-error`, `timeout-minutes`. |
| `resolve.go` | `Resolver` for `uses:` references. `GitResolver` lists a repository's refs once with `git ls-remote` (cached under `<data>/actions-refs.json` for an hour; `ORE_ACTIONS_ORIGIN` overrides github.com), prefers the peeled tag, then the tag, then the branch; 40-hex refs are `sha-pinned`. `StaticResolver` is the fixture for tests. A ref that does not exist is `ErrRefNotFound` (refused); an unreachable resolver is `ErrResolverUnavailable` (divergence). |
| `compile.go` | The compiler. Triggers → `trigger.events` (structured filters the executor matches) and `trigger.filters.cel`; `workflow_dispatch.inputs` → `trigger.inputs`. Jobs → tasks in topological order (`needs` cycles and unknown jobs are refusals), matrix expansion (product → `exclude` → `include` with the GitHub extend-or-add rule), `runs-on` → image and OS (`ubuntu-*` → `ghcr.io/catthehacker/ubuntu:act-<version>` or `ORE_ACTIONS_IMAGE`; `container.image` wins), `env` merged workflow → job and stored in `params.env`, `timeout-minutes` → `limits.timeout_seconds`, `permissions`, `environment`, `continue-on-error`. Expressions that use only `matrix`/`strategy` and pure functions are evaluated at plan time (so `runs-on: ${{ matrix.os }}` and `name: Go ${{ matrix.go }}` resolve); the rest stay verbatim for the executor. Every expression is parsed and checked for unknown contexts, unknown functions and object filters. Every `secrets.NAME` reference becomes an entry of `inputs.secrets` (path `repos/<org>/<repo>/secrets/<NAME>`). Native actions declare their caches and artifacts on the task (`inputs.caches`, `outputs.caches`, `outputs.artifacts`, `inputs.artifacts_in`). |
| `native.go` | Native implementations registered with `RegisterNativeStep`: `actions/checkout` (the workspace is already materialized server-side; `path:` copies it into a sub-directory; another `repository` fails loudly), `actions/setup-go` (uses the Go on PATH when it satisfies `go-version`/`go-version-file`, otherwise downloads the release from go.dev into the tool cache, which is mounted at `/opt/hostedtoolcache` and persists across runs; `cache: true` restores and saves the module and build caches keyed by `hashFiles('**/go.sum')`), `actions/setup-node` (same shape against nodejs.org; `cache: npm\|yarn\|pnpm`), `actions/setup-python` (uses the interpreter on PATH, fails with a clear message otherwise; `cache: pip`), `actions/cache`, `actions/cache/restore`, `actions/cache/save` (outputs `cache-hit`, `cache-matched-key`, `cache-primary-key`; `lookup-only`, `fail-on-cache-miss`; saved as a post step only when the task succeeded and the exact key missed), `actions/upload-artifact` (globs, `!` exclusions, `if-no-files-found`, `overwrite`, `include-hidden-files`), `actions/download-artifact` (by name, or all artifacts with `pattern` and `merge-multiple`; `run-id`/`repository` are refused). |
| `frontend.go` | `Frontend` lists `.github/workflows/*.yml\|yaml` and compiles each into a `Plan` (IR, divergences, refusal). |
| `register.go` | The registry extension: wires the stores and the compiler onto the server's `Runner`, exposes them as `Services["pipelines.stores"]` and `Services["pipelines.actions"]`, mounts the routes below. |
| `testdata/` | The conformance corpus (19 workflows), their golden IR and `CONFORMANCE.md`. |

### Routes

| Route | What |
|---|---|
| `POST /api/v0/repos/{org}/{name}/secrets` `{name, value, environment?}` | Create or rotate a secret (201, never returns the value). Names: letters, digits, `_`, not starting with a digit or `GITHUB_`. |
| `GET /api/v0/repos/{org}/{name}/secrets` | Names, environments and timestamps only. |
| `DELETE /api/v0/repos/{org}/{name}/secrets/{secret}?environment=` | 204 / 404. |
| `GET /api/v0/repos/{org}/{name}/runs/{id}/artifacts` | The run's artifacts (name, size, files, digest). |
| `GET /api/v0/repos/{org}/{name}/runs/{id}/artifacts/{name}` | The zip (`application/zip`, `X-Ore-Artifact-Digest`). |
| `GET /api/v0/repos/{org}/{name}/runs/{id}/plan` | The compiled IR, divergences and refusal stored on the run. |
| `GET /api/v0/repos/{org}/{name}/workflows/plan?ref=` | Compile the workflows at a ref without running them (the `ore pipeline plan` of docs/04 A.3). |

### Per-task record

`pipeline_runs.tasks` now holds, per task: `id`, `name`, `image`, `status`, `exit_code`, `duration_ms`, `reason` (`timeout`, `condition-false`, `fail-fast`, `unsupported-os`, `unsupported`, ...), `pipeline`, `content_hash`, `cache_hit`, `continue_on_error`, `matrix_key`, `steps[]` (`outcome`, `conclusion`, `exit_code`, `duration_ms`, `reason`, masked `outputs`), `outputs`, `summary` (Markdown from `$GITHUB_STEP_SUMMARY`), `annotations`, the task's `divergences`, and `attributes`: `cicd.pipeline.name`, `cicd.pipeline.run.id`, `cicd.pipeline.task.name`, `cicd.pipeline.task.run.id`, `cicd.pipeline.task.run.result` (`success\|failure\|timeout\|error\|skip`), `cicd.pipeline.task.type`, `vcs.*`, `ore.content_hash`, `ore.cache_hit`, `ore.trust_scope`, `ore.executor`, `ore.image`, `span.start_time`, `span.end_time`, `span.duration_ms`, `span.queue_ms`. The same hash and result go on `io.ore.pipeline.run.task.finished`.

## Conformance

`go test ./internal/pipelines/actions -run TestConformanceCorpus` compiles every workflow under `testdata/` against a fixed repository, commit and resolver and compares the IR with its golden (`-update` regenerates the goldens and `testdata/CONFORMANCE.md`).

| Measure | Result |
|---|---|
| Workflows in the corpus | 19 (Go, Node, Python, Rust, matrix include/exclude, needs DAG with outputs, cache restore/save, artifacts between jobs, outputs/env/summary, conditionals, workflow_dispatch inputs, schedule, reusable-workflow stub, unsupported features, secrets and environments, timeouts and continue-on-error, Docker build, release on tags, monorepo paths) |
| Compiled to IR (pass rate) | 19/19 (100%) |
| Matching the golden IR | 19/19 (100%) |
| Compiled with zero divergences (fully supported) | 5/19 (26%): needs-dag, node-ci, outputs-env-summary, python-ci, rust-ci |
| Refused by the planner | 0/19 |

Run for real on the host executor (`TestConformanceCorpusRunsOnHost`): `outputs-env-summary`, `artifacts-between-jobs`, `conditionals`, `cache-restore-keys` (npm from PATH) and `go-ci` (setup-go, `go vet`, `go test`, coverage artifact, the lint action recorded as a loud skip, the binary artifact). In a container when Docker is available (`TestRunInContainer`, `ORE_ACTIONS_IMAGE=alpine:3` so the gate does not pull the runner image): checkout, `$GITHUB_ENV`/`$GITHUB_PATH` across `docker exec`s, a secret injected by name and masked, a cache saved in one job and restored in the next, an artifact uploaded in one job and downloaded in the next. The setup-go download path was exercised once against go.dev (Go 1.25 on a 1.26 host) and works; the gate pins the host's version so it stays offline.

## Divergence codes

Refusals (the run fails before any task starts, the plan row carries `refused`): `workflow.no-jobs`, `needs.unknown`, `needs.cycle`, `uses.malformed`, `uses.ref-not-found`. A refused workflow whose trigger does not match the event is logged but does not fail the run.

| Code | Meaning |
|---|---|
| `trigger.unsupported-event` | `on` has an event other than push, pull_request, workflow_dispatch, schedule (e.g. `release`, `issues`, `pull_request_target`); the workflow does not run for it. |
| `trigger.schedule-unscheduled` | `schedule` is parsed and recorded; v0 does not fire scheduled runs. |
| `trigger.unknown-filter` | An unknown filter under an event. |
| `trigger.none` | No `on` at all. |
| `workflow.unknown-key`, `job.unknown-key`, `step.unknown-key` | Keys the compiler does not know; ignored. |
| `job.reusable-workflow` | `jobs.<id>.uses`; the job is skipped loudly and its dependents see it as skipped. |
| `job.concurrency-ignored` | Job-level concurrency; runs are sequential in v0. |
| `job.container-options-ignored` | `container.options`, `credentials`, `ports`, `volumes`; only the image is used. |
| `job.services-unsupported` | Service containers are not started. |
| `job.environment-dynamic` | `environment` is an expression the planner cannot evaluate; secrets resolve from the repository scope. |
| `job.continue-on-error-dynamic`, `step.continue-on-error-dynamic` | A `continue-on-error` expression that is not plan-time evaluable (`matrix` and `strategy` are); treated as false. |
| `runs-on.unsupported-os` | macOS or Windows; the task is skipped. |
| `runs-on.labels-mapped` | `self-hosted` or custom labels mapped to the default linux image. |
| `runs-on.missing`, `runs-on.dynamic` | No `runs-on`, or one the planner cannot evaluate; default image. |
| `matrix.empty` | No combination remains after `exclude`; the job has no instances. |
| `matrix.dynamic` | A matrix (or axis, or include/exclude) from an expression; not expanded. |
| `step.unsupported-shell` | `pwsh`, `powershell`, `cmd`; the step fails at run time. |
| `uses.local-action`, `uses.docker-action` | `./path` and `docker://` steps; skipped loudly at run time. |
| `uses.unsupported-action` | A marketplace action without a native implementation; skipped loudly at run time, the step records `reason: unsupported-action`. |
| `uses.unresolved` | The resolver could not reach the repository; the reference is recorded as written (`resolution: unresolved`). |
| `uses.input-unsupported` | An input that changes semantics Ore cannot honour (checkout `repository`, `ref`, `submodules`; download-artifact `run-id`, `repository`, `github-token`). |
| `uses.input-missing` | A required input (`cache.key`, `cache.path`, `upload-artifact.path`) is missing; the step fails at run time. |
| `cache.path-unsupported` | A cache path with globs or escaping the workspace; skipped. |
| `artifact.invalid-name` | An artifact name with separators, `..`, a leading dot or control characters; the upload is refused at run time. |
| `expr.parse-error`, `expr.unknown-context`, `expr.unknown-function`, `expr.object-filter` | Expression problems found at plan time; the step or job fails at run time with the same message. |
| `secrets.dynamic-reference` | `toJSON(secrets)` or `secrets[expr]`; only `secrets.NAME` references are injected. |
| `secrets.github-token` | `secrets.GITHUB_TOKEN`; an empty value is injected. |

## Edge cases (named tests)

| Test | Where |
|---|---|
| `TestEdgeCyclicNeeds` (refused at compile and before any task starts), `TestEdgeUnknownNeedsRefused` | `actions/edge_test.go` |
| `TestEdgeMatrixExcludeRemovesAll` (job has no instances; dependents lose the need), `TestEdgeMatrixIncludeExclude` (extend-or-add rule, `runs-on: ${{ matrix.os }}`, `continue-on-error` from the matrix) | `actions/edge_test.go` |
| `TestEdgeIfFailureRunsOnlyAfterFailure` (step and job level `failure()`, `always()`, `success()`, `needs.<id>.result`) | `actions/edge_test.go` |
| `TestEdgeContinueOnErrorDoesNotFailJob` (step outcome/conclusion; a `continue-on-error` job fails itself, not the run) | `actions/edge_test.go` |
| `TestEdgeTimeoutKillsSleepingStep` (step timeout kills `sleep` and marks the step failed with `reason: timeout`, later `always()` steps still run; a job timeout cancels the rest) | `actions/edge_test.go` |
| `TestEdgeCacheMissThenHitAndTrustScopes` (a fork saves a key the default branch uses and the default branch does not restore it; an internal branch restores the default branch's entry by restore key; the fork keeps its own) and `TestCacheTrustScopes` (store level) | `actions/edge_test.go`, `pipelines/stores_test.go` |
| `TestEdgeSecretMissingRefusesBeforeStart` (clear error naming the secret, the task and the route; no task event) | `actions/edge_test.go` |
| `TestEdgeSecretNeverInLogAndScopedToStep` (value never in the log, task JSON or events; `::add-mask::` honoured; a step that does not reference the secret does not see it; environment-scoped value) | `actions/edge_test.go` |
| `TestEdgeArtifactNameTraversalRefused` (compile divergence and run-time refusal for `../`; an upload path outside the workspace is not collected) and `TestArtifactsStore` | `actions/edge_test.go`, `pipelines/stores_test.go` |
| `TestEdgeUsesMutableTagResolvedToSHA` (`v4` → SHA with `tag-mutable-resolved`, 40-hex → `sha-pinned`, `main` → `branch-resolved`; the SHA is in the content hash) | `actions/edge_test.go` |
| `TestEdgeUsesRefNotFoundRefused` (divergence plus refusal; an unavailable resolver is only a divergence) | `actions/edge_test.go` |
| `TestEdgeZeroJobs` (`jobs: {}` and no `jobs`) | `actions/edge_test.go` |
| `TestEdgeUnsupportedStepIsLoudSkip`, `TestEdgeTriggerFiltersAndPullRequest` (paths, tags, branches, pull_request context) | `actions/edge_test.go` |
| Expression semantics, interpolation, analysis, globs and `hashFiles`; secrets at rest and masking; content hash determinism; workflow commands and `$GITHUB_OUTPUT` heredocs | `pipelines/expr_test.go`, `pipelines/stores_test.go` |
| The API routes end to end (secrets write-only, artifact zip download, plan on the run, plan without running) | `actions/api_test.go` |

## Deferred

- **Task memoization**: the content hash is computed and recorded on every task and event, but a matching hash from a prior successful run is not yet used to skip execution and link outputs (docs/04 A.2). Reusing results silently would surprise users before the cache-hit UI exists; the hash is there so the planner can do it next.
- **Trust scopes for forks**: v0 has no fork entity; runs on the default branch are `default`, everything else `internal`. `fork` is exercised through `pipelines.WithTrust` and the store. `protected` branches other than the default are not distinguished yet.
- **`uses:` ancestry verification and vendoring** (docs/04 A.5): references are pinned to a SHA and recorded; whether the SHA is an ancestor of the protected history is not checked, and actions are not vendored with a digest.
- **Image digests**: resolved only for images already present locally (`docker image inspect`); absent otherwise (`inputs.image.digest` omitted).
- **Not implemented natively**: reusable workflows, local actions, `docker://` actions, service containers, container options, macOS and Windows runners, scheduled triggers, `workflow_dispatch` from the API (inputs compile to `trigger.inputs`; the `inputs` context carries the defaults), `pull_request_target`, concurrency groups, parallel execution of independent tasks (`max-parallel` is recorded), `vars`, `GITHUB_TOKEN`, OIDC. Each of these is a divergence code above.
- **Setup shims**: setup-python does not download interpreters; setup-go and setup-node download only exact or `major.minor` versions and depend on `curl` or `wget` in the image.
- **Cache paths with globs** (`**/node_modules`) are skipped; GitHub supports them.
- **Logs**: masking is exact-match per line; GitHub additionally masks some encodings of a value.

## Verification

`CGO_ENABLED=0 scripts/e2e.sh` (gofmt, vet, `go test ./...`, build, `ore demo --exit` on Docker) passes on the branch; `ore demo` is unchanged because the demo repository has a `.ore/pipeline.yaml`, which keeps precedence over workflows.
