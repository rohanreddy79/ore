// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/web"
)

func init() {
	registry.Register("pipelines.actions", Extension)
}

// Extension wires the slice into the server: the compiler as the Runner's
// Frontend, the native stores on the Runner, and the API routes for
// secrets, artifacts and compiled plans.
func Extension(ctx context.Context, s *web.Server) error {
	if s.Runner == nil {
		return nil
	}
	data := s.Runner.DataDir
	if data == "" {
		data = filepath.Dir(s.LogsDir)
	}
	if err := os.MkdirAll(data, 0o755); err != nil {
		return err
	}
	stores := pipelines.NewStores(s.DB, data)
	s.Runner.Stores = stores
	s.Runner.DataDir = data
	fe := &Frontend{Compiler: &Compiler{Resolver: NewGitResolver(filepath.Join(data, "actions-refs.json")), Image: os.Getenv("ORE_ACTIONS_IMAGE"), ImageDigest: dockerImageDigest}}
	s.Runner.Frontend = fe
	if s.Services == nil {
		s.Services = map[string]any{}
	}
	s.Services["pipelines.actions"] = fe
	s.Services["pipelines.stores"] = stores
	s.AddRoutes(func(mux *http.ServeMux) { Routes(mux, s, stores, fe) })
	return nil
}

var (
	digestMu    sync.Mutex
	digestCache = map[string]string{}
)

// dockerImageDigest returns the digest of a locally present image, or "".
// Images are not pulled at plan time.
func dockerImageDigest(ctx context.Context, ref string) string {
	digestMu.Lock()
	if d, ok := digestCache[ref]; ok {
		digestMu.Unlock()
		return d
	}
	digestMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{index .RepoDigests 0}}", ref).Output()
	d := ""
	if err == nil {
		if _, after, ok := strings.Cut(strings.TrimSpace(string(out)), "@"); ok {
			d = after
		}
	}
	digestMu.Lock()
	digestCache[ref] = d
	digestMu.Unlock()
	return d
}

func repoOf(s *web.Server, r *http.Request) (ledger.Repo, error) {
	return s.DB.RepoByName(r.Context(), r.PathValue("org"), r.PathValue("name"))
}

func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ledger.ErrNotFound):
		web.JSONError(w, http.StatusNotFound, err)
	case errors.Is(err, pipelines.ErrArtifactExists):
		web.JSONError(w, http.StatusConflict, err)
	default:
		web.JSONError(w, http.StatusInternalServerError, err)
	}
}

