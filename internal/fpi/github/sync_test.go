// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

func TestReconcileFindsMovedBranch(t *testing.T) {
	h, _, up, _ := linked(t)
	base := up.tip(t, "refs/heads/main")
	head := openPR(t, up, 3, "feature/x", map[string]string{"x": "1\n"})
	h.deliver("pull_request", "s-0", h.mustLink("acme", "app").Secret, prPayload(t, "pull_request_opened.json", up, 3, head, base, nil))
	// Things move on GitHub, no webhook arrives: a branch, a tag, the PR head, and a branch is deleted.
	moved := up.commit(t, "main", "No webhook for this one", map[string]string{"y": "2\n"})
	up.setRef(t, "refs/tags/v1", moved)
	head2 := up.commit(t, "feature/x", "x2", map[string]string{"x": "2\n"})
	up.setRef(t, "refs/pull/3/head", head2)
	up.commit(t, "gone", "temporary", map[string]string{"g": "1\n"})
	results, err := h.svc.SyncAll(context.Background())
	if err != nil || len(results) != 1 || results[0].Status != "ok" {
		t.Fatalf("sync: %+v %v", results, err)
	}
	up.deleteRef(t, "refs/heads/gone")
	results, _ = h.svc.SyncAll(context.Background())
	refs := h.refs("acme", "app")
	if refs["refs/heads/main"] != moved || refs["refs/tags/v1"] != moved {
		t.Fatalf("refs after reconcile: %v", refs)
	}
	if _, ok := refs["refs/heads/gone"]; ok {
		t.Fatal("deleted branch survived reconciliation")
	}
	var found bool
	for _, u := range h.events("io.ore.ref.updated") {
		d := data(t, u)
		if d["ref"] == "refs/heads/main" && d["new"] == moved && d["via"] == "github" && u.OreActor.Kind == "app" {
			found = true
		}
	}
	if !found {
		t.Fatal("moved branch not recorded via github by the worker")
	}
	if del := h.events("io.ore.ref.deleted"); len(del) != 1 || data(t, del[0])["ref"] != "refs/heads/gone" {
		t.Fatalf("ref.deleted: %+v", del)
	}
	sc := h.events("io.ore.github.sync.completed")
	if len(sc) < 2 { // the initial mirror and at least one reconcile with updates
		t.Fatalf("sync.completed: %d", len(sc))
	}
	var ch ledger.Change
	h.api("GET", "/api/v0/repos/acme/app/changes/3", nil, &ch)
	if ch.HeadOID != head2 || results[0].Status != "ok" {
		t.Fatalf("PR head after reconcile: %+v", ch)
	}
	var link LinkResponse
	h.api("GET", "/api/v0/repos/acme/app/github/link", nil, &link)
	if link.Link.LastSyncStatus != "ok" || link.Link.LastSyncAt == nil || link.Link.NextSyncAt != nil {
		t.Fatalf("link sync state: %+v", link.Link)
	}
	// A quiet reconcile records nothing new.
	n := len(h.events("io.ore."))
	if _, err := h.svc.SyncAll(context.Background()); err != nil || len(h.events("io.ore.")) != n {
		t.Fatalf("quiet reconcile emitted events: %v", err)
	}
	// The manual sync endpoint reports the same shape.
	var sr SyncResult
	if code := h.api("POST", "/api/v0/repos/acme/app/github/sync", map[string]any{}, &sr); code != 200 || sr.Status != "ok" {
		t.Fatalf("sync endpoint: %d %+v", code, sr)
	}
}

