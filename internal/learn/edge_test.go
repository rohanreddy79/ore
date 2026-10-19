// SPDX-License-Identifier: AGPL-3.0-only

package learn

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/ledger"
)

// completeFirstLesson finishes "your first change" for the admin learner
// through the API alone (server-side commits), for tests that need a
// completed run or a satisfied prerequisite.
func completeFirstLesson(t *testing.T, e *env) Run {
	t.Helper()
	res := e.start(adminToken, StartRequest{Lesson: "git-and-forge/your-first-change"}, 201)
	run := res.Run
	e.must(adminToken, "POST", "/api/v0/repos/"+run.Repo+"/commits", map[string]any{"branch": "learn/first-change-admin", "message": "learn: add hello.md", "files": []map[string]string{{"path": "learn/hello.md", "content": "Hello\n"}}}, 201, nil)
	e.waitStep(adminToken, run.ID, 3)
	e.must(adminToken, "POST", "/api/v0/repos/"+run.Repo+"/changes", map[string]string{"source_ref": "learn/first-change-admin"}, 201, nil)
	e.waitStep(adminToken, run.ID, 4)
	e.must(adminToken, "POST", "/api/v0/repos/"+run.Repo+"/changes/1/merge", map[string]string{}, 200, nil)
	return e.waitCompleted(adminToken, run.ID).Run
}

func TestEdgeAssertMissingRepository(t *testing.T) {
	e := newEnv(t)
	env := &AssertEnv{Ctx: e.ctx, DB: e.db, ReposDir: e.s.ReposDir}
	expr, _ := ParseAssert("ref.exists('x')")
	if _, err := env.Evaluate(expr); err == nil || !strings.Contains(err.Error(), "no repository") {
		t.Fatalf("expected an error without a repository, got %v", err)
	}
	res := e.start(adminToken, StartRequest{Lesson: "git-and-forge/your-first-change"}, 201)
	run := res.Run
	// the repository directory disappears underneath the run
	if err := os.RemoveAll(filepath.Join(e.s.ReposDir, "acme", strings.TrimPrefix(run.Repo, "acme/")+".git")); err != nil {
		t.Fatal(err)
	}
	code, body := e.call(adminToken, "POST", "/api/v0/learn/runs/"+run.ID+"/check", map[string]any{})
	if code != 422 || !strings.Contains(body, "missing on disk") {
		t.Fatalf("check on a missing repository: %d %s", code, body)
	}
	// and when the repository row is gone the error names the run's repository id
	e.db.Exec(e.ctx, `DELETE FROM lesson_runs WHERE id = ?`, run.ID)
	if _, err := e.svc.Check(e.ctx, run.ID, "check"); err == nil {
		t.Fatal("check on a deleted run must fail")
	}
}

func TestEdgePrerequisiteNotCompleted(t *testing.T) {
	e := newEnv(t)
	code, body := e.call(adminToken, "POST", "/api/v0/learn/runs", StartRequest{Lesson: "git-and-forge/branches-and-protection"})
	if code != 409 || field(body, "prerequisite") != "git-and-forge/your-first-change" || !strings.Contains(body, "Your first change") {
		t.Fatalf("expected a 409 naming the prerequisite: %d %s", code, body)
	}
	var lessons []LessonSummary
	e.must(adminToken, "GET", "/api/v0/learn/lessons", nil, 200, &lessons)
	if lessons[1].Status != "locked" {
		t.Fatalf("locked: %+v", lessons[1])
	}
	completeFirstLesson(t, e)
	e.start(adminToken, StartRequest{Lesson: "git-and-forge/branches-and-protection"}, 201)
}

