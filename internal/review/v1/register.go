// SPDX-License-Identifier: AGPL-3.0-only

// Package reviewv1 is the review v1 slice's extension: it wires the review
// service's patch sets, interdiffs, comments, structured reviews, stacks and
// landing strategies into the HTTP API through internal/registry, and
// publishes the service as s.Services["review.landing"] so the agent loop
// can pick a landing strategy. It is a subpackage because internal/registry
// imports internal/web, which imports internal/review: registering from
// package review itself would be an import cycle.
package reviewv1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/review"
	"github.com/orevcs/ore/internal/web"
)

func init() { registry.Register("review.v1", Extension) }

// Extension attaches review v1 to a server. It is exported so tests can
// apply it to an httptest server without the registry.
func Extension(ctx context.Context, s *web.Server) error {
	if s.Review == nil {
		s.Review = &review.Service{DB: s.DB, ReposDir: s.ReposDir}
	}
	if s.Review.Verifier == nil && s.Runner != nil {
		s.Review.Verifier = s.Runner
	}
	if s.Services == nil {
		s.Services = map[string]any{}
	}
	s.Services["review.landing"] = s.Review // *review.Service: Land(ctx, repo, change, LandRequest, actor)
	s.Services["review.v1"] = s.Review
	s.AddRoutes(func(mux *http.ServeMux) { routes(s, mux) })
	return nil
}

type handler func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, actor ledger.Actor) error

func routes(s *web.Server, mux *http.ServeMux) {
	handle := func(pattern string, fn handler) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			actor := s.RequireActor(w, r)
			if actor == nil {
				return
			}
			repo, err := s.DB.RepoByName(r.Context(), r.PathValue("org"), r.PathValue("name"))
			if err != nil {
				writeErr(w, err)
				return
			}
			if err := fn(w, r, repo, *actor); err != nil {
				writeErr(w, err)
			}
		})
	}
	change := func(r *http.Request, repo ledger.Repo) (ledger.Change, error) {
		n, _ := strconv.Atoi(r.PathValue("n"))
		return s.DB.Change(r.Context(), repo, n)
	}
	svc := func() *review.Service { return s.Review }
	const base = "/api/v0/repos/{org}/{name}"

	// policy
	handle("GET "+base+"/review/policy", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		p, err := svc().Policy(r.Context(), repo)
		if err != nil {
			return err
		}
		return web.JSON(w, 200, p)
	})
	handle("PUT "+base+"/review/policy", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		var in review.Policy
		if err := decode(r, &in, false); err != nil {
			return err
		}
		p, err := svc().SetPolicy(r.Context(), repo, in, a)
		if err != nil {
			return err
		}
		return web.JSON(w, 200, p)
	})

	// patch sets and interdiffs
	handle("GET "+base+"/changes/{n}/patchsets", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		c, err := change(r, repo)
		if err != nil {
			return err
		}
		sets, err := svc().PatchSets(r.Context(), repo, c)
		if err != nil {
			return err
		}
		if sets == nil {
			sets = []review.PatchSet{}
		}
		return web.JSON(w, 200, sets)
	})
	handle("GET "+base+"/changes/{n}/interdiff", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		c, err := change(r, repo)
		if err != nil {
			return err
		}
		q := r.URL.Query()
		from, _ := strconv.Atoi(q.Get("from"))
		to, _ := strconv.Atoi(q.Get("to"))
		if from <= 0 {
			return fmt.Errorf("%w: from=<patch set> is required", review.ErrBadRequest)
		}
		format := q.Get("format")
		id, err := svc().Interdiff(r.Context(), repo, c, from, to, format == "range-diff" || q.Get("range_diff") == "1")
		if err != nil {
			return err
		}
		switch format {
		case "diff":
			w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
			_, err = w.Write([]byte(id.Diff))
			return err
		case "range-diff":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, err = w.Write([]byte(id.RangeDiff))
			return err
		}
		return web.JSON(w, 200, id)
	})

	// comments
	handle("GET "+base+"/changes/{n}/comments", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		c, err := change(r, repo)
		if err != nil {
			return err
		}
		list, err := svc().Comments(r.Context(), repo, c)
		if err != nil {
			return err
		}
		return web.JSON(w, 200, list)
	})
	handle("POST "+base+"/changes/{n}/comments", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		c, err := change(r, repo)
		if err != nil {
			return err
		}
		var in review.CommentRequest
		if err := decode(r, &in, false); err != nil {
			return err
		}
		cm, err := svc().PostComment(r.Context(), repo, c, in, a)
		if err != nil {
			return err
		}
		return web.JSON(w, http.StatusCreated, cm)
	})
	handle("POST "+base+"/changes/{n}/comments/{id}/resolve", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		c, err := change(r, repo)
		if err != nil {
			return err
		}
		var in struct {
			Resolved *bool `json:"resolved"`
		}
		_ = decode(r, &in, true)
		resolved := in.Resolved == nil || *in.Resolved
		cm, err := svc().ResolveComment(r.Context(), repo, c, r.PathValue("id"), resolved, a)
		if err != nil {
			return err
		}
		return web.JSON(w, 200, cm)
	})

	// reviews and approvals
	handle("GET "+base+"/changes/{n}/reviews", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		c, err := change(r, repo)
		if err != nil {
			return err
		}
		list, err := svc().Reviews(r.Context(), repo, c)
		if err != nil {
			return err
		}
		return web.JSON(w, 200, list)
	})
	handle("POST "+base+"/changes/{n}/reviews", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		c, err := change(r, repo)
		if err != nil {
			return err
		}
		var in review.ReviewRequest
		if err := decode(r, &in, false); err != nil {
			return err
		}
		rv, err := svc().SubmitReview(r.Context(), repo, c, in, a)
		if err != nil {
			return err
		}
		return web.JSON(w, http.StatusCreated, rv)
	})
	handle("GET "+base+"/changes/{n}/approvals", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		c, err := change(r, repo)
		if err != nil {
			return err
		}
		st, err := svc().Approvals(r.Context(), repo, c)
		if err != nil {
			return err
		}
		return web.JSON(w, 200, st)
	})

	// landing
	handle("POST "+base+"/changes/{n}/land", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		c, err := change(r, repo)
		if err != nil {
			return err
		}
		var in review.LandRequest
		if err := decode(r, &in, true); err != nil {
			return err
		}
		l, err := svc().Land(r.Context(), repo, c, in, a)
		c, _ = s.DB.Change(r.Context(), repo, c.Number)
		if err != nil {
			var le *review.LandingError
			if errors.As(err, &le) {
				return web.JSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "landing": le.Landing, "change": c})
			}
			return err
		}
		return web.JSON(w, 200, map[string]any{"landing": l, "change": c})
	})
	handle("GET "+base+"/changes/{n}/landings", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		c, err := change(r, repo)
		if err != nil {
			return err
		}
		list, err := svc().Landings(r.Context(), repo, c)
		if err != nil {
			return err
		}
		return web.JSON(w, 200, list)
	})

	// stacks
	handle("GET "+base+"/stacks", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		list, err := svc().Stacks(r.Context(), repo)
		if err != nil {
			return err
		}
		return web.JSON(w, 200, list)
	})
	handle("POST "+base+"/stacks", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		var in review.StackRequest
		if err := decode(r, &in, false); err != nil {
			return err
		}
		st, err := svc().OpenStack(r.Context(), repo, in, a)
		if err != nil {
			return err
		}
		return web.JSON(w, http.StatusCreated, st)
	})
	handle("GET "+base+"/stacks/{id}", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		st, err := svc().Stack(r.Context(), repo, r.PathValue("id"))
		if err != nil {
			return err
		}
		return web.JSON(w, 200, st)
	})
	handle("POST "+base+"/stacks/{id}/land", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		st, err := svc().Stack(r.Context(), repo, r.PathValue("id"))
		if err != nil {
			return err
		}
		var in review.LandRequest
		if err := decode(r, &in, true); err != nil {
			return err
		}
		st, landings, err := svc().LandStack(r.Context(), repo, st, in, a)
		if err != nil {
			var se *review.StackError
			if errors.As(err, &se) {
				return web.JSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "stack": se.Stack, "landings": se.Landings})
			}
			return err
		}
		return web.JSON(w, 200, map[string]any{"stack": st, "landings": landings})
	})
}

