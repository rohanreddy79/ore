# Review v1: patch sets, interdiffs, comments, structured reviews, stacks, landing strategies

Branch `slice/review-v1` (worktree branch `rohan/slice-review-v1-d74e36`). Registers as `review.v1`. Migration `0007_review.sql`.

Changes now survive rebases, form stacks, and land with one of three strategies, including the Scion-style optimistic landing with automatic revert (ADR 0008, ADR 0014 §3–4, ADR 0015, `research/07` "no merge-queue lock; post-merge testing; automated revert").

## What landed

| Area | Files | Summary |
|---|---|---|
| Data model | `internal/ledger/migrations/0007_review.sql`, `internal/review/model.go`, `internal/review/store.go` | `patch_sets`, `change_comments`, `reviews`, `stacks`, `landings`, `review_policies`; every row carries `org_id`; every write emits its event in the same transaction. |
| Patch sets and interdiffs | `internal/review/patchsets.go`, `internal/review/gitops.go` | A new patch set is recorded whenever a change's source branch moves: through `Open` on an existing branch, through `Merge` seeing a moved head, and lazily for plain pushes (`SyncPatchSets` runs on every review v1 read and before every landing and attributes the patch set to the pusher found in `ref_updates`). Interdiffs are rebase-aware. |
| Comments | `internal/review/comments.go` | Anchored to (patch set, path, line range); re-anchored to the latest patch set through a zero-context diff line map, or marked `outdated`. Lanes `human`, `bot`, `agent`. Late comments on merged changes are allowed. |
| Reviews and policy | `internal/review/reviews.go` | Verdict `comment`, `approve`, `request_changes` with findings in the `spec/safe-outputs` `Finding` shape (validated). Per-repository policy: `required_approvals` (default 0), `allow_agent_approvals` (default false), `default_strategy` (default `queue`). |
| Stacks | `internal/review/stacks.go` | Ordered branches, bottom first; each change targets the branch below. Lands bottom-up with a server-side rebase (`merge-tree --merge-base` + `commit-tree`, no working copy), compare-and-swap branch moves, a new patch set per rebase, and a clean abort on the first conflict. |
| Landing strategies | `internal/review/landing.go` | `Service.Land(ctx, repo, change, LandRequest{Strategy}, actor)`: `queue` (the v0 `Merge`), `land_when_green`, `optimistic` with automatic revert, `io.ore.change.reverted` and a signal issue. |
| HTTP API and wiring | `internal/review/v1/register.go` | Routes below; `s.Services["review.landing"]` and `s.Services["review.v1"]` hold the `*review.Service`; the pipelines runner is wired as the service's `Verifier`. |
| Additive edits to `internal/review/review.go` | | `Service.Verifier` field; patch-set recording in `Open` (both paths) and in `Merge` when the head moved; a test-only hook before the compare-and-swap update so the retry path can be exercised. Exported API unchanged. |
| One line in `cmd/ore/main.go` | | `_ "github.com/orevcs/ore/internal/review/v1"`. See "Why a subpackage" below. |

### Why a subpackage and one import line

`internal/registry` imports `internal/web`, and `internal/web` imports `internal/review` (it holds a `*review.Service`). A `register.go` inside package `review` would therefore be an import cycle, so the extension lives in `internal/review/v1` (package `reviewv1`). Unlike the other slices, no placeholder package for it was blank-imported by `cmd/ore/main.go`, so this slice adds exactly one line to the existing blank-import block. Nothing else in `cmd/` or `internal/web` changed. If a different wiring is preferred, drop that line and import the package from wherever the slices are listed.

## Data model

```
changes (v0) 1 ─── n patch_sets        number, base_oid, head_oid, pushed_by
          1 ─── n change_comments      patch_set, path, start_line, end_line, lane, resolved
          1 ─── n reviews              patch_set, reviewer, lane, verdict, findings (JSON)
          1 ─── n landings             patch_set, strategy, status, merge_oid, revert_oid, verification_run_id, issue_number
repos     1 ─── 1 review_policies      required_approvals, allow_agent_approvals, default_strategy
          1 ─── n stacks               target_ref, change_ids (ordered JSON), state, failed_change_id, error
```

- `change_id` columns reference `changes.id` (the row id), the same way `pipeline_runs.change_id` does. The Gerrit-style change-id string stays `changes.change_id`.
- A **patch set** is the `(base, head)` pair that was reviewable at a point in time. Patch set 1 is recorded when a change opens (and backfilled for changes that predate this slice). Each later head gets the next number with the merge base against the target at that moment, so a rebase is a patch set with a new base.
- **Change states** gain `reverted` (after an optimistic landing was reverted). The next patch set on the branch reopens the change (`io.ore.change.reopened`) and clears `merge_oid`.
- **Lanes** derive from the actor: human → `human`, agent → `agent`, app/system → `bot`. A request may lower its lane (a human token posting for a tool); a non-human actor can never claim the human lane.
- **Events** (all `io.ore.<entity>.<verb>`, emitted with the row): `patchset.recorded`, `comment.posted|resolved|reopened`, `review.submitted`, `repository.review_policy.changed`, `stack.opened|landing|landed|aborted`, `landing.requested|verifying|landed|failed|reverted|revert_failed`, `change.retargeted|rebased|reopened|reverted|revert_failed`. The v0 `change.patchset.pushed` event from `UpdateChangeHead` is still emitted.

