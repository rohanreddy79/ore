// SPDX-License-Identifier: AGPL-3.0-only

// Package web exposes Ore over HTTP: the git smart protocol, the v0 REST API
// (JSON, bearer token), and a minimal server-rendered UI. Every write goes
// through the same services the agent loop uses, so the UI, the CLI and the
// agents are indistinguishable on the ledger except for their actor.
package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/agent"
	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/githttp"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
	"github.com/orevcs/ore/internal/review"
)

// Levels names the autonomy levels (docs/12).
var Levels = []string{"manual", "assist", "author", "land", "ship", "operate"}

// Server wires the services.
type Server struct {
	DB       *ledger.DB
	ReposDir string
	LogsDir  string
	Token    string
	BaseURL  string
	Git      *githttp.Handler
	Runner   *pipelines.Runner
	Deployer *delivery.Deployer
	Review   *review.Service
	Loop     *agent.Loop
	Log      *slog.Logger

	// Authenticate, when set, replaces the single-token authenticator (for the
	// identity extension: users, tokens, SSH keys). It must return nil for
	// requests that carry no valid credential.
	Authenticate func(r *http.Request) *ledger.Actor
	// Services holds feature-specific services attached by extensions, by name.
	Services map[string]any

	routes []func(mux *http.ServeMux)
	mounts []mount
	index  http.Handler
	// root is the handler Handler built; UI forms dispatch to the slices' own
	// API routes through it in-process (see ui_core.go).
	root http.Handler
	// StreamInterval is the poll interval of the SSE streams (500 ms unless a test shortens it).
	StreamInterval time.Duration
}

type mount struct {
	prefix string
	h      http.Handler
}

// Mount serves every request under prefix (which must start and end with "/",
// e.g. "/app/") with h, ahead of the mux and therefore free of ServeMux
// pattern conflicts. The prefix without its trailing slash is served too.
// Longer prefixes win over shorter ones.
func (s *Server) Mount(prefix string, h http.Handler) {
	if !strings.HasPrefix(prefix, "/") || !strings.HasSuffix(prefix, "/") || prefix == "/" {
		panic("web.Mount: prefix must look like /name/")
	}
	s.mounts = append(s.mounts, mount{prefix: prefix, h: h})
	sort.SliceStable(s.mounts, func(i, j int) bool { return len(s.mounts[i].prefix) > len(s.mounts[j].prefix) })
}

// SetIndex replaces the handler for the exact path "/" (for example to send
// browsers to a single-page app once it is built).
func (s *Server) SetIndex(h http.Handler) { s.index = h }

// AddRoutes registers a function that mounts routes on the server's mux.
// Extensions call this instead of editing Handler. Patterns use Go 1.22
// ServeMux syntax; use RequireActor to authenticate.
func (s *Server) AddRoutes(fn func(mux *http.ServeMux)) { s.routes = append(s.routes, fn) }

// RequireActor authenticates a request for an extension route; it writes a
// 401 and returns nil when the request has no valid credential.
func (s *Server) RequireActor(w http.ResponseWriter, r *http.Request) *ledger.Actor {
	actor := s.authenticate(r)
	if actor == nil {
		jsonErr(w, http.StatusUnauthorized, errors.New("missing or invalid token"))
		return nil
	}
	return actor
}

// JSON writes a JSON response; shared with extensions.
func JSON(w http.ResponseWriter, code int, v any) error { return respondCode(w, code, nil, v) }

// JSONError writes a JSON error; shared with extensions.
func JSONError(w http.ResponseWriter, code int, err error) { jsonErr(w, code, err) }

// Decode strictly decodes a JSON body; shared with extensions.
func Decode(r *http.Request, v any) error { return decode(r, v) }

type ctxKey int

const actorKey ctxKey = 1

