// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/orevcs/ore/internal/fpi"
	"github.com/orevcs/ore/internal/ledger"
)

const statusCursor = "statuses"

// PostStatuses tails the ledger's pipeline events from the reporter's cursor
// and posts a commit status for every run on a mirrored commit of a linked
// repository: pending when the run starts, success / failure / error when
// it finishes, each with the Ore run URL. A rate-limit response stops the
// pass without advancing the cursor and defers the next one. Returns the
// number of statuses posted.
func (svc *Service) PostStatuses(ctx context.Context) (int, error) {
	if svc.Client == nil {
		return 0, nil
	}
	svc.mu.Lock()
	deferUntil := svc.statusDeferUntil
	svc.mu.Unlock()
	if svc.now().Before(deferUntil) {
		return 0, nil
	}
	cursor, _, err := svc.store.cursor(ctx, statusCursor)
	if err != nil {
		return 0, err
	}
	evs, err := svc.DB.Events(ctx, ledger.EventsOptions{AfterSeq: cursor, Type: "io.ore.pipeline.run.", Limit: 500})
	if err != nil || len(evs) == 0 {
		return 0, err
	}
	links, err := svc.store.allLinks(ctx)
	if err != nil {
		return 0, err
	}
	byRepo := map[string]Link{}
	for _, l := range links {
		byRepo[l.RepoID] = l
	}
	type pending struct {
		run  ledger.PipelineRun
		link Link
	}
	var order []string
	latest := map[string]pending{}
	for _, ev := range evs {
		if ev.Type != "io.ore.pipeline.run.started" && ev.Type != "io.ore.pipeline.run.finished" {
			continue
		}
		var run ledger.PipelineRun
		if err := json.Unmarshal(ev.Data, &run); err != nil || run.ID == "" {
			continue
		}
		l, ok := byRepo[run.RepoID]
		if !ok {
			continue
		}
		if _, seen := latest[run.ID]; !seen {
			order = append(order, run.ID)
		}
		latest[run.ID] = pending{run: run, link: l}
	}
	posted := 0
	for _, id := range order {
		p := latest[id]
		state, desc := statusFor(p.run)
		if state == "" {
			continue
		}
		if done, err := svc.store.statusPosted(ctx, p.run.ID, state); err != nil {
			return posted, err
		} else if done {
			continue
		}
		repo, err := svc.DB.RepoByID(ctx, p.run.RepoID)
		if err != nil {
			continue
		}
		if !svc.isMirrored(ctx, repo, p.run.CommitOID) {
			continue
		}
		target := fmt.Sprintf("%s/%s/%s/runs/%s", svc.BaseURL, repo.Org, repo.Name, p.run.ID)
		st := fpi.Status{State: state, Context: StatusContext, Description: desc, TargetURL: target}
		if err := svc.Client.CreateStatus(ctx, p.link.Owner, p.link.Name, p.run.CommitOID, st); err != nil {
			var rl *RateLimitError
			var apiErr *APIError
			switch {
			case errors.As(err, &rl):
				next := svc.now().Add(rl.RetryAfter)
				svc.mu.Lock()
				svc.statusDeferUntil = next
				svc.mu.Unlock()
				_ = svc.store.emit(ctx, repo, "io.ore.github.sync.deferred", repo.Org+"/"+repo.Name+"/github/statuses", ledger.Actor{Kind: "app", ID: "github"},
					map[string]any{"what": "statuses", "run_id": p.run.ID, "retry_after_seconds": int(rl.RetryAfter.Seconds()), "next_attempt": next.UTC().Format("2006-01-02T15:04:05Z07:00"), "error": err.Error()})
				return posted, err
			case errors.As(err, &apiErr) && apiErr.Status == http.StatusUnprocessableEntity:
				// GitHub does not know the commit after all; record it so it is not retried.
				svc.log().Warn("github: status rejected", "repo", repo.Org+"/"+repo.Name, "sha", p.run.CommitOID, "err", err)
				_ = svc.store.recordStatus(ctx, repo, p.link, p.run.ID, p.run.CommitOID, state, "rejected:"+StatusContext, target)
				continue
			}
			return posted, err
		}
		if err := svc.store.recordStatus(ctx, repo, p.link, p.run.ID, p.run.CommitOID, state, StatusContext, target); err != nil {
			return posted, err
		}
		posted++
	}
	return posted, svc.store.setCursor(ctx, statusCursor, evs[len(evs)-1].Seq)
}

// statusFor maps a run to a GitHub status state and description.
func statusFor(run ledger.PipelineRun) (string, string) {
	n := 0
	var tasks []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if json.Unmarshal([]byte(run.Tasks), &tasks) == nil {
		n = len(tasks)
	}
	var failed []string
	for _, t := range tasks {
		if t.Status == "failed" {
			failed = append(failed, t.ID)
		}
	}
	switch run.Status {
	case "running", "queued":
		return fpi.StatusPending, "Ore pipeline is running"
	case "succeeded":
		return fpi.StatusSuccess, fmt.Sprintf("Ore pipeline succeeded (%d tasks)", n)
	case "failed":
		return fpi.StatusFailure, "Ore pipeline failed: " + strings.Join(failed, ", ")
	case "error":
		return fpi.StatusError, "Ore pipeline could not run"
	}
	return "", ""
}