func TestEdgeHintsExhausted(t *testing.T) {
	e := newEnv(t)
	res := e.start(adminToken, StartRequest{Lesson: "git-and-forge/your-first-change", Language: "pt-BR"}, 201)
	run := res.Run
	var h Hint
	e.must(adminToken, "POST", "/api/v0/learn/runs/"+run.ID+"/hint", map[string]any{}, 200, &h)
	if h.Index != 1 || h.Remaining != 1 || h.Language != "pt-BR" || !strings.Contains(h.Text, "git push -u origin HEAD") {
		t.Fatalf("hint 1: %+v", h)
	}
	e.must(adminToken, "POST", "/api/v0/learn/runs/"+run.ID+"/hint", map[string]any{}, 200, &h)
	if h.Index != 2 || h.Remaining != 0 {
		t.Fatalf("hint 2: %+v", h)
	}
	code, body := e.call(adminToken, "POST", "/api/v0/learn/runs/"+run.ID+"/hint", map[string]any{})
	if code != 409 || !strings.Contains(body, "hints exhausted") {
		t.Fatalf("third hint: %d %s", code, body)
	}
	v := e.view(adminToken, run.ID, "")
	if v.HintsShown != 2 || v.HintsLeft != 0 || v.Run.HintsUsed != 2 || len(v.Current.Hints) != 2 {
		t.Fatalf("view: %+v", v)
	}
	if evs := e.lessonEvents("io.ore.lesson.run.hint.used"); len(evs) != 2 {
		t.Fatalf("hint events: %d", len(evs))
	}
	// a failed explicit check unlocks the automatic hint after hint_after_failures
	var out CheckOutcome
	e.must(adminToken, "POST", "/api/v0/learn/runs/"+run.ID+"/check", map[string]any{}, 200, &out)
	if out.Passed || out.Run.Attempts != 1 || out.HintReady || len(out.Result.Checks) != 1 || out.Result.Checks[0].Result != false {
		t.Fatalf("check: %s", fmtJSON(out))
	}
	e.must(adminToken, "POST", "/api/v0/learn/runs/"+run.ID+"/check", map[string]any{}, 200, &out)
	if !out.HintReady || out.Run.Attempts != 2 {
		t.Fatalf("second check: %s", fmtJSON(out))
	}
}

func TestEdgeSameLessonStartedTwice(t *testing.T) {
	e := newEnv(t)
	first := e.start(adminToken, StartRequest{Lesson: "git-and-forge/your-first-change"}, 201)
	second := e.start(adminToken, StartRequest{Lesson: "git-and-forge/your-first-change"}, 200)
	if !second.Resumed || second.Run.ID != first.Run.ID || !strings.Contains(second.Note, "resuming") {
		t.Fatalf("second start: %+v", second)
	}
	var runs []Run
	e.must(adminToken, "GET", "/api/v0/learn/runs", nil, 200, &runs)
	if len(runs) != 1 {
		t.Fatalf("runs: %d", len(runs))
	}
	if n, _ := e.db.Repos(e.ctx, "acme"); len(n) != 1 {
		t.Fatalf("a resumed run must not create another scratch repository: %d", len(n))
	}
	// another learner gets their own run and repository
	other := e.user("other", "acme")
	theirs := e.start(other, StartRequest{Lesson: "git-and-forge/your-first-change"}, 201)
	if theirs.Run.ID == first.Run.ID || theirs.Run.Repo == first.Run.Repo {
		t.Fatalf("other learner: %+v", theirs.Run)
	}
	if code, _ := e.call(other, "GET", "/api/v0/learn/runs/"+first.Run.ID, nil); code != 403 {
		t.Fatalf("a run is private to its learner: %d", code)
	}
}

