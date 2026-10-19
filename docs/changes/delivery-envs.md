# Delivery slice: environments, policy, approvals, remote targets, rollouts, previews, DORA

Branch `slice/delivery-envs` (worktree `rohan/slice-delivery-envs-49a251`). Registers as `delivery.environments`. Migration `0006_delivery.sql`.

## What landed

**Environments are first-class objects.** `environments` rows per repository: name, kind (`production | staging | preview | ephemeral`), a target (`local-docker`, `ssh-docker {host, user, port, key_ref}`, or `none`) and a policy (below). Every change emits `io.ore.environment.created|updated|deleted`.

**Deploy requests go through policy.** A deployment is `requested` → `awaiting_approval` (when the policy needs N approvals or a wait timer) → `started` → `succeeded | failed` → optionally `rolled_back` (replaced by a rollback deployment) or `stopped`. `refused` (policy) and `rejected` (an approver) are terminal. Every transition is an `io.ore.deployment.<state>` event whose data carries the deployment, the environment kind and target, and the `policy_decision` (outcome, every rule checked with pass/fail and detail, approvals counted, wait-until) so an auditor can replay it. The decision, the health result, the failure text and the recovery time are also stored on the one-to-one `deployment_rollouts` row, which is what `GET .../deployments/{id}` returns together with the approvals and the TTL.

