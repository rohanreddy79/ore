// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// TestLinkPushPipelineStatus is the whole flow against the fake GitHub:
// link a repository (initial mirror), receive a push webhook (ref update on
// the ledger via "github"), run the pipeline on the mirrored commit, and
// see the result posted back as a commit status with the Ore run URL.
func TestLinkPushPipelineStatus(t *testing.T) {
	gh := newFakeGitHub(t)
	up := gh.addRepo("octo", "app", map[string]string{
		"README.md":          "# app\n",
		".ore/pipeline.yaml": "version: 0\ntasks:\n  - id: check\n    run: test -f README.md\n",
	})
	up.commit(t, "feature/retry", "Start retry work", map[string]string{"uploader.go": "package uploader\n"})
	up.setRef(t, "refs/tags/v0.1.0", up.tip(t, "refs/heads/main"))
	h := newHarness(t, gh, Config{Token: testToken})
	h.createRepo("acme", "app")

	// 1. link: the initial mirror brings every branch and tag, recorded via "github".
	res := h.link("acme", "app", "octo", "app")
	if res.Link.Owner != "octo" || res.Link.Name != "app" || len(res.Link.Secret) < 32 || res.WebhookURL != "http://ore.test/hooks/github" {
		t.Fatalf("link: %+v", res)
	}
	if len(res.Mirror.Updates) != 3 {
		t.Fatalf("expected main, feature/retry and the tag, got %+v", res.Mirror.Updates)
	}
	refs := h.refs("acme", "app")
	if refs["refs/heads/main"] != up.tip(t, "refs/heads/main") || refs["refs/heads/feature/retry"] != up.tip(t, "refs/heads/feature/retry") || refs["refs/tags/v0.1.0"] == "" {
		t.Fatalf("refs after link: %v", refs)
	}
	evs := h.events("io.ore.ref.updated")
	if len(evs) != 3 {
		t.Fatalf("expected 3 ref.updated events, got %d", len(evs))
	}
	for _, e := range evs {
		if !strings.Contains(string(e.Data), `"via":"github"`) {
			t.Fatalf("ref update not attributed to github: %s", e.Data)
		}
	}
	if len(h.events("io.ore.github.linked")) != 1 || gh.gitRejected != 0 || gh.gitRequests == 0 {
		t.Fatalf("linked=%d gitRequests=%d gitRejected=%d", len(h.events("io.ore.github.linked")), gh.gitRequests, gh.gitRejected)
	}

	// 2. a push on GitHub, delivered by webhook.
	before := up.tip(t, "refs/heads/main")
	after := up.commit(t, "main", "Fix the greeting", map[string]string{"main.go": "package main\n"})
	code, body := h.deliver("push", "72d3162e-cc78-11e3-81ab-4c9367dc0958", res.Link.Secret, payload(t, "push.json", up, map[string]any{"before": before, "after": after}))
	if code != 200 {
		t.Fatalf("push webhook: %d %v", code, body)
	}
	if h.refs("acme", "app")["refs/heads/main"] != after {
		t.Fatalf("main not mirrored after push")
	}
	evs = h.events("io.ore.ref.updated")
	if len(evs) != 4 || evs[3].OreActor.ID != "github:octocat" {
		t.Fatalf("push ref updates: %d %+v", len(evs), evs[len(evs)-1].OreActor)
	}
	if d := data(t, evs[3]); d["old"] != before || d["new"] != after || d["ref"] != "refs/heads/main" || d["via"] != "github" {
		t.Fatalf("push ref update data: %v", d)
	}
	if ms := h.events("io.ore.github.push.mirrored"); len(ms) != 1 || !strings.Contains(string(ms[0].Data), `"forced":false`) {
		t.Fatalf("push.mirrored: %+v", ms)
	}
	if ds := h.events("io.ore.github.delivery.received"); len(ds) != 1 || !strings.Contains(string(ds[0].Data), "72d3162e-cc78-11e3-81ab-4c9367dc0958") {
		t.Fatalf("delivery.received: %+v", ds)
	}

	// 3. a pipeline run on the mirrored commit, then the status reporter.
	var run ledger.PipelineRun
	if code := h.api("POST", "/api/v0/repos/acme/app/runs", map[string]any{"ref": after, "wait": true}, &run); code != 200 || run.Status != "succeeded" {
		t.Fatalf("run: %d %+v", code, run)
	}
	n, err := h.svc.PostStatuses(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("post statuses: %d %v", n, err)
	}
	calls := gh.statusCalls()
	if len(calls) != 1 || calls[0].SHA != after || calls[0].Body["state"] != "success" || calls[0].Body["context"] != StatusContext ||
		calls[0].Body["target_url"] != "http://ore.test/acme/app/runs/"+run.ID || !strings.Contains(calls[0].Body["description"], "succeeded") {
		t.Fatalf("status calls: %+v", calls)
	}
	if ps := h.events("io.ore.github.status.posted"); len(ps) != 1 {
		t.Fatalf("status.posted events: %d", len(ps))
	}
	// A second pass posts nothing new.
	if n, err := h.svc.PostStatuses(context.Background()); err != nil || n != 0 || len(gh.statusCalls()) != 1 {
		t.Fatalf("second pass: %d %v %d", n, err, len(gh.statusCalls()))
	}
	// The tracking namespace mirrors GitHub exactly.
	rp := h.svc.repoPath(ledger.Repo{Org: "acme", Name: "app"})
	all, _ := gitx.Refs(context.Background(), rp)
	if all[TrackingPrefix+"heads/main"] != after || all[TrackingPrefix+"tags/v0.1.0"] == "" {
		t.Fatalf("tracking refs: %v", all)
	}
}