func TestSyncRateLimitedDefers(t *testing.T) {
	h, gh, up, _ := linked(t)
	gh.setRetryAfter(120)
	before := gh.callCount("GET", "/repos/octo/app")
	results, _ := h.svc.SyncAll(context.Background())
	if len(results) != 1 || results[0].Status != "deferred" || results[0].NextAt == nil || !results[0].NextAt.Equal(h.now().Add(120*time.Second)) {
		t.Fatalf("results: %+v", results)
	}
	def := h.events("io.ore.github.sync.deferred")
	if len(def) != 1 || data(t, def[0])["retry_after_seconds"] != float64(120) {
		t.Fatalf("sync.deferred: %+v", def)
	}
	var link LinkResponse
	h.api("GET", "/api/v0/repos/acme/app/github/link", nil, &link)
	if link.Link.LastSyncStatus != "deferred" || link.Link.NextSyncAt == nil {
		t.Fatalf("link: %+v", link.Link)
	}
	// Until Retry-After elapses the worker does not call GitHub again.
	gh.setRetryAfter(0)
	h.tick(60 * time.Second)
	results, _ = h.svc.SyncAll(context.Background())
	if results[0].Status != "skipped" || gh.callCount("GET", "/repos/octo/app") != before+1 {
		t.Fatalf("backoff not honored: %+v calls=%d", results, gh.callCount("GET", "/repos/octo/app"))
	}
	// Afterwards it resumes and catches up.
	moved := up.commit(t, "main", "while limited", map[string]string{"z": "1\n"})
	h.tick(61 * time.Second)
	results, _ = h.svc.SyncAll(context.Background())
	if results[0].Status != "ok" || h.refs("acme", "app")["refs/heads/main"] != moved {
		t.Fatalf("resume: %+v", results)
	}
	var resumed LinkResponse
	h.api("GET", "/api/v0/repos/acme/app/github/link", nil, &resumed)
	if resumed.Link.LastSyncStatus != "ok" || resumed.Link.NextSyncAt != nil || resumed.Link.LastSyncError != "" {
		t.Fatalf("link after resume: %+v", resumed.Link)
	}
	// The manual sync endpoint surfaces a 503 with Retry-After while limited.
	gh.setRetryAfter(30)
	var raw string
	if code := h.api("POST", "/api/v0/repos/acme/app/github/sync", map[string]any{}, &raw); code != 503 {
		t.Fatalf("sync while limited: %d %s", code, raw)
	}
}

func TestSyncFailureRecorded(t *testing.T) {
	h, gh, _, _ := linked(t)
	delete(gh.repos, "octo/app") // the repository disappears (deleted, renamed, or access revoked)
	results, err := h.svc.SyncAll(context.Background())
	if err != nil || results[0].Status != "failed" || !strings.Contains(results[0].Error, "HTTP 404") {
		t.Fatalf("results: %+v %v", results, err)
	}
	if f := h.events("io.ore.github.sync.failed"); len(f) != 1 {
		t.Fatalf("sync.failed: %d", len(f))
	}
}

func TestWorkerLoop(t *testing.T) {
	gh := newFakeGitHub(t)
	up := gh.addRepo("octo", "app", nil)
	h := newHarness(t, gh, Config{Token: testToken, SyncInterval: 30 * time.Millisecond, StatusInterval: 30 * time.Millisecond, Now: time.Now})
	h.createRepo("acme", "app")
	h.link("acme", "app", "octo", "app")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.svc.Run(ctx)
	moved := up.commit(t, "main", "moved", map[string]string{"w": "1\n"})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && h.refs("acme", "app")["refs/heads/main"] != moved {
		time.Sleep(20 * time.Millisecond)
	}
	if h.refs("acme", "app")["refs/heads/main"] != moved {
		t.Fatal("the worker did not reconcile")
	}
	var run ledger.PipelineRun
	h.api("POST", "/api/v0/repos/acme/app/runs", map[string]any{"ref": moved, "wait": true}, &run)
	for time.Now().Before(deadline) && len(gh.statusCalls()) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if calls := gh.statusCalls(); len(calls) == 0 || calls[0].SHA != moved {
		t.Fatalf("the worker did not post the status: %+v", calls)
	}
	cancel()
}
