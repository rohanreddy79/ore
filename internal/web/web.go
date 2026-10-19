// SPDX-License-Identifier: AGPL-3.0-only

// Package web exposes Ore over HTTP: the git smart protocol, the v0 REST API
// (JSON, bearer token), and a minimal server-rendered UI. Every write goes
// through the same services the agent loop uses, so the UI, the CLI and the
// agents are indistinguishable on the ledger except for their actor.
package web

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
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
	"sync"
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
				if r.Context().Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					// The client went away or hit its own deadline; not a server
					// failure. 499 (client closed request) keeps it out of the
					// error log and the 5xx metrics.
					jsonErr(w, 499, errors.New("request canceled"))
					return
				}
				switch {
				case errors.Is(err, ledger.ErrNotFound):
					code = http.StatusNotFound
				case errors.Is(err, review.ErrNothingToMerge), isConflict(err), errors.Is(err, errBadRequest), errors.Is(err, errConflict), ledger.IsUniqueViolation(err):
					code = http.StatusConflict
					if errors.Is(err, errBadRequest) {
						code = http.StatusBadRequest
					}
				}
				if code >= 500 && s.Log != nil {
					// The access log has the status; this line has the cause.
					s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err.Error())
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
	// tree: ?ref= &path=<subtree> &depth=1 (children only; directories end
	// with "/") &limit= &offset=. Recursive listings are capped so a 100k-file
	// repository never produces a multi-megabyte response by default; the
	// X-Ore-Tree-Total and X-Ore-Tree-Truncated headers say when to page.
	api("GET /api/v0/repos/{org}/{name}/tree", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		q := r.URL.Query()
		ref := q.Get("ref")
		if ref == "" {
			ref = "refs/heads/" + repo.DefaultBranch
		}
		sub := strings.Trim(q.Get("path"), "/")
		if strings.Contains(sub, "..") {
			return fmt.Errorf("%w: bad path", errBadRequest)
		}
		recursive := q.Get("depth") != "1"
		// Listings are immutable per commit: resolve the ref once and serve
		// repeats from a bounded cache instead of re-running ls-tree over
		// 100k entries for every request.
		oid, err := gitx.ResolveRef(r.Context(), s.repoPath(repo), ref)
		if err != nil {
			return gitErr(err)
		}
		v, err := treeCache.get(repo.ID+"|"+oid+"|"+sub+"|"+strconv.FormatBool(recursive), func() (any, int, error) {
			files, err := gitx.ListTreeAt(r.Context(), s.repoPath(repo), oid, sub, recursive)
			if err != nil {
				return nil, 0, err
			}
			if files == nil {
				files = []string{}
			}
			return files, pathsSize(files), nil
		})
		if err != nil {
			return gitErr(err)
		}
		files := v.([]string)
		limit, offset := atoi(q.Get("limit")), atoi(q.Get("offset"))
		if limit <= 0 {
			limit = 20000
		}
		if limit > 100000 {
			limit = 100000
		}
		if offset < 0 {
			offset = 0
		}
		total := len(files)
		if offset > total {
			offset = total
		}
		end := offset + limit
		if end > total {
			end = total
		}
		w.Header().Set("X-Ore-Tree-Total", strconv.Itoa(total))
		w.Header().Set("X-Ore-Tree-Truncated", strconv.FormatBool(end < total || offset > 0))
		// A page of a 100k-path listing is hundreds of kilobytes of JSON;
		// encode each page once per commit and serve the bytes.
		page, err := readCache.get("treejson|"+repo.ID+"|"+oid+"|"+sub+"|"+strconv.FormatBool(recursive)+"|"+strconv.Itoa(offset)+"|"+strconv.Itoa(end), func() (any, int, error) {
			b, err := json.Marshal(files[offset:end])
			return b, len(b), err
		})
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(page.([]byte))
		return nil
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
		file := r.URL.Query().Get("path")
		oid, err := gitx.ResolveRef(r.Context(), s.repoPath(repo), ref)
		if err != nil {
			return gitErr(err)
		}
		v, err := readCache.get("file|"+repo.ID+"|"+oid+"|"+file, func() (any, int, error) {
			b, err := gitx.ReadFile(r.Context(), s.repoPath(repo), oid, file)
			if err != nil {
				return nil, 0, err
			}
			if len(b) > maxCachedBlob {
				return b, -1, nil
			}
			return b, len(b), nil
		})
		if err != nil {
			return gitErr(err)
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(v.([]byte))
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
		oid, err := gitx.ResolveRef(r.Context(), s.repoPath(repo), ref)
		if err != nil {
			return gitErr(err)
		}
		v, err := readCache.get("log|"+repo.ID+"|"+oid, func() (any, int, error) {
			entries, err := gitx.Log(r.Context(), s.repoPath(repo), oid, 50)
			if err != nil {
				return nil, 0, err
			}
			if entries == nil {
				entries = []gitx.LogEntry{}
			}
			return entries, len(entries) * 256, nil
		})
		if err != nil {
			return gitErr(err)
		}
		return respond(w, nil, v)
	})
	// server-side commit through the API: no clone, no working copy (ADR 0014).
	// `base` is the commit the caller built on: a new branch starts there
	// (default: the default branch's tip) and an existing branch must still be
	// there (409 "branch moved" with the tip otherwise). `merge` makes the
	// commit a merge of that commit into the branch: the tree is the
	// server-side three-way merge with `files` as resolutions, and a conflict
	// no file resolves is a 409 listing the paths. Content is text unless
	// `encoding` is "base64".
	api("POST /api/v0/repos/{org}/{name}/commits", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		repo, err := s.repo(r)
		if err != nil {
			return err
		}
		var in struct {
			Branch  string `json:"branch"`
			Message string `json:"message"`
			Base    string `json:"base"`
			Merge   string `json:"merge"`
			Files   []struct {
				Path     string `json:"path"`
				Content  string `json:"content"`
				Encoding string `json:"encoding"`
				Delete   bool   `json:"delete"`
				Mode     string `json:"mode"`
			} `json:"files"`
		}
		if err := decode(r, &in); err != nil || (len(in.Files) == 0 && in.Merge == "") {
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
		edits := make([]gitx.FileEdit, 0, len(in.Files))
		for _, f := range in.Files {
			if f.Path == "" || strings.Contains(f.Path, "..") {
				return fmt.Errorf("%w: bad path %q", errBadRequest, f.Path)
			}
			e := gitx.FileEdit{Path: f.Path, Mode: f.Mode}
			if !f.Delete {
				e.Content = []byte(f.Content)
				if f.Encoding == "base64" {
					if e.Content, err = base64.StdEncoding.DecodeString(f.Content); err != nil {
						return fmt.Errorf("%w: %s: bad base64 content", errBadRequest, f.Path)
					}
				} else if f.Encoding != "" {
					return fmt.Errorf("%w: %s: encoding must be base64 or empty", errBadRequest, f.Path)
				}
			}
			edits = append(edits, e)
		}
		var base, mergeOID string
		if in.Base != "" {
			if base, err = gitx.RevParse(r.Context(), rp, in.Base); err != nil {
				return fmt.Errorf("%w: base: %v", errBadRequest, gitErr(err))
			}
		}
		if in.Merge != "" {
			if mergeOID, err = gitx.RevParse(r.Context(), rp, in.Merge); err != nil {
				return fmt.Errorf("%w: merge: %v", errBadRequest, gitErr(err))
			}
		}
		author := gitx.Author{Name: a.ID, Email: a.ID + "@" + repo.Org + ".invalid"}
		// Server-side commits to one branch take turns inside this process
		// (a lost compare-and-swap costs three git processes and a retry, so
		// fifty writers racing is slower and less fair than fifty queueing);
		// the compare-and-swap below still guards against pushes and other
		// processes moving the branch.
		if err := refLocks.lock(r.Context(), repo.ID+"|"+ref); err != nil {
			return err
		}
		defer refLocks.unlock(repo.ID + "|" + ref)
		var oid, old, parent string
		var lastErr error
		for attempt := 0; attempt < 8; attempt++ {
			var err error
			parent, err = gitx.RevParse(r.Context(), rp, ref)
			old = parent
			if err != nil { // new branch: start from base, else from the default branch if it exists
				old = gitx.ZeroOID
				if base != "" {
					parent = base
				} else {
					parent, _ = gitx.RevParse(r.Context(), rp, "refs/heads/"+repo.DefaultBranch)
				}
			} else if base != "" && parent != base {
				return respondCode(w, http.StatusConflict, nil, map[string]any{"error": errBranchMoved.Error() + ": " + in.Branch + " is at " + parent + ", not " + base, "tip": parent, "base": base})
			}
			if mergeOID == "" {
				oid, err = gitx.CommitFiles(r.Context(), rp, parent, edits, in.Message+"\n", author)
			} else {
				oid, err = s.mergeCommit(w, r, rp, parent, mergeOID, edits, in.Message, author)
				if err == nil && oid == "" {
					return nil // the 409 (conflicts, nothing to merge) is written
				}
			}
			if err != nil {
				return err
			}
			if err := gitx.UpdateRef(r.Context(), rp, ref, oid, old); err != nil {
				lastErr = err
				time.Sleep(time.Duration(10*(attempt+1)) * time.Millisecond)
				continue
			}
			lastErr = nil
			break
		}
		if lastErr != nil {
			return fmt.Errorf("%w: branch %s kept moving: %v", errConflict, in.Branch, lastErr)
		}
		if err := s.DB.RecordRefUpdate(r.Context(), repo, ref, old, oid, "api", a); err != nil {
			return err
		}
		return respondCode(w, http.StatusCreated, nil, map[string]string{"commit": oid, "ref": ref, "parent": parent})
	})
	// the workstation client's reads (client_routes.go)
	api("GET /api/v0/repos/{org}/{name}/archive", s.archive)
	api("GET /api/v0/repos/{org}/{name}/diff", s.diff)

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