// Routes mounts the slice's API.
func Routes(mux *http.ServeMux, s *web.Server, stores *pipelines.Stores, fe *Frontend) {
	// secrets by reference: values are written, never read back
	mux.HandleFunc("POST /api/v0/repos/{org}/{name}/secrets", func(w http.ResponseWriter, r *http.Request) {
		actor := s.RequireActor(w, r)
		if actor == nil {
			return
		}
		repo, err := repoOf(s, r)
		if err != nil {
			fail(w, err)
			return
		}
		var in struct {
			Name        string `json:"name"`
			Value       string `json:"value"`
			Environment string `json:"environment"`
		}
		if err := web.Decode(r, &in); err != nil {
			web.JSONError(w, http.StatusBadRequest, err)
			return
		}
		info, err := stores.Secrets.Set(r.Context(), repo, in.Environment, in.Name, in.Value, *actor)
		if err != nil {
			web.JSONError(w, http.StatusBadRequest, err)
			return
		}
		web.JSON(w, http.StatusCreated, info)
	})
	mux.HandleFunc("GET /api/v0/repos/{org}/{name}/secrets", func(w http.ResponseWriter, r *http.Request) {
		if s.RequireActor(w, r) == nil {
			return
		}
		repo, err := repoOf(s, r)
		if err != nil {
			fail(w, err)
			return
		}
		list, err := stores.Secrets.List(r.Context(), repo)
		if err != nil {
			fail(w, err)
			return
		}
		web.JSON(w, http.StatusOK, list)
	})
	mux.HandleFunc("DELETE /api/v0/repos/{org}/{name}/secrets/{secret}", func(w http.ResponseWriter, r *http.Request) {
		actor := s.RequireActor(w, r)
		if actor == nil {
			return
		}
		repo, err := repoOf(s, r)
		if err != nil {
			fail(w, err)
			return
		}
		if err := stores.Secrets.Delete(r.Context(), repo, r.URL.Query().Get("environment"), r.PathValue("secret"), *actor); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// artifacts of a run
	mux.HandleFunc("GET /api/v0/repos/{org}/{name}/runs/{id}/artifacts", func(w http.ResponseWriter, r *http.Request) {
		if s.RequireActor(w, r) == nil {
			return
		}
		if _, err := repoOf(s, r); err != nil {
			fail(w, err)
			return
		}
		list, err := stores.Artifacts.List(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		web.JSON(w, http.StatusOK, list)
	})
	mux.HandleFunc("GET /api/v0/repos/{org}/{name}/runs/{id}/artifacts/{artifact}", func(w http.ResponseWriter, r *http.Request) {
		if s.RequireActor(w, r) == nil {
			return
		}
		if _, err := repoOf(s, r); err != nil {
			fail(w, err)
			return
		}
		name := r.PathValue("artifact")
		if err := pipelines.ValidArtifactName(name); err != nil {
			web.JSONError(w, http.StatusBadRequest, err)
			return
		}
		art, err := stores.Artifacts.Get(r.Context(), r.PathValue("id"), name)
		if err != nil {
			fail(w, err)
			return
		}
		f, err := os.Open(art.Path)
		if err != nil {
			fail(w, err)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".zip"))
		w.Header().Set("X-Ore-Artifact-Digest", art.Digest)
		http.ServeContent(w, r, name+".zip", time.Time{}, f)
	})

	// the compiled plan (IR and divergences) stored on a run
	mux.HandleFunc("GET /api/v0/repos/{org}/{name}/runs/{id}/plan", func(w http.ResponseWriter, r *http.Request) {
		if s.RequireActor(w, r) == nil {
			return
		}
		if _, err := repoOf(s, r); err != nil {
			fail(w, err)
			return
		}
		plans, err := PlansForRun(r.Context(), s.DB, r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		web.JSON(w, http.StatusOK, plans)
	})

	// compile the workflows at a ref without running them
	mux.HandleFunc("GET /api/v0/repos/{org}/{name}/workflows/plan", func(w http.ResponseWriter, r *http.Request) {
		if s.RequireActor(w, r) == nil {
			return
		}
		repo, err := repoOf(s, r)
		if err != nil {
			fail(w, err)
			return
		}
		ref := r.URL.Query().Get("ref")
		if ref == "" {
			ref = "refs/heads/" + repo.DefaultBranch
		}
		rp := filepath.Join(s.ReposDir, repo.Org, repo.Name+".git")
		oid, err := gitx.RevParse(r.Context(), rp, ref)
		if err != nil {
			web.JSONError(w, http.StatusBadRequest, err)
			return
		}
		work, err := os.MkdirTemp("", "ore-plan-*")
		if err != nil {
			fail(w, err)
			return
		}
		defer os.RemoveAll(work)
		if err := gitx.Archive(r.Context(), rp, oid, work); err != nil {
			fail(w, err)
			return
		}
		plans, err := fe.Compile(r.Context(), pipelines.CompileRequest{Work: work, Repo: repo, Commit: oid, Executor: s.Runner.Executor, Trust: pipelines.TrustInternal})
		if err != nil {
			fail(w, err)
			return
		}
		if plans == nil {
			plans = []pipelines.Plan{}
		}
		web.JSON(w, http.StatusOK, map[string]any{"commit": oid, "plans": plans})
	})
}

// StoredPlan is a row of pipeline_plans.
type StoredPlan struct {
	ID          string                 `json:"id"`
	RunID       string                 `json:"run_id"`
	Source      string                 `json:"source"`
	Frontend    string                 `json:"frontend"`
	IR          json.RawMessage        `json:"ir"`
	Divergences []pipelines.Divergence `json:"divergences"`
	Refused     string                 `json:"refused,omitempty"`
	CreatedAt   string                 `json:"created_at"`
}

// PlansForRun reads the plans stored on a run.
func PlansForRun(ctx context.Context, db *ledger.DB, runID string) ([]StoredPlan, error) {
	rows, err := db.Query(ctx, `SELECT id, run_id, source, frontend, ir, divergences, refused, created_at FROM pipeline_plans WHERE run_id = ? ORDER BY source`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StoredPlan{}
	for rows.Next() {
		var p StoredPlan
		var ir, div string
		if err := rows.Scan(&p.ID, &p.RunID, &p.Source, &p.Frontend, &ir, &div, &p.Refused, &p.CreatedAt); err != nil {
			return nil, err
		}
		p.IR = json.RawMessage(ir)
		if err := json.Unmarshal([]byte(div), &p.Divergences); err != nil || p.Divergences == nil {
			p.Divergences = []pipelines.Divergence{}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
