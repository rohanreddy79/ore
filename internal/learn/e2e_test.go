// SPDX-License-Identifier: AGPL-3.0-only

package learn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orevcs/ore/internal/githttp"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/identity"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
	"github.com/orevcs/ore/internal/review"
	reviewv1 "github.com/orevcs/ore/internal/review/v1"
	"github.com/orevcs/ore/internal/web"
)

const adminToken = "admin-secret-token"

// env is a full server (HTTP API, git smart HTTP, identity, review v1, the
// host pipeline executor) with the learn extension and its worker running
// at a short interval, plus helpers that drive real git and the API.
type env struct {
	t     *testing.T
	ctx   context.Context
	srv   *httptest.Server
	s     *web.Server
	svc   *Service
	db    *ledger.DB
	dir   string
	home  string
	token string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	backend, err := gitx.HTTPBackendPath(ctx)
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	db, err := ledger.Open(ctx, filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repos, logs := filepath.Join(dir, "repos"), filepath.Join(dir, "logs")
	os.MkdirAll(repos, 0o755)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &web.Server{DB: db, ReposDir: repos, LogsDir: logs, Token: adminToken, BaseURL: "http://test", Log: quiet, Services: map[string]any{},
		Git:    &githttp.Handler{DB: db, ReposDir: repos, Backend: backend, Log: quiet},
		Runner: &pipelines.Runner{DB: db, ReposDir: repos, LogsDir: logs, Executor: "host"},
		Review: &review.Service{DB: db, ReposDir: repos},
	}
	t.Setenv("ORE_SSH_ADDR", "off")
	t.Setenv("ORE_LEARN_INTERVAL", "20ms")
	t.Setenv("ORE_LESSONS_DIR", "")
	if err := identity.Extension(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := reviewv1.Extension(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := Extension(ctx, s); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	home := filepath.Join(dir, "home")
	os.MkdirAll(home, 0o700)
	e := &env{t: t, ctx: ctx, srv: srv, s: s, svc: s.Services["learn"].(*Service), db: db, dir: dir, home: home, token: adminToken}
	e.must(adminToken, "POST", "/api/v0/orgs", map[string]string{"name": "acme"}, 201, nil)
	return e
}

func (e *env) call(token, method, path string, in any) (int, string) {
	e.t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func (e *env) must(token, method, path string, in any, want int, out any) string {
	e.t.Helper()
	code, body := e.call(token, method, path, in)
	if code != want {
		e.t.Fatalf("%s %s: got %d, want %d: %s", method, path, code, want, body)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(body), out); err != nil {
			e.t.Fatalf("%s %s: bad json: %v: %s", method, path, err, body)
		}
	}
	return body
}

// user creates a user with a read+write token; orgs are memberships.
func (e *env) user(name string, orgs ...string) string {
	e.t.Helper()
	var ms []map[string]string
	for _, o := range orgs {
		ms = append(ms, map[string]string{"org": o, "role": "member"})
	}
	e.must(adminToken, "POST", "/api/v0/identity/users", map[string]any{"name": name, "email": name + "@example.com", "role": "member", "memberships": ms}, 201, nil)
	var tok struct {
		Token string `json:"token"`
	}
	e.must(adminToken, "POST", "/api/v0/identity/tokens", map[string]any{"user": name, "name": "test", "scopes": []string{"read", "write"}}, 201, &tok)
	return tok.Token
}

// git runs git with an isolated HOME and a fixed identity; it fails the test on error.
func (e *env) git(dir string, args ...string) string {
	e.t.Helper()
	out, err := e.gitErr(dir, args...)
	if err != nil {
		e.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

func (e *env) gitErr(dir string, args ...string) (string, error) {
	full := append([]string{"-c", "user.name=Learner", "-c", "user.email=learner@example.com", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+e.home, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// clone clones org/name over HTTP with the token as the password.
func (e *env) clone(repo, user, token string) string {
	e.t.Helper()
	dir := filepath.Join(e.dir, "clones", strings.ReplaceAll(repo, "/", "-")+"-"+user)
	os.MkdirAll(filepath.Dir(dir), 0o755)
	url := strings.Replace(e.srv.URL, "http://", "http://"+user+":"+token+"@", 1) + "/" + repo + ".git"
	e.git("", "clone", "--quiet", url, dir)
	return dir
}

func (e *env) write(dir, rel, content string) {
	e.t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// start begins a lesson for the admin learner.
func (e *env) start(token string, req StartRequest, want int) StartResult {
	e.t.Helper()
	var res StartResult
	e.must(token, "POST", "/api/v0/learn/runs", req, want, &res)
	return res
}

// view fetches the learner's view of a run.
func (e *env) view(token, runID, lang string) RunView {
	e.t.Helper()
	var v RunView
	q := ""
	if lang != "" {
		q = "?lang=" + lang
	}
	e.must(token, "GET", "/api/v0/learn/runs/"+runID+q, nil, 200, &v)
	return v
}

// waitStep polls until the worker moved the run to step (or completed it).
func (e *env) waitStep(token, runID string, step int) RunView {
	e.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var v RunView
	for time.Now().Before(deadline) {
		v = e.view(token, runID, "")
		if v.Run.Status == StatusCompleted || v.Run.Step >= step {
			return v
		}
		time.Sleep(25 * time.Millisecond)
	}
	out, _ := e.svc.Check(e.ctx, runID, "check")
	b, _ := json.MarshalIndent(out, "", "  ")
	e.t.Fatalf("run %s did not reach step %d (at step %d, status %s); last check:\n%s", runID, step, v.Run.Step, v.Run.Status, b)
	return v
}

func (e *env) waitCompleted(token, runID string) RunView {
	e.t.Helper()
	v := e.waitStep(token, runID, 99)
	if v.Run.Status != StatusCompleted {
		e.t.Fatalf("run %s not completed: %+v", runID, v.Run)
	}
	return v
}

func (e *env) lessonEvents(typ string) []ledger.Event {
	evs, _ := e.db.Events(e.ctx, ledger.EventsOptions{Type: typ, Limit: 1000})
	return evs
}

func repoName(res StartResult) string { return res.Run.Repo }

func fmtJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

var _ = fmt.Sprint
