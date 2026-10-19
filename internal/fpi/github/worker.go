// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"errors"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

// SyncResult is what a reconciliation pass found for one link.
type SyncResult struct {
	Link    string      `json:"link"`
	Status  string      `json:"status"` // ok | deferred | failed | skipped
	Updates []RefChange `json:"updates"`
	Pulls   []int       `json:"pulls_updated,omitempty"`
	Error   string      `json:"error,omitempty"`
	NextAt  *time.Time  `json:"next_at,omitempty"`
}

var syncActor = ledger.Actor{Kind: "app", ID: "github"}

// SyncLink reconciles one link: checks the repository on the API (which
// also surfaces rate limiting), re-fetches branches, tags and open pull
// request heads, records whatever moved without a webhook, and updates the
// link's sync state. A 429 defers the link until Retry-After and records
// io.ore.github.sync.deferred.
func (svc *Service) SyncLink(ctx context.Context, link Link) (SyncResult, error) {
	res := SyncResult{Link: link.FullName()}
	if svc.Client == nil {
		return res, ErrNoToken
	}
	repo, err := svc.DB.RepoByID(ctx, link.RepoID)
	if err != nil {
		return res, err
	}
	m := svc.lock(repo.ID)
	m.Lock()
	defer m.Unlock()
	now := svc.now()
	subject := repo.Org + "/" + repo.Name + "/github"
	fail := func(err error) (SyncResult, error) {
		var rl *RateLimitError
		if errors.As(err, &rl) {
			next := now.Add(rl.RetryAfter)
			res.Status, res.Error, res.NextAt = "deferred", err.Error(), &next
			_ = svc.store.updateSync(ctx, link, "deferred", err.Error(), now, &next)
			_ = svc.store.emit(ctx, repo, "io.ore.github.sync.deferred", subject, syncActor,
				map[string]any{"what": "sync", "retry_after_seconds": int(rl.RetryAfter.Seconds()), "next_sync_at": next.UTC().Format("2006-01-02T15:04:05Z07:00"), "error": err.Error()})
			return res, err
		}
		res.Status, res.Error = "failed", err.Error()
		_ = svc.store.updateSync(ctx, link, "failed", err.Error(), now, nil)
		_ = svc.store.emit(ctx, repo, "io.ore.github.sync.failed", subject, syncActor, map[string]any{"error": err.Error()})
		return res, err
	}
	gh, err := svc.Client.Repo(ctx, link.Owner, link.Name)
	if err != nil {
		return fail(err)
	}
	if gh.CloneURL != "" && gh.CloneURL != link.CloneURL {
		link.CloneURL = gh.CloneURL
		_, _ = svc.DB.Exec(ctx, `UPDATE github_links SET clone_url = ?, default_branch = ? WHERE id = ?`, gh.CloneURL, gh.DefaultBranch, link.ID)
	}
	open, err := svc.store.openPRs(ctx, repo.ID)
	if err != nil {
		return fail(err)
	}
	var pulls []int
	for _, p := range open {
		pulls = append(pulls, p.GitHubNumber)
	}
	mr, err := svc.mirror(ctx, link, repo, syncActor, pulls)
	if err != nil {
		return fail(err)
	}
	res.Updates = mr.Updates
	for _, p := range open {
		head, ok := mr.PullHeads[p.GitHubNumber]
		if !ok || head == p.HeadSHA {
			continue
		}
		ch, err := svc.DB.Change(ctx, repo, p.ChangeNumber)
		if err != nil || ch.State != "open" {
			continue
		}
		if ch.HeadOID != head {
			if err := svc.DB.UpdateChangeHead(ctx, repo, ch, head, syncActor); err != nil {
				return fail(err)
			}
		}
		_ = svc.store.setPRHead(ctx, repo.ID, p.GitHubNumber, head)
		res.Pulls = append(res.Pulls, p.GitHubNumber)
	}
	res.Status = "ok"
	if err := svc.store.updateSync(ctx, link, "ok", "", now, nil); err != nil {
		return res, err
	}
	if len(res.Updates) > 0 || len(res.Pulls) > 0 {
		_ = svc.store.emit(ctx, repo, "io.ore.github.sync.completed", subject, syncActor, map[string]any{"updates": res.Updates, "pulls_updated": res.Pulls, "tracking": mr.Tracking})
	}
	return res, nil
}

// SyncAll reconciles every link whose deferral (if any) has expired.
func (svc *Service) SyncAll(ctx context.Context) ([]SyncResult, error) {
	links, err := svc.store.allLinks(ctx)
	if err != nil {
		return nil, err
	}
	var out []SyncResult
	for _, l := range links {
		if l.NextSyncAt != nil {
			if t, err := time.Parse("2006-01-02T15:04:05.000Z07:00", *l.NextSyncAt); err == nil && svc.now().Before(t) {
				out = append(out, SyncResult{Link: l.FullName(), Status: "skipped", NextAt: &t})
				continue
			}
		}
		res, err := svc.SyncLink(ctx, l)
		if err != nil {
			svc.log().Warn("github: sync", "link", l.FullName(), "status", res.Status, "err", err)
		}
		out = append(out, res)
	}
	return out, nil
}

// Run drives reconciliation and status reporting until ctx ends.
func (svc *Service) Run(ctx context.Context) {
	var syncC, statusC <-chan time.Time
	if svc.SyncInterval > 0 {
		t := time.NewTicker(svc.SyncInterval)
		defer t.Stop()
		syncC = t.C
	}
	if svc.StatusInterval > 0 {
		t := time.NewTicker(svc.StatusInterval)
		defer t.Stop()
		statusC = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-syncC:
			if _, err := svc.SyncAll(ctx); err != nil {
				svc.log().Error("github: sync all", "err", err)
			}
		case <-statusC:
			if _, err := svc.PostStatuses(ctx); err != nil {
				svc.log().Warn("github: post statuses", "err", err)
			}
		}
	}
}