// decode reads a strict JSON body; optional allows an empty body.
func decode(r *http.Request, v any, optional bool) error {
	err := web.Decode(r, v)
	if err == nil {
		return nil
	}
	if optional && errors.Is(err, io.EOF) {
		return nil
	}
	var se *json.SyntaxError
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &se) {
		return fmt.Errorf("%w: invalid JSON body: %v", review.ErrBadRequest, err)
	}
	return fmt.Errorf("%w: %v", review.ErrBadRequest, err)
}

func writeErr(w http.ResponseWriter, err error) {
	var ce *review.ConflictError
	var pe *review.PolicyError
	var le *review.LandingError
	var se *review.StackError
	switch {
	case errors.Is(err, ledger.ErrNotFound):
		web.JSONError(w, http.StatusNotFound, err)
	case errors.Is(err, review.ErrBadRequest):
		web.JSONError(w, http.StatusBadRequest, err)
	case errors.As(err, &pe):
		_ = web.JSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "approvals": pe.Status})
	case errors.As(err, &le):
		_ = web.JSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "landing": le.Landing})
	case errors.As(err, &se):
		_ = web.JSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "stack": se.Stack, "landings": se.Landings})
	case errors.As(err, &ce), errors.Is(err, review.ErrNothingToMerge), errors.Is(err, review.ErrSourceGone), errors.Is(err, review.ErrNotOpen),
		errors.Is(err, review.ErrVerificationFailed), errors.Is(err, review.ErrReverted), errors.Is(err, review.ErrStackMoved), errors.Is(err, review.ErrNoVerifier):
		web.JSONError(w, http.StatusConflict, err)
	default:
		web.JSONError(w, http.StatusInternalServerError, err)
	}
}
