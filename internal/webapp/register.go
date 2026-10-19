// SPDX-License-Identifier: AGPL-3.0-only

package webapp

import (
	"context"
	"net/http"
	"strings"

	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/web"
)

// Name is the extension's registry name.
const Name = "webapp"

func init() { registry.Register(Name, Extension) }

// Extension mounts the app under /app/ and its support endpoints under
// /api/v0/webapp/. It publishes *Info as s.Services["webapp"] so the owner of
// the server-rendered index can redirect "/" to the app when it is built.
func Extension(ctx context.Context, s *web.Server) error {
	if s.Services == nil {
		s.Services = map[string]any{}
	}
	info := &Info{Built: Built(), Prefix: Prefix}
	s.Services[Name] = info
	s.AddRoutes(func(mux *http.ServeMux) { routes(s, mux, info) })
	return nil
}

func routes(srv *web.Server, mux *http.ServeMux, info *Info) {
	s := serverT{srv}
	app := appHandler()
	// Served ahead of the mux through web.Server.Mount, so no ServeMux pattern
	// can conflict with the server-rendered routes, on any host. "/app" itself
	// redirects to "/app/"; deeper paths are the app's client-side routes.
	srv.Mount(Prefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == strings.TrimSuffix(Prefix, "/") {
			http.Redirect(w, r, Prefix, http.StatusMovedPermanently)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		app.ServeHTTP(w, r)
	}))
	if info.Built {
		srv.SetIndex(http.RedirectHandler(Prefix, http.StatusFound))
	}

	// Session: the app signs in with a token pasted into a form and keeps the
	// same ore_token cookie the server-rendered pages use.
	mux.HandleFunc("GET /api/v0/webapp/session", s.withActor(sessionGet))
	mux.HandleFunc("POST /api/v0/webapp/session", func(w http.ResponseWriter, r *http.Request) { sessionCreate(s.Server, w, r) })
	mux.HandleFunc("DELETE /api/v0/webapp/session", sessionDelete)

	// What this server can do, so the app hides what is not there.
	mux.HandleFunc("GET /api/v0/webapp/features", s.withActor(func(w http.ResponseWriter, r *http.Request, a actorT) error {
		return web.JSON(w, 200, features(s.Server, info))
	}))

	// Live data.
	mux.HandleFunc("GET /api/v0/webapp/events/stream", s.withActor(func(w http.ResponseWriter, r *http.Request, a actorT) error {
		return streamEvents(s.Server, w, r)
	}))
	mux.HandleFunc("GET /api/v0/webapp/runs/{id}/log/stream", s.withActor(func(w http.ResponseWriter, r *http.Request, a actorT) error {
		run, err := s.DB.PipelineRun(r.Context(), r.PathValue("id"))
		if err != nil {
			return err
		}
		return streamLog(w, r, run.LogPath, func(ctx context.Context) (string, bool) {
			cur, err := s.DB.PipelineRun(ctx, run.ID)
			if err != nil {
				return "", true
			}
			return cur.Status, cur.Status != "queued" && cur.Status != "running"
		})
	}))
	mux.HandleFunc("GET /api/v0/webapp/agent/{id}/log/stream", s.withActor(func(w http.ResponseWriter, r *http.Request, a actorT) error {
		run, err := s.DB.AgentRun(r.Context(), r.PathValue("id"))
		if err != nil {
			return err
		}
		return streamLog(w, r, run.LogPath, func(ctx context.Context) (string, bool) {
			cur, err := s.DB.AgentRun(ctx, run.ID)
			if err != nil {
				return "", true
			}
			return cur.Status, cur.Status != "running"
		})
	}))

	// Code browsing helpers the REST API lacks.
	mux.HandleFunc("GET /api/v0/webapp/repos/{org}/{name}/commits/{oid}", s.withRepo(commitDetail))
	mux.HandleFunc("GET /api/v0/webapp/repos/{org}/{name}/blob", s.withRepo(blob))
	mux.HandleFunc("GET /api/v0/webapp/repos/{org}/{name}/compare", s.withRepo(compare))
	mux.HandleFunc("GET /api/v0/webapp/search", s.withActor(func(w http.ResponseWriter, r *http.Request, a actorT) error {
		return search(s.Server, w, r)
	}))
}