**The v0 entry points are unchanged for repositories without environment records.** `Deployer.Deploy/Rollback/Stop` first ask the service whether the environment has a record; if it does not, the implicit local-docker path from v0 runs exactly as before (`ore demo`, the agent loop's preview and production deploys, the v0 `POST /deployments` route). When a record exists, the same calls go through policy, so the agent loop at L4 is governed by the production environment's policy with no change to the loop. The small, additive edit to `delivery.go` is a field (`Deployer.Environments`) and a three-line hook at the top of each of the three methods.

**Remote target `ssh-docker`.** The image is built on the Ore host, shipped with `docker save IMAGE | ssh -i KEY -p PORT user@host docker load` (no registry yet), and run with the same `docker run/port/rm/logs` flow against `DOCKER_HOST=ssh://user@host:port`. The docker CLI calls `ssh` from `PATH` and takes no key option, so the service puts a shim first on `PATH` that adds `-i KEY -o BatchMode=yes -o StrictHostKeyChecking=accept-new -p PORT`. Keys come from `key_ref`: `file:/path`, `env:VAR`, or `secret:NAME` through the secrets store another slice registers as `Services["secrets"]` (looked up lazily; an `interface{ Secret(ctx, orgID, name string) (string, error) }`). Deployment URLs use the target host.

**Health-gated rollouts with automatic rollback.** After the container starts, the environment's `health_path` is polled until 2xx/3xx or `health_timeout_seconds`. On failure the container's last 60 log lines are captured, the container is removed, the deployment transitions to `failed` (`io.ore.deployment.failed`), and when `auto_rollback` is on and a previous succeeded deployment exists it is redeployed as a rollback (approvals and wait timer waived, recorded as `bypass` on its decision; allowed refs and the agent rule still apply). The failed deployment then transitions to `rolled_back` with `recovery_seconds` (failure → rollback healthy) on the event and the rollout row. An issue titled `Deployment <id> to <env> failed health check` is opened by the system actor with the cause, the probe result, the rollback outcome and the log excerpt: the signal for the agent loop.

**Preview lifecycle.** Deployments into `preview`/`ephemeral` environments get a TTL (`preview_ttl_seconds`, default 2 h) in `preview_ttls`. A background reaper (every minute; `Service.Tick`) stops expired previews, removes their containers and records `io.ore.deployment.stopped` with `reason: "ttl"`. Implicit `preview` deployments (no record) are reaped after the default 2 h as well. `POST .../deployments/{id}/extend {"hours": N}` or `{"seconds": N}` moves the expiry to now + N, clamped to 24 h from now (`clamped: true` in the response and on `io.ore.deployment.extended`). The reaper also starts deployments whose wait timer elapsed.

**DORA view.** `GET /api/v0/repos/{org}/{name}/dora?days=30` computes from the ledger for production (environment kind `production`, or the implicit `production` name): deployment frequency (total started, succeeded, rollbacks, per day, per week); lead time (change opened → the production success that shipped it: a success ships every change merged since the previous success; median and p90, nearest rank); change failure rate (non-rollback deployments that failed, were rolled back, or have a rollback pointing at them, over all non-rollback deployments); failed-deployment recovery time (the `recovery_seconds` recorded on the rollback, else the time to the next success in the environment; median and p90).

## REST

All under `/api/v0/repos/{org}/{name}`, bearer token (basic auth `user:token` sets the actor id, which is what approver lists match).

| Method and path | Purpose | Codes |
|---|---|---|
| `GET/POST /environments` | list; create `{name, kind?, target?, policy?}` (policy merges over the kind's defaults) | 201, 400 bad kind/field, 409 duplicate |
| `GET/PUT/DELETE /environments/{env}` | read; merge update (no rename); delete (deployments stay on the ledger) | 200, 404 |
| `POST /environments/{env}/deployments {ref}` | request a deployment of a branch, full ref or commit | 202 awaiting approval, 201 ran (status `succeeded` or `failed` in the body), 403 refused by policy (decision in the body, recorded), 409 environment busy (recorded as refused) |
| `GET /environments/{env}/deployments` | deployments of one environment | 200 |
| `POST /environments/{env}/rollback` | redeploy the release before the one in effect | 201, 409 nothing deployed / no previous |
| `POST /environments/{env}/stop {reason?}` | stop the environment's container | 200, 409 nothing deployed |
| `GET /deployments/{id}` | deployment with rollout record, approvals, TTL | 200, 404 |
| `POST /deployments/{id}/approve {comment?}` | approve; runs the deployment when the policy is satisfied | 200 (body shows `awaiting_approval` or the final status), 403 not an approver, 409 not awaiting approval |
| `POST /deployments/{id}/reject {comment?}` | reject | 200, 403, 409 |
| `POST /deployments/{id}/extend {hours|seconds}` | extend a preview TTL (clamped to 24 h) | 200, 409 not a running preview |
| `GET /dora?days=30` | the four keys | 200 |

The v0 `POST /deployments`, `/deployments/rollback` and `/deployments/stop` routes keep working and go through policy when a record exists; being outside this slice's files they answer 201 with `status: awaiting_approval` for parked deployments and 500 with the policy or conflict message for refusals.

## Policy fields

| Field | Meaning | Default (production / staging / preview, ephemeral) |
|---|---|---|
| `allowed_refs` | glob patterns over full ref names (`refs/heads/main`, `refs/tags/v*`; a bare name means `refs/heads/<name>`); empty allows any; a commit no ref points at fails a non-empty list | `[]` |
| `required_approvals` | N distinct approvers before the deployment starts | 0 |
| `approvers` | actor ids allowed to approve or reject; empty means any non-agent actor | `[]` |
| `wait_seconds` | timer between the request and the start | 0 |
| `require_green_pipeline` | refuse a commit without a succeeded pipeline run | false |
| `allow_agent_deploys` | let actors of kind `agent` request deployments | false / true / true |
| `health_path` | path probed after start (must begin with `/`) | `/` |
| `health_timeout_seconds` | probe deadline | 60 |
| `auto_rollback` | redeploy the previous succeeded deployment on health failure | true / true / false |
| `preview_ttl_seconds` | lifetime of preview deployments | 7200 for preview and ephemeral |

The evaluator (`delivery.Evaluate`) is a pure function: hard rules (ref, agent, green pipeline, a rejection) refuse; otherwise outstanding approvals or the wait timer park the deployment; otherwise it starts. Rollbacks pass `bypass`, which waives approvals and the timer only. ADR 0011 replaces this rule table with Cedar later; the `Decision` shape (checks with rule, pass, detail) is what a Cedar evaluator will fill.

## Remote target setup and manual verification

On the target host: install Docker, create a user in the `docker` group, put the deploy public key in its `authorized_keys`. The Ore host needs `docker` and `ssh` on `PATH` and the private key readable by the server process.

```bash
ore api POST /api/v0/repos/acme/app/environments '{"name":"staging","kind":"staging",
  "target":{"kind":"ssh-docker","host":"staging.example.net","user":"ore","port":22,"key_ref":"file:/etc/ore/deploy_key"},
  "policy":{"health_path":"/healthz","health_timeout_seconds":90,"auto_rollback":true}}'
ore api POST /api/v0/repos/acme/app/environments/staging/deployments '{"ref":"main"}'
```

What happens, in order: `docker build -q -t ore/acme-app:<sha12> <tree>` locally; `docker save … | ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -p 22 -i /etc/ore/deploy_key -o IdentitiesOnly=yes ore@staging.example.net docker load`; then with `DOCKER_HOST=ssh://ore@staging.example.net:22`: `docker rm -f ore-acme-app-staging`, `docker run -d --name ore-acme-app-staging --label … -p 0:8080 ore/acme-app:<sha12>`, `docker port`, and the health probe at `http://staging.example.net:<port>/healthz`. The same flow is exercised against a real host by `ORE_TEST_SSH_TARGET=ore@staging.example.net:22 ORE_TEST_SSH_KEY=/etc/ore/deploy_key go test -run TestDockerSSHTargetAgainstRealHost ./internal/delivery/` (skipped otherwise). Without a host, `TestSSHEngineRunsTheSameFlowOverDockerHostSSH` asserts the exact command sequence, the `DOCKER_HOST` value on every remote command, the shim's contents and the save|load pipe against a recorded runner, and the Docker-gated rollout test runs the identical build/run/probe flow on the local engine (`local-docker` fallback).

## Tests

- Unit: policy evaluator (every rule, distinct approvers, rejection, wait timer, bypass), `RefAllowed` globs, `CanApprove`, kind defaults and validation, the state machine (allowed and refused transitions, terminal and in-flight states), the ssh engine command sequence, key reference schemes (`file:`, `env:`, `secret:`, errors), `percentile`.
- Service (SQLite, `none` target or a fake Docker engine whose containers are one HTTP server): approval flow with ledger event sequence, the edge cases below, TTL and reaper, wait timer via `Tick`, implicit previews, environment CRUD events, DORA from a failure-and-rollback scenario.
- httptest end-to-end (`internal/delivery/environments`, server built like `ore serve` with `registry.Apply`): create a production environment requiring one approval → request → `awaiting_approval` (202) → non-approver 403 → approve → `succeeded` → rollback with no previous 409 → second release through the v0 route (hook) → reject → approve after reject 409 → second release approved → rollback 201 → DORA → every event type present with `policy_decision` → preview TTL, clamped extension, stop, delete. A second test keeps the implicit path byte-for-byte: seven deployment events for two deploys and a stop.
- Docker (`ORE_TEST_DOCKER=1`): busybox `httpd` images, one serving `/health`, one not; the bad release fails its probe in 8 s, is rolled back automatically, the rollback serves `/health`, exactly one container holds the environment's name, the failed container is gone, the issue names the deployment and `status 404`, and `recovery_seconds` is recorded.

### Edge cases (named tests)

| Case | Test | Result |
|---|---|---|
| Deploy from a disallowed ref | `TestDeployFromDisallowedRefIsRefusedAndRecorded`, e2e step 2 | refused, `requested,refused` on the ledger with the decision; 403 |
| Agent deploy to an environment that forbids agents | `TestAgentDeployToEnvironmentThatForbidsAgentsIsRefused` (through `Deployer.Deploy`, the loop's entry point) | refused and recorded; the ref is inferred from the refs pointing at the commit |
| Approval by a non-approver | `TestRequestAwaitsApprovalThenSucceeds`, e2e step 3 | `ErrForbidden` / 403, `io.ore.deployment.approval.refused` emitted, nothing counted |
| Double approval by the same user | `TestDoubleApprovalBySameUserCountsOnce` | both rows kept, counted once, stays awaiting until the second approver |
| Approval after the deployment was rejected | `TestApprovalAfterRejectionConflicts`, e2e step 5 | 409 (also for a second reject) |
| Rollback with no previous deployment | `TestRollbackWithoutPreviousDeploymentConflicts`, e2e step 4 | 409 for nothing deployed and for no previous |
| Health timeout with auto-rollback off | `TestHealthTimeoutWithAutoRollbackOffStaysFailedAndOpensIssue` | stays `failed`, container removed, issue opened with the log excerpt and `auto-rollback: off` |
| TTL extension past the maximum | `TestPreviewTTLReaperAndExtension`, e2e step 9 | clamped to 24 h from now, `clamped: true` |
| Reaper runs with no expired previews | `TestPreviewTTLReaperAndExtension` | 0 stopped, no new ledger events |
| Two deployments requested for the same environment concurrently | `TestTwoConcurrentDeploymentsNeverRunTwoContainers` | the second is refused (409) and recorded as `refused`; one `docker run` in total; a request while another awaits approval is refused the same way |

## Deviations and notes

- The ownership map puts this slice's registration in `internal/delivery/`, but `internal/web` imports `internal/delivery`, so a `register.go` there would be an import cycle (`delivery → registry → web → delivery`). Registration and routes live in the new subpackage `internal/delivery/environments` (as `pipelines/actions` does), which required one blank import line in `cmd/ore/main.go`. The review slice has the same cycle and will need the same line.
- `Policy.AllowAgentDeploys` is a plain bool, so an environment created through `Service.CreateEnvironment` directly (not the API) with a zero policy forbids agents; the API merges input over `DefaultPolicy(kind)`.
- A deployment that ran and failed answers 201 on the request route with `status: failed` in the body; the failure detail is in `rollout.failure` and `rollout.health`.

## Deferred

- The reconciler path (rendered-manifest branches and an in-cluster agent; docs/04 §B.3 P2) and Kubernetes targets.
- Feature flags (OpenFeature) and rollouts with stages and metric analysis; today a rollout is one container swap with a health gate.
- OpenBao-compatible secrets (ADR 0012): `secret:` key references resolve through whatever `Services["secrets"]` another slice registers; there is no secrets API in this slice.
- A registry: images reach remote hosts by `docker save | ssh docker load`.
- Cedar (ADR 0011): the built-in evaluator records decisions in the shape Cedar will fill.
- Business-hours windows, required attestations and SBOM deltas in policy (docs/04 §B.7).
- UI: environments, approvals and DORA are API-only; the v0 repository page still lists deployments.
