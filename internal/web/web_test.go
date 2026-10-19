// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orevcs/ore/internal/agent"
	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
	"github.com/orevcs/ore/internal/review"
	"log/slog"
)

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := ledger.Open(context.Background(), filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repos, logs := filepath.Join(dir, "repos"), filepath.Join(dir, "logs")
	s := &Server{DB: db, ReposDir: repos, LogsDir: logs, Token: "tok", BaseURL: "http://test", Log: slog.Default(),
		Runner:   &pipelines.Runner{DB: db, ReposDir: repos, LogsDir: logs, Executor: "host"},
		Deployer: &delivery.Deployer{DB: db, ReposDir: repos, Executor: "none"},
		Review:   &review.Service{DB: db, ReposDir: repos},
	}
	s.Loop = &agent.Loop{DB: db, ReposDir: repos, LogsDir: logs, Runner: s.Runner, Deployer: s.Deployer, Review: s.Review,
		AdapterCmd: `sed -i.bak 's/World/Ore/' greeting.txt && rm greeting.txt.bak`, AdapterName: "demo-script"}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, "tok"
}

func call(t *testing.T, srv *httptest.Server, token, method, path string, in any, out any) int {
	t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 400 {
		if s, ok := out.(*string); ok {
			*s = string(data)
		} else if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s: bad json %v: %s", method, path, err, data)
		}
	}
	return resp.StatusCode
}

func TestAPIEndToEnd(t *testing.T) {
	srv, tok := newTestServer(t)
	if code := call(t, srv, "", "GET", "/api/v0/orgs", nil, nil); code != 401 {
		t.Fatalf("expected 401 without token, got %d", code)
	}
	if code := call(t, srv, tok, "POST", "/api/v0/orgs", map[string]string{"name": "acme"}, nil); code != 201 {
		t.Fatalf("create org: %d", code)
	}
	var repo ledger.Repo
	if code := call(t, srv, tok, "POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "app", "autonomy_level": 3}, &repo); code != 201 {
		t.Fatalf("create repo: %d", code)
	}
	// server-side commit through the API
	var commit struct{ Commit, Ref string }
	code := call(t, srv, tok, "POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "main", "message": "init", "files": []map[string]any{
		{"path": "greeting.txt", "content": "Hello, World!\n"},
		{"path": ".ore/pipeline.yaml", "content": "version: 0\ntasks:\n  - id: test\n    run: grep -q 'Hello, Ore!' greeting.txt\n"},
	}}, &commit)
	if code != 201 || commit.Ref != "refs/heads/main" || len(commit.Commit) != 40 {
		t.Fatalf("commit: %d %+v", code, commit)
	}
	var refs map[string]string
	call(t, srv, tok, "GET", "/api/v0/repos/acme/app/refs", nil, &refs)
	if refs["refs/heads/main"] != commit.Commit {
		t.Fatalf("refs: %v", refs)
	}
	// red pipeline on main is the signal
	var run ledger.PipelineRun
	call(t, srv, tok, "POST", "/api/v0/repos/acme/app/runs", map[string]any{"wait": true}, &run)
	if run.Status != "failed" {
		t.Fatalf("expected red main, got %s", run.Status)
	}
	var issue ledger.Issue
	call(t, srv, tok, "POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": "fix greeting"}, &issue)
	var arun ledger.AgentRun
	if code := call(t, srv, tok, "POST", "/api/v0/repos/acme/app/agent/runs", map[string]any{"issue": issue.Number, "wait": true}, &arun); code != 200 {
		t.Fatalf("agent run: %d", code)
	}
	if arun.Status != "completed" {
		var log string
		call(t, srv, tok, "GET", "/api/v0/repos/acme/app/agent/runs/"+arun.ID+"/log", nil, &log)
		t.Fatalf("agent run %s %q\n%s", arun.Status, arun.Error, log)
	}
	var ch ledger.Change
	call(t, srv, tok, "GET", "/api/v0/repos/acme/app/changes/1", nil, &ch)
	if ch.State != "merged" {
		t.Fatalf("change: %+v", ch)
	}
	var diff string
	call(t, srv, tok, "GET", "/api/v0/repos/acme/app/changes/1/diff", nil, &diff)
	if !strings.Contains(diff, "+Hello, Ore!") {
		t.Fatalf("diff: %s", diff)
	}
	// merged change cannot be merged again (409)
	if code := call(t, srv, tok, "POST", "/api/v0/repos/acme/app/changes/1/merge", map[string]string{}, nil); code != 500 && code != 409 {
		t.Fatalf("re-merge: %d", code)
	}
	var file string
	call(t, srv, tok, "GET", "/api/v0/repos/acme/app/file?path=greeting.txt", nil, &file)
	if file != "Hello, Ore!\n" {
		t.Fatalf("file on main: %q", file)
	}
	var evs []ledger.Event
	call(t, srv, tok, "GET", "/api/v0/events?type=io.ore.", nil, &evs)
	if len(evs) < 12 {
		t.Fatalf("expected a full ledger, got %d events", len(evs))
	}
	// UI renders
	req, _ := http.NewRequest("GET", srv.URL+"/acme/app/changes/1", nil)
	req.AddCookie(&http.Cookie{Name: "ore_token", Value: tok})
	resp, _ := http.DefaultClient.Do(req)
	page, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(page), "Merge server-side") && !strings.Contains(string(page), "merged") {
		t.Fatalf("ui: %d %s", resp.StatusCode, page[:200])
	}
}

