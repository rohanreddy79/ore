// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/web"
)

// maxEnvelopeBytes bounds a request body: 50 requests of 64 KiB plus envelope.
const maxEnvelopeBytes = 50*MaxCommentBytes + 64*1024

// BearerToken extracts a bearer token from the request, if any.
func BearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return ""
}

// Principal is who is calling a gateway (or MCP) endpoint: a human/admin
// actor, or an agent session.
type Principal struct {
	Actor   ledger.Actor
	Session *Session // nil for admin callers
}

// Authenticate resolves the caller: a gateway session token first, otherwise
// the server's own authenticator (the admin token, or the identity extension).
// It writes a 401 and returns ok=false when neither applies. A closed
// session authenticates (so its use can be refused and recorded); callers
// check Session.State.
func (g *Gateway) Authenticate(s *web.Server, w http.ResponseWriter, r *http.Request) (Principal, bool) {
	if tok := BearerToken(r); IsSessionToken(tok) {
		sess, err := g.SessionByToken(r.Context(), tok)
		if err != nil {
			web.JSONError(w, http.StatusUnauthorized, errors.New("missing or invalid token"))
			return Principal{}, false
		}
		return Principal{Actor: ledger.Actor{Kind: "agent", ID: sess.AgentID}, Session: &sess}, true
	}
	actor := s.RequireActor(w, r)
	if actor == nil {
		return Principal{}, false
	}
	return Principal{Actor: *actor}, true
}

// Routes mounts the gateway's REST API on mux.
func (g *Gateway) Routes(s *web.Server) func(mux *http.ServeMux) {
	return func(mux *http.ServeMux) {
		// create a session for an agent on one repository (admin only)
		mux.HandleFunc("POST /api/v0/gateway/sessions", func(w http.ResponseWriter, r *http.Request) {
			actor := s.RequireActor(w, r)
			if actor == nil {
				return
			}
			var in struct {
				AgentID      string `json:"agent_id"`
				Repo         string `json:"repo"`
				RepositoryID string `json:"repository_id"`
			}
			if err := web.Decode(r, &in); err != nil {
				web.JSONError(w, http.StatusBadRequest, fmt.Errorf("bad request: %v", err))
				return
			}
			if in.AgentID == "" || (in.Repo == "" && in.RepositoryID == "") {
				web.JSONError(w, http.StatusBadRequest, errors.New("agent_id and repo (org/name) or repository_id are required"))
				return
			}
			var repo ledger.Repo
			var err error
			if in.RepositoryID != "" {
				repo, err = s.DB.RepoByID(r.Context(), in.RepositoryID)
			} else {
				org, name, ok := strings.Cut(in.Repo, "/")
				if !ok {
					web.JSONError(w, http.StatusBadRequest, errors.New("repo must be org/name"))
					return
				}
				repo, err = s.DB.RepoByName(r.Context(), org, name)
			}
			if err != nil {
				web.JSONError(w, statusFor(err), err)
				return
			}
			sess, token, err := g.CreateSession(r.Context(), repo, in.AgentID, *actor)
			if err != nil {
				web.JSONError(w, statusFor(err), err)
				return
			}
			web.JSON(w, http.StatusCreated, map[string]any{"session": sess, "token": token})
		})
		mux.HandleFunc("GET /api/v0/gateway/sessions", func(w http.ResponseWriter, r *http.Request) {
			if a := s.RequireActor(w, r); a == nil {
				return
			}
			repoID := ""
			if q := r.URL.Query().Get("repo"); q != "" {
				org, name, _ := strings.Cut(q, "/")
				repo, err := s.DB.RepoByName(r.Context(), org, name)
				if err != nil {
					web.JSONError(w, statusFor(err), err)
					return
				}
				repoID = repo.ID
			}
			list, err := g.Sessions(r.Context(), repoID, 100)
			if err != nil {
				web.JSONError(w, http.StatusInternalServerError, err)
				return
			}
			web.JSON(w, http.StatusOK, list)
		})
		mux.HandleFunc("GET /api/v0/gateway/{session}", func(w http.ResponseWriter, r *http.Request) {
			p, sess, ok := g.sessionCall(s, w, r)
			if !ok {
				return
			}
			_ = p
			web.JSON(w, http.StatusOK, sess)
		})
		mux.HandleFunc("POST /api/v0/gateway/{session}/close", func(w http.ResponseWriter, r *http.Request) {
			p, sess, ok := g.sessionCall(s, w, r)
			if !ok {
				return
			}
			var in struct {
				Reason string `json:"reason"`
			}
			_ = web.Decode(r, &in)
			closed, err := g.CloseSession(r.Context(), sess, in.Reason, p.Actor)
			if err != nil {
				web.JSONError(w, statusFor(err), err)
				return
			}
			web.JSON(w, http.StatusOK, closed)
		})
		// the write path: a safe-outputs envelope for one session
		mux.HandleFunc("POST /api/v0/gateway/{session}/requests", func(w http.ResponseWriter, r *http.Request) {
			_, sess, ok := g.sessionCall(s, w, r)
			if !ok {
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, maxEnvelopeBytes+1))
			if err != nil {
				web.JSONError(w, http.StatusBadRequest, err)
				return
			}
			if len(body) > maxEnvelopeBytes {
				web.JSONError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("envelope larger than %d bytes", maxEnvelopeBytes))
				return
			}
			resp, err := g.Submit(r.Context(), sess, body)
			if err != nil {
				web.JSONError(w, statusFor(err), err)
				return
			}
			code := http.StatusOK
			if resp.Executed == 0 && resp.Refused > 0 {
				code = http.StatusForbidden
			}
			web.JSON(w, code, resp)
		})
		// issue comments written through the gateway, readable by anyone with a
		// token; change comments are served by review v1 (one store, lanes).
		mux.HandleFunc("GET /api/v0/repos/{org}/{name}/issues/{n}/comments", func(w http.ResponseWriter, r *http.Request) {
			g.commentsRoute(s, w, r, "issue")
		})
	}
}

