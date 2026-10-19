// SPDX-License-Identifier: AGPL-3.0-only

package webapp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/review"
	"github.com/orevcs/ore/internal/web"

	// The same slices `ore serve` loads, so the test server authenticates and
	// reports features the way the binary does.
	_ "github.com/orevcs/ore/internal/connect/gateway"
	_ "github.com/orevcs/ore/internal/connect/mcp"
	_ "github.com/orevcs/ore/internal/delivery/environments"
	_ "github.com/orevcs/ore/internal/fpi/github"
	_ "github.com/orevcs/ore/internal/identity"
	_ "github.com/orevcs/ore/internal/pipelines/actions"
	_ "github.com/orevcs/ore/internal/review/v1"
)

func newTestServer(t *testing.T) (*httptest.Server, *web.Server) {
	t.Helper()
	t.Setenv("ORE_SSH_ADDR", "off")
	dir := t.TempDir()
	db, err := ledger.Open(context.Background(), filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repos, logs := filepath.Join(dir, "repos"), filepath.Join(dir, "logs")
	s := &web.Server{DB: db, ReposDir: repos, LogsDir: logs, Token: "tok", BaseURL: "http://test", Log: slog.Default(),
		Runner:   &pipelines.Runner{DB: db, ReposDir: repos, LogsDir: logs, Executor: "host"},
		Deployer: &delivery.Deployer{DB: db, ReposDir: repos, Executor: "none"},
		Review:   &review.Service{DB: db, ReposDir: repos},
		Services: map[string]any{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := registry.Apply(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Services[Name].(*Info); !ok {
		t.Fatal("extension must publish Services[webapp]")
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, s
}

type caller struct {
	t     *testing.T
	srv   *httptest.Server
	token string
}

func (c caller) req(method, path string, in any, hdr map[string]string) *http.Response {
	c.t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.srv.URL+path, body)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.srv.Client().Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp
}

func (c caller) do(method, path string, in any, out any) int {
	c.t.Helper()
	resp := c.req(method, path, in, nil)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && len(b) > 0 && resp.StatusCode < 300 {
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("%s %s: decode %v: %s", method, path, err, b)
		}
	}
	return resp.StatusCode
}

func seed(t *testing.T, c caller) {
	t.Helper()
	if code := c.do("POST", "/api/v0/orgs", map[string]string{"name": "acme"}, nil); code != 201 {
		t.Fatalf("org: %d", code)
	}
	if code := c.do("POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "app"}, nil); code != 201 {
		t.Fatalf("repo: %d", code)
	}
	files := []map[string]any{
		{"path": "README.md", "content": "# app\n\nhello\n"},
		{"path": "main.go", "content": "package main\n\nfunc main() {}\n"},
		{"path": ".ore/pipeline.yaml", "content": "version: 0\ntasks:\n  - id: say\n    run: for i in 1 2 3; do echo line $i; done\n"},
	}
	if code := c.do("POST", "/api/v0/repos/acme/app/commits", map[string]any{"message": "import", "files": files}, nil); code != 201 {
		t.Fatalf("commit: %d", code)
	}
}

func TestAppRoutesServeShellOrBuildPage(t *testing.T) {
	srv, _ := newTestServer(t)
	c := caller{t: t, srv: srv}
	for _, p := range []string{"/app/", "/app/acme/app/changes/1", "/app/login", "/app/acme/app/blob/main/README.md", "/app/acme/app/tree/main/docs/notes.txt"} {
		resp := c.req("GET", p, nil, nil)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if Built() {
			if resp.StatusCode != 200 || !strings.Contains(string(b), `<div id="root">`) {
				t.Fatalf("%s: built app should serve the shell, got %d %q", p, resp.StatusCode, b[:min(len(b), 80)])
			}
			if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
				t.Fatalf("shell must not be cached: %q", cc)
			}
		} else {
			if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(b), "npm run build") {
				t.Fatalf("%s: unbuilt app should explain how to build, got %d", p, resp.StatusCode)
			}
		}
	}
	// A missing asset is a 404, never the shell; so is a missing top-level file.
	for _, p := range []string{"/app/assets/nope.js", "/app/nope.json"} {
		resp := c.req("GET", p, nil, nil)
		resp.Body.Close()
		if Built() && resp.StatusCode != 404 {
			t.Fatalf("missing asset %s: %d", p, resp.StatusCode)
		}
	}
	// Four-segment paths are served through the host-qualified patterns.
	req4, _ := http.NewRequest("GET", srv.URL+"/app/acme/app/issues", nil)
	req4.Host = "localhost:7777"
	r4, _ := srv.Client().Do(req4)
	r4.Body.Close()
	if (Built() && r4.StatusCode != 200) || (!Built() && r4.StatusCode != 503) {
		t.Fatalf("4-segment app path on a known host: %d", r4.StatusCode)
	}
	// "/" redirects to the app on known hosts once the app is built.
	cl0 := srv.Client()
	cl0.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req0, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req0.Host = "localhost"
	r0, _ := cl0.Do(req0)
	r0.Body.Close()
	if Built() && (r0.StatusCode != 302 || r0.Header.Get("Location") != "/app/") {
		t.Fatalf("index redirect: %d %s", r0.StatusCode, r0.Header.Get("Location"))
	}
	if !Built() && r0.StatusCode == 302 {
		t.Fatal("index must not redirect to an unbuilt app")
	}
	// /app redirects to /app/.
	cl := srv.Client()
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, _ := cl.Get(srv.URL + "/app")
	resp.Body.Close()
	if resp.StatusCode != 301 || resp.Header.Get("Location") != "/app/" {
		t.Fatalf("/app: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestSessionCookieFlow(t *testing.T) {
	srv, _ := newTestServer(t)
	anon := caller{t: t, srv: srv}
	if code := anon.do("GET", "/api/v0/webapp/session", nil, nil); code != 401 {
		t.Fatalf("anonymous session: %d", code)
	}
	if code := anon.do("POST", "/api/v0/webapp/session", map[string]string{"token": "wrong"}, nil); code != 401 {
		t.Fatalf("bad token: %d", code)
	}
	if code := anon.do("POST", "/api/v0/webapp/session", map[string]string{"token": ""}, nil); code != 400 {
		t.Fatalf("empty token: %d", code)
	}
	resp := anon.req("POST", "/api/v0/webapp/session", map[string]string{"token": "tok"}, nil)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("sign in: %d", resp.StatusCode)
	}
	var cookie *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == cookieName {
			cookie = ck
		}
	}
	if cookie == nil || cookie.Value != "tok" || !cookie.HttpOnly || cookie.Path != "/" {
		t.Fatalf("cookie: %+v", cookie)
	}
	// The cookie authenticates the REST API and the session endpoint.
	req, _ := http.NewRequest("GET", srv.URL+"/api/v0/webapp/session", nil)
	req.AddCookie(cookie)
	r2, _ := srv.Client().Do(req)
	var me struct{ Actor ledger.Actor }
	json.NewDecoder(r2.Body).Decode(&me)
	r2.Body.Close()
	if r2.StatusCode != 200 || me.Actor.ID != "admin" {
		t.Fatalf("session with cookie: %d %+v", r2.StatusCode, me)
	}
	req, _ = http.NewRequest("GET", srv.URL+"/api/v0/repos", nil)
	req.AddCookie(cookie)
	r3, _ := srv.Client().Do(req)
	r3.Body.Close()
	if r3.StatusCode != 200 {
		t.Fatalf("api with cookie: %d", r3.StatusCode)
	}
	// Sign out clears it.
	r4 := anon.req("DELETE", "/api/v0/webapp/session", nil, nil)
	r4.Body.Close()
	if r4.StatusCode != 204 {
		t.Fatalf("sign out: %d", r4.StatusCode)
	}
	cleared := false
	for _, ck := range r4.Cookies() {
		if ck.Name == cookieName && ck.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("sign out must expire the cookie")
	}
}

func TestFeaturesListsExtensions(t *testing.T) {
	srv, _ := newTestServer(t)
	c := caller{t: t, srv: srv, token: "tok"}
	var f map[string]any
	if code := c.do("GET", "/api/v0/webapp/features", nil, &f); code != 200 {
		t.Fatalf("features: %d", code)
	}
	if f["review_v1"] != true || f["identity"] != true || f["fleet"] != false {
		t.Fatalf("features: %+v", f)
	}
	if wa, ok := f["webapp"].(map[string]any); !ok || wa["prefix"] != "/app/" {
		t.Fatalf("webapp info: %+v", f["webapp"])
	}
}

// readSSE collects events from a stream until fn returns true or the deadline passes.
func readSSE(t *testing.T, body io.Reader, deadline time.Duration, fn func(id, event, data string) bool) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var id, ev string
		var data []string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if len(data) > 0 || ev != "" {
					if fn(id, ev, strings.Join(data, "\n")) {
						return
					}
				}
				id, ev, data = "", "", nil
			case strings.HasPrefix(line, ":"):
			case strings.HasPrefix(line, "id: "):
				id = line[4:]
			case strings.HasPrefix(line, "event: "):
				ev = line[7:]
			case strings.HasPrefix(line, "data: "):
				data = append(data, line[6:])
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(deadline):
		t.Fatal("stream: deadline passed")
	}
}

