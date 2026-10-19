// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

type planResponse struct {
	JobID  string         `json:"job_id"`
	Report FidelityReport `json:"report"`
}

func linesByObject(r FidelityReport) map[string]FidelityLine {
	m := map[string]FidelityLine{}
	for _, l := range r.Lines {
		m[l.Object] = l
	}
	return m
}

func importHarness(t *testing.T) (*harness, *fakeGitHub, *fakeRepo) {
	t.Helper()
	gh := newFakeGitHub(t)
	up := seedImportRepo(t, gh)
	h := newHarness(t, gh, Config{Token: testToken})
	h.createRepo("acme", "app")
	return h, gh, up
}

func (h *harness) runImport(source string, opts ImportOptions) ImportJob {
	h.t.Helper()
	opts.Wait = true
	var job ImportJob
	var raw string
	code := h.api("POST", "/api/v0/repos/acme/app/import/run", ImportRequest{Source: source, Options: opts}, &raw)
	if code != 200 {
		h.t.Fatalf("import/run: %d %s", code, raw)
	}
	if err := json.Unmarshal([]byte(raw), &job); err != nil {
		h.t.Fatal(err)
	}
	return job
}

func (h *harness) count(q string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.db.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func TestImportPlanFidelityReport(t *testing.T) {
	h, gh, _ := importHarness(t)
	var out planResponse
	if code := h.api("POST", "/api/v0/repos/acme/app/import/plan", ImportRequest{Source: "github.com/octo/app"}, &out); code != 200 {
		t.Fatalf("plan: %d", code)
	}
	r := out.Report
	l := linesByObject(r)
	want := map[string][2]any{
		"branches": {3, FidelityExact}, "tags": {1, FidelityExact}, "lfs_objects": {1, FidelityNone}, "codeowners": {1, FidelityBestEffort},
		"workflows": {2, FidelityHigh}, "issues": {3, FidelityExact}, "pull_requests": {2, FidelityHigh}, "releases": {1, FidelityExact}, "assets": {2, FidelityExact},
		"labels": {2, FidelityBestEffort}, "milestones": {1, FidelityBestEffort}, "wiki": {0, FidelityExact}, "branch_protection": {1, FidelityHigh},
		"webhooks": {1, FidelityHigh}, "identities": {2, FidelityHigh},
	}
	for obj, w := range want {
		got, ok := l[obj]
		if !ok || got.Count != w[0].(int) || got.Fidelity != w[1].(string) {
			t.Errorf("%s: got %+v, want count %v fidelity %v", obj, got, w[0], w[1])
		}
	}
	if r.MaxNumber != 5 || !r.NumbersPreserved || !strings.Contains(r.NumbersNote, "1..5") {
		t.Fatalf("numbers: %d %v %q", r.MaxNumber, r.NumbersPreserved, r.NumbersNote)
	}
	if len(r.Workflows) != 2 || r.Workflows[0].Path != ".github/workflows/ci.yml" || r.Workflows[0].Fidelity != FidelityExact || r.Workflows[1].Fidelity != FidelityHigh || len(r.Workflows[1].Divergences) == 0 {
		t.Fatalf("workflows: %+v", r.Workflows)
	}
	if r.Score <= 0.5 || r.Score >= 1 || r.Estimates.Objects < 10 || r.Estimates.APICalls < 10 || r.Estimates.Seconds < 1 || r.Estimates.SizeKB != 128 {
		t.Fatalf("score %v estimates %+v", r.Score, r.Estimates)
	}
	if len(r.Identities) != 2 || r.Identities[0].Login != "octocat" || r.Identities[0].Contributions != 12 {
		t.Fatalf("identities: %+v", r.Identities)
	}
	if len(r.Warnings) == 0 || !strings.Contains(r.Warnings[0], "LFS") {
		t.Fatalf("warnings: %v", r.Warnings)
	}
	if l["branch_protection"].Details.(map[string]any)["required_approving_review_count"] != float64(1) {
		t.Fatalf("protection details: %v", l["branch_protection"].Details)
	}
	// Nothing changed: no link, no issues, no git data, only the planned event.
	if code := h.api("GET", "/api/v0/repos/acme/app/github/link", nil, nil); code != 404 {
		t.Fatalf("plan linked the repository: %d", code)
	}
	if h.count(`SELECT COUNT(1) FROM issues`) != 0 || h.count(`SELECT COUNT(1) FROM import_mappings`) != 0 || gh.gitRequests != 2 { // one ls-remote: info/refs only
		t.Fatalf("plan changed state: issues=%d mappings=%d git=%d", h.count(`SELECT COUNT(1) FROM issues`), h.count(`SELECT COUNT(1) FROM import_mappings`), gh.gitRequests)
	}
	if evs := h.events("io.ore.import."); len(evs) != 1 || evs[0].Type != "io.ore.import.planned" {
		t.Fatalf("events: %+v", evs)
	}
	var job ImportJob
	if code := h.api("GET", "/api/v0/repos/acme/app/import/jobs/"+out.JobID, nil, &job); code != 200 || job.Status != JobPlanned || job.Report == nil {
		t.Fatalf("planned job: %d %+v", code, job)
	}
}

func TestImportPlanSourceErrors(t *testing.T) {
	h, _, _ := importHarness(t)
	var out map[string]any
	if code := h.api("POST", "/api/v0/repos/acme/app/import/plan", ImportRequest{Source: "github.com/nobody/nothing"}, &out); code != 404 || !strings.Contains(out["error"].(string), "github.com/nobody/nothing does not exist") {
		t.Fatalf("unknown source: %d %v", code, out)
	}
	if code := h.api("POST", "/api/v0/repos/acme/app/import/plan", ImportRequest{Source: "not a source"}, &out); code != 400 {
		t.Fatalf("bad source: %d %v", code, out)
	}
	if code := h.api("POST", "/api/v0/repos/acme/app/import/plan", ImportRequest{}, &out); code != 400 {
		t.Fatalf("empty source: %d %v", code, out)
	}
	if code := h.api("POST", "/api/v0/repos/acme/app/import/run", ImportRequest{Source: "github.com/nobody/nothing"}, &out); code != 404 {
		t.Fatalf("run unknown source: %d %v", code, out)
	}
	if h.count(`SELECT COUNT(1) FROM import_jobs`) != 0 {
		t.Fatal("a job was recorded for a failed plan")
	}
}

func TestParseSource(t *testing.T) {
	for _, in := range []string{"github.com/octo/app", "https://github.com/octo/app", "https://github.com/octo/app.git", "git@github.com:octo/app.git", "octo/app", " GitHub.com/octo/app/ "} {
		o, n, err := ParseSource(in)
		if err != nil || !strings.EqualFold(o, "octo") || n != "app" {
			t.Errorf("%q: %q %q %v", in, o, n, err)
		}
	}
	for _, in := range []string{"", "octo", "octo/app/extra", "github.com/octo/app?x=1", "octo/ app"} {
		if _, _, err := ParseSource(in); err == nil {
			t.Errorf("%q: expected an error", in)
		}
	}
}

func TestImportRunEndToEnd(t *testing.T) {
	h, gh, up := importHarness(t)
	job := h.runImport("github.com/octo/app", ImportOptions{})
	if job.Status != JobCompleted || job.Phase != "done" || job.FinishedAt == nil {
		t.Fatalf("job: %s %s %v errors=%+v", job.Status, job.Phase, job.Error, job.Errors)
	}
	wantProgress := map[string]int{"branches": 3, "tags": 1, "issues": 3, "issue_comments": 2, "pull_requests": 2, "pull_comments": 1, "patch_sets": 2, "patch_sets_unreachable": 1,
		"review_comments": 2, "review_comments_migrated": 1, "reviews": 2, "approvals": 1, "releases": 1, "assets": 2, "labels": 2, "milestones": 1, "webhooks": 1, "workflows": 2, "identities": 3}
	for k, v := range wantProgress {
		if job.Progress[k] != v {
			t.Errorf("progress %s = %d, want %d (all: %v)", k, job.Progress[k], v, job.Progress)
		}
	}
	// The repository is linked and mirrored (the import reuses the mirror).
	refs := h.refs("acme", "app")
	if refs["refs/heads/main"] != up.tip(t, "refs/heads/main") || refs["refs/tags/v1.0.0"] == "" || refs["refs/heads/feature/retry"] == "" {
		t.Fatalf("refs: %v", refs)
	}
	// Issues keep their numbers, state, author and timestamps; the reservation marker at #5 became the real #5.
	var issues []ledger.Issue
	h.api("GET", "/api/v0/repos/acme/app/issues", nil, &issues)
	if len(issues) != 3 || issues[0].Number != 5 || issues[1].Number != 2 || issues[2].Number != 1 {
		t.Fatalf("issues: %+v", issues)
	}
	if issues[2].Title != "Flaky retry in the uploader" || issues[2].State != "open" || issues[2].CreatedAt != "2026-09-01T10:00:00Z" || issues[1].State != "closed" || issues[1].ClosedAt == nil {
		t.Fatalf("issue fields: %+v", issues)
	}
	if h.count(`SELECT COUNT(1) FROM comments WHERE target_kind = 'issue' AND target_number = 1 AND actor_id = 'github:hubot'`) != 1 || h.count(`SELECT COUNT(1) FROM comments WHERE created_at = '2026-09-02T09:00:00Z'`) != 1 {
		t.Fatal("issue comments not imported with author and timestamp")
	}
	// Pull requests became changes with provenance, patch sets, comments and reviews.
	var changes []ledger.Change
	h.api("GET", "/api/v0/repos/acme/app/changes", nil, &changes)
	if len(changes) != 2 || changes[1].Number != 3 || changes[0].Number != 4 {
		t.Fatalf("changes: %+v", changes)
	}
	merged, open := changes[1], changes[0]
	if merged.State != "merged" || merged.MergeOID == nil || *merged.MergeOID != up.tip(t, "refs/heads/main") || merged.Provenance != `{"pr":3,"source":"github"}` || merged.SourceRef != TrackingPrefix+"pull/3/head" || merged.TargetRef != "refs/heads/main" || merged.CreatedAt != "2026-09-03T10:00:00Z" {
		t.Fatalf("merged change: %+v", merged)
	}
	if open.State != "open" || open.Title != "Add metrics" {
		t.Fatalf("open change: %+v", open)
	}
	var diff string
	if code := h.api("GET", "/api/v0/repos/acme/app/changes/3/diff", nil, &diff); code != 200 || !strings.Contains(diff, "+func Retry() {}") {
		t.Fatalf("diff of imported change: %d %s", code, diff)
	}
	if h.count(`SELECT COUNT(1) FROM patch_sets WHERE change_id = ?`, merged.ID) != 1 {
		t.Fatal("patch sets of #3")
	}
	rows, err := h.db.Query(context.Background(), `SELECT body, start_line, patch_set, path FROM change_comments WHERE change_id = ? ORDER BY created_at`, merged.ID)
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	var anchored, migrated int
	for rows.Next() {
		var body, path string
		var start *int
		var ps int
		rows.Scan(&body, &start, &ps, &path)
		bodies = append(bodies, body)
		switch {
		case strings.Contains(body, "migrated anchor"):
			migrated++
			if start != nil || !strings.Contains(body, "uploader.go:9 at 0123456789") {
				t.Fatalf("migrated comment should carry the original anchor and no line: %q %v", body, start)
			}
		case start != nil:
			anchored++
			if *start != 3 || ps != 1 || path != "uploader.go" {
				t.Fatalf("anchored comment: line %d ps %d path %s", *start, ps, path)
			}
		}
	}
	rows.Close()
	if len(bodies) != 3 || anchored != 1 || migrated != 1 {
		t.Fatalf("change comments: %v anchored=%d migrated=%d", bodies, anchored, migrated)
	}
	if h.count(`SELECT COUNT(1) FROM reviews WHERE change_id = ? AND verdict = 'approve' AND reviewer_id = 'github:alice' AND created_at = '2026-09-03T14:00:00Z'`, merged.ID) != 1 || h.count(`SELECT COUNT(1) FROM reviews WHERE change_id = ?`, merged.ID) != 2 {
		t.Fatal("reviews not imported with verdict and timestamp")
	}
	// Releases and assets on disk.
	var rels []ImportedRelease
	h.api("GET", "/api/v0/repos/acme/app/import/releases", nil, &rels)
	if len(rels) != 1 || rels[0].Tag != "v1.0.0" || len(rels[0].Assets) != 2 || rels[0].Assets[0].Status != "ok" {
		t.Fatalf("releases: %+v", rels)
	}
	if b, err := os.ReadFile(rels[0].Assets[0].Path); err != nil || string(b) != "binary payload" || !strings.HasPrefix(rels[0].Assets[0].Path, filepath.Join(h.dir, "imports", "acme", "app", "releases", "v1.0.0")) {
		t.Fatalf("asset on disk: %v %q", err, rels[0].Assets[0].Path)
	}
	// Webhooks recreated with new secrets, listed on the job for rotation.
	var full ImportJob
	h.api("GET", "/api/v0/repos/acme/app/import/jobs/"+job.ID, nil, &full)
	if len(full.Webhooks) != 1 || full.Webhooks[0].URL != "https://ci.example.org/github" || len(full.Webhooks[0].Secret) < 32 || len(full.Webhooks[0].Events) != 2 {
		t.Fatalf("webhooks: %+v", full.Webhooks)
	}
	// Workflows compiled with the conformance report attached.
	if len(full.Conformance) != 2 || full.Conformance[0].Path != ".github/workflows/ci.yml" || !full.Conformance[0].Compiled || full.Conformance[1].Fidelity != FidelityHigh {
		t.Fatalf("conformance: %+v", full.Conformance)
	}
	// Identities became mannequins.
	var ids []ImportedIdentity
	h.api("GET", "/api/v0/repos/acme/app/import/identities", nil, &ids)
	if len(ids) != 3 || ids[0].Login != "alice" || ids[1].Login != "hubot" || ids[1].Kind != "Bot" || ids[2].Login != "octocat" || ids[2].Mentions < 12 || !contains(ids[2].Roles, "committer") || !contains(ids[0].Roles, "reviewer") {
		t.Fatalf("identities: %+v", ids)
	}
	// Ledger.
	for typ, n := range map[string]int{"io.ore.import.started": 1, "io.ore.import.completed": 1, "io.ore.import.numbers.reserved": 1, "io.ore.issue.imported": 3, "io.ore.change.imported": 2, "io.ore.comment.imported": 5, "io.ore.review.submitted": 2, "io.ore.release.imported": 1, "io.ore.import.webhook.recreated": 1, "io.ore.import.identity.recorded": 3, "io.ore.github.linked": 1} {
		if got := len(h.events(typ)); got != n {
			t.Errorf("%s: %d events, want %d", typ, got, n)
		}
	}
	// Verify: everything matches and the report is signed.
	var rep VerifyReport
	if code := h.api("POST", "/api/v0/repos/acme/app/import/verify", ImportRequest{}, &rep); code != 200 {
		t.Fatalf("verify: %d", code)
	}
	if !rep.OK || rep.Refs.Checked != 4 || rep.Refs.Matched != 4 || rep.Issues.Matched != 3 || rep.Releases.Matched != 1 || rep.JobID != job.ID {
		t.Fatalf("verify report: %+v", rep)
	}
	if !rep.VerifySignature() {
		t.Fatal("signature does not verify")
	}
	tampered := rep
	tampered.Issues.Matched = 2
	if tampered.VerifySignature() {
		t.Fatal("tampered report verified")
	}
	if ve := h.events("io.ore.import.verified"); len(ve) != 1 || data(t, ve[0])["ok"] != true || data(t, ve[0])["signature"] != rep.Signature {
		t.Fatalf("verified event: %+v", ve)
	}
	// Continuity: a native issue continues after the highest imported number, and the webhook keeps mirrored issues current.
	var native ledger.Issue
	h.api("POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": "native"}, &native)
	if native.Number != 6 {
		t.Fatalf("native issue number: %d", native.Number)
	}
	link := h.mustLink("acme", "app")
	code, out := h.deliver("issues", "d-edit", link.Secret, payload(t, "issues_edited.json", up, map[string]any{"issue.number": float64(1), "issue.title": "Flaky retry (edited on GitHub)"}))
	if code != 200 {
		t.Fatalf("webhook after import: %d %v", code, out)
	}
	var edited ledger.Issue
	h.api("GET", "/api/v0/repos/acme/app/issues/1", nil, &edited)
	if edited.Title != "Flaky retry (edited on GitHub)" {
		t.Fatalf("webhook did not update the imported issue: %+v", edited)
	}
	// Running again changes nothing (every object is mapped).
	again := h.runImport("github.com/octo/app", ImportOptions{})
	if again.Status != JobCompleted || again.Progress["issues"] != 0 || again.Progress["pull_requests"] != 0 || again.Progress["assets"] != 0 || h.count(`SELECT COUNT(1) FROM issues`) != 4 || h.count(`SELECT COUNT(1) FROM changes`) != 2 {
		t.Fatalf("second run: %+v issues=%d", again.Progress, h.count(`SELECT COUNT(1) FROM issues`))
	}
	_ = gh
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestImportResumeAfterCrashMidIssues(t *testing.T) {
	gh := newFakeGitHub(t)
	up := gh.addRepo("octo", "many", nil)
	for i := 1; i <= 130; i++ {
		var comments []map[string]any
		if i%40 == 0 {
			comments = []map[string]any{comment("hubot", "c", "2026-09-01T00:00:00Z")}
		}
		up.addIssue(i, fmt.Sprintf("Issue %d", i), "", "open", "octocat", "2026-09-01T00:00:00Z", nil, comments)
	}
	h := newHarness(t, gh, Config{Token: testToken})
	h.createRepo("acme", "app")
	// The second page of issues fails once: the job stops mid-issues with its cursor saved.
	gh.failOnce["direction=asc&per_page=100&page=2"] = 500
	job := h.runImport("github.com/octo/many", ImportOptions{})
	if job.Status != JobFailed || job.Phase != "issues" || job.Cursors["issues"] != 2 || job.Progress["issues"] != 100 || !strings.Contains(job.Error, "HTTP 500") {
		t.Fatalf("after crash: %s %s cursors=%v progress=%v err=%s", job.Status, job.Phase, job.Cursors, job.Progress, job.Error)
	}
	if fe := h.events("io.ore.import.failed"); len(fe) != 1 {
		t.Fatalf("failed events: %d", len(fe))
	}
	if h.count(`SELECT COUNT(1) FROM issues`) != 101 { // 100 imported + the reservation marker at #130
		t.Fatalf("issues after crash: %d", h.count(`SELECT COUNT(1) FROM issues`))
	}
	var resumed ImportJob
	if code := h.api("POST", "/api/v0/repos/acme/app/import/jobs/"+job.ID+"/resume", ImportRequest{Options: ImportOptions{Wait: true}}, &resumed); code != 200 {
		t.Fatalf("resume: %d", code)
	}
	if resumed.Status != JobCompleted || resumed.Progress["issues"] != 130 || resumed.Progress["issue_comments"] != 3 {
		t.Fatalf("resumed: %s %v %s", resumed.Status, resumed.Progress, resumed.Error)
	}
	if n := h.count(`SELECT COUNT(1) FROM issues`); n != 130 {
		t.Fatalf("issues after resume: %d (duplicates or missing)", n)
	}
	if h.count(`SELECT COUNT(1) FROM import_mappings WHERE kind = 'issue'`) != 130 || h.count(`SELECT COUNT(DISTINCT number) FROM issues`) != 130 {
		t.Fatal("mappings or numbers inconsistent after resume")
	}
	var last ledger.Issue
	if code := h.api("GET", "/api/v0/repos/acme/app/issues/130", nil, &last); code != 200 || last.Title != "Issue 130" || last.State != "open" {
		t.Fatalf("issue #130 should replace the reservation marker: %d %+v", code, last)
	}
	if len(h.events("io.ore.import.resumed")) != 2 { // the first run resumes from planned → running too
		t.Fatalf("resumed events: %d", len(h.events("io.ore.import.resumed")))
	}
}

func TestImportRateLimitMidRunDefersAndResumes(t *testing.T) {
	h, gh, _ := importHarness(t)
	gh.failOnce["/pulls?state=all&sort=created"] = 429
	job := h.runImport("github.com/octo/app", ImportOptions{})
	if job.Status != JobDeferred || job.Phase != "pulls" || job.NextAttemptAt == nil || job.Progress["issues"] != 3 {
		t.Fatalf("deferred: %s %s %v %v", job.Status, job.Phase, job.NextAttemptAt, job.Progress)
	}
	def := h.events("io.ore.import.deferred")
	if len(def) != 1 || data(t, def[0])["retry_after_seconds"] != float64(60) {
		t.Fatalf("deferred event: %+v", def)
	}
	if n := h.svc.ResumeDeferredImports(context.Background()); n != 0 {
		t.Fatalf("resumed before Retry-After: %d", n)
	}
	h.tick(61 * time.Second)
	if n := h.svc.ResumeDeferredImports(context.Background()); n != 1 {
		t.Fatalf("resume after Retry-After: %d", n)
	}
	var done ImportJob
	h.api("GET", "/api/v0/repos/acme/app/import/jobs/"+job.ID, nil, &done)
	if done.Status != JobCompleted || done.Progress["pull_requests"] != 2 || done.Progress["issues"] != 3 || h.count(`SELECT COUNT(1) FROM issues`) != 3 {
		t.Fatalf("after resume: %s %v", done.Status, done.Progress)
	}
}

func TestImportNumberContinuity(t *testing.T) {
	h, _, _ := importHarness(t)
	// A native issue already holds #1: numbers cannot all be preserved and the plan says so.
	var native ledger.Issue
	h.api("POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": "native first"}, &native)
	var out planResponse
	h.api("POST", "/api/v0/repos/acme/app/import/plan", ImportRequest{Source: "github.com/octo/app"}, &out)
	if out.Report.NumbersPreserved || linesByObject(out.Report)["issues"].Fidelity != FidelityHigh || len(out.Report.Warnings) < 2 {
		t.Fatalf("plan with native issues: %+v", out.Report)
	}
	job := h.runImport("github.com/octo/app", ImportOptions{})
	if job.Status != JobCompleted || job.Progress["issues_renumbered"] != 1 || job.Progress["issues"] != 3 {
		t.Fatalf("job: %s %v", job.Status, job.Progress)
	}
	var issues []ledger.Issue
	h.api("GET", "/api/v0/repos/acme/app/issues", nil, &issues)
	// native #1, GitHub #1 → #6 (next free, no reservation without preserved numbers), GitHub #2 and #5 keep theirs.
	numbers := map[int]string{}
	for _, i := range issues {
		numbers[i.Number] = i.Title
	}
	if len(issues) != 4 || numbers[1] != "native first" || numbers[2] != "Old question" || numbers[5] != "Document the retry policy" || numbers[6] != "Flaky retry in the uploader" {
		t.Fatalf("numbers: %v", numbers)
	}
	m, ok, _ := h.svc.store.mapping(context.Background(), h.must("acme", "app").ID, "issue", "1")
	if !ok || m.TargetNumber != 6 || m.Extra["preserved"] != false {
		t.Fatalf("mapping for GitHub #1: %+v", m)
	}
	var next ledger.Issue
	h.api("POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": "after import"}, &next)
	if next.Number != 7 {
		t.Fatalf("next native number: %d", next.Number)
	}
	// The webhook path still resolves GitHub #1 to Ore #6.
	link := h.mustLink("acme", "app")
	h.deliver("issues", "d-close", link.Secret, payload(t, "issues_closed.json", nil, map[string]any{"repository.full_name": "octo/app", "repository.name": "app", "repository.owner.login": "octo", "issue.number": float64(1), "issue.title": "Flaky retry in the uploader"}))
	var six ledger.Issue
	h.api("GET", "/api/v0/repos/acme/app/issues/6", nil, &six)
	if six.State != "closed" || numbers[1] != "native first" {
		t.Fatalf("webhook on renumbered issue: %+v", six)
	}
}

