// SPDX-License-Identifier: AGPL-3.0-only

package webapp

import (
	"context"
	"net/http"
	"net/url"
	"os"
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
	mux.Handle("GET /app", http.RedirectHandler(Prefix, http.StatusMovedPermanently))
	// Go's mux refuses patterns that overlap without one being more specific,
	// and internal/web owns host-less wildcards such as GET /{org}/{name} and
	// GET /{org}/{name}/changes/{n}. Host-less patterns below cover every
	// path shape that cannot collide with those (1, 2, 3 and 5+ segments under
	// /app/); the host-qualified catch-alls cover the rest (4-segment paths
	// like /app/org/repo/issues) for the hosts this server is reached as,
	// because a pattern with a host never conflicts and always wins. The same
	// rule lets "/" redirect to the app on those hosts.
	mux.Handle("GET /app/{$}", app)
	mux.Handle("GET /app/{a}", app)
	mux.Handle("GET /app/{a}/{b}", app)
	mux.Handle("GET /app/{a}/{b}/{c}/{rest...}", app)
	for _, h := range knownHosts(s.Server) {
		mux.Handle("GET "+h+"/app/{path...}", app)
		if info.Built {
			mux.Handle("GET "+h+"/{$}", http.RedirectHandler(Prefix, http.StatusFound))
		}
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

// knownHosts lists the host names the app's catch-all routes are registered
// for: the host of the server's base URL, the loopback names, and anything in
// ORE_WEBAPP_HOSTS (comma-separated) for deployments behind a proxy that
// rewrites the Host header. Ports are not part of a host pattern.
func knownHosts(s *web.Server) []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		h = strings.TrimSpace(strings.ToLower(h))
		h = strings.TrimPrefix(strings.TrimSuffix(h, "]"), "[")
		if h == "" || strings.ContainsAny(h, "/ \t") || seen[h] {
			return
		}
		seen[h] = true
		out = append(out, h)
	}
	if s.BaseURL != "" {
		if u, err := url.Parse(s.BaseURL); err == nil {
			add(u.Hostname())
		}
	}
	add("localhost")
	add("127.0.0.1")
	add("::1")
	for _, h := range strings.Split(os.Getenv("ORE_WEBAPP_HOSTS"), ",") {
		if u, err := url.Parse("http://" + strings.TrimSpace(h)); err == nil && strings.TrimSpace(h) != "" {
			add(u.Hostname())
		}
	}
	return out
}