func TestTreeListingIsScoped(t *testing.T) {
	srv, tok := newTestServer(t)
	call(t, srv, tok, "POST", "/api/v0/orgs", map[string]string{"name": "acme"}, nil)
	call(t, srv, tok, "POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "big"}, nil)
	var files []map[string]any
	for i := 0; i < 300; i++ {
		files = append(files, map[string]any{"path": fmt.Sprintf("pkg%02d/file%03d.txt", i%10, i), "content": "x"})
	}
	files = append(files, map[string]any{"path": "README.md", "content": "hi"})
	if code := call(t, srv, tok, "POST", "/api/v0/repos/acme/big/commits", map[string]any{"branch": "main", "message": "many", "files": files}, nil); code != 201 {
		t.Fatalf("commit: %d", code)
	}
	get := func(q string) ([]string, http.Header) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v0/repos/acme/big/tree"+q, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out []string
		json.NewDecoder(resp.Body).Decode(&out)
		return out, resp.Header
	}
	all, h := get("")
	if len(all) != 301 || h.Get("X-Ore-Tree-Total") != "301" || h.Get("X-Ore-Tree-Truncated") != "false" {
		t.Fatalf("full listing: %d %v", len(all), h)
	}
	page, h := get("?limit=100&offset=250")
	if len(page) != 51 || h.Get("X-Ore-Tree-Truncated") != "true" {
		t.Fatalf("page: %d %v", len(page), h)
	}
	top, _ := get("?depth=1")
	if len(top) != 11 || top[0] != "README.md" || top[1] != "pkg00/" {
		t.Fatalf("children: %v", top)
	}
	sub, h := get("?path=pkg03&depth=1")
	if len(sub) != 30 || h.Get("X-Ore-Tree-Total") != "30" || !strings.HasPrefix(sub[0], "pkg03/file") {
		t.Fatalf("subdir children: %d %v", len(sub), sub[:2])
	}
	rec, _ := get("?path=pkg03")
	if len(rec) != 30 {
		t.Fatalf("subdir recursive: %d", len(rec))
	}
}

// A second create of the same org or repo is a 409, not a 500: the scale
// harness's ensureRepo and the GitHub importer both rely on it.
func TestDuplicateOrgAndRepoReturn409(t *testing.T) {
	srv, tok := newTestServer(t)
	for i, want := range []int{201, 409} {
		if code := call(t, srv, tok, "POST", "/api/v0/orgs", map[string]string{"name": "dup"}, nil); code != want {
			t.Fatalf("create org #%d: %d, want %d", i+1, code, want)
		}
	}
	for i, want := range []int{201, 409} {
		if code := call(t, srv, tok, "POST", "/api/v0/repos", map[string]any{"org": "dup", "name": "app"}, nil); code != want {
			t.Fatalf("create repo #%d: %d, want %d", i+1, code, want)
		}
	}
}

