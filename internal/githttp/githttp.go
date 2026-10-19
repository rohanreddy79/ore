// SPDX-License-Identifier: AGPL-3.0-only

// Package githttp serves git's smart HTTP protocol for Ore's repositories
// behind Ore's authentication and policy, and records every ref update on
// the ledger by snapshotting refs around each receive-pack.
package githttp

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"path/filepath"
	"strings"
	"sync"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// Auth resolves credentials to an actor; nil means unauthenticated.
type Auth func(r *http.Request) *ledger.Actor

// Handler serves /{org}/{repo}.git/... .
type Handler struct {
	DB       *ledger.DB
	ReposDir string
	Backend  string // path to git-http-backend
	Auth     Auth
	Log      *slog.Logger

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// RepoPath returns the on-disk bare repository path.
func RepoPath(reposDir, org, name string) string {
	return filepath.Join(reposDir, org, name+".git")
}

func (h *Handler) lock(key string) *sync.Mutex {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.locks == nil {
		h.locks = map[string]*sync.Mutex{}
	}
	m, ok := h.locks[key]
	if !ok {
		m = &sync.Mutex{}
		h.locks[key] = m
	}
	return m
}

// ServeHTTP routes a git request. Expected path: /{org}/{repo}.git/{rest}.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(parts) < 3 || !strings.HasSuffix(parts[1], ".git") {
		http.NotFound(w, r)
		return
	}
	org, name := parts[0], strings.TrimSuffix(parts[1], ".git")
	repo, err := h.DB.RepoByName(r.Context(), org, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	actor := h.Auth(r)
	if actor == nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="ore"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	isReceive := strings.HasSuffix(r.URL.Path, "/git-receive-pack") ||
		(strings.HasSuffix(r.URL.Path, "/info/refs") && r.URL.Query().Get("service") == "git-receive-pack")
	repoPath := RepoPath(h.ReposDir, org, name)
	protected := "refs/heads/" + repo.DefaultBranch

	env := []string{
		"GIT_PROJECT_ROOT=" + h.ReposDir,
		"GIT_HTTP_EXPORT_ALL=1",
		"REMOTE_USER=" + actor.ID,
		"ORE_ACTOR_KIND=" + actor.Kind,
		"ORE_ACTOR_ID=" + actor.ID,
		"ORE_PROTECTED_REFS=" + protected,
		"PATH_INFO=/" + org + "/" + name + ".git/" + parts[2],
	}
	cgiHandler := &cgi.Handler{Path: h.Backend, Root: "", Env: env, Logger: nil}

	if !isReceive || !strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
		cgiHandler.ServeHTTP(w, r)
		return
	}

	// A push: serialize per repository, snapshot refs, serve, snapshot again, record.
	m := h.lock(repo.ID)
	m.Lock()
	defer m.Unlock()
	ctx := context.Background()
	before, err := gitx.Refs(ctx, repoPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cgiHandler.ServeHTTP(w, r)
	after, err := gitx.Refs(ctx, repoPath)
	if err != nil {
		h.Log.Error("refs after push", "err", err)
		return
	}
	for _, u := range gitx.DiffRefs(before, after) {
		if err := h.DB.RecordRefUpdate(ctx, repo, u.Ref, u.Old, u.New, "push", *actor); err != nil {
			h.Log.Error("record ref update", "err", err)
		}
	}
}