func TestImportReviewCommentOnVanishedDiffMarkedMigrated(t *testing.T) {
	h, _, _ := importHarness(t)
	job := h.runImport("github.com/octo/app", ImportOptions{Releases: boolp(false), Webhooks: boolp(false)})
	if job.Status != JobCompleted || job.Progress["review_comments_migrated"] != 1 || job.Progress["review_comments"] != 2 {
		t.Fatalf("job: %s %v", job.Status, job.Progress)
	}
	m, ok, _ := h.svc.store.mapping(context.Background(), h.must("acme", "app").ID, "review_comment", "502")
	if !ok || m.Extra["anchor"] != "migrated" || !strings.Contains(m.Extra["original_anchor"].(string), "uploader.go:9") {
		t.Fatalf("migrated mapping: %+v", m)
	}
	m, ok, _ = h.svc.store.mapping(context.Background(), h.must("acme", "app").ID, "review_comment", "501")
	if !ok || m.Extra["anchor"] != "exact" {
		t.Fatalf("exact mapping: %+v", m)
	}
	// The unreachable pre-force-push head is reported, not fabricated.
	var found bool
	for _, e := range job.Errors {
		if e.Phase == "pulls" && e.Object == "#3" && strings.Contains(e.Error, "earlier head") {
			found = true
		}
	}
	if !found || job.Progress["releases"] != 0 || job.Progress["webhooks"] != 0 {
		t.Fatalf("errors: %+v progress: %v", job.Errors, job.Progress)
	}
}