func TestEdgeInvalidAssertInManifest(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "bad", "steps"), 0o755)
	os.WriteFile(filepath.Join(dir, "bad", "lesson.yaml"), []byte("id: x/bad\nversion: 0.1.0\ntrack: 9-x\nsteps:\n  - id: one\n    assert:\n      query: ref.exists('a') && change.opened('a')\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "bad", "steps", "en.md"), []byte("---\ntitle: bad\n---\n## Step 1 — a\nx\n"), 0o644)
	_, err := LoadCatalog(dir)
	if err == nil || !strings.Contains(err.Error(), "lesson.yaml:7") || !strings.Contains(err.Error(), "unknown function change.opened") {
		t.Fatalf("expected a line-numbered unknown-function error, got %v", err)
	}
	// the extension refuses to start with a broken lessons directory
	t.Setenv("ORE_LESSONS_DIR", dir)
	if _, err := LoadCatalog(os.Getenv("ORE_LESSONS_DIR")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestEdgeLanguageNotAvailable(t *testing.T) {
	e := newEnv(t)
	res := e.start(adminToken, StartRequest{Lesson: "git-and-forge/your-first-change", Language: "zh-Hans"}, 201)
	if !res.LanguageFallback || res.RequestedLanguage != "zh-Hans" || res.Run.Language != "en" || !strings.Contains(res.Note, "not available in zh-Hans") || !strings.Contains(res.Note, "en, hi, pt-BR") {
		t.Fatalf("fallback: %+v", res)
	}
	if v := e.view(adminToken, res.Run.ID, "ja"); !v.Fallback || v.Language != "en" || !strings.Contains(v.Current.Title, "Clone") {
		t.Fatalf("view: %+v", v)
	}
	code, body := e.call(adminToken, "GET", "/api/v0/learn/lessons/git-and-forge/your-first-change?lang=ja", nil)
	if code != 200 || field(body, "language_fallback") != true || !strings.Contains(body, "not available in ja") {
		t.Fatalf("lesson: %d %s", code, body)
	}
	req, _ := newRequest(e, "GET", "/api/v0/learn/lessons/git-and-forge/your-first-change/steps.md?lang=ja")
	resp, err := httpDo(req)
	if err != nil {
		t.Fatal(err)
	}
	body = resp.body
	if resp.code != 200 || resp.header.Get("X-Ore-Language-Fallback") != "en" || resp.header.Get("Content-Language") != "en" || !strings.HasPrefix(body, "<!-- git-and-forge/your-first-change is not available in ja") || !strings.Contains(body, "## Step 1") {
		t.Fatalf("steps.md: %d %v %s", resp.code, resp.header, body[:80])
	}
	req, _ = newRequest(e, "GET", "/api/v0/learn/lessons/git-and-forge/your-first-change/steps.md?lang=hi")
	resp, _ = httpDo(req)
	if resp.header.Get("Content-Language") != "hi" || resp.header.Get("X-Ore-Language-Fallback") != "" || !strings.Contains(resp.body, "## चरण 1") {
		t.Fatalf("hindi steps.md: %v", resp.header)
	}
}

func TestEdgeWorkerNoActiveRuns(t *testing.T) {
	e := newEnv(t)
	e.must(adminToken, "POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "plain"}, 201, nil)
	e.must(adminToken, "POST", "/api/v0/repos/acme/plain/commits", map[string]any{"branch": "main", "message": "init", "files": []map[string]string{{"path": "README.md", "content": "x"}}}, 201, nil)
	e.must(adminToken, "POST", "/api/v0/repos/acme/plain/commits", map[string]any{"branch": "learn/first-change-admin", "message": "noise", "files": []map[string]string{{"path": "learn/hello.md", "content": "x"}}}, 201, nil)
	w := &Worker{S: e.svc}
	if err := w.Start(e.ctx); err != nil {
		t.Fatal(err)
	}
	w.cursor = 0 // replay everything
	n, err := w.Tick(e.ctx)
	if err != nil || n != 0 {
		t.Fatalf("evaluated %d runs (%v); a repository without active runs is no work", n, err)
	}
	if evs := e.lessonEvents("io.ore.lesson."); len(evs) != 0 {
		t.Fatalf("lesson events emitted: %d", len(evs))
	}
}

func TestEdgeCompletedRunReceivesLateEvents(t *testing.T) {
	e := newEnv(t)
	run := completeFirstLesson(t, e)
	before := len(e.lessonEvents("io.ore.lesson."))
	// late events on the lesson repository: a new branch and commit
	e.must(adminToken, "POST", "/api/v0/repos/"+run.Repo+"/commits", map[string]any{"branch": "learn/first-change-admin-again", "message": "late", "files": []map[string]string{{"path": "learn/late.md", "content": "x"}}}, 201, nil)
	w := &Worker{S: e.svc}
	w.Start(e.ctx)
	w.cursor = 0
	if n, _ := w.Tick(e.ctx); n != 0 {
		t.Fatalf("a completed run was evaluated %d time(s)", n)
	}
	after, _ := e.svc.Run(e.ctx, run.ID)
	if after.Status != StatusCompleted || after.Step != run.Step || after.UpdatedAt != run.UpdatedAt {
		t.Fatalf("completed run changed: %+v", after)
	}
	if n := len(e.lessonEvents("io.ore.lesson.")); n != before {
		t.Fatalf("late events produced %d lesson events", n-before)
	}
	// an explicit check on a completed run is a no-op that reports completion
	var out CheckOutcome
	e.must(adminToken, "POST", "/api/v0/learn/runs/"+run.ID+"/check", map[string]any{}, 200, &out)
	if !out.Completed || !out.Passed || len(out.Advanced) != 0 {
		t.Fatalf("check on completed: %s", fmtJSON(out))
	}
}

func TestEdgeExplainWithoutLesson(t *testing.T) {
	e := newEnv(t)
	evs := e.lessonEvents("io.ore.org.created")
	if len(evs) == 0 {
		t.Fatal("no org event")
	}
	code, body := e.call(adminToken, "GET", "/api/v0/learn/explain?event="+evs[0].ID, nil)
	if code != 404 || field(body, "nearest_track") != "1-git-and-forge" || !strings.Contains(body, "git-and-forge/your-first-change") || !strings.Contains(body, "no lesson explains events of type io.ore.org.created") {
		t.Fatalf("unknown event type: %d %s", code, body)
	}
	// a known state whose lesson is not shipped yet names its track and the nearest shipped one
	code, body = e.call(adminToken, "GET", "/api/v0/learn/explain?state=deployment.blocked", nil)
	if code != 404 || !strings.Contains(body, "delivery/environments-and-approvals") || field(body, "nearest_track") != "1-git-and-forge" {
		t.Fatalf("unshipped lesson: %d %s", code, body)
	}
	// a shipped state answers with the lesson
	var ex Explanation
	e.must(adminToken, "GET", "/api/v0/learn/explain?state=ref.protected", nil, 200, &ex)
	if ex.Lesson == nil || ex.Lesson.ID != "git-and-forge/branches-and-protection" {
		t.Fatalf("ref.protected: %+v", ex)
	}
	if code, _ := e.call(adminToken, "GET", "/api/v0/learn/explain?event=evt_nope", nil); code != 404 {
		t.Fatalf("missing event: %d", code)
	}
	if code, body := e.call(adminToken, "GET", "/api/v0/learn/explain", nil); code != 200 || !strings.Contains(body, "pipeline.failed.test") {
		t.Fatalf("catalog: %d %s", code, body)
	}
	// classification of failed runs from their task record and log
	dir := t.TempDir()
	logPath := filepath.Join(dir, "run.log")
	os.WriteFile(logPath, []byte("[ore] refused: secrets not defined: TOKEN\n"), 0o644)
	if st := e.svc.classifyRun(e.ctx, "", "[]", logPath); st != "secret.missing" {
		t.Fatal(st)
	}
	if st := e.svc.classifyRun(e.ctx, "", `[{"id":"build","status":"failed","reason":"timeout"}]`, ""); st != "pipeline.failed.timeout" {
		t.Fatal(st)
	}
	if st := e.svc.classifyRun(e.ctx, "", `[{"id":"unit-test","status":"failed"}]`, ""); st != "pipeline.failed.test" {
		t.Fatal(st)
	}
	if st := e.svc.classifyRun(e.ctx, "", `[{"id":"install","status":"failed"}]`, ""); st != "pipeline.failed.dependency" {
		t.Fatal(st)
	}
}

func TestEdgeInPlaceRunOnUnreadableRepository(t *testing.T) {
	e := newEnv(t)
	e.must(adminToken, "POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "app"}, 201, nil)
	e.must(adminToken, "POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "main", "message": "init", "files": []map[string]string{{"path": "README.md", "content": "x"}}}, 201, nil)
	outsider := e.user("outsider") // no membership in acme
	code, body := e.call(outsider, "POST", "/api/v0/learn/runs", StartRequest{Lesson: "git-and-forge/your-first-change", Repo: "acme/app"})
	if code != 403 || !strings.Contains(body, "outsider cannot read acme/app") {
		t.Fatalf("outsider: %d %s", code, body)
	}
	if code, _ := e.call(outsider, "POST", "/api/v0/learn/runs", StartRequest{Lesson: "git-and-forge/your-first-change", Repo: "acme/missing"}); code != 404 {
		t.Fatalf("missing repo: %d", code)
	}
	// a member runs the same lesson in place on the named repository
	member := e.user("member", "acme")
	res := e.start(member, StartRequest{Lesson: "git-and-forge/your-first-change", Repo: "acme/app"}, 201)
	if res.Run.Mode != ModeInPlace || res.Run.Repo != "acme/app" {
		t.Fatalf("in place: %+v", res.Run)
	}
	// the outsider cannot create a scratch repository in acme either
	if code, body := e.call(outsider, "POST", "/api/v0/learn/runs", StartRequest{Lesson: "git-and-forge/your-first-change", Org: "acme"}); code != 403 || !strings.Contains(body, "cannot create repositories in acme") {
		t.Fatalf("scratch in a foreign org: %d %s", code, body)
	}
	// an in_place lesson without a repository is a 400 (none of the built-in lessons, so synthesize)
	e.svc.Catalog.lessons["x/in-place"] = &Lesson{Manifest: &Manifest{ID: "x/in-place", Version: "0", Track: "9", Mode: ModeInPlace, Steps: []ManifestStep{{ID: "s", Assert: Assert{Query: "true"}}}}, Texts: map[string]*Steps{"en": {Language: "en", Title: "x", Steps: []StepText{{Number: 1}}}}}
	if code, body := e.call(member, "POST", "/api/v0/learn/runs", StartRequest{Lesson: "x/in-place"}); code != 400 || !strings.Contains(body, "name the repository") {
		t.Fatalf("in_place without repo: %d %s", code, body)
	}
	_ = context.Background
	_ = ledger.System
}

// field decodes a JSON body and returns one top-level field.
func field(body, key string) any {
	var m map[string]any
	_ = json.Unmarshal([]byte(body), &m)
	return m[key]
}
