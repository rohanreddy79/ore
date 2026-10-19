// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"testing"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

func TestStatusPendingThenFinal(t *testing.T) {
	h, gh, up, _ := linked(t)
	repo := h.must("acme", "app")
	ctx := context.Background()
	run := ledger.PipelineRun{CommitOID: up.tip(t, "refs/heads/main")}
	if err := h.db.InsertPipelineRun(ctx, repo, &run, ledger.System); err != nil {
		t.Fatal(err)
	}
	if n, _ := h.svc.PostStatuses(ctx); n != 0 { // queued is not reported
		t.Fatalf("queued posted %d", n)
	}
	if err := h.db.UpdatePipelineRun(ctx, repo, &run, "running", "", ledger.System); err != nil {
		t.Fatal(err)
	}
	if n, err := h.svc.PostStatuses(ctx); err != nil || n != 1 || gh.statusCalls()[0].Body["state"] != "pending" {
		t.Fatalf("pending: %d %v %+v", n, err, gh.statusCalls())
	}
	if err := h.db.UpdatePipelineRun(ctx, repo, &run, "failed", `[{"id":"test","status":"failed"}]`, ledger.System); err != nil {
		t.Fatal(err)
	}
	if n, err := h.svc.PostStatuses(ctx); err != nil || n != 1 {
		t.Fatalf("failed: %d %v", n, err)
	}
	calls := gh.statusCalls()
	if len(calls) != 2 || calls[1].Body["state"] != "failure" || calls[1].Body["description"] != "Ore pipeline failed: test" {
		t.Fatalf("calls: %+v", calls)
	}
	// error status
	run2 := ledger.PipelineRun{CommitOID: up.tip(t, "refs/heads/main")}
	h.db.InsertPipelineRun(ctx, repo, &run2, ledger.System)
	h.db.UpdatePipelineRun(ctx, repo, &run2, "running", "", ledger.System)
	h.db.UpdatePipelineRun(ctx, repo, &run2, "error", "", ledger.System)
	h.svc.PostStatuses(ctx) // started and finished in one batch: only the final state is posted
	calls = gh.statusCalls()
	if len(calls) != 3 || calls[2].Body["state"] != "error" {
		t.Fatalf("error: %+v", calls)
	}
}

func TestStatusOnlyForMirroredCommits(t *testing.T) {
	h, gh, _, _ := linked(t)
	// A commit made through Ore's API on a local branch is unknown to GitHub.
	var commit struct{ Commit string }
	h.api("POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "ore/local", "message": "local", "files": []map[string]any{{"path": "l", "content": "1"}}}, &commit)
	var run ledger.PipelineRun
	h.api("POST", "/api/v0/repos/acme/app/runs", map[string]any{"ref": commit.Commit, "wait": true}, &run)
	if n, err := h.svc.PostStatuses(context.Background()); err != nil || n != 0 || len(gh.statusCalls()) != 0 {
		t.Fatalf("posted for a local commit: %d %v %+v", n, err, gh.statusCalls())
	}
	// Unlinked repositories are never reported either.
	h.createRepo("acme", "other")
	h.api("POST", "/api/v0/repos/acme/other/commits", map[string]any{"message": "x", "files": []map[string]any{{"path": "f", "content": "1"}}}, &commit)
	h.api("POST", "/api/v0/repos/acme/other/runs", map[string]any{"ref": commit.Commit, "wait": true}, &run)
	if n, _ := h.svc.PostStatuses(context.Background()); n != 0 || len(gh.statusCalls()) != 0 {
		t.Fatal("posted for an unlinked repository")
	}
}

func TestStatusRateLimitDefers(t *testing.T) {
	h, gh, up, _ := linked(t)
	var run ledger.PipelineRun
	h.api("POST", "/api/v0/repos/acme/app/runs", map[string]any{"ref": up.tip(t, "refs/heads/main"), "wait": true}, &run)
	gh.setRetryAfter(45)
	n, err := h.svc.PostStatuses(context.Background())
	if n != 0 || err == nil {
		t.Fatalf("expected a rate-limit error, got %d %v", n, err)
	}
	if def := h.events("io.ore.github.sync.deferred"); len(def) != 1 || data(t, def[0])["what"] != "statuses" {
		t.Fatalf("deferred: %+v", def)
	}
	gh.setRetryAfter(0)
	hits := gh.callCount("POST", "/repos/octo/app/statuses/"+run.CommitOID)
	if n, err := h.svc.PostStatuses(context.Background()); n != 0 || err != nil || gh.callCount("POST", "/repos/octo/app/statuses/"+run.CommitOID) != hits {
		t.Fatalf("posted during backoff: %d %v", n, err)
	}
	h.tick(46 * time.Second)
	if n, err := h.svc.PostStatuses(context.Background()); n != 1 || err != nil {
		t.Fatalf("after backoff: %d %v", n, err)
	}
	if calls := gh.statusCalls(); len(calls) != 1 || calls[0].Body["state"] != "success" {
		t.Fatalf("calls: %+v", calls)
	}
}

func TestStatusRejectedShaNotRetried(t *testing.T) {
	h, gh, up, _ := linked(t)
	repo := h.must("acme", "app")
	ctx := context.Background()
	// A run on a commit GitHub does not have anymore (the fake answers 422 for unknown objects).
	sha := up.commit(t, "temp", "temp", map[string]string{"t": "1"})
	h.svc.SyncAll(ctx)
	up.deleteRef(t, "refs/heads/temp")
	run := ledger.PipelineRun{CommitOID: sha}
	h.db.InsertPipelineRun(ctx, repo, &run, ledger.System)
	h.db.UpdatePipelineRun(ctx, repo, &run, "running", "", ledger.System)
	h.db.UpdatePipelineRun(ctx, repo, &run, "succeeded", "[]", ledger.System)
	// Make the fake forget the object by pointing at a repository without it.
	empty := gh.addRepo("octo", "empty", nil)
	gh.mu.Lock()
	gh.repos["octo/app"].path = empty.path
	gh.mu.Unlock()
	if n, err := h.svc.PostStatuses(ctx); err != nil || n != 0 {
		t.Fatalf("rejected status: %d %v", n, err)
	}
	if n, err := h.svc.PostStatuses(ctx); err != nil || n != 0 || gh.callCount("POST", "/repos/octo/app/statuses/"+sha) != 1 {
		t.Fatalf("rejected status retried: %d %v calls=%d", n, err, gh.callCount("POST", "/repos/octo/app/statuses/"+sha))
	}
}
