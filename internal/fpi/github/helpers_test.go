// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
	"github.com/orevcs/ore/internal/review"
	"github.com/orevcs/ore/internal/web"
)

const testToken = "ghp_test_token_0123456789"

var testAuthor = gitx.Author{Name: "Octo Cat", Email: "octocat@github.com"}

// fakeGitHub is GitHub for the tests: the REST endpoints the adapter uses
// (repos, statuses, issues, matching-refs) behind a bearer token, rate
// limiting on demand, and the repositories' git data served over the smart
// HTTP protocol by git-http-backend behind the same token as HTTP basic
// credentials, exactly as github.com does.
type fakeGitHub struct {
	t       *testing.T
	srv     *httptest.Server
	root    string
	backend string

	mu            sync.Mutex
	repos         map[string]*fakeRepo
	statuses      []statusCall
	calls         map[string]int
	retryAfter    int // > 0: API answers 429 with this Retry-After
	rateLimitHits int
	gitRequests   int
	gitRejected   int
	// failOnce answers the given status once for the first request whose path+query contains the key.
	failOnce map[string]int
}

type fakeRepo struct {
	id            int64
	owner, name   string
	path          string
	defaultBranch string
	issues        []map[string]any
	imp           *fakeImportData
}

type statusCall struct {
	Owner, Name, SHA string
	Body             map[string]string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	backend, err := gitx.HTTPBackendPath(context.Background())
	if err != nil {
		t.Skip(err)
	}
	f := &fakeGitHub{t: t, root: t.TempDir(), backend: backend, repos: map[string]*fakeRepo{}, calls: map[string]int{}, failOnce: map[string]int{}}
	mux := http.NewServeMux()
	api := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, repo *fakeRepo)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			f.calls[r.Method+" "+r.URL.Path]++
			ra := f.retryAfter
			if ra > 0 {
				f.rateLimitHits++
			}
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if r.Header.Get("Authorization") != "Bearer "+testToken {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"message":"Bad credentials","documentation_url":"https://docs.github.com/rest"}`)
				return
			}
			if ra > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(ra))
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{"message":"API rate limit exceeded for installation ID 44556677.","documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api"}`)
				return
			}
			f.mu.Lock()
			repo := f.repos[strings.ToLower(r.PathValue("owner")+"/"+r.PathValue("name"))]
			var injected int
			for key, status := range f.failOnce {
				if status > 0 && strings.Contains(r.URL.RequestURI(), key) {
					injected = status
					f.failOnce[key] = 0
					break
				}
			}
			f.mu.Unlock()
			if injected > 0 {
				if injected == http.StatusTooManyRequests {
					w.Header().Set("Retry-After", "60")
					w.Header().Set("X-RateLimit-Remaining", "0")
				}
				w.WriteHeader(injected)
				fmt.Fprintf(w, `{"message":"injected failure %d"}`, injected)
				return
			}
			if repo == nil {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"message":"Not Found","documentation_url":"https://docs.github.com/rest/repos/repos#get-a-repository"}`)
				return
			}
			fn(w, r, repo)
		})
	}
	api("GET /repos/{owner}/{name}", func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": repo.id, "name": repo.name, "full_name": repo.owner + "/" + repo.name, "private": false,
			"owner":             map[string]any{"login": repo.owner, "type": "Organization"},
			"html_url":          "https://github.com/" + repo.owner + "/" + repo.name,
			"clone_url":         f.srv.URL + "/git/" + repo.owner + "/" + repo.name + ".git",
			"default_branch":    repo.defaultBranch,
			"size":              128,
			"has_wiki":          repo.imp != nil && repo.imp.hasWiki,
			"has_issues":        true,
			"open_issues_count": len(repo.issues),
		})
	})
	api("POST /repos/{owner}/{name}/statuses/{sha}", func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(422)
			fmt.Fprint(w, `{"message":"Validation Failed"}`)
			return
		}
		sha := r.PathValue("sha")
		if _, err := gitx.Run(r.Context(), repo.path, nil, "cat-file", "-e", sha+"^{commit}"); err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"message":"No commit found for SHA: `+sha+`","documentation_url":"https://docs.github.com/rest/commits/statuses#create-a-commit-status"}`)
			return
		}
		f.mu.Lock()
		f.statuses = append(f.statuses, statusCall{Owner: repo.owner, Name: repo.name, SHA: sha, Body: body})
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": len(f.statuses), "state": body["state"], "context": body["context"], "target_url": body["target_url"]})
	})
	f.importRoutes(api)
	api("GET /repos/{owner}/{name}/git/matching-refs/{prefix...}", func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) {
		refs, err := gitx.Refs(r.Context(), repo.path)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		out := []map[string]any{}
		for name, oid := range refs {
			if strings.HasPrefix(name, "refs/"+r.PathValue("prefix")) {
				out = append(out, map[string]any{"ref": name, "node_id": "REF_x", "url": f.srv.URL + "/repos/" + repo.owner + "/" + repo.name + "/git/" + name, "object": map[string]any{"sha": oid, "type": "commit"}})
			}
		}
		json.NewEncoder(w).Encode(out)
	})
	// git over smart HTTP: /git/{owner}/{name}.git/...
	mux.HandleFunc("/git/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.gitRequests++
		f.mu.Unlock()
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+testToken))
		if r.Header.Get("Authorization") != want {
			f.mu.Lock()
			f.gitRejected++
			f.mu.Unlock()
			w.Header().Set("WWW-Authenticate", `Basic realm="GitHub"`)
			http.Error(w, "Repository not found.", http.StatusUnauthorized)
			return
		}
		h := &cgi.Handler{Path: f.backend, Root: "/git", Env: []string{"GIT_PROJECT_ROOT=" + f.root, "GIT_HTTP_EXPORT_ALL=1"}}
		h.ServeHTTP(w, r)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// addRepo creates owner/name on the fake with one commit on main.