// File and log reads are cached per commit, so they must follow the branch
// as it moves: a new commit changes the resolved oid and therefore the key.
func TestFileAndLogFollowTheBranchAcrossCommits(t *testing.T) {
	srv, tok := newTestServer(t)
	call(t, srv, tok, "POST", "/api/v0/orgs", map[string]string{"name": "acme"}, nil)
	call(t, srv, tok, "POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "cache"}, nil)
	for i, want := range []string{"one\n", "two\n"} {
		var commit struct{ Commit string }
		if code := call(t, srv, tok, "POST", "/api/v0/repos/acme/cache/commits", map[string]any{"branch": "main", "message": fmt.Sprintf("c%d", i), "files": []map[string]any{{"path": "f.txt", "content": want}}}, &commit); code != 201 {
			t.Fatalf("commit %d: %d", i, code)
		}
		req, _ := http.NewRequest("GET", srv.URL+"/api/v0/repos/acme/cache/file?path=f.txt", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		for pass := 0; pass < 2; pass++ { // second pass is served from the cache
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 || string(body) != want {
				t.Fatalf("file after commit %d (pass %d): %d %q", i, pass, resp.StatusCode, body)
			}
		}
		var log []map[string]any
		if code := call(t, srv, tok, "GET", "/api/v0/repos/acme/cache/log", nil, &log); code != 200 || len(log) != i+1 {
			t.Fatalf("log after commit %d: %d %d entries", i, code, len(log))
		}
	}
	if code := call(t, srv, tok, "GET", "/api/v0/repos/acme/cache/file?path=missing.txt", nil, nil); code != 404 {
		t.Fatalf("missing file: %d", code)
	}
	if code := call(t, srv, tok, "GET", "/api/v0/repos/acme/cache/log?ref=refs/heads/nope", nil, nil); code != 404 {
		t.Fatalf("missing branch: %d", code)
	}
}

// Concurrent misses on one key share a single load; a failed leader does not
// poison the waiters, who load on their own; values past the budget evict
// the oldest entries.
func TestMemCacheSingleflightAndEviction(t *testing.T) {
	c := newMemCache(1000)
	var loads int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]any, 20)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := c.get("k", func() (any, int, error) {
				atomic.AddInt32(&loads, 1)
				<-release
				return "v", 1, nil
			})
			if err != nil {
				t.Error(err)
			}
			results[i] = v
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := atomic.LoadInt32(&loads); n != 1 {
		t.Fatalf("loads: %d, want 1", n)
	}
	for _, v := range results {
		if v != "v" {
			t.Fatalf("result %v", v)
		}
	}
	// a failing leader: waiters retry on their own and succeed
	var failLoads int32
	gate := make(chan struct{})
	wg = sync.WaitGroup{}
	errs := make([]error, 5)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = c.get("f", func() (any, int, error) {
				if atomic.AddInt32(&failLoads, 1) == 1 {
					<-gate
					return nil, 0, errors.New("leader cancelled")
				}
				return "ok", 1, nil
			})
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	failed := 0
	for _, err := range errs {
		if err != nil {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("%d callers failed, want only the leader", failed)
	}
	// eviction: the budget is 1000 bytes, each entry costs size+len(key)+64
	for i := 0; i < 50; i++ {
		c.get(fmt.Sprintf("e%02d", i), func() (any, int, error) { return i, 100, nil })
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.size > c.max || len(c.entries) >= 50 || len(c.entries) == 0 {
		t.Fatalf("size %d entries %d", c.size, len(c.entries))
	}
	if _, ok := c.entries["e49"]; !ok {
		t.Fatal("newest entry evicted")
	}
}
