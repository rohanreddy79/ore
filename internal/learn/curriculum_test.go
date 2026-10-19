// SPDX-License-Identifier: AGPL-3.0-only

package learn

import (
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/ledger"
)

// TestCurriculum runs every lesson of the built-in curriculum end to end on
// a real server: the admin learner clones the scratch repository over HTTP,
// pushes branches and commits with git, and opens, reviews, runs and merges
// through the API, while the worker advances the run from ledger events.
func TestCurriculum(t *testing.T) {
	e := newEnv(t)
	learner := adminToken

	t.Run("your-first-change", func(t *testing.T) {
		res := e.start(learner, StartRequest{Lesson: "git-and-forge/your-first-change", Language: "en"}, 201)
		run := res.Run
		if run.Step != 1 || run.Mode != ModeScratch || !strings.HasPrefix(run.Repo, "acme/learn-your-first-change-admin") || run.Learner.ID != "admin" {
			t.Fatalf("run: %+v", run)
		}
		v := e.view(learner, run.ID, "")
		if v.Current == nil || v.Current.Number != 1 || !strings.Contains(v.Current.Body, "git switch -c learn/first-change") || v.HintsTotal != 2 || v.HintUnlocked {
			t.Fatalf("view: %+v", v)
		}
		if hv := e.view(learner, run.ID, "hi"); hv.Language != "hi" || !strings.Contains(hv.Current.Title, "Clone") || hv.LessonTitle != "आपका पहला change" {
			t.Fatalf("hindi view: %+v", hv)
		}
		dir := e.clone(run.Repo, "admin", adminToken)
		e.git(dir, "switch", "-c", "learn/first-change-admin")
		e.git(dir, "push", "-u", "origin", "HEAD")
		v = e.waitStep(learner, run.ID, 2)
		if v.Run.Step != 2 || v.Current.Number != 2 {
			t.Fatalf("after branch: %+v", v.Run)
		}
		e.write(dir, "learn/hello.md", "Hello from admin\n")
		e.git(dir, "add", "learn/hello.md")
		e.git(dir, "commit", "-s", "-m", "learn: add hello.md")
		e.git(dir, "push")
		e.waitStep(learner, run.ID, 3)
		e.must(learner, "POST", "/api/v0/repos/"+run.Repo+"/changes", map[string]string{"source_ref": "learn/first-change-admin", "title": "learn: add hello.md"}, 201, nil)
		e.waitStep(learner, run.ID, 4)
		e.must(learner, "POST", "/api/v0/repos/"+run.Repo+"/changes/1/merge", map[string]string{}, 200, nil)
		v = e.waitCompleted(learner, run.ID)
		if v.Run.CompletedAt == nil || v.Run.Evidence["credential"] != "track-1-progress" || v.Run.Attempts != 0 {
			t.Fatalf("completed run: %+v", v.Run)
		}
		if evs := e.lessonEvents("io.ore.lesson.run.step.completed"); len(evs) != 4 {
			t.Fatalf("step events: %d", len(evs))
		}
		if evs := e.lessonEvents("io.ore.lesson.run.completed"); len(evs) != 1 || !strings.Contains(string(evs[0].Data), `"credential":"track-1-progress"`) {
			t.Fatalf("completed events: %d", len(evs))
		}
	})

	t.Run("branches-and-protection", func(t *testing.T) {
		res := e.start(learner, StartRequest{Lesson: "git-and-forge/branches-and-protection", Language: "pt-BR"}, 201)
		run := res.Run
		if run.Language != "pt-BR" || res.LanguageFallback {
			t.Fatalf("language: %+v", res)
		}
		dir := e.clone(run.Repo, "admin", adminToken)
		e.write(dir, "README.md", "# Branches and protection\n\ndirect push\n")
		e.git(dir, "commit", "-am", "learn: try a direct push")
		out, err := e.gitErr(dir, "push")
		if err == nil || !strings.Contains(out, "protected") {
			t.Fatalf("expected the protected-ref refusal, got %v\n%s", err, out)
		}
		e.git(dir, "switch", "-c", "learn/protect-admin")
		e.git(dir, "push", "-u", "origin", "HEAD")
		e.waitStep(learner, run.ID, 2)
		e.write(dir, "learn/branches.md", "The default branch is protected: direct pushes are refused.\n")
		e.git(dir, "add", "learn/branches.md")
		e.git(dir, "commit", "-s", "-m", "learn: describe branch protection")
		e.git(dir, "push")
		e.waitStep(learner, run.ID, 3)
		e.must(learner, "POST", "/api/v0/repos/"+run.Repo+"/changes", map[string]string{"source_ref": "learn/protect-admin"}, 201, nil)
		e.waitStep(learner, run.ID, 4)
		e.must(learner, "POST", "/api/v0/repos/"+run.Repo+"/changes/1/merge", map[string]string{}, 200, nil)
		e.waitCompleted(learner, run.ID)
	})

	t.Run("resolving-a-conflict", func(t *testing.T) {
		res := e.start(learner, StartRequest{Lesson: "git-and-forge/resolving-a-conflict"}, 201)
		run := res.Run
		dir := e.clone(run.Repo, "admin", adminToken)
		if log := e.git(dir, "log", "--oneline"); strings.Count(log, "\n") != 2 {
			t.Fatalf("template history: %s", log)
		}
		e.git(dir, "switch", "-c", "learn/conflict-admin", "HEAD~1")
		e.write(dir, "menu.txt", "Today's special: pasta\n")
		e.git(dir, "commit", "-am", "learn: today's special is pasta")
		e.git(dir, "push", "-u", "origin", "HEAD")
		e.waitStep(learner, run.ID, 2)
		e.must(learner, "POST", "/api/v0/repos/"+run.Repo+"/changes", map[string]string{"source_ref": "learn/conflict-admin", "title": "learn: pasta"}, 201, nil)
		v := e.waitStep(learner, run.ID, 3)
		// the lesson's own evidence records the conflict
		if ev := fmtJSON(v.Run.Evidence["2-open-a-conflicting-change"]); !strings.Contains(ev, "conflicts with refs/heads/main in menu.txt") {
			t.Fatalf("evidence: %s", ev)
		}
		e.git(dir, "fetch", "origin")
		if out, err := e.gitErr(dir, "merge", "origin/main"); err == nil || !strings.Contains(out, "CONFLICT") {
			t.Fatalf("expected a merge conflict: %v\n%s", err, out)
		}
		e.write(dir, "menu.txt", "Today's special: pasta and salad\n")
		e.git(dir, "add", "menu.txt")
		e.git(dir, "commit", "-s", "-m", "learn: resolve the special")
		e.git(dir, "push")
		e.waitStep(learner, run.ID, 4)
		e.must(learner, "POST", "/api/v0/repos/"+run.Repo+"/changes/1/merge", map[string]string{}, 200, nil)
		e.waitCompleted(learner, run.ID)
	})

	t.Run("a-change-with-a-review", func(t *testing.T) {
		mentor := e.user("mentor", "acme")
		res := e.start(learner, StartRequest{Lesson: "git-and-forge/a-change-with-a-review"}, 201)
		run := res.Run
		var policy struct {
			RequiredApprovals int `json:"required_approvals"`
		}
		e.must(learner, "GET", "/api/v0/repos/"+run.Repo+"/review/policy", nil, 200, &policy)
		if policy.RequiredApprovals != 1 {
			t.Fatalf("the template's review policy was not applied: %+v", policy)
		}
		dir := e.clone(run.Repo, "admin", adminToken)
		e.git(dir, "switch", "-c", "learn/review-admin")
		e.write(dir, "learn/review.md", "# What a reviewer looks for\n")
		e.git(dir, "add", "learn/review.md")
		e.git(dir, "commit", "-s", "-m", "learn: what a reviewer looks for")
		e.git(dir, "push", "-u", "origin", "HEAD")
		e.waitStep(learner, run.ID, 2)
		e.must(learner, "POST", "/api/v0/repos/"+run.Repo+"/changes", map[string]string{"source_ref": "learn/review-admin"}, 201, nil)
		e.waitStep(learner, run.ID, 3)
		// landing is refused until the mentor approves
		if code, body := e.call(learner, "POST", "/api/v0/repos/"+run.Repo+"/changes/1/land", map[string]any{}); code < 400 {
			t.Fatalf("landing without approval must be refused: %d %s", code, body)
		}
		e.must(mentor, "POST", "/api/v0/repos/"+run.Repo+"/changes/1/reviews", map[string]any{"verdict": "approve", "body": "Looks good."}, 201, nil)
		e.waitStep(learner, run.ID, 4)
		e.must(learner, "POST", "/api/v0/repos/"+run.Repo+"/changes/1/land", map[string]any{}, 200, nil)
		e.waitCompleted(learner, run.ID)
	})

	t.Run("a-green-pipeline", func(t *testing.T) {
		res := e.start(learner, StartRequest{Lesson: "git-and-forge/a-green-pipeline"}, 201)
		run := res.Run
		var red ledger.PipelineRun
		e.must(learner, "POST", "/api/v0/repos/"+run.Repo+"/runs", map[string]any{"wait": true}, 200, &red)
		if red.Status != "failed" {
			t.Fatalf("the template pipeline must be red at first: %+v", red)
		}
		dir := e.clone(run.Repo, "admin", adminToken)
		e.git(dir, "switch", "-c", "learn/pipeline-admin")
		e.write(dir, "learn/pipeline.md", "This pipeline is green.\n")
		e.git(dir, "add", "learn/pipeline.md")
		e.git(dir, "commit", "-s", "-m", "learn: make the pipeline green")
		e.git(dir, "push", "-u", "origin", "HEAD")
		e.waitStep(learner, run.ID, 2)
		var green ledger.PipelineRun
		e.must(learner, "POST", "/api/v0/repos/"+run.Repo+"/runs", map[string]any{"ref": "refs/heads/learn/pipeline-admin", "wait": true}, 200, &green)
		if green.Status != "succeeded" {
			t.Fatalf("green run: %+v", green)
		}
		e.waitStep(learner, run.ID, 3)
		e.must(learner, "POST", "/api/v0/repos/"+run.Repo+"/changes", map[string]string{"source_ref": "learn/pipeline-admin"}, 201, nil)
		e.waitStep(learner, run.ID, 4)
		e.must(learner, "POST", "/api/v0/repos/"+run.Repo+"/changes/1/merge", map[string]string{}, 200, nil)
		e.waitCompleted(learner, run.ID)

		// learn at the error: the red run's event maps to this lesson
		evs := e.lessonEvents("io.ore.pipeline.run.finished")
		var redEvent string
		for _, ev := range evs {
			if strings.Contains(string(ev.Data), `"id":"`+red.ID+`"`) {
				redEvent = ev.ID
			}
		}
		if redEvent == "" {
			t.Fatal("no finished event for the red run")
		}
		var ex Explanation
		e.must(learner, "GET", "/api/v0/learn/explain?event="+redEvent, nil, 200, &ex)
		if ex.State.State != "pipeline.failed.test" || ex.Lesson == nil || ex.Lesson.ID != "git-and-forge/a-green-pipeline" || ex.Lesson.Status != "" {
			t.Fatalf("explain: %+v", ex)
		}
	})

	t.Run("progress", func(t *testing.T) {
		var progress struct {
			Completed int             `json:"completed"`
			Total     int             `json:"total"`
			Lessons   []LessonSummary `json:"lessons"`
		}
		e.must(learner, "GET", "/api/v0/learn/progress", nil, 200, &progress)
		if progress.Completed != 5 || progress.Total != 5 {
			t.Fatalf("progress: %+v", progress)
		}
		for _, l := range progress.Lessons {
			if l.Status != "completed" || l.RunID == "" {
				t.Fatalf("lesson status: %+v", l)
			}
		}
		var runs []Run
		e.must(learner, "GET", "/api/v0/learn/runs?status=completed", nil, 200, &runs)
		if len(runs) != 5 {
			t.Fatalf("runs: %d", len(runs))
		}
		// another learner sees nothing completed and lessons 2-5 locked
		other := e.user("newcomer", "acme")
		var lessons []LessonSummary
		e.must(other, "GET", "/api/v0/learn/lessons?lang=hi", nil, 200, &lessons)
		if len(lessons) != 5 || lessons[0].Status != "available" || lessons[1].Status != "locked" || lessons[0].Title != "आपका पहला change" || strings.Join(lessons[0].Languages, ",") != "en,hi,pt-BR" {
			t.Fatalf("lessons for a newcomer: %s", fmtJSON(lessons))
		}
	})
}
