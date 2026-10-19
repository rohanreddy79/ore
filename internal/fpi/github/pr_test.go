// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"strconv"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/ledger"
)

// openPR simulates a pull request on the fake: a branch plus refs/pull/N/head.
func openPR(t *testing.T, up *fakeRepo, n int, branch string, files map[string]string) string {
	t.Helper()
	sha := up.commit(t, branch, "PR #"+branch, files)
	up.setRef(t, "refs/pull/"+itoa(n)+"/head", sha)
	return sha
}

func itoa(n int) string { return strconv.Itoa(n) }

func prPayload(t *testing.T, name string, up *fakeRepo, n int, headSHA, baseSHA string, extra map[string]any) []byte {
	t.Helper()
	edits := map[string]any{"number": float64(n), "pull_request.number": float64(n), "pull_request.head.sha": headSHA, "pull_request.base.sha": baseSHA}
	for k, v := range extra {
		edits[k] = v
	}
	return payload(t, name, up, edits)
}

func TestPullRequestMirrored(t *testing.T) {
	h, _, up, res := linked(t)
	base := up.tip(t, "refs/heads/main")
	head := openPR(t, up, 7, "feature/retry", map[string]string{"uploader.go": "retry v1\n"})
	code, out := h.deliver("pull_request", "p-1", res.Link.Secret, prPayload(t, "pull_request_opened.json", up, 7, head, base, nil))
	if code != 200 {
		t.Fatalf("opened: %d %v", code, out)
	}
	var ch ledger.Change
	if code := h.api("GET", "/api/v0/repos/acme/app/changes/7", nil, &ch); code != 200 {
		t.Fatalf("change #7: %d", code)
	}
	if ch.Provenance != `{"pr":7,"source":"github"}` || ch.HeadOID != head || ch.BaseOID != base || ch.SourceRef != TrackingPrefix+"pull/7/head" || ch.TargetRef != "refs/heads/main" || ch.Title != "Retry uploads with exponential backoff" {
		t.Fatalf("change: %+v", ch)
	}
	var diff string
	if code := h.api("GET", "/api/v0/repos/acme/app/changes/7/diff", nil, &diff); code != 200 || !strings.Contains(diff, "+retry v1") {
		t.Fatalf("diff: %d %s", code, diff)
	}
	if op := h.events("io.ore.change.opened"); len(op) != 1 || op[0].OreActor.ID != "github:octocat" {
		t.Fatalf("change.opened: %+v", op)
	}
	// synchronize: new head, same change.
	head2 := up.commit(t, "feature/retry", "More retry", map[string]string{"uploader.go": "retry v2\n"})
	up.setRef(t, "refs/pull/7/head", head2)
	h.deliver("pull_request", "p-2", res.Link.Secret, prPayload(t, "pull_request_synchronize.json", up, 7, head2, base, nil))
	h.api("GET", "/api/v0/repos/acme/app/changes/7", nil, &ch)
	if ch.HeadOID != head2 || len(h.events("io.ore.change.patchset.pushed")) != 1 {
		t.Fatalf("synchronize: %+v", ch)
	}
	// edited: title and body follow GitHub.
	h.deliver("pull_request", "p-3", res.Link.Secret, prPayload(t, "pull_request_synchronize.json", up, 7, head2, base, map[string]any{"action": "edited", "pull_request.title": "Retry uploads (final)"}))
	h.api("GET", "/api/v0/repos/acme/app/changes/7", nil, &ch)
	if ch.Title != "Retry uploads (final)" || len(h.events("io.ore.change.updated")) != 1 {
		t.Fatalf("edited: %+v", ch)
	}
	// closed + merged on GitHub: merged in Ore with the merge commit; the base moves with the next push.
	merge := up.commit(t, "main", "Merge pull request #7", map[string]string{"uploader.go": "retry v2\n"})
	h.deliver("pull_request", "p-4", res.Link.Secret, prPayload(t, "pull_request_closed.json", up, 7, head2, base, map[string]any{"pull_request.merge_commit_sha": merge}))
	h.api("GET", "/api/v0/repos/acme/app/changes/7", nil, &ch)
	if ch.State != "merged" || ch.MergeOID == nil || *ch.MergeOID != merge {
		t.Fatalf("merged: %+v", ch)
	}
	mg := h.events("io.ore.change.merged")
	if len(mg) != 1 || data(t, mg[0])["via"] != "github" {
		t.Fatalf("change.merged: %+v", mg)
	}
	if h.refs("acme", "app")["refs/heads/main"] != merge {
		t.Fatal("main not mirrored with the merge")
	}
	// Closing again is a no-op.
	h.deliver("pull_request", "p-5", res.Link.Secret, prPayload(t, "pull_request_closed.json", up, 7, head2, base, nil))
	if len(h.events("io.ore.change.merged")) != 1 {
		t.Fatal("merged twice")
	}
	var changes []ledger.Change
	h.api("GET", "/api/v0/repos/acme/app/changes", nil, &changes)
	if len(changes) != 1 {
		t.Fatalf("changes: %+v", changes)
	}
}