// sessionCall authenticates a request on /api/v0/gateway/{session}: the admin
// token may act on any session; a session token only on its own session.
func (g *Gateway) sessionCall(s *web.Server, w http.ResponseWriter, r *http.Request) (Principal, Session, bool) {
	p, ok := g.Authenticate(s, w, r)
	if !ok {
		return p, Session{}, false
	}
	id := r.PathValue("session")
	if p.Session != nil {
		if p.Session.ID != id {
			web.JSONError(w, http.StatusForbidden, fmt.Errorf("token is for session %s, not %s", p.Session.ID, id))
			return p, Session{}, false
		}
		return p, *p.Session, true
	}
	sess, err := g.Session(r.Context(), id)
	if err != nil {
		web.JSONError(w, statusFor(err), err)
		return p, Session{}, false
	}
	return p, sess, true
}

func (g *Gateway) commentsRoute(s *web.Server, w http.ResponseWriter, r *http.Request, kind string) {
	p, ok := g.Authenticate(s, w, r)
	if !ok {
		return
	}
	repo, err := s.DB.RepoByName(r.Context(), r.PathValue("org"), r.PathValue("name"))
	if err != nil {
		web.JSONError(w, statusFor(err), err)
		return
	}
	if p.Session != nil && p.Session.RepoID != repo.ID {
		web.JSONError(w, http.StatusForbidden, fmt.Errorf("%s/%s is outside the session scope (%s)", repo.Org, repo.Name, p.Session.Scope))
		return
	}
	var n int
	fmt.Sscanf(r.PathValue("n"), "%d", &n)
	_, id, _, err := g.resolveTarget(r.Context(), repo, fmt.Sprintf("%ss/%d", kind, n), kind == "change")
	if err != nil {
		web.JSONError(w, http.StatusNotFound, err)
		return
	}
	list, err := g.Comments(r.Context(), repo, kind, id)
	if err != nil {
		web.JSONError(w, http.StatusInternalServerError, err)
		return
	}
	web.JSON(w, http.StatusOK, list)
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, ledger.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrSessionClosed):
		return http.StatusConflict
	case IsValidationError(err):
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}
