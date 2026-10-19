// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/review"
	"github.com/orevcs/ore/internal/web"
)

func newHTTPServer(t *testing.T) (*httptest.Server, *web.Server) {
	t.Helper()
	dir := t.TempDir()
	db, err := ledger.Open(context.Background(), filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repos := filepath.Join(dir, "repos")
	s := &web.Server{DB: db, ReposDir: repos, LogsDir: filepath.Join(dir, "logs"), Token: "admin-token", BaseURL: "http://test", Log: slog.Default(),
		Review: &review.Service{DB: db, ReposDir: repos}, Services: map[string]any{}}
	if err := registry.Apply(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if _, ok := From(s); !ok {
		t.Fatal("gateway not attached as Services[gateway]")
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, s
}

func call(t *testing.T, srv *httptest.Server, token, method, path string, in any, out any) int {
	t.Helper()
	var body io.Reader
	if in != nil {
		if s, ok := in.(string); ok {
			body = strings.NewReader(s)
		} else {
			b, _ := json.Marshal(in)
			body = bytes.NewReader(b)
		}
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
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s: bad json %v: %s", method, path, err, data)
		}
	}
	return resp.StatusCode
}

func TestRESTSessionsAndRequests(t *testing.T) {
	srv, _ := newHTTPServer(t)
	admin := "admin-token"
	call(t, srv, admin, "POST", "/api/v0/orgs", map[string]string{"name": "acme"}, nil)
	var repo ledger.Repo
	call(t, srv, admin, "POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "app"}, &repo)
	call(t, srv, admin, "POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "main", "message": "init", "files": []map[string]any{{"path": "a.txt", "content": "a\n"}}}, nil)
	call(t, srv, admin, "POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": "bug"}, nil)

	// creating a session needs the admin token and a real repository
	if code := call(t, srv, "", "POST", "/api/v0/gateway/sessions", map[string]string{"agent_id": "bot", "repo": "acme/app"}, nil); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code := call(t, srv, admin, "POST", "/api/v0/gateway/sessions", map[string]string{"agent_id": "bot", "repo": "acme/missing"}, nil); code != 404 {
		t.Fatalf("missing repo: %d", code)
	}
	if code := call(t, srv, admin, "POST", "/api/v0/gateway/sessions", map[string]string{"repo": "acme/app"}, nil); code != 400 {
		t.Fatalf("missing agent: %d", code)
	}
	var created struct {
		Session Session
		Token   string
	}
	if code := call(t, srv, admin, "POST", "/api/v0/gateway/sessions", map[string]string{"agent_id": "bot", "repo": "acme/app"}, &created); code != 201 || !IsSessionToken(created.Token) {
		t.Fatalf("create: %d %+v", code, created)
	}
	s := created.Session
	// a session token cannot mint sessions, but it can read and close its own
	if code := call(t, srv, created.Token, "POST", "/api/v0/gateway/sessions", map[string]string{"agent_id": "bot", "repo": "acme/app"}, nil); code != 401 {
		t.Fatalf("session token minting: %d", code)
	}
	var got Session
	if code := call(t, srv, created.Token, "GET", "/api/v0/gateway/"+s.ID, nil, &got); code != 200 || got.ID != s.ID {
		t.Fatalf("get own: %d %+v", code, got)
	}
	var list []Session
	if code := call(t, srv, admin, "GET", "/api/v0/gateway/sessions?repo=acme/app", nil, &list); code != 200 || len(list) != 1 {
		t.Fatalf("list: %d %+v", code, list)
	}
	// writes with the session token: the envelope must name the session
	env := func(repoID, reqs string) string {
		return `{"version":"0","session":{"agent_id":"bot","session_id":"` + s.ID + `","repository_id":"` + repoID + `"},"requests":[` + reqs + `]}`
	}
	var resp Response
	if code := call(t, srv, created.Token, "POST", "/api/v0/gateway/"+s.ID+"/requests", env(repo.ID, `{"kind":"open_issue","title":"from the gateway","body":"b"},{"kind":"comment","target":"issues/1","body":"hi"}`), &resp); code != 200 || resp.Executed != 2 {
		t.Fatalf("submit: %d %+v", code, resp)
	}
	if resp.Results[0].Issue == nil || resp.Results[0].Issue.Number != 2 || resp.Results[1].Comment == nil {
		t.Fatalf("results %+v", resp.Results)
	}
	// comments are readable through the REST API
	var comments []Comment
	if code := call(t, srv, admin, "GET", "/api/v0/repos/acme/app/issues/1/comments", nil, &comments); code != 200 || len(comments) != 1 || comments[0].Body != "hi" {
		t.Fatalf("comments: %d %+v", code, comments)
	}
	// all refused -> 403, still a decision body
	if code := call(t, srv, created.Token, "POST", "/api/v0/gateway/"+s.ID+"/requests", env("acme/elsewhere", `{"kind":"open_issue","title":"x","body":""}`), &resp); code != 403 || resp.Results[0].Code != CodeOutsideScope {
		t.Fatalf("outside scope: %d %+v", code, resp)
	}
	// the admin token may drive a session too; the write still runs as the agent
	if code := call(t, srv, admin, "POST", "/api/v0/gateway/"+s.ID+"/requests", env(repo.ID, `{"kind":"open_issue","title":"admin-driven","body":""}`), &resp); code != 200 || resp.Executed != 1 {
		t.Fatalf("admin submit: %d %+v", code, resp)
	}
	var evs []ledger.Event
	call(t, srv, admin, "GET", "/api/v0/events?type=io.ore.issue.opened", nil, &evs)
	if len(evs) != 3 || evs[1].OreActor.Kind != "agent" || evs[2].OreActor.ID != "bot" {
		t.Fatalf("issue events %+v", evs)
	}
	// a token for another session cannot act on this one
	var other struct{ Token string }
	call(t, srv, admin, "POST", "/api/v0/gateway/sessions", map[string]string{"agent_id": "bot2", "repo": "acme/app"}, &other)
	if code := call(t, srv, other.Token, "POST", "/api/v0/gateway/"+s.ID+"/requests", env(repo.ID, `{"kind":"open_issue","title":"x","body":""}`), nil); code != 403 {
		t.Fatalf("cross-session: %d", code)
	}
	// bad envelope bodies are refused (400 is only for unreadable transport)
	if code := call(t, srv, created.Token, "POST", "/api/v0/gateway/"+s.ID+"/requests", `not json`, &resp); code != 403 || resp.Results[0].Code != CodeInvalid {
		t.Fatalf("bad body: %d %+v", code, resp)
	}
	// close, then the token is refused everywhere and the refusal is recorded
	if code := call(t, srv, created.Token, "POST", "/api/v0/gateway/"+s.ID+"/close", map[string]string{"reason": "done"}, &got); code != 200 || got.State != "closed" {
		t.Fatalf("close: %d %+v", code, got)
	}
	if code := call(t, srv, created.Token, "POST", "/api/v0/gateway/"+s.ID+"/close", map[string]string{}, nil); code != 409 {
		t.Fatalf("close twice: %d", code)
	}
	if code := call(t, srv, created.Token, "POST", "/api/v0/gateway/"+s.ID+"/requests", env(repo.ID, `{"kind":"open_issue","title":"late","body":""}`), &resp); code != 403 || resp.Results[0].Code != CodeSessionClosed {
		t.Fatalf("after close: %d %+v", code, resp)
	}
	call(t, srv, admin, "GET", "/api/v0/events?type="+EventWriteGated, nil, &evs)
	var codes []string
	for _, ev := range evs {
		var d struct{ Code string }
		json.Unmarshal(ev.Data, &d)
		codes = append(codes, d.Code)
	}
	if strings.Join(codes, ",") != "outside_scope,invalid,session_closed" {
		t.Fatalf("gated codes %v", codes)
	}
	// an unknown session id
	if code := call(t, srv, admin, "GET", "/api/v0/gateway/gws_nope", nil, nil); code != 404 {
		t.Fatalf("unknown session: %d", code)
	}
}
