# Agent fleet: roles, adversarial review, budgets, watchers, evidence-based autonomy levels, landing by level

Wave 2, built on the review session's branch (`rohan/slice-review-v1-d74e36`) after merging `main` at 39cabc7. Registers as `agent.fleet`. Migration `0008_fleet.sql`. Owns `internal/agent/fleet/` and the new files `internal/agent/governor.go` and `internal/agent/fleetloop.go`; `internal/agent/agent.go` received additive edits only (exported API unchanged).

The loop (docs/12 §1) is now governed: budgets and the kill switch gate every step, an adversarial reviewer runs between verification and landing, landing follows the autonomy level through review v1's strategies, deployments follow the delivery environments' policy, watchers turn failure signals into implementer runs with loop prevention, and the level itself moves on evidence (ADR 0009, ADR 0015, docs/12 §2-5).

## What landed

| Area | Files | Summary |
|---|---|---|
| Governor seam | `internal/agent/governor.go` | `agent.Governor` (Admit, Gate, Role, Review), `RoleSpec`, `ReviewOutcome`, `agent.ErrRefused`. `Loop.Governor` is nil for the ungoverned v0 loop. |
| Landing by level | `internal/agent/fleetloop.go`, additive edits in `agent.go` | `StrategyForLevel`: L3 lands with `land_when_green`, L4+ with `optimistic` (post-merge verification, automatic revert, the revert issue as fix signal) through `review.Service.Land`, the service published as `s.Services["review.landing"]`. L2 opens the change and stops as before. The head verification the loop already ran is reused by `land_when_green` (`LandRequest.VerificationRunID`, an additive field in review v1), so a head is never verified twice. |
| Deploy policy | `fleetloop.go` (`deployOutcome`) | `Deployer.Deploy` already routes through the environment's policy when a record exists; the loop now ends `blocked` (not `failed`) when the policy refuses the agent or parks the deployment awaiting approval, with the decision text. Repositories without environment records keep the implicit local path (`ore demo` unchanged). |
| Roles | `fleet/roles.go`, `fleet_roles` | `implementer`, `reviewer`, `watcher`, `dependency-updater`: adapter command, adapter name (provenance label), prompt profile, budgets, enabled. Defaults: the implementer is the server's adapter; the reviewer is off until a command is configured; the watcher is on; the dependency updater is a configured role with no worker yet (deferred). The loop takes the implementer's adapter, name and prompt profile from the role. |
| Adversarial reviewer | `fleet/reviewer.go` | At L3+ after verification: a checkout of the branch, `$ORE_DIFF_FILE`, `$ORE_PLAN`, `$ORE_PROMPT`, and `$ORE_FINDINGS_FILE` to write findings in the `spec/safe-outputs` `Finding` shape. Findings are posted as a review in the agent lane through review v1 (`request_changes` when blocking, else `approve`, which never counts toward required approvals unless the repository allows agent approvals). A critical or high finding with confidence ≥ 0.7 blocks landing; the loop sends the findings back to the implementer (`$ORE_FINDINGS`, `$ORE_RETRY=1`) in the same workspace, pushes the fix as a new patch set, verifies and reviews again, then stops `blocked` if still blocked. Malformed or missing findings fail the review (`io.ore.agent.review.failed`) and block landing without a retry. `fleet.ScriptReviewer` is the deterministic reviewer for tests and demos (added lines containing `FIXME` are high findings). |
| Budgets | `fleet/budgets.go`, `fleet_budgets` | Per repository (principal `""`) and per agent principal (the adapter name; the identity slice's `agents.budgets` JSON is honoured when the fleet has no row): `ci_minutes_per_day`, `runs_per_day`, `open_changes`, `writes_per_hour`, 0 = unlimited. Usage is computed from the ledger (agent runs, open agent changes by provenance, `io.ore.pipeline.run.finished` durations by agent actors, agent write events in the last hour). Checked at admission and before every step for the budgets that step spends; a breach ends the run `blocked` with `io.ore.agent.budget.exceeded`. |
| Watchers | `fleet/watcher.go`, `fleet_signals`, `fleet_cursors` | A background worker tails the ledger: `io.ore.change.reverted` (its issue), the failure issues delivery opens (`Deployment … failed health check`), red pipelines on the default branch (it opens `Pipeline red on <branch>: …` unless a human requested the run), and `deployment.failed|rolled_back`, `budget.exceeded`, `security.*` for evidence. It starts an implementer run on the issue when the level is ≥ 3, the fleet is not paused and budgets allow. Every decision is a Signal (`claimed` → `started|skipped` with the reason), claimed through `UNIQUE(repo_id, event_seq)` so two watchers never start two runs. |
| Evidence and levels | `fleet/evidence.go`, `fleet_settings`, `fleet_change_flags` | Per repository over a window (default 30 days, restarted at the last automatic level change): agent change acceptance rate, post-merge fix rate, production rollback rate and recovery time, budget overruns, security alerts, flags. `Recommend` is pure; `Evaluate` records `io.ore.fleet.evidence.evaluated` and applies the recommendation when `auto_demote` (default on) or `auto_promote` (default off) says so, through `ledger.SetAutonomyLevel` with the evidence attached to the reason. `POST …/changes/{n}/flag` marks "should not have landed" and demotes at L3+. |
| Kill switch | `fleet.go`, `fleet_instance`, `fleet_settings.paused` | `POST /api/v0/fleet/pause|resume` (instance) and `POST …/fleet/pause|resume` (repository). Paused: new runs are refused at admission (`agent.ErrRefused`), in-flight runs stop at their next step as `blocked`, the watcher skips. |
| Wiring | `fleet/register.go`, one line in `cmd/ore/main.go` | Registers `agent.fleet`; sets `s.Loop.Governor`; publishes `s.Services["agent.fleet"]`; starts the watcher. The blank import `_ "github.com/orevcs/ore/internal/agent/fleet"` had to go into `main.go`'s slice block because `internal/agent` cannot import `web`: see "One line in main.go". |

### One line in main.go

`internal/registry` imports `internal/web`, which imports `internal/agent`, so a `register.go` in package `agent` would be an import cycle; the extension lives in `internal/agent/fleet` and nothing imported it. The ops slice owns `main.go` in wave 2; this slice adds exactly one blank-import line in the existing slice block (alphabetically first). If that conflicts, drop it on either side and re-add once.

## Role and budget model

```
fleet_roles      (repo, name) → adapter_cmd, adapter_name, prompt_profile, budgets JSON, enabled
fleet_budgets    (repo, principal) → ci_minutes_per_day, runs_per_day, open_changes, writes_per_hour   principal "" = the repository
fleet_settings   repo → paused, auto_promote (off), auto_demote (on), watcher_retries (1), watcher_window_minutes (60), evidence_window_days (30), thresholds JSON, last_level_change_at
fleet_instance   the instance kill switch
fleet_signals    one row per handled ledger event: watcher, issue, cause run, run started, depth, decision, reason
fleet_change_flags  one per flagged change
fleet_cursors    one ledger cursor per watcher id
```

- A **principal** is the agent actor id the loop acts as (`run.Adapter`, the implementer role's `adapter_name`). Setting a role's budgets stores them as that principal's budget.
- **Budget checks by step**: admission and `plan` check `runs_per_day`; `change`, `land`, `deploy`, `observe` check `writes_per_hour`; `verify` checks `open_changes`, `ci_minutes_per_day` and `writes_per_hour`; `review` and `land` check `ci_minutes_per_day`.
- **Loop prevention**: a signal caused by a run the watcher itself started within `watcher_window_minutes` is a retry; the chain depth of the new run is the cause's depth + 1 and it is refused when depth > `watcher_retries`. With the default of 1: a human-caused failure gets one watcher run, that run's failure gets one retry, the retry's failure is refused. `watcher_retries: 0` still answers human-caused failures once.
- **Red main**: only pipeline runs the system or an agent triggered become issues (a human who ran the pipeline is watching); `ore demo`'s intentionally red step 3 is human-requested and leaves no issue.

## Evidence formulas and thresholds

| Measure | Formula (window) |
|---|---|
| acceptance_rate | merged agent changes (including later reverted) / decided agent changes (merged + closed) |
| post_merge_fix_rate | (reverted + flagged) / merged agent changes |
| rollback_rate | production deployments that failed or were rolled back / non-rollback production deployments that ran (`production` and every environment of kind `production`) |
| recovery_median_seconds | median time from a failed production deployment to the next succeeded one |
| budget_overruns | `io.ore.agent.budget.exceeded` events for the repository |
| security_alerts | `io.ore.security.*` events for the repository (none are emitted yet) |

Defaults (`fleet_settings.thresholds`, zero means default): promote when decided ≥ 10, acceptance ≥ 0.8, fix rate ≤ 0.1, rollback rate ≤ 0.05, recovery median ≤ 3600 s, no overruns, alerts or flags, and never past L4 (`max_auto_promote_level`; L5 is a human's decision). Demote one level (floor L2) when rollback rate > 0.2 over ≥ 2 deployments, or fix rate > 0.3 over ≥ 3 merges, or overruns ≥ 3, or any security alert, or any flag. Demotion signals win over promotion evidence. The window restarts at the last automatic change so the same evidence never moves the level twice. Every change is `io.ore.repository.autonomy.changed` (via `ledger.SetAutonomyLevel`) whose `reason` carries the rule and the evidence JSON; `io.ore.fleet.evidence.evaluated` carries the full evidence and recommendation for every evaluation.

## API

Instance: `GET /api/v0/fleet`, `POST /api/v0/fleet/pause {reason}`, `POST /api/v0/fleet/resume`.

Repository, under `/api/v0/repos/{org}/{name}/fleet`: `GET` (status), `POST /pause`, `POST /resume`, `GET|PUT /settings`, `GET /roles`, `GET|PUT /roles/{role}`, `GET|PUT /budgets {principal, budget}`, `GET /evidence`, `POST /evidence/apply`, `GET /signals`, `POST /changes/{n}/flag {reason}` (also mounted at `…/changes/{n}/flag` as the assignment named it). Errors: 400 validation, 404, 409 paused, duplicate flag or refused run.

A refused run through the v0 `POST …/agent/runs` route answers 500 with the fleet's message (`agent run refused: fleet paused: …`), because that route's error mapping is in `web.go`, which this slice does not edit; the UI slice may map `agent.ErrRefused` to 409.

## Edge cases (each is a named test in `internal/agent/fleet`)

| Case | Test |
|---|---|
| L3 lands through `land_when_green`, reusing the head's verification run; L4 lands optimistically and verifies the merge; L2 opens and stops | `TestLevel3LandsWhenGreenThroughTheLoop`, `TestLevel4LandsOptimisticallyGreen`, `TestLevel2OpensTheChangeAndStops` |
| L4 with a failing post-merge verification: revert, fix signal issue, watcher retry succeeds and closes it | `TestLevel4FailingPostMergeVerificationRevertsAndWatcherRetrySucceeds` |
| Adversarial reviewer blocks, the retry fixes it; a second block stops the run | `TestAdversarialReviewerBlocksThenTheRetryFixesIt` |
| Reviewer adapter returning malformed findings (or none, or findings outside the spec): failed review, landing blocked, nothing posted | `TestReviewerReturningMalformedFindingsBlocksLanding` |
| Agent approval never counts toward required approvals | `TestAgentApprovalNeverCountsTowardRequiredApprovals` (and review v1's `TestAgentApprovalDoesNotCount`) |
| Environment that forbids agents: deploy refused and recorded, run blocked after the change landed; the implicit environment still deploys | `TestEnvironmentThatForbidsAgentsBlocksTheRun` |
| Budget exhausted mid-run (open_changes at verify), run budget refused at admission, per-principal budgets | `TestBudgetExhaustedMidRun`, `TestExceededRules`, `TestUsageComesFromTheLedger` |
| Paused repository refuses runs; in-flight runs stop at the next step; the instance switch | `TestPausedRepositoryRefusesRunsAndStopsInFlightRuns` |
| Watcher loop prevention: a failing retry does not trigger infinite runs; `watcher_retries: 0` | `TestWatcherLoopPreventionStopsAFailingRetry` |
| Two watchers racing on one signal: one run; no action at L2, when paused, or on human-opened issues | `TestTwoWatchersRacingOnOneSignalStartOneRun` |
| Red pipeline on main opens an issue and a run; human-requested runs are not signals | `TestRedPipelineOnMainOpensAnIssueAndStartsARun` |
| Automatic demotion on rollback rate, with the evidence on the event; no second demotion for the same evidence; `auto_demote` off recommends only | `TestAutomaticDemotionOnRollbackRateWithEvidenceOnTheEvent` |
| Promotion recommended but not applied when automatic promotion is off; a human applies it | `TestPromotionRecommendedButNotAppliedWhenAutomaticPromotionIsOff` |
| Flagging a change demotes with evidence; agents cannot flag; a second flag is a 409; L2 flags do not demote | `TestFlaggingAChangeDemotesWithEvidence` |
| Recommender math (every rule, floors and caps, threshold defaults) | `TestRecommenderMath`, `TestEvidenceFromTheLedger` |
| Roles: defaults, overrides applied to the run (adapter, provenance, prompt profile), role budgets | `TestRolesDefaultsAndOverrides` |
| Whole surface over HTTP with the host executor: status, roles, budgets, an L3 run with the reviewer and retry, evidence, pause/resume, settings, apply, flag, signals | `TestHTTPFleetEndToEnd`, `TestExtensionRegistersAndGovernsTheLoop` |

Run: `CGO_ENABLED=0 go test ./internal/agent/...`; the gate is `scripts/e2e.sh`.

## Deferred

- **Dependency updater**: the role exists; no worker opens dependency changes yet.
- **Agent principals from identity**: budgets are keyed by the adapter name the loop acts as; the identity slice's `agents` table is read for defaults but principals are not yet resolved to tokens.
- **Security alerts**: counted from `io.ore.security.*` events, which no slice emits yet.
- **Watcher placement**: one watcher per server process; several replicas work (claims are unique) but each keeps its own cursor and re-reads history on first start.
- **Refused runs answer 500 through the v0 route** until `web.go` maps `agent.ErrRefused`.
- **Per-step budgets for CI minutes count finished runs only**; a run in progress is not charged until it finishes.
- **Evidence for promotion to L5** is intentionally not automated.