// errConflict is answered with 409.
var errConflict = errors.New("conflict")

// treeCache memoizes tree listings keyed by (repo, commit, path, mode); a
// commit's tree never changes, so entries are evicted only for space.
var treeCache = newMemCache(readCacheBytes())

// refLocks serializes server-side writers per branch within this process.
var refLocks = &keyedLocks{m: map[string]chan struct{}{}}

type keyedLocks struct {
	mu sync.Mutex
	m  map[string]chan struct{}
}

// lock waits for the key or the context, whichever comes first.
func (k *keyedLocks) lock(ctx context.Context, key string) error {
	k.mu.Lock()
	ch, ok := k.m[key]
	if !ok {
		ch = make(chan struct{}, 1)
		k.m[key] = ch
	}
	k.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (k *keyedLocks) unlock(key string) {
	k.mu.Lock()
	ch := k.m[key]
	k.mu.Unlock()
	<-ch
}

// readCache memoizes git-backed reads that are immutable per commit (the log
// below a commit, a file at a commit) so a hot branch is served from memory
// once its ref is resolved; ORE_READ_CACHE_MB bounds it (default 256).
var readCache = newMemCache(readCacheBytes())

func readCacheBytes() int {
	if v, err := strconv.Atoi(os.Getenv("ORE_READ_CACHE_MB")); err == nil && v >= 0 {
		return v << 20
	}
	return 256 << 20
}

// maxCachedBlob keeps single huge files from evicting everything else.
const maxCachedBlob = 4 << 20

type memCache struct {
	mu       sync.Mutex
	max      int
	size     int
	order    []string
	entries  map[string]memEntry
	inflight map[string]*memCall
}

type memEntry struct {
	v    any
	size int
}

// memCall is one in-flight load that concurrent requests for the same key
// wait on instead of each spawning their own git process (singleflight).
type memCall struct {
	done chan struct{}
	v    any
	err  error
}

func newMemCache(maxBytes int) *memCache {
	return &memCache{max: maxBytes, entries: map[string]memEntry{}, inflight: map[string]*memCall{}}
}

// get returns the cached value or loads it. Concurrent callers for one key
// share a single load; if that load fails (for example because the leader's
// request was cancelled) each waiter loads on its own. A loader that reports
// a negative size opts out of caching for that value.
func (c *memCache) get(key string, load func() (v any, size int, err error)) (any, error) {
	for {
		c.mu.Lock()
		if e, ok := c.entries[key]; ok {
			c.mu.Unlock()
			return e.v, nil
		}
		if cl, ok := c.inflight[key]; ok {
			c.mu.Unlock()
			<-cl.done
			if cl.err == nil {
				return cl.v, nil
			}
			break // the leader failed: load for ourselves
		}
		cl := &memCall{done: make(chan struct{})}
		c.inflight[key] = cl
		c.mu.Unlock()
		v, size, err := load()
		c.mu.Lock()
		delete(c.inflight, key)
		if err == nil && size >= 0 && size <= c.max {
			c.store(key, v, size)
		}
		c.mu.Unlock()
		cl.v, cl.err = v, err
		close(cl.done)
		return v, err
	}
	v, size, err := load()
	if err == nil && size >= 0 && size <= c.max {
		c.mu.Lock()
		c.store(key, v, size)
		c.mu.Unlock()
	}
	return v, err
}

// store inserts under c.mu and evicts the oldest entries past the budget.
func (c *memCache) store(key string, v any, size int) {
	if _, ok := c.entries[key]; ok {
		return
	}
	size += len(key) + 64
	c.entries[key] = memEntry{v: v, size: size}
	c.order = append(c.order, key)
	c.size += size
	for c.size > c.max && len(c.order) > 1 {
		oldest := c.order[0]
		c.order = c.order[1:]
		c.size -= c.entries[oldest].size
		delete(c.entries, oldest)
	}
}

// pathsSize estimates a listing's footprint for the cache budget.
func pathsSize(files []string) int {
	n := 24 * len(files)
	for _, f := range files {
		n += len(f)
	}
	return n
}

// gitErr maps git's "unknown revision" and "not a valid object name" failures
// (a ref or object that does not exist) to ErrNotFound so the API answers 404
// instead of 500 for an empty repository or a bad ref.
func gitErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "unknown revision") || strings.Contains(msg, "Not a valid object name") ||
		strings.Contains(msg, "not a valid object name") || strings.Contains(msg, "bad revision") ||
		strings.Contains(msg, "does not exist in") || strings.Contains(msg, "Not a valid commit name") ||
		strings.Contains(msg, "exists on disk, but not in") || strings.Contains(msg, "Needed a single revision") {
		return fmt.Errorf("%w: %s", ledger.ErrNotFound, strings.TrimSpace(strings.SplitN(msg, ":", 2)[len(strings.SplitN(msg, ":", 2))-1]))
	}
	return err
}

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