func boolp(b bool) *bool { return &b }

func TestImportAssetDownloadFailureContinues(t *testing.T) {
	h, _, up := importHarness(t)
	up.data().assetFail[40011] = 500 // app.tar.gz
	job := h.runImport("github.com/octo/app", ImportOptions{})
	if job.Status != JobCompleted || job.Progress["assets"] != 1 || job.Progress["assets_failed"] != 1 || job.Progress["identities"] != 3 {
		t.Fatalf("job: %s %v %s", job.Status, job.Progress, job.Error)
	}
	var found bool
	for _, e := range job.Errors {
		if e.Phase == "releases" && e.Object == "v1.0.0/app.tar.gz" && strings.Contains(e.Error, "500") {
			found = true
		}
	}
	if !found {
		t.Fatalf("asset error not recorded: %+v", job.Errors)
	}
	var rels []ImportedRelease
	h.api("GET", "/api/v0/repos/acme/app/import/releases", nil, &rels)
	if len(rels) != 1 || len(rels[0].Assets) != 2 || rels[0].Assets[0].Status != "failed" || rels[0].Assets[0].Error == "" || rels[0].Assets[1].Status != "ok" {
		t.Fatalf("assets: %+v", rels[0].Assets)
	}
	var rep VerifyReport
	h.api("POST", "/api/v0/repos/acme/app/import/verify", ImportRequest{Job: job.ID}, &rep)
	if rep.OK || len(rep.Releases.Mismatched) != 1 || !strings.Contains(rep.Releases.Mismatched[0], "app.tar.gz not downloaded") || rep.Issues.Matched != 3 {
		t.Fatalf("verify: %+v", rep)
	}
	// Once the asset is available again, a resume (re-run) downloads it and verify is clean.
	delete(up.data().assetFail, 40011)
	again := h.runImport("github.com/octo/app", ImportOptions{})
	if again.Progress["assets"] != 1 || again.Progress["assets_failed"] != 0 {
		t.Fatalf("retry: %v", again.Progress)
	}
	h.api("POST", "/api/v0/repos/acme/app/import/verify", ImportRequest{Job: again.ID}, &rep)
	if !rep.OK {
		t.Fatalf("verify after retry: %+v", rep)
	}
}