func (s *Server) authenticate(r *http.Request) *ledger.Actor {
	if s.Authenticate != nil {
		return s.Authenticate(r)
	}
	token := ""
	user := "admin"
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		token = strings.TrimPrefix(h, "Bearer ")
	} else if u, p, ok := r.BasicAuth(); ok {
		token, user = p, u
	} else if c, err := r.Cookie("ore_token"); err == nil {
		token = c.Value
	} else if q := r.URL.Query().Get("token"); q != "" {
		token = q
	}
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.Token)) != 1 {
		return nil
	}
	if user == "" {
		user = "admin"
	}
	return &ledger.Actor{Kind: "human", ID: user}
}

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	if s.Git != nil {
		s.Git.Auth = s.authenticate
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })

	api := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			actor := s.authenticate(r)
			if actor == nil {
				jsonErr(w, http.StatusUnauthorized, errors.New("missing or invalid token"))
				return
			}
			if err := fn(w, r, *actor); err != nil {
				code := http.StatusInternalServerError
				switch {
				case errors.Is(err, ledger.ErrNotFound):
					code = http.StatusNotFound
				case errors.Is(err, review.ErrNothingToMerge), isConflict(err), errors.Is(err, errBadRequest):
					code = http.StatusConflict
					if errors.Is(err, errBadRequest) {
						code = http.StatusBadRequest
					}
				}
				jsonErr(w, code, err)
			}
		})
	}

	// orgs and repos
	api("GET /api/v0/orgs", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		orgs, err := s.DB.Orgs(r.Context())
		return respond(w, err, orgs)
	})
	api("POST /api/v0/orgs", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		var in struct {
			Name string `json:"name"`
		}
		if err := decode(r, &in); err != nil || !validName(in.Name) {
			return fmt.Errorf("%w: name is required ([a-z0-9-_.])", errBadRequest)
		}
		org, err := s.DB.CreateOrg(r.Context(), in.Name, a)
		return respondCode(w, http.StatusCreated, err, org)
	})
	api("GET /api/v0/repos", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repos, err := s.DB.Repos(r.Context(), r.URL.Query().Get("org"))
		return respond(w, err, repos)
	})
	api("POST /api/v0/repos", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		var in struct {
			Org           string `json:"org"`
			Name          string `json:"name"`
			DefaultBranch string `json:"default_branch"`
			AutonomyLevel *int   `json:"autonomy_level"`
			DeployPort    int    `json:"deploy_port"`
		}
		if err := decode(r, &in); err != nil || !validName(in.Org) || !validName(in.Name) {
			return fmt.Errorf("%w: org and name are required", errBadRequest)
		}
		org, err := s.DB.OrgByName(r.Context(), in.Org)
		if err != nil {
			return err
		}
		level := 2
		if in.AutonomyLevel != nil {
			level = *in.AutonomyLevel
		}
		repo, err := s.DB.CreateRepo(r.Context(), org, in.Name, in.DefaultBranch, level, in.DeployPort, a)
		if err != nil {
			return err
		}
		if err := gitx.InitBare(r.Context(), githttp.RepoPath(s.ReposDir, repo.Org, repo.Name), repo.DefaultBranch); err != nil {
			return err
		}
		return respondCode(w, http.StatusCreated, nil, repo)
	})
	api("GET /api/v0/repos/{org}/{name}", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		return respond(w, err, repo)
	})
	api("PATCH /api/v0/repos/{org}/{name}", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		var in struct {
			AutonomyLevel *int   `json:"autonomy_level"`
			Reason        string `json:"reason"`
		}
		if err := decode(r, &in); err != nil || in.AutonomyLevel == nil {
			return fmt.Errorf("%w: autonomy_level is required", errBadRequest)
		}
		if err := s.DB.SetAutonomyLevel(r.Context(), repo, *in.AutonomyLevel, in.Reason, a); err != nil {
			return err
		}
		repo.AutonomyLevel = *in.AutonomyLevel
		return respond(w, nil, repo)
	})
	api("GET /api/v0/repos/{org}/{name}/refs", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		refs, err := gitx.Refs(r.Context(), s.repoPath(repo))
		return respond(w, err, refs)
	})
	api("GET /api/v0/repos/{org}/{name}/tree", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		ref := r.URL.Query().Get("ref")
		if ref == "" {
			ref = "refs/heads/" + repo.DefaultBranch
		}
		files, err := gitx.ListTree(r.Context(), s.repoPath(repo), ref)
		return respond(w, err, files)
	})
	api("GET /api/v0/repos/{org}/{name}/file", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		ref := r.URL.Query().Get("ref")
		if ref == "" {
			ref = "refs/heads/" + repo.DefaultBranch
		}
		b, err := gitx.ReadFile(r.Context(), s.repoPath(repo), ref, r.URL.Query().Get("path"))
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(b)
		return nil
	})
	api("GET /api/v0/repos/{org}/{name}/log", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		ref := r.URL.Query().Get("ref")
		if ref == "" {
			ref = "refs/heads/" + repo.DefaultBranch
		}
		entries, err := gitx.Log(r.Context(), s.repoPath(repo), ref, 50)
		return respond(w, err, entries)
	})
	// server-side commit through the API: no clone, no working copy (ADR 0014)
	api("POST /api/v0/repos/{org}/{name}/commits", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		var in struct {
			Branch  string `json:"branch"`
			Message string `json:"message"`
			Files   []struct {
				Path    string `json:"path"`
				Content string `json:"content"`
				Delete  bool   `json:"delete"`
				Mode    string `json:"mode"`
			} `json:"files"`
		}
		if err := decode(r, &in); err != nil || len(in.Files) == 0 {
			return fmt.Errorf("%w: files are required", errBadRequest)
		}
		if in.Branch == "" {
			in.Branch = repo.DefaultBranch
		}
		if in.Message == "" {
			in.Message = "Update via API"
		}
		rp := s.repoPath(repo)
		ref := "refs/heads/" + in.Branch
		parent, err := gitx.RevParse(r.Context(), rp, ref)
		old := parent
		if err != nil { // new branch: start from the default branch if it exists
			old = gitx.ZeroOID
			parent, _ = gitx.RevParse(r.Context(), rp, "refs/heads/"+repo.DefaultBranch)
		}
		edits := make([]gitx.FileEdit, 0, len(in.Files))
		for _, f := range in.Files {
			if f.Path == "" || strings.Contains(f.Path, "..") {
				return fmt.Errorf("%w: bad path %q", errBadRequest, f.Path)
			}
			e := gitx.FileEdit{Path: f.Path, Mode: f.Mode}
			if !f.Delete {
				e.Content = []byte(f.Content)
			}
			edits = append(edits, e)
		}
		author := gitx.Author{Name: a.ID, Email: a.ID + "@" + repo.Org + ".invalid"}
		oid, err := gitx.CommitFiles(r.Context(), rp, parent, edits, in.Message+"\n", author)
		if err != nil {
			return err
		}
		if err := gitx.UpdateRef(r.Context(), rp, ref, oid, old); err != nil {
			return err
		}
		if err := s.DB.RecordRefUpdate(r.Context(), repo, ref, old, oid, "api", a); err != nil {
			return err
		}
		return respondCode(w, http.StatusCreated, nil, map[string]string{"commit": oid, "ref": ref})
	})

	// issues
	api("GET /api/v0/repos/{org}/{name}/issues", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		issues, err := s.DB.Issues(r.Context(), repo)
		return respond(w, err, issues)
	})
	api("POST /api/v0/repos/{org}/{name}/issues", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		var in struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if err := decode(r, &in); err != nil || in.Title == "" {
			return fmt.Errorf("%w: title is required", errBadRequest)
		}
		issue, err := s.DB.CreateIssue(r.Context(), repo, in.Title, in.Body, a)
		return respondCode(w, http.StatusCreated, err, issue)
	})
	api("GET /api/v0/repos/{org}/{name}/issues/{n}", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		issue, err := s.DB.Issue(r.Context(), repo, atoi(r.PathValue("n")))
		return respond(w, err, issue)
	})
	api("POST /api/v0/repos/{org}/{name}/issues/{n}/close", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		issue, err := s.DB.Issue(r.Context(), repo, atoi(r.PathValue("n")))
		if err != nil {
			return err
		}
		var in struct {
			Reason string `json:"reason"`
		}
		_ = decode(r, &in)
		if err := s.DB.CloseIssue(r.Context(), repo, issue, in.Reason, a); err != nil {
			return err
		}
		issue.State = "closed"
		return respond(w, nil, issue)
	})

	// changes
	api("GET /api/v0/repos/{org}/{name}/changes", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		changes, err := s.DB.Changes(r.Context(), repo)
		return respond(w, err, changes)
	})
	api("POST /api/v0/repos/{org}/{name}/changes", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		var in review.OpenRequest
		if err := decode(r, &in); err != nil || in.SourceRef == "" {
			return fmt.Errorf("%w: source_ref is required", errBadRequest)
		}
		ch, err := s.Review.Open(r.Context(), repo, in, a)
		return respondCode(w, http.StatusCreated, err, ch)
	})
	api("GET /api/v0/repos/{org}/{name}/changes/{n}", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		ch, err := s.DB.Change(r.Context(), repo, atoi(r.PathValue("n")))
		return respond(w, err, ch)
	})
	api("GET /api/v0/repos/{org}/{name}/changes/{n}/diff", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		ch, err := s.DB.Change(r.Context(), repo, atoi(r.PathValue("n")))
		if err != nil {
			return err
		}
		diff, err := s.Review.Diff(r.Context(), repo, ch)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
		w.Write([]byte(diff))
		return nil
	})
	api("POST /api/v0/repos/{org}/{name}/changes/{n}/merge", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		ch, err := s.DB.Change(r.Context(), repo, atoi(r.PathValue("n")))
		if err != nil {
			return err
		}
		oid, err := s.Review.Merge(r.Context(), repo, ch, a)
		if err != nil {
			return err
		}
		ch, _ = s.DB.Change(r.Context(), repo, ch.Number)
		return respond(w, nil, map[string]any{"merge_commit": oid, "change": ch})
	})
	api("POST /api/v0/repos/{org}/{name}/changes/{n}/close", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		ch, err := s.DB.Change(r.Context(), repo, atoi(r.PathValue("n")))
		if err != nil {
			return err
		}
		var in struct {
			Reason string `json:"reason"`
		}
		_ = decode(r, &in)
		return respond(w, s.DB.CloseChange(r.Context(), repo, ch, in.Reason, a), map[string]string{"state": "closed"})
	})

	// pipelines
	api("GET /api/v0/repos/{org}/{name}/runs", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		runs, err := s.DB.PipelineRuns(r.Context(), repo, 50)
		return respond(w, err, runs)
	})
	api("POST /api/v0/repos/{org}/{name}/runs", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		var in struct {
			Ref    string `json:"ref"`
			Change *int   `json:"change"`
			Wait   bool   `json:"wait"`
		}
		_ = decode(r, &in)
		if in.Ref == "" {
			in.Ref = "refs/heads/" + repo.DefaultBranch
		}
		var changeID *string
		if in.Change != nil {
			ch, err := s.DB.Change(r.Context(), repo, *in.Change)
			if err != nil {
				return err
			}
			changeID = &ch.ID
			in.Ref = ch.HeadOID
		}
		oid, err := gitx.RevParse(r.Context(), s.repoPath(repo), in.Ref)
		if err != nil {
			return fmt.Errorf("%w: %v", errBadRequest, err)
		}
		if in.Wait {
			run, err := s.Runner.Run(r.Context(), repo, oid, changeID, a)
			return respond(w, err, run)
		}
		run := ledger.PipelineRun{CommitOID: oid, ChangeID: changeID}
		if err := s.DB.InsertPipelineRun(r.Context(), repo, &run, a); err != nil {
			return err
		}
		go s.runQueued(repo, run, a)
		return respondCode(w, http.StatusAccepted, nil, run)
	})
	api("GET /api/v0/repos/{org}/{name}/runs/{id}", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		run, err := s.DB.PipelineRun(r.Context(), r.PathValue("id"))
		return respond(w, err, run)
	})
	api("GET /api/v0/repos/{org}/{name}/runs/{id}/log", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		run, err := s.DB.PipelineRun(r.Context(), r.PathValue("id"))
		if err != nil {
			return err
		}
		return serveLog(w, run.LogPath)
	})

	// deployments
	api("GET /api/v0/repos/{org}/{name}/deployments", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		deps, err := s.DB.Deployments(r.Context(), repo, 50)
		return respond(w, err, deps)
	})
	api("POST /api/v0/repos/{org}/{name}/deployments", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		var in struct {
			Ref         string `json:"ref"`
			Environment string `json:"environment"`
		}
		_ = decode(r, &in)
		if in.Ref == "" {
			in.Ref = "refs/heads/" + repo.DefaultBranch
		}
		if in.Environment == "" {
			in.Environment = "preview"
		}
		oid, err := gitx.RevParse(r.Context(), s.repoPath(repo), in.Ref)
		if err != nil {
			return fmt.Errorf("%w: %v", errBadRequest, err)
		}
		dep, err := s.Deployer.Deploy(r.Context(), repo, oid, in.Environment, a, nil)
		return respondCode(w, http.StatusCreated, err, dep)
	})
	api("POST /api/v0/repos/{org}/{name}/deployments/rollback", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		var in struct {
			Environment string `json:"environment"`
		}
		_ = decode(r, &in)
		if in.Environment == "" {
			in.Environment = "production"
		}
		dep, err := s.Deployer.Rollback(r.Context(), repo, in.Environment, a)
		return respond(w, err, dep)
	})
	api("POST /api/v0/repos/{org}/{name}/deployments/stop", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		var in struct {
			Environment string `json:"environment"`
		}
		_ = decode(r, &in)
		return respond(w, s.Deployer.Stop(r.Context(), repo, in.Environment, a), map[string]string{"status": "stopped"})
	})

	// agent loop
	api("GET /api/v0/repos/{org}/{name}/agent/runs", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		runs, err := s.DB.AgentRuns(r.Context(), repo, 50)
		return respond(w, err, runs)
	})
	api("POST /api/v0/repos/{org}/{name}/agent/runs", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		var in struct {
			Issue  *int   `json:"issue"`
			Prompt string `json:"prompt"`
			Wait   bool   `json:"wait"`
		}
		_ = decode(r, &in)
		req := agent.Request{Prompt: in.Prompt}
		if in.Issue != nil {
			issue, err := s.DB.Issue(r.Context(), repo, *in.Issue)
			if err != nil {
				return err
			}
			req.Issue = &issue
		}
		if req.Issue == nil && req.Prompt == "" {
			return fmt.Errorf("%w: issue or prompt is required", errBadRequest)
		}
		if in.Wait {
			run, err := s.Loop.RunSync(r.Context(), repo, req, a)
			return respond(w, err, run)
		}
		run, err := s.Loop.Start(r.Context(), repo, req, a)
		return respondCode(w, http.StatusAccepted, err, run)
	})
	api("GET /api/v0/repos/{org}/{name}/agent/runs/{id}", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		run, err := s.DB.AgentRun(r.Context(), r.PathValue("id"))
		return respond(w, err, run)
	})
	api("GET /api/v0/repos/{org}/{name}/agent/runs/{id}/log", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		run, err := s.DB.AgentRun(r.Context(), r.PathValue("id"))
		if err != nil {
			return err
		}
		return serveLog(w, run.LogPath)
	})

	// ledger
	api("GET /api/v0/events", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		q := r.URL.Query()
		after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
		limit, _ := strconv.Atoi(q.Get("limit"))
		evs, err := s.DB.Events(r.Context(), ledger.EventsOptions{AfterSeq: after, Type: q.Get("type"), Subject: q.Get("subject"), Limit: limit})
		if evs == nil {
			evs = []ledger.Event{}
		}
		return respond(w, err, evs)
	})

	s.mountStreams(mux)
	s.mountUI(mux)
	for _, fn := range s.routes {
		fn(mux)
	}

	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// git smart HTTP: /{org}/{repo}.git/...
		if s.Git != nil && strings.Contains(r.URL.Path, ".git/") {
			s.Git.ServeHTTP(w, r)
			return
		}
		if s.index != nil && r.URL.Path == "/" {
			s.index.ServeHTTP(w, r)
			return
		}
		for _, m := range s.mounts {
			if strings.HasPrefix(r.URL.Path, m.prefix) || r.URL.Path == strings.TrimSuffix(m.prefix, "/") {
				m.h.ServeHTTP(w, r)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
	s.root = root
	return root
}

func (s *Server) runQueued(repo ledger.Repo, run ledger.PipelineRun, a ledger.Actor) {
	if err := s.Runner.Execute(context.Background(), repo, &run, a); err != nil {
		s.Log.Error("pipeline run", "id", run.ID, "err", err)
	}
}

func (s *Server) repo(r *http.Request) (ledger.Repo, error) {
	return s.DB.RepoByName(r.Context(), r.PathValue("org"), r.PathValue("name"))
}

func (s *Server) repoPath(repo ledger.Repo) string {
	return filepath.Join(s.ReposDir, repo.Org, repo.Name+".git")
}

var errBadRequest = errors.New("bad request")

func isConflict(err error) bool {
	var ce *review.ConflictError
	return errors.As(err, &ce)
}

func validName(s string) bool {
	if s == "" || len(s) > 64 || strings.HasPrefix(s, ".") {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func decode(r *http.Request, v any) error {
	if r.Body == nil {
		return errors.New("empty body")
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func respond(w http.ResponseWriter, err error, v any) error {
	return respondCode(w, http.StatusOK, err, v)
}

func respondCode(w http.ResponseWriter, code int, err error, v any) error {
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func serveLog(w http.ResponseWriter, path string) error {
	if path == "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("(no log yet)\n"))
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(b)
	return nil
}