## API

All under `/api/v0/repos/{org}/{name}`, bearer token, JSON; errors are `{"error": ...}` with 400 (validation), 404, 409 (policy, conflict, nothing to merge, source gone, not open, verification failed, reverted).

| Method and path | Body / query | Returns |
|---|---|---|
| `GET /review/policy`, `PUT /review/policy` | `{required_approvals, allow_agent_approvals, default_strategy}` | the policy |
| `GET /changes/{n}/patchsets` | | `[{number, base_oid, head_oid, pushed_by, created_at}]` |
| `GET /changes/{n}/interdiff?from=1&to=2` | `format=diff` (raw), `format=range-diff` (git range-diff text) | `{from, to, identical, rebased, diff, range_diff?, note?}` |
| `GET /changes/{n}/comments`, `POST /changes/{n}/comments` | `{patch_set?, path?, start_line?, end_line?, body, lane?}` | comments with `anchor` (position on the latest patch set) and `outdated` |
| `POST /changes/{n}/comments/{id}/resolve` | `{resolved?: bool}` | the comment |
| `GET /changes/{n}/reviews`, `POST /changes/{n}/reviews` | `{verdict, findings[], body?, lane?, patch_set?}` | reviews with `lane` and `counts` |
| `GET /changes/{n}/approvals` | | `{patch_set, required, approvals, approvers, ignored, changes_requested_by, landable, reason}` |
| `POST /changes/{n}/land` | `{strategy?}` | 200 `{landing, change}` when it stuck; 409 `{error, landing, change}` otherwise (the landing record says why) |
| `GET /changes/{n}/landings` | | the attempts |
| `GET /stacks`, `POST /stacks` | `{branches: [bottom, ..., top], target_ref?}` | the stack with its changes |
| `GET /stacks/{id}` | | the stack, `state` ∈ open, landing, landed, aborted, plus `failed_change_number` and `error` |
| `POST /stacks/{id}/land` | `{strategy?}` | 200 `{stack, landings}`; 409 `{error, stack, landings}` on abort |

## Approval policy

Evaluated on the **latest patch set only**, using each reviewer's latest non-comment verdict there:

- `required_approvals` (default 0, so `ore demo` and the v0 agent loop are unchanged) counting approvals from the human lane; agent and bot approvals are listed under `ignored` unless `allow_agent_approvals` is on.
- A `request_changes` from a counting lane blocks landing regardless of the count, until that reviewer's later verdict supersedes it.
- A push creates a new patch set, so all approvals reset. `POST /land` then answers 409 with the approval status.

`Merge` (the v0 primitive) is unchanged and does not consult the policy; `Land` does, for every strategy. The agent loop still calls `Merge` directly at L3+; switching it to `Land` through `s.Services["review.landing"]` is the follow-up noted below.

## Landing strategies and when to use which

| Strategy | What happens | Use when |
|---|---|---|
| `queue` (default) | Policy check, then the v0 server-side merge: `merge-tree` against the current tip, a merge commit, compare-and-swap `update-ref`, retried up to three times if the tip moved. No verification by Ore. | L2–L3 repositories whose pipeline already ran on the head and whose humans decide; the conventional merge queue (ADR 0008). |
| `land_when_green` | Policy check, run the pipeline on the **head**, merge only if the run succeeded and the branch did not move during the run. | L3: "merge when verification is green". Cheap and conservative; the merge result itself is not tested. |
| `optimistic` | Policy check, merge immediately, run the pipeline on the **merge commit**, and if it fails: create a revert commit server-side (three-way merge with the merge as base, so later commits on the target survive), move the target with compare-and-swap, mark the change `reverted`, emit `io.ore.change.reverted`, and open the issue "Change #n reverted: verification failed" whose body carries the run id and the last 40 log lines as the fix signal. | L4–L5: the Scion model, no merge lock, verification after merge, agents fix what breaks. Needs a pipeline that is a trustworthy oracle. |

Verification and revert in the optimistic path run detached from the request's cancellation, so a client that disconnects cannot cause a spurious revert. Both verifying strategies fail closed (no merge) when no verifier is configured.

After a revert, the fix must be **rebased onto the target** (or revert the revert): the original commits are already ancestors of the target through the reverted merge, so a fix stacked on top of them would merge as "no change" for everything the revert undid. The issue body says so.

## Stacks

`POST /stacks {branches: [b1, b2, b3]}` opens or adopts one change per branch (`b1 → main`, `b2 → b1`, `b3 → b2`); branches must be linear (each contains the previous). `POST /stacks/{id}/land` walks bottom-up:

1. change 1 lands with the requested strategy;
2. for every change above it: the commits not yet upstream by patch id (`rev-list --cherry-pick --right-only`) are replayed onto the target's new tip with `merge-tree --merge-base=<parent>` and `commit-tree` (author and date preserved, committer Ore); the branch moves to the rebased tip with compare-and-swap; the change is retargeted to the stack's target; a new patch set is recorded (`io.ore.change.rebased`); then it lands;
3. the first conflict or refusal aborts: lower changes stay merged, the failing and upper changes stay open, and the stack records `state: aborted`, `failed_change_number` and the error (`io.ore.stack.aborted`). Landing the stack again after the author restacks resumes from the first unmerged change.

## Edge cases (each is a named test)

| Case | Test |
|---|---|
| A push after approval invalidates approvals (new patch set, approvals reset per policy) | `TestPushAfterApprovalResetsApprovals`, HTTP: `TestHTTPReviewsPolicyAndApprovals` |
| Interdiff between identical patch sets is empty | `TestInterdiffIdenticalPatchSetsIsEmpty` |
| Interdiff across a rebase shows only the patch's own change | `TestInterdiffRebasedShowsOnlyThePatchDelta` |
| A comment on a line deleted in the next patch set becomes outdated; a surviving line is re-anchored | `TestCommentOnDeletedLineBecomesOutdated`, `TestCommentReanchoredWhenLinesStillExist`, `TestLineMapper` |
| A stack with a conflicting middle change aborts, lower changes merged, upper changes open with a clear status; relanding resumes | `TestStackConflictingMiddleChangeAborts`, HTTP: `TestHTTPStacks` |
| Two optimistic landings racing on the same target: the second recomputes onto the first after the compare-and-swap fails once | `TestOptimisticLandingsRaceCASRetry` |
| Optimistic landing whose verification passes: no revert, no issue | `TestOptimisticLandingGreenNoRevert` |
| Optimistic landing whose verification fails: revert exists, target points at it, change `reverted`, issue opened with the log excerpt, event emitted; the rebased fix reopens and lands | `TestOptimisticLandingRedRevertsAndOpensIssue`, HTTP: `TestHTTPLandingStrategiesEndToEnd` |
| Landing a change whose source branch was deleted: 409, no landing recorded, reads still work | `TestLandDeletedSourceBranch`, HTTP 409 in `TestHTTPLandingStrategiesEndToEnd` |
| An agent (or bot) approval does not count unless the repository allows it; an agent cannot post in the human lane | `TestAgentApprovalDoesNotCount` |
| A merged change receiving a late comment: allowed, lane recorded | `TestLateCommentOnMergedChange` |
| `land_when_green` refuses a red head and lands a green one; refuses a head that moved during verification | `TestLandWhenGreenMergesOnlyWhenGreen`, `TestLandWhenGreenRefusesAHeadThatMovedDuringVerification` |
| Verifying strategies fail closed without a verifier; `queue` still works | `TestVerifyingStrategiesFailClosedWithoutVerifier` |
| `request_changes` blocks until the same reviewer's later verdict supersedes it; the agent lane never blocks | `TestRequestChangesBlocksUntilSuperseded` |
| Patch sets are detected for plain pushes, attributed to the pusher, not duplicated by reopening, and get a new base on rebase | `TestPatchSetDetection` |
| Stack ordering, adoption of existing changes, rejection of non-linear or unknown branches | `TestStackOrdering` |
| Server-side rebase preserves author/message, skips patches already upstream, stops at the conflicting commit | `TestRebaseCommitsReplaysOntoNewTip` |
| All three strategies end to end over HTTP with the host executor | `TestHTTPLandingStrategiesEndToEnd` |

Run: `CGO_ENABLED=0 go test ./internal/review/...`; the gate is `scripts/e2e.sh`.

## Deferred

- **The agent loop still lands with `Merge`.** Switching it to `Land` with the repository's default strategy (optimistic at L4+) is one call through `s.Services["review.landing"]`; it was left out because `internal/agent` belongs to no slice in the ownership map.
- **UI.** Patch sets, interdiffs, comment lanes and stacks are API-only; `internal/web/templates.go` was not touched.
- **Patch sets on push are recorded lazily** (on the next review v1 read or landing), not from a receive-pack hook; the `ref_updates` ledger gives the pusher. A ledger tailer that records them eagerly is a small follow-up.
- **Re-anchoring uses the per-path zero-context diff**, not `git blame`; renames mark comments outdated. Persisting anchors (rather than computing them on read) can come with the UI.
- **Interdiff fallback.** When the old patch set does not replay cleanly onto the new base, the interdiff is a diff of the two diffs with a `note`.
- **`land_when_green` verifies the head, not a speculative merge** of head onto the current tip, as specified; testing the merge result is `optimistic` without the optimism and is a strategy worth adding.
- **Stacks replay non-merge commits only**, as `git rebase` does: a merge of the target into a stacked branch is dropped and its upstream commits are excluded by patch id; a stacked branch whose own work lives in merge commits needs `--rebase-merges` semantics that are not implemented. After a conflict the author restacks and lands again.
- **No `refs/meta/changes/<id>` mirroring** of review metadata yet (ADR 0008).
- **Bisect on batched optimistic landings** (which change broke the target when several landed between runs) is out of scope; today each landing verifies its own merge commit.