func TestEventsStreamIsLiveAndResumable(t *testing.T) {
	srv, _ := newTestServer(t)
	c := caller{t: t, srv: srv, token: "tok"}
	seed(t, c)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v0/webapp/events/stream?type=io.ore.issue.", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type %q", ct)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		c.do("POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": "first"}, nil)
		c.do("POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": "second"}, nil)
	}()
	var seqs []string
	var titles []string
	readSSE(t, resp.Body, 10*time.Second, func(id, ev, data string) bool {
		if ev == "ready" {
			return false
		}
		var e ledger.Event
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			t.Fatalf("bad event json: %v", err)
		}
		if e.Type != "io.ore.issue.opened" {
			t.Fatalf("filter leaked %s", e.Type)
		}
		var d struct{ Title string }
		json.Unmarshal(e.Data, &d)
		seqs = append(seqs, id)
		titles = append(titles, d.Title)
		return len(titles) == 2
	})
	if strings.Join(titles, ",") != "first,second" {
		t.Fatalf("titles %v", titles)
	}
	cancel()

	// Resume after the first id: only the second event again, no duplicates.
	req2, _ := http.NewRequest("GET", srv.URL+"/api/v0/webapp/events/stream?type=io.ore.issue.", nil)
	req2.Header.Set("Authorization", "Bearer tok")
	req2.Header.Set("Last-Event-ID", seqs[0])
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	resp2, err := srv.Client().Do(req2.WithContext(ctx2))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var resumed []string
	readSSE(t, resp2.Body, 5*time.Second, func(id, ev, data string) bool {
		if ev == "ready" {
			return false
		}
		resumed = append(resumed, id)
		return true
	})
	if len(resumed) != 1 || resumed[0] != seqs[1] {
		t.Fatalf("resume: got %v want [%s]", resumed, seqs[1])
	}
}

