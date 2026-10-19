// SPDX-License-Identifier: AGPL-3.0-only

package learn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/web"
)

// Routes mounts the learning API under /api/v0/learn/ and
// /api/v0/repos/{org}/{name}/learn/.
func (s *Service) Routes(srv *web.Server) func(mux *http.ServeMux) {
	return func(mux *http.ServeMux) {
		handle := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error) {
			mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
				actor := srv.RequireActor(w, r)
				if actor == nil {
					return
				}
				if err := fn(w, r, *actor); err != nil {
					writeErr(w, err)
				}
			})
		}
		lang := func(r *http.Request) string { return r.URL.Query().Get("lang") }

		handle("GET /api/v0/learn/lessons", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			list, err := s.Lessons(r.Context(), a, lang(r))
			if err != nil {
				return err
			}
			return web.JSON(w, 200, list)
		})
		handle("GET /api/v0/learn/lessons/{id...}", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			id := r.PathValue("id")
			if strings.HasSuffix(id, "/steps.md") {
				l, ok := s.Catalog.Lesson(strings.TrimSuffix(id, "/steps.md"))
				if !ok {
					return fmt.Errorf("%w: lesson %s", ErrNotFound, id)
				}
				text, fallback := l.Text(lang(r))
				w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
				w.Header().Set("Content-Language", text.Language)
				if fallback {
					w.Header().Set("X-Ore-Language-Fallback", "en")
					fmt.Fprintf(w, "<!-- %s is not available in %s yet; this is the English text (available: %s) -->\n", l.Manifest.ID, lang(r), strings.Join(l.Languages(), ", "))
				}
				_, err := w.Write([]byte(text.Raw))
				return err
			}
			l, ok := s.Catalog.Lesson(id)
			if !ok {
				return fmt.Errorf("%w: lesson %s", ErrNotFound, id)
			}
			text, fallback := l.Text(lang(r))
			sum := s.Summary(l, lang(r))
			s.annotate(r, a, &sum)
			out := map[string]any{"lesson": sum, "manifest": l.Manifest, "language": text.Language, "language_fallback": fallback, "steps": text.Steps, "states": statesFor(l.Manifest.ID)}
			if fallback {
				out["note"] = fmt.Sprintf("this lesson is not available in %s yet; showing English (available: %s)", lang(r), strings.Join(l.Languages(), ", "))
			}
			return web.JSON(w, 200, out)
		})
		handle("POST /api/v0/learn/runs", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			var in StartRequest
			if err := web.Decode(r, &in); err != nil {
				return fmt.Errorf("%w: %v", ErrBadRequest, err)
			}
			res, err := s.Start(r.Context(), in, a)
			if err != nil {
				return err
			}
			code := 201
			if res.Resumed {
				code = 200
			}
			return web.JSON(w, code, res)
		})
		handle("POST /api/v0/repos/{org}/{name}/learn/runs", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			var in StartRequest
			if err := web.Decode(r, &in); err != nil {
				return fmt.Errorf("%w: %v", ErrBadRequest, err)
			}
			in.Repo = r.PathValue("org") + "/" + r.PathValue("name")
			res, err := s.Start(r.Context(), in, a)
			if err != nil {
				return err
			}
			code := 201
			if res.Resumed {
				code = 200
			}
			return web.JSON(w, code, res)
		})
		handle("GET /api/v0/learn/runs", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			runs, err := s.Runs(r.Context(), a, r.URL.Query().Get("status"))
			if err != nil {
				return err
			}
			return web.JSON(w, 200, runs)
		})
		handle("GET /api/v0/repos/{org}/{name}/learn/runs", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			repo, err := s.DB.RepoByName(r.Context(), r.PathValue("org"), r.PathValue("name"))
			if err != nil {
				return err
			}
			runs, err := s.queryRuns(r.Context(), `SELECT `+runCols+` FROM lesson_runs WHERE repo_id = ? ORDER BY started_at DESC`, repo.ID)
			if err != nil {
				return err
			}
			return web.JSON(w, 200, runs)
		})
		own := func(r *http.Request, a ledger.Actor) (Run, error) {
			run, err := s.Run(r.Context(), r.PathValue("id"))
			if err != nil {
				return run, err
			}
			if run.Learner != a {
				return run, &ForbiddenError{Reason: "this run belongs to " + run.Learner.Kind + "/" + run.Learner.ID}
			}
			return run, nil
		}
		handle("GET /api/v0/learn/runs/{id}", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			run, err := own(r, a)
			if err != nil {
				return err
			}
			v, err := s.View(r.Context(), run, lang(r))
			if err != nil {
				return err
			}
			return web.JSON(w, 200, v)
		})
		handle("POST /api/v0/learn/runs/{id}/hint", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			if _, err := own(r, a); err != nil {
				return err
			}
			h, err := s.NextHint(r.Context(), r.PathValue("id"))
			if err != nil {
				return err
			}
			return web.JSON(w, 200, h)
		})
		handle("POST /api/v0/learn/runs/{id}/check", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			if _, err := own(r, a); err != nil {
				return err
			}
			out, err := s.Check(r.Context(), r.PathValue("id"), "check")
			if err != nil {
				if out.Error != "" {
					return web.JSON(w, 422, out)
				}
				return err
			}
			return web.JSON(w, 200, out)
		})
		handle("GET /api/v0/learn/progress", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			p, err := s.ProgressFor(r.Context(), a)
			if err != nil {
				return err
			}
			lessons, _ := s.Lessons(r.Context(), a, lang(r))
			completed := 0
			for _, l := range lessons {
				if l.Status == "completed" {
					completed++
				}
			}
			return web.JSON(w, 200, map[string]any{"learner": a, "progress": p, "lessons": lessons, "completed": completed, "total": len(lessons)})
		})
		handle("GET /api/v0/learn/explain", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			q := r.URL.Query()
			var ex Explanation
			var err error
			switch {
			case q.Get("event") != "":
				ex, err = s.Explain(r.Context(), q.Get("event"), lang(r))
			case q.Get("state") != "":
				ex, err = s.ExplainState(q.Get("state"), lang(r))
			default:
				return web.JSON(w, 200, map[string]any{"states": States, "usage": "GET /api/v0/learn/explain?event=<ledger event id> or ?state=<state>"})
			}
			if err != nil {
				return err
			}
			return web.JSON(w, 200, ex)
		})
	}
}