func (f *fakeGitHub) addRepo(owner, name string, files map[string]string) *fakeRepo {
	f.t.Helper()
	ctx := context.Background()
	path := filepath.Join(f.root, owner, name+".git")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if _, err := gitx.Run(ctx, "", nil, "init", "--quiet", "--bare", "--initial-branch=main", path); err != nil {
		f.t.Fatal(err)
	}
	r := &fakeRepo{id: int64(1296269 + len(f.repos)), owner: owner, name: name, path: path, defaultBranch: "main"}
	f.mu.Lock()
	f.repos[strings.ToLower(owner+"/"+name)] = r
	f.mu.Unlock()
	if files == nil {
		files = map[string]string{"README.md": "# " + name + "\n"}
	}
	r.commit(f.t, "main", "Initial commit", files)
	return r
}

func (r *fakeRepo) tip(t *testing.T, ref string) string {
	t.Helper()
	refs, err := gitx.Refs(context.Background(), r.path)
	if err != nil {
		t.Fatal(err)
	}
	return refs[ref]
}

// commit adds a commit on branch (created from main when missing) and returns its id.
func (r *fakeRepo) commit(t *testing.T, branch, msg string, files map[string]string) string {
	t.Helper()
	ctx := context.Background()
	ref := "refs/heads/" + branch
	parent := r.tip(t, ref)
	old := parent
	if parent == "" {
		old = gitx.ZeroOID
		parent = r.tip(t, "refs/heads/main")
	}
	var edits []gitx.FileEdit
	for p, c := range files {
		edits = append(edits, gitx.FileEdit{Path: p, Content: []byte(c)})
	}
	oid, err := gitx.CommitFiles(ctx, r.path, parent, edits, msg+"\n", testAuthor)
	if err != nil {
		t.Fatal(err)
	}
	if err := gitx.UpdateRef(ctx, r.path, ref, oid, old); err != nil {
		t.Fatal(err)
	}
	return oid
}

// setRef force-updates (or creates) any ref, like a force push or a tag.
func (r *fakeRepo) setRef(t *testing.T, ref, oid string) {
	t.Helper()
	if _, err := gitx.Run(context.Background(), r.path, nil, "update-ref", ref, oid); err != nil {
		t.Fatal(err)
	}
}

func (r *fakeRepo) deleteRef(t *testing.T, ref string) {
	t.Helper()
	if _, err := gitx.Run(context.Background(), r.path, nil, "update-ref", "-d", ref); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeGitHub) setRetryAfter(seconds int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retryAfter = seconds
}

func (f *fakeGitHub) statusCalls() []statusCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]statusCall(nil), f.statuses...)
}

func (f *fakeGitHub) callCount(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method+" "+path]
}

// harness is an Ore server with the adapter attached.
type harness struct {
	t   *testing.T
	srv *httptest.Server
	s   *web.Server
	svc *Service
	gh  *fakeGitHub
	db  *ledger.DB
	tok string
	dir string
	// clock is the injected time; advance it with h.tick.
	clock time.Time
	mu    sync.Mutex
}