func TestLogStreamBatchesAndFinishes(t *testing.T) {
	srv, _ := newTestServer(t)
	c := caller{t: t, srv: srv, token: "tok"}
	seed(t, c)
	var run ledger.PipelineRun
	if code := c.do("POST", "/api/v0/repos/acme/app/runs", map[string]any{"wait": true}, &run); code != 200 {
		t.Fatalf("run: %d", code)
	}
	if run.Status != "succeeded" {
		t.Fatalf("run status %s", run.Status)
	}
	resp := c.req("GET", "/api/v0/webapp/runs/"+run.ID+"/log/stream", nil, nil)
	defer resp.Body.Close()
	var lines []string
	var statuses []string
	gotDone := false
	readSSE(t, resp.Body, 10*time.Second, func(id, ev, data string) bool {
		switch ev {
		case "lines":
			var batch []string
			json.Unmarshal([]byte(data), &batch)
			lines = append(lines, batch...)
		case "status":
			statuses = append(statuses, data)
		case "done":
			gotDone = true
			return true
		}
		return false
	})
	if !gotDone {
		t.Fatal("no done event")
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"line 1", "line 2", "line 3"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("log missing %q in %q", want, joined)
		}
	}
	if len(statuses) == 0 || !strings.Contains(statuses[0], "succeeded") {
		t.Fatalf("statuses %v", statuses)
	}
	// Resuming after N lines skips them.
	resp2 := c.req("GET", "/api/v0/webapp/runs/"+run.ID+"/log/stream?after="+itoa(len(lines)-1), nil, nil)
	defer resp2.Body.Close()
	var tail []string
	readSSE(t, resp2.Body, 10*time.Second, func(id, ev, data string) bool {
		if ev == "lines" {
			var batch []string
			json.Unmarshal([]byte(data), &batch)
			tail = append(tail, batch...)
		}
		return ev == "done"
	})
	if len(tail) != 1 || tail[0] != lines[len(lines)-1] {
		t.Fatalf("resume tail %v", tail)
	}
	// Unknown run: 404.
	if code := c.do("GET", "/api/v0/webapp/runs/prun_nope/log/stream", nil, nil); code != 404 {
		t.Fatalf("unknown run: %d", code)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestCommitBlobCompareAndSearch(t *testing.T) {
	srv, _ := newTestServer(t)
	c := caller{t: t, srv: srv, token: "tok"}
	seed(t, c)
	var log []struct{ OID, Subject string }
	if code := c.do("GET", "/api/v0/repos/acme/app/log", nil, &log); code != 200 || len(log) != 1 {
		t.Fatalf("log: %d %v", code, log)
	}
	var cd CommitDetail
	if code := c.do("GET", "/api/v0/webapp/repos/acme/app/commits/"+log[0].OID, nil, &cd); code != 200 {
		t.Fatalf("commit: %d", code)
	}
	if cd.Subject != "import" || len(cd.Parents) != 0 || !strings.Contains(cd.Diff, "diff --git a/README.md") || !strings.HasPrefix(cd.Diff, "diff --git") {
		t.Fatalf("commit detail: %+v", cd)
	}
	if code := c.do("GET", "/api/v0/webapp/repos/acme/app/commits/deadbeef", nil, nil); code != 404 {
		t.Fatalf("unknown commit: %d", code)
	}
	if code := c.do("GET", "/api/v0/webapp/repos/acme/app/commits/--help", nil, nil); code != 400 {
		t.Fatalf("option injection: %d", code)
	}

	var b Blob
	if code := c.do("GET", "/api/v0/webapp/repos/acme/app/blob?path=README.md", nil, &b); code != 200 {
		t.Fatalf("blob: %d", code)
	}
	if b.Binary || b.Lines != 3 || !strings.HasPrefix(b.Content, "# app") || b.LastCommit == nil || b.LastCommit.Subject != "import" {
		t.Fatalf("blob: %+v", b)
	}
	if code := c.do("GET", "/api/v0/webapp/repos/acme/app/blob?path=missing.txt", nil, nil); code != 404 {
		t.Fatalf("missing blob: %d", code)
	}
	if code := c.do("GET", "/api/v0/webapp/repos/acme/app/blob?path=../etc", nil, nil); code != 400 {
		t.Fatalf("traversal: %d", code)
	}

	// A second commit on a branch, then compare.
	if code := c.do("POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "feature", "message": "tweak", "files": []map[string]any{{"path": "README.md", "content": "# app\n\nhello there\n"}}}, nil); code != 201 {
		t.Fatalf("branch commit: %d", code)
	}
	var cmp struct {
		Diff    string
		Commits []struct{ Subject string }
	}
	if code := c.do("GET", "/api/v0/webapp/repos/acme/app/compare?base=main&head=feature", nil, &cmp); code != 200 {
		t.Fatalf("compare: %d", code)
	}
	if !strings.Contains(cmp.Diff, "+hello there") || len(cmp.Commits) != 1 || cmp.Commits[0].Subject != "tweak" {
		t.Fatalf("compare: %+v", cmp)
	}
	if code := c.do("GET", "/api/v0/webapp/repos/acme/app/compare?base=main&head=nope", nil, nil); code != 404 {
		t.Fatalf("compare unknown: %d", code)
	}

	c.do("POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": "Retry loop never backs off"}, nil)
	var res struct {
		Hits []struct{ Kind, Repo, Title, URL string }
	}
	if code := c.do("GET", "/api/v0/webapp/search?q=retry", nil, &res); code != 200 || len(res.Hits) != 1 || res.Hits[0].Kind != "issue" || res.Hits[0].URL != "/app/acme/app/issues/1" {
		t.Fatalf("search: %d %+v", code, res)
	}
	if code := c.do("GET", "/api/v0/webapp/search?q=acme", nil, &res); code != 200 || len(res.Hits) != 1 || res.Hits[0].Kind != "repo" {
		t.Fatalf("search repo: %+v", res)
	}
	if code := c.do("GET", "/api/v0/webapp/search?q=", nil, &res); code != 200 || len(res.Hits) != 0 {
		t.Fatalf("empty search: %+v", res)
	}
}
