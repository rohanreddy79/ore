// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/review"
)

// applyPush mirrors after a push delivery. The fetch is the source of truth
// (the payload's before/after are informational), so a delivery that arrives
// late or out of order still converges, deletions and force pushes included.
// io.ore.github.push.mirrored summarizes the delivery and what was applied.
func (svc *Service) applyPush(ctx context.Context, link Link, repo ledger.Repo, ev PushEvent, actor ledger.Actor, delivery string) (MirrorResult, error) {
	res, err := svc.mirror(ctx, link, repo, actor, nil)
	if err != nil {
		return res, err
	}
	forced := ev.Forced
	for _, u := range res.Updates {
		if u.Ref == ev.Ref && u.Forced {
			forced = true
		}
	}
	data := map[string]any{"ref": ev.Ref, "before": ev.Before, "after": ev.After, "created": ev.Created, "deleted": ev.Deleted, "forced": forced,
		"pusher": ev.Pusher, "delivery": delivery, "updates": res.Updates}
	if err := svc.store.emit(ctx, repo, "io.ore.github.push.mirrored", repo.Org+"/"+repo.Name+"/"+ev.Ref, actor, data); err != nil {
		return res, err
	}
	return res, nil
}

// applyIssue mirrors an issues delivery into Ore's issues with the GitHub
// number preserved when free. Replays of "opened" become edits.
func (svc *Service) applyIssue(ctx context.Context, repo ledger.Repo, ev IssueEvent, actor ledger.Actor) (map[string]any, error) {
	m, ok, err := svc.store.issueMapping(ctx, repo.ID, ev.Number)
	if err != nil {
		return nil, err
	}
	if !ok {
		if ev.Action == "closed" || ev.Action == "edited" || ev.Action == "reopened" {
			// First time Ore hears of this issue: mirror it in its current state.
			ev.State = map[bool]string{true: "closed", false: "open"}[ev.Action == "closed"]
		}
		issue, err := svc.store.createIssue(ctx, repo, ev, actor)
		if err != nil {
			return nil, err
		}
		return map[string]any{"issue": issue.Number, "github": ev.Number, "created": true}, nil
	}
	issue, err := svc.DB.Issue(ctx, repo, m.IssueNumber)
	if err != nil {
		return nil, err
	}
	changed, err := svc.store.updateIssue(ctx, repo, issue, ev, actor)
	if err != nil {
		return nil, err
	}
	return map[string]any{"issue": issue.Number, "github": ev.Number, "changed": changed}, nil
}

// applyPullRequest mirrors a pull_request delivery into a change whose source
// is the tracking ref refs/github/pull/N/head. A PR already mirrored only
// gets its head (or title, body, state) updated: never a second change.
func (svc *Service) applyPullRequest(ctx context.Context, link Link, repo ledger.Repo, ev PullRequestEvent, actor ledger.Actor) (map[string]any, error) {
	res, err := svc.mirror(ctx, link, repo, actor, []int{ev.Number})
	if err != nil {
		return nil, err
	}
	rp := svc.repoPath(repo)
	src := fmt.Sprintf("%spull/%d/head", TrackingPrefix, ev.Number)
	head, ok := res.PullHeads[ev.Number]
	if !ok {
		if ev.HeadSHA == "" {
			return nil, fmt.Errorf("pull request #%d: head not available from GitHub", ev.Number)
		}
		head = ev.HeadSHA
	}
	m, mapped, err := svc.store.prMapping(ctx, repo.ID, ev.Number)
	if err != nil {
		return nil, err
	}
	if !mapped {
		if ev.Action == "closed" {
			return map[string]any{"github": ev.Number, "ignored": "closed before it was mirrored"}, nil
		}
		target := "refs/heads/" + ev.BaseRef
		targetOID, err := gitx.RevParse(ctx, rp, target)
		if err != nil {
			return nil, fmt.Errorf("pull request #%d: base %s: %w", ev.Number, ev.BaseRef, err)
		}
		if _, err := gitx.RevParse(ctx, rp, src); err != nil {
			return nil, fmt.Errorf("pull request #%d: head %s: %w", ev.Number, head, err)
		}
		base, err := gitx.MergeBase(ctx, rp, targetOID, head)
		if err != nil {
			return nil, err
		}
		if base == head {
			return map[string]any{"github": ev.Number, "ignored": review.ErrNothingToMerge.Error()}, nil
		}
		prov, _ := json.Marshal(map[string]any{"source": "github", "pr": ev.Number})
		title := ev.Title
		if title == "" {
			title = fmt.Sprintf("Pull request #%d", ev.Number)
		}
		c := ledger.Change{ChangeID: review.NewChangeID(), Title: title, Body: ev.Body, SourceRef: src, TargetRef: target, BaseOID: base, HeadOID: head, Provenance: string(prov)}
		if err := svc.store.createChange(ctx, repo, &c, ev, actor); err != nil {
			return nil, err
		}
		return map[string]any{"change": c.Number, "github": ev.Number, "created": true, "head": head}, nil
	}
	ch, err := svc.DB.Change(ctx, repo, m.ChangeNumber)
	if err != nil {
		return nil, err
	}
	detail := map[string]any{"change": ch.Number, "github": ev.Number}
	if ch.State == "open" && ch.HeadOID != head {
		if err := svc.DB.UpdateChangeHead(ctx, repo, ch, head, actor); err != nil {
			return nil, err
		}
		ch.HeadOID = head
		detail["head"] = head
	}
	if m.HeadSHA != head {
		_ = svc.store.setPRHead(ctx, repo.ID, ev.Number, head)
	}
	switch ev.Action {
	case "closed":
		if ch.State != "open" {
			detail["unchanged"] = ch.State
			return detail, nil
		}
		if ev.Merged {
			if err := svc.store.markMergedOnGitHub(ctx, repo, ch, ev.MergeCommitSHA, ev, actor); err != nil {
				return nil, err
			}
			detail["merged"] = ev.MergeCommitSHA
		} else {
			if err := svc.store.closePR(ctx, repo, ch, ev, actor); err != nil {
				return nil, err
			}
			detail["closed"] = true
		}
	default:
		changed, err := svc.store.updateChangeMeta(ctx, repo, ch, ev, actor)
		if err != nil {
			return nil, err
		}
		detail["updated"] = changed
	}
	return detail, nil
}