func TestPullRequestAlreadyMirroredNoDuplicate(t *testing.T) {
	h, _, up, res := linked(t)
	base := up.tip(t, "refs/heads/main")
	head := openPR(t, up, 7, "feature/retry", map[string]string{"uploader.go": "v1\n"})
	h.deliver("pull_request", "q-1", res.Link.Secret, prPayload(t, "pull_request_opened.json", up, 7, head, base, nil))
	// GitHub redelivers "opened" (new delivery id) after the head moved: the head updates, no second change.
	head2 := up.commit(t, "feature/retry", "v2", map[string]string{"uploader.go": "v2\n"})
	up.setRef(t, "refs/pull/7/head", head2)
	code, out := h.deliver("pull_request", "q-2", res.Link.Secret, prPayload(t, "pull_request_opened.json", up, 7, head2, base, nil))
	if code != 200 {
		t.Fatalf("redelivered opened: %d %v", code, out)
	}
	var changes []ledger.Change
	h.api("GET", "/api/v0/repos/acme/app/changes", nil, &changes)
	if len(changes) != 1 || changes[0].HeadOID != head2 || changes[0].Number != 7 {
		t.Fatalf("changes: %+v", changes)
	}
	if len(h.events("io.ore.change.opened")) != 1 || len(h.events("io.ore.change.patchset.pushed")) != 1 {
		t.Fatalf("opened=%d patchsets=%d", len(h.events("io.ore.change.opened")), len(h.events("io.ore.change.patchset.pushed")))
	}
	// closed without merge
	h.deliver("pull_request", "q-3", res.Link.Secret, prPayload(t, "pull_request_closed.json", up, 7, head2, base, map[string]any{"pull_request.merged": false, "pull_request.merge_commit_sha": nil}))
	h.api("GET", "/api/v0/repos/acme/app/changes", nil, &changes)
	if changes[0].State != "closed" || len(h.events("io.ore.change.closed")) != 1 {
		t.Fatalf("closed: %+v", changes[0])
	}
	// A PR whose number collides with an existing change gets the next free number.
	var native ledger.Change
	h.api("POST", "/api/v0/repos/acme/app/changes", map[string]string{"source_ref": "feature/retry", "title": "native"}, &native)
	head9 := openPR(t, up, 8, "feature/eight", map[string]string{"eight": "8\n"})
	h.deliver("pull_request", "q-4", res.Link.Secret, prPayload(t, "pull_request_opened.json", up, 8, head9, base, nil))
	h.api("GET", "/api/v0/repos/acme/app/changes", nil, &changes)
	if native.Number != 8 || changes[0].Number != 9 || !strings.Contains(changes[0].Provenance, `"pr":8`) {
		t.Fatalf("collision: native=%d changes=%+v", native.Number, changes)
	}
	// A PR closed before Ore ever saw it is acknowledged and ignored.
	code, out = h.deliver("pull_request", "q-5", res.Link.Secret, prPayload(t, "pull_request_closed.json", up, 55, head2, base, nil))
	if code != 200 || !strings.Contains(out["outcomes"].([]any)[0].(map[string]any)["detail"].(map[string]any)["ignored"].(string), "closed before") {
		t.Fatalf("unknown closed PR: %d %v", code, out)
	}
}