func statesFor(lessonID string) []State {
	var out []State
	for _, st := range States {
		if st.LessonID == lessonID {
			out = append(out, st)
		}
	}
	if out == nil {
		out = []State{}
	}
	return out
}

// Lessons is the catalog with the learner's status on each lesson.
func (s *Service) Lessons(ctx context.Context, a ledger.Actor, lang string) ([]LessonSummary, error) {
	return s.lessons(ctx, a, lang)
}

func writeErr(w http.ResponseWriter, err error) {
	var refused *RefusedError
	var forbidden *ForbiddenError
	var noLesson *NoLessonError
	switch {
	case errors.As(err, &noLesson):
		ex := noLesson.Explanation
		web.JSON(w, http.StatusNotFound, map[string]any{"error": ex.Message, "state": ex.State, "nearest_track": ex.NearestTrack, "nearest_lessons": ex.Nearest, "event_id": ex.EventID, "event_type": ex.EventType})
	case errors.As(err, &refused):
		web.JSON(w, http.StatusConflict, map[string]any{"error": refused.Message, "code": refused.Code, "prerequisite": refused.Prerequisite})
	case errors.As(err, &forbidden):
		web.JSONError(w, http.StatusForbidden, err)
	case errors.Is(err, ErrHintsExhausted):
		web.JSONError(w, http.StatusConflict, err)
	case errors.Is(err, ErrNotFound):
		web.JSONError(w, http.StatusNotFound, err)
	case errors.Is(err, ErrBadRequest):
		web.JSONError(w, http.StatusBadRequest, err)
	default:
		web.JSONError(w, http.StatusInternalServerError, err)
	}
}

// annotate adds the learner's status to a summary.
func (s *Service) annotate(r *http.Request, a ledger.Actor, sum *LessonSummary) {
	list, err := s.lessons(r.Context(), a, r.URL.Query().Get("lang"))
	if err != nil {
		return
	}
	for _, l := range list {
		if l.ID == sum.ID {
			sum.Status, sum.RunID = l.Status, l.RunID
		}
	}
}