// maxJSONBody bounds API request bodies; the commits endpoint carries file
// contents, so the limit is generous but finite.
const maxJSONBody = 64 << 20

func decode(r *http.Request, v any) error {
	if r.Body == nil {
		return errors.New("empty body")
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxJSONBody))
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

// mergeCommit builds the merge of other into tip with edits as resolutions
// and returns the commit, or writes a 409 and returns "" when other is
// already contained or a conflict is left unresolved.
func (s *Server) mergeCommit(w http.ResponseWriter, r *http.Request, rp, tip, other string, edits []gitx.FileEdit, message string, author gitx.Author) (string, error) {
	if tip == "" {
		return "", fmt.Errorf("%w: merge needs an existing branch", errBadRequest)
	}
	if gitx.IsAncestor(r.Context(), rp, other, tip) {
		return "", respondCode(w, http.StatusConflict, nil, map[string]any{"error": "nothing to merge: " + other + " is already contained", "tip": tip})
	}
	tree, conflicts, err := gitx.MergeTree(r.Context(), rp, tip, other)
	if err != nil && !errors.Is(err, gitx.ErrConflict) {
		return "", err
	}
	var conflicted []string
	if errors.Is(err, gitx.ErrConflict) {
		for _, p := range strings.Split(strings.TrimSpace(conflicts), "\n") {
			if p != "" {
				conflicted = append(conflicted, p)
			}
		}
		resolved := map[string]bool{}
		for _, e := range edits {
			resolved[e.Path] = true
		}
		var unresolved []string
		for _, p := range conflicted {
			if !resolved[p] {
				unresolved = append(unresolved, p)
			}
		}
		if len(unresolved) > 0 {
			return "", respondCode(w, http.StatusConflict, nil, map[string]any{"error": fmt.Sprintf("merge conflicts in %d path(s)", len(unresolved)), "conflicts": conflicted, "unresolved": unresolved, "tip": tip, "merge": other})
		}
	}
	if message == "" || message == "Update via API" {
		message = "Merge " + other[:8] + " into the change"
	}
	return gitx.CommitTreeEdits(r.Context(), rp, tree, []string{tip, other}, edits, message+"\n", author)
}
