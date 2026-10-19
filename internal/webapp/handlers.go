// SPDX-License-Identifier: AGPL-3.0-only

package webapp

import (
	"errors"
	"net/http"
	"strings"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/web"
)

type actorT = ledger.Actor

type actorHandler func(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error
type repoHandler func(w http.ResponseWriter, r *http.Request, s *web.Server, repo ledger.Repo, actor ledger.Actor) error

// serverT wraps *web.Server so the helper methods below read naturally at the
// call site (s.withActor, s.withRepo) without touching package web.
type serverT struct{ *web.Server }

func (s serverT) withActor(fn actorHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := s.RequireActor(w, r)
		if actor == nil {
			return
		}
		if err := fn(w, r, *actor); err != nil {
			writeErr(w, err)
		}
	}
}

func (s serverT) withRepo(fn repoHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := s.RequireActor(w, r)
		if actor == nil {
			return
		}
		repo, err := s.DB.RepoByName(r.Context(), r.PathValue("org"), r.PathValue("name"))
		if err != nil {
			writeErr(w, err)
			return
		}
		if err := fn(w, r, s.Server, repo, *actor); err != nil {
			writeErr(w, err)
		}
	}
}

var errBadRequest = errors.New("bad request")

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, ledger.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, errBadRequest):
		code = http.StatusBadRequest
	case strings.Contains(err.Error(), "exists on disk, but not in") || strings.Contains(err.Error(), "does not exist in") || strings.Contains(err.Error(), "unknown revision") || strings.Contains(err.Error(), "bad revision") || strings.Contains(err.Error(), "Not a valid object name"):
		code = http.StatusNotFound
	}
	web.JSONError(w, code, err)
}