func TestImportEmptyRepository(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.addRepo("octo", "empty", nil)
	h := newHarness(t, gh, Config{Token: testToken})
	h.createRepo("acme", "app")
	var out planResponse
	if code := h.api("POST", "/api/v0/repos/acme/app/import/plan", ImportRequest{Source: "github.com/octo/empty"}, &out); code != 200 {
		t.Fatalf("plan: %d", code)
	}
	l := linesByObject(out.Report)
	if l["issues"].Count != 0 || l["pull_requests"].Count != 0 || l["releases"].Count != 0 || l["workflows"].Count != 0 || out.Report.MaxNumber != 0 || out.Report.Score != 1 {
		t.Fatalf("empty plan: %+v", out.Report)
	}
	job := h.runImport("github.com/octo/empty", ImportOptions{})
	if job.Status != JobCompleted || job.Progress["branches"] != 1 || job.Progress["issues"] != 0 || len(job.Errors) != 0 || h.count(`SELECT COUNT(1) FROM issues`) != 0 {
		t.Fatalf("job: %s %v %+v", job.Status, job.Progress, job.Errors)
	}
	var rep VerifyReport
	h.api("POST", "/api/v0/repos/acme/app/import/verify", ImportRequest{}, &rep)
	if !rep.OK || rep.Refs.Matched != 1 || rep.Issues.Checked != 0 {
		t.Fatalf("verify: %+v", rep)
	}
	var native ledger.Issue
	h.api("POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": "first"}, &native)
	if native.Number != 1 {
		t.Fatalf("no reservation expected on an empty source: %d", native.Number)
	}
}