func newHarness(t *testing.T, gh *fakeGitHub, cfg Config) *harness {
	t.Helper()
	dir := t.TempDir()
	db, err := ledger.Open(context.Background(), filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repos, logs := filepath.Join(dir, "repos"), filepath.Join(dir, "logs")
	s := &web.Server{DB: db, ReposDir: repos, LogsDir: logs, Token: "tok", BaseURL: "http://ore.test", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Runner:   &pipelines.Runner{DB: db, ReposDir: repos, LogsDir: logs, Executor: "host"},
		Deployer: &delivery.Deployer{DB: db, ReposDir: repos, Executor: "none"},
		Review:   &review.Service{DB: db, ReposDir: repos},
	}
	h := &harness{t: t, s: s, gh: gh, db: db, tok: "tok", dir: dir, clock: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	if gh != nil && cfg.API == "" {
		cfg.API = gh.srv.URL
	}
	if cfg.Now == nil {
		cfg.Now = h.now
	}
	if cfg.SyncInterval == 0 {
		cfg.SyncInterval = -1
	}
	if cfg.StatusInterval == 0 {
		cfg.StatusInterval = -1
	}
	h.svc = New(cfg, s)
	h.svc.Attach()
	h.srv = httptest.NewServer(s.Handler())
	t.Cleanup(h.srv.Close)
	return h
}

func (h *harness) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clock
}

func (h *harness) tick(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clock = h.clock.Add(d)
}

func (h *harness) api(method, path string, in, out any) int {
	h.t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, body)
	req.Header.Set("Authorization", "Bearer "+h.tok)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil {
		if s, ok := out.(*string); ok {
			*s = string(data)
		} else if resp.StatusCode < 400 {
			if err := json.Unmarshal(data, out); err != nil {
				h.t.Fatalf("%s %s: bad json %v: %s", method, path, err, data)
			}
		} else if m, ok := out.(*map[string]any); ok {
			_ = json.Unmarshal(data, m)
		}
	}
	return resp.StatusCode
}

func (h *harness) createRepo(org, name string) ledger.Repo {
	h.t.Helper()
	if code := h.api("POST", "/api/v0/orgs", map[string]string{"name": org}, nil); code != 201 && code != 409 {
		h.t.Fatalf("create org: %d", code)
	}
	var repo ledger.Repo
	if code := h.api("POST", "/api/v0/repos", map[string]any{"org": org, "name": name, "autonomy_level": 2}, &repo); code != 201 {
		h.t.Fatalf("create repo: %d", code)
	}
	return repo
}

func (h *harness) link(org, name, owner, ghName string) LinkResponse {
	h.t.Helper()
	var res LinkResponse
	var raw string
	code := h.api("POST", "/api/v0/repos/"+org+"/"+name+"/github/link", LinkRequest{Owner: owner, Name: ghName}, &raw)
	if code != 201 {
		h.t.Fatalf("link: %d %s", code, raw)
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		h.t.Fatal(err)
	}
	return res
}

func (h *harness) refs(org, name string) map[string]string {
	h.t.Helper()
	var refs map[string]string
	if code := h.api("GET", "/api/v0/repos/"+org+"/"+name+"/refs", nil, &refs); code != 200 {
		h.t.Fatalf("refs: %d", code)
	}
	return refs
}

func (h *harness) events(typ string) []ledger.Event {
	h.t.Helper()
	evs, err := h.db.Events(context.Background(), ledger.EventsOptions{Type: typ, Limit: 1000})
	if err != nil {
		h.t.Fatal(err)
	}
	return evs
}

// deliver posts a signed webhook and returns the status and decoded body.
func (h *harness) deliver(event, deliveryID, secret string, payload []byte) (int, map[string]any) {
	h.t.Helper()
	return h.deliverRaw(event, deliveryID, Sign(secret, payload), "application/json", payload)
}

func (h *harness) deliverRaw(event, deliveryID, signature, contentType string, payload []byte) (int, map[string]any) {
	h.t.Helper()
	req, _ := http.NewRequest("POST", h.srv.URL+"/hooks/github", bytes.NewReader(payload))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "GitHub-Hookshot/8a4c1a2")
	req.Header.Set(HeaderEvent, event)
	req.Header.Set(HeaderDelivery, deliveryID)
	if signature != "" {
		req.Header.Set(HeaderSignature, signature)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return resp.StatusCode, out
}

// data decodes an event's payload.
func data(t *testing.T, e ledger.Event) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(e.Data, &m); err != nil {
		t.Fatalf("event %s data: %v", e.Type, err)
	}
	return m
}

// fixture loads a recorded payload.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// payload loads a fixture, retargets it at the fake repository and applies edits
// ("a.b.c" paths) so the recorded shape is reused with live object ids.
func payload(t *testing.T, name string, repo *fakeRepo, edits map[string]any) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(fixture(t, name), &m); err != nil {
		t.Fatal(err)
	}
	if repo != nil {
		set(m, "repository.name", repo.name)
		set(m, "repository.full_name", repo.owner+"/"+repo.name)
		set(m, "repository.owner.login", repo.owner)
		set(m, "repository.id", float64(repo.id))
	}
	for k, v := range edits {
		set(m, k, v)
	}
	b, _ := json.Marshal(m)
	return b
}

func set(m map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	m[parts[len(parts)-1]] = v
}

func (h *harness) must(org, name string) ledger.Repo {
	h.t.Helper()
	repo, err := h.db.RepoByName(context.Background(), org, name)
	if err != nil {
		h.t.Fatal(err)
	}
	return repo
}

func (h *harness) mustLink(org, name string) Link {
	h.t.Helper()
	l, err := h.svc.store.linkByRepo(context.Background(), h.must(org, name).ID)
	if err != nil {
		h.t.Fatal(err)
	}
	return l
}
