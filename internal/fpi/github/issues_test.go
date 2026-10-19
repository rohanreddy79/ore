// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"testing"

	"github.com/orevcs/ore/internal/ledger"
)

func TestIssuesMirrored(t *testing.T) {
	h, _, up, res := linked(t)
	// opened: the Ore issue keeps the GitHub number when it is free.
	code, out := h.deliver("issues", "i-1", res.Link.Secret, payload(t, "issues_opened.json", up, nil))
	if code != 200 {
		t.Fatalf("opened: %d %v", code, out)
	}
	var issue ledger.Issue
	if code := h.api("GET", "/api/v0/repos/acme/app/issues/42", nil, &issue); code != 200 || issue.Title != "Fix the flaky retry in the uploader" || issue.State != "open" {
		t.Fatalf("issue #42: %d %+v", code, issue)
	}
	op := h.events("io.ore.issue.opened")
	if len(op) != 1 || op[0].OreActor.ID != "github:octocat" || data(t, op[0])["github"].(map[string]any)["number"] != float64(42) {
		t.Fatalf("issue.opened: %+v", op)
	}
	// edited
	h.deliver("issues", "i-2", res.Link.Secret, payload(t, "issues_edited.json", up, nil))
	h.api("GET", "/api/v0/repos/acme/app/issues/42", nil, &issue)
	if issue.Title != "Fix the flaky retry in the uploader (timeouts)" || len(h.events("io.ore.issue.updated")) != 1 {
		t.Fatalf("edited: %+v", issue)
	}
	// a replayed "opened" with a new delivery id is an edit, never a second issue
	h.deliver("issues", "i-3", res.Link.Secret, payload(t, "issues_opened.json", up, nil))
	var issues []ledger.Issue
	h.api("GET", "/api/v0/repos/acme/app/issues", nil, &issues)
	if len(issues) != 1 {
		t.Fatalf("duplicate issue: %+v", issues)
	}
	// closed
	h.deliver("issues", "i-4", res.Link.Secret, payload(t, "issues_closed.json", up, nil))
	h.api("GET", "/api/v0/repos/acme/app/issues/42", nil, &issue)
	cl := h.events("io.ore.issue.closed")
	if issue.State != "closed" || len(cl) != 1 || cl[0].OreActor.ID != "github:hubot" {
		t.Fatalf("closed: %+v %+v", issue, cl)
	}
	// closing twice changes nothing
	h.deliver("issues", "i-5", res.Link.Secret, payload(t, "issues_closed.json", up, nil))
	if len(h.events("io.ore.issue.closed")) != 1 {
		t.Fatal("closed twice")
	}
	// reopened
	h.deliver("issues", "i-6", res.Link.Secret, payload(t, "issues_opened.json", up, map[string]any{"action": "reopened"}))
	var reopened ledger.Issue
	h.api("GET", "/api/v0/repos/acme/app/issues/42", nil, &reopened)
	if reopened.State != "open" || reopened.ClosedAt != nil {
		t.Fatalf("reopened: %+v", reopened)
	}
	// A native issue created afterwards continues after the highest number.
	var native ledger.Issue
	h.api("POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": "native"}, &native)
	if native.Number != 43 {
		t.Fatalf("native issue number: %d", native.Number)
	}
}

func TestIssueNumberCollisionFallsBack(t *testing.T) {
	h, _, up, res := linked(t)
	var native ledger.Issue
	h.api("POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": "native first"}, &native)
	// GitHub #1 collides with Ore #1: the mirrored issue takes the next number and the mapping remembers it.
	h.deliver("issues", "c-1", res.Link.Secret, payload(t, "issues_opened.json", up, map[string]any{"issue.number": float64(1), "issue.title": "from github"}))
	var issues []ledger.Issue
	h.api("GET", "/api/v0/repos/acme/app/issues", nil, &issues)
	if len(issues) != 2 || issues[0].Number != 2 || issues[0].Title != "from github" || issues[1].Title != "native first" {
		t.Fatalf("issues: %+v", issues)
	}
	// Closing GitHub #1 closes Ore #2, not the native #1.
	h.deliver("issues", "c-2", res.Link.Secret, payload(t, "issues_closed.json", up, map[string]any{"issue.number": float64(1), "issue.title": "from github"}))
	h.api("GET", "/api/v0/repos/acme/app/issues", nil, &issues)
	if issues[0].State != "closed" || issues[1].State != "open" {
		t.Fatalf("after close: %+v", issues)
	}
	// An issue first seen through "closed" is mirrored closed.
	h.deliver("issues", "c-3", res.Link.Secret, payload(t, "issues_closed.json", up, map[string]any{"issue.number": float64(9), "issue.title": "late"}))
	var late ledger.Issue
	if code := h.api("GET", "/api/v0/repos/acme/app/issues/9", nil, &late); code != 200 || late.State != "closed" {
		t.Fatalf("late issue: %d %+v", code, late)
	}
}