func TestImportWebhooksNotReadable(t *testing.T) {
	h, _, up := importHarness(t)
	up.data().hooksForbidden = true
	var out planResponse
	h.api("POST", "/api/v0/repos/acme/app/import/plan", ImportRequest{Source: "github.com/octo/app"}, &out)
	if l := linesByObject(out.Report)["webhooks"]; l.Fidelity != FidelityNone || !strings.Contains(l.Reason, "admin:repo_hook") {
		t.Fatalf("webhooks line: %+v", l)
	}
	job := h.runImport("github.com/octo/app", ImportOptions{})
	var noted bool
	for _, e := range job.Errors {
		if e.Phase == "webhooks" && strings.Contains(e.Error, "admin:repo_hook") {
			noted = true
		}
	}
	if job.Status != JobCompleted || !noted || job.Progress["webhooks"] != 0 {
		t.Fatalf("job: %s %+v", job.Status, job.Errors)
	}
}

func TestImportWikiCloned(t *testing.T) {
	h, gh, up := importHarness(t)
	up.data().hasWiki = true
	wiki := gh.addRepo("octo", "app.wiki", map[string]string{"Home.md": "# Wiki\n"})
	_ = wiki
	var out planResponse
	h.api("POST", "/api/v0/repos/acme/app/import/plan", ImportRequest{Source: "github.com/octo/app"}, &out)
	if linesByObject(out.Report)["wiki"].Count != 1 {
		t.Fatalf("wiki line: %+v", linesByObject(out.Report)["wiki"])
	}
	job := h.runImport("github.com/octo/app", ImportOptions{Issues: boolp(false), Pulls: boolp(false), Releases: boolp(false)})
	if job.Status != JobCompleted || job.Progress["wiki"] != 1 {
		t.Fatalf("job: %s %v %+v", job.Status, job.Progress, job.Errors)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "repos", "acme", "app.wiki.git", "HEAD")); err != nil {
		t.Fatalf("wiki not cloned: %v", err)
	}
	// Second run fetches into the existing wiki clone.
	if again := h.runImport("github.com/octo/app", ImportOptions{Issues: boolp(false), Pulls: boolp(false), Releases: boolp(false)}); again.Status != JobCompleted || again.Progress["wiki"] != 1 {
		t.Fatalf("second wiki run: %+v", again)
	}
}

