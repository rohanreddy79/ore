// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/agent"
	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
	"github.com/orevcs/ore/internal/review"
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