func TestImportListsJobs(t *testing.T) {
	h, _, _ := importHarness(t)
	h.api("POST", "/api/v0/repos/acme/app/import/plan", ImportRequest{Source: "github.com/octo/app"}, nil)
	h.runImport("github.com/octo/app", ImportOptions{Pulls: boolp(false), Releases: boolp(false)})
	var jobs []ImportJob
	if code := h.api("GET", "/api/v0/repos/acme/app/import/jobs", nil, &jobs); code != 200 || len(jobs) != 2 || jobs[0].Status != JobCompleted || jobs[1].Status != JobPlanned {
		t.Fatalf("jobs: %d %+v", code, jobs)
	}
	h.createRepo("acme", "other")
	if code := h.api("GET", "/api/v0/repos/acme/other/import/jobs/"+jobs[0].ID, nil, nil); code != 404 {
		t.Fatalf("job of another repository: %d", code)
	}
	var out map[string]any
	if code := h.api("POST", "/api/v0/repos/acme/other/import/verify", ImportRequest{}, &out); code != 404 {
		t.Fatalf("verify without import: %d %v", code, out)
	}
}

func TestImportWithoutToken(t *testing.T) {
	gh := newFakeGitHub(t)
	seedImportRepo(t, gh)
	h := newHarness(t, gh, Config{})
	h.createRepo("acme", "app")
	var out map[string]any
	if code := h.api("POST", "/api/v0/repos/acme/app/import/plan", ImportRequest{Source: "github.com/octo/app"}, &out); code != 503 || !strings.Contains(out["error"].(string), EnvToken) {
		t.Fatalf("plan without token: %d %v", code, out)
	}
}
