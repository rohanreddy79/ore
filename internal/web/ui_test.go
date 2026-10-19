// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/orevcs/ore/internal/agent"
	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/review"
	"github.com/orevcs/ore/internal/web"

	// Every wave-1 slice, as cmd/ore links them.
	_ "github.com/orevcs/ore/internal/connect/gateway"
	_ "github.com/orevcs/ore/internal/connect/mcp"
	_ "github.com/orevcs/ore/internal/delivery/environments"
	_ "github.com/orevcs/ore/internal/fpi/github"
	_ "github.com/orevcs/ore/internal/identity"
	_ "github.com/orevcs/ore/internal/pipelines/actions"
	_ "github.com/orevcs/ore/internal/review/v1"
)

const adminTok = "ui-admin-token"

// ui is a server built like `ore serve` (every extension applied) with a
// cookie-signed-in browser client and a bearer API client.
type ui struct {
	t       *testing.T
	s       *web.Server
	srv     *httptest.Server
	db      *ledger.DB
	repo    ledger.Repo
	browser *http.Client
	logs    string
}

func newUI(t *testing.T) *ui {
	t.Helper()
	dir := t.TempDir()
	db, err := ledger.Open(context.Background(), filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repos, logs := filepath.Join(dir, "repos"), filepath.Join(dir, "logs")
	os.MkdirAll(repos, 0o755)
	os.MkdirAll(logs, 0o755)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &web.Server{DB: db, ReposDir: repos, LogsDir: logs, Token: adminTok, BaseURL: "http://test", Log: quiet, Services: map[string]any{},
		Runner:         &pipelines.Runner{DB: db, ReposDir: repos, LogsDir: logs, Executor: "host"},
		Deployer:       &delivery.Deployer{DB: db, ReposDir: repos, Executor: "none"},
		Review:         &review.Service{DB: db, ReposDir: repos},
		StreamInterval: 20 * time.Millisecond,
	}
	s.Loop = &agent.Loop{DB: db, ReposDir: repos, LogsDir: logs, Runner: s.Runner, Deployer: s.Deployer, Review: s.Review, AdapterCmd: "true", AdapterName: "noop"}
	t.Setenv("ORE_SSH_ADDR", "127.0.0.1:0")
	t.Setenv("ORE_GITHUB_SYNC_INTERVAL", "off")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := registry.Apply(ctx, s); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	u := &ui{t: t, s: s, srv: srv, db: db, logs: logs, browser: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	return u
}

// api calls the JSON API with the admin token.
func (u *ui) api(method, path string, in any, out any) int {
	u.t.Helper()
	return u.apiAs(adminTok, method, path, in, out)
}

func (u *ui) apiAs(token, method, path string, in any, out any) int {
	u.t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, u.srv.URL+path, body)
	req.Header.Set("Authorization", "Bearer "+token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		u.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 400 {
		if err := json.Unmarshal(data, out); err != nil {
			u.t.Fatalf("%s %s: bad json %v: %s", method, path, err, data)
		}
	}
	if resp.StatusCode >= 400 && out != nil {
		u.t.Logf("%s %s -> %d %s", method, path, resp.StatusCode, data)
	}
	return resp.StatusCode
}

// login signs the browser in through the form with a token.
func (u *ui) login(token string) {
	u.t.Helper()
	code, _ := u.get("/login")
	if code != 200 {
		u.t.Fatalf("GET /login: %d", code)
	}
	code, _, loc := u.post("/login", url.Values{"token": {token}, "next": {"/"}})
	if code != 303 || loc != "/" {
		u.t.Fatalf("login: %d %s", code, loc)
	}
}

func (u *ui) csrf() string {
	base, _ := url.Parse(u.srv.URL)
	for _, c := range u.browser.Jar.Cookies(base) {
		if c.Name == "ore_csrf" {
			return c.Value
		}
	}
	return ""
}

func (u *ui) get(path string) (int, string) {
	u.t.Helper()
	resp, err := u.browser.Get(u.srv.URL + path)
	if err != nil {
		u.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if loc := resp.Header.Get("Location"); loc != "" {
		return resp.StatusCode, loc
	}
	return resp.StatusCode, string(b)
}

// post submits a form with the CSRF token taken from the cookie.
func (u *ui) post(path string, form url.Values) (int, string, string) {
	u.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	if form.Get("csrf") == "" {
		form.Set("csrf", u.csrf())
	}
	resp, err := u.browser.PostForm(u.srv.URL+path, form)
	if err != nil {
		u.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header.Get("Location")
}

// follow posts a form and returns the page it redirects to.
func (u *ui) follow(path string, form url.Values) string {
	u.t.Helper()
	code, body, loc := u.post(path, form)
	if code != 303 {
		u.t.Fatalf("POST %s: %d %s", path, code, body)
	}
	code, page := u.get(loc)
	if code != 200 {
		u.t.Fatalf("GET %s after POST %s: %d", loc, path, code)
	}
	return page
}

// populate creates acme/app with a commit on main and a feature branch, a
// change, an issue, a pipeline run, a deployment, an environment awaiting
// approval, a user with a token and a key, and a gateway session.
type fixture struct {
	change    ledger.Change
	issue     ledger.Issue
	run       ledger.PipelineRun
	dep       ledger.Deployment
	awaiting  string
	aliceTok  string
	sessionID string
}

func (u *ui) populate() fixture {
	t := u.t
	t.Helper()
	var f fixture
	if code := u.api("POST", "/api/v0/orgs", map[string]string{"name": "acme"}, nil); code != 201 {
		t.Fatalf("org: %d", code)
	}
	if code := u.api("POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "app", "autonomy_level": 2}, &u.repo); code != 201 {
		t.Fatalf("repo: %d", code)
	}
	u.api("POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "main", "message": "init", "files": []map[string]any{
		{"path": "README.md", "content": "# app\n"}, {"path": ".ore/pipeline.yaml", "content": "version: 0\ntasks:\n  - id: test\n    run: echo testing && true\n"}}}, nil)
	u.api("POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "feature", "message": "feature", "files": []map[string]any{{"path": "feature.txt", "content": "hello\n"}}}, nil)
	if code := u.api("POST", "/api/v0/repos/acme/app/changes", map[string]any{"source_ref": "feature", "title": "Add feature"}, &f.change); code != 201 {
		t.Fatalf("change: %d", code)
	}
	if code := u.api("POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": "Greeting is wrong", "body": "<b>bold</b> body"}, &f.issue); code != 201 {
		t.Fatalf("issue: %d", code)
	}
	if code := u.api("POST", "/api/v0/repos/acme/app/runs", map[string]any{"wait": true}, &f.run); code != 200 {
		t.Fatalf("run: %d", code)
	}
	if code := u.api("POST", "/api/v0/repos/acme/app/deployments", map[string]any{"environment": "preview"}, &f.dep); code != 201 {
		t.Fatalf("deployment: %d", code)
	}
	if code := u.api("POST", "/api/v0/repos/acme/app/environments", map[string]any{"name": "staging", "kind": "staging", "target": map[string]any{"kind": "none"},
		"policy": map[string]any{"required_approvals": 1, "approvers": []string{"admin"}}}, nil); code != 201 {
		t.Fatalf("environment: %d", code)
	}
	var parked struct{ ID string }
	if code := u.api("POST", "/api/v0/repos/acme/app/environments/staging/deployments", map[string]any{"ref": "main"}, &parked); code != 202 {
		t.Fatalf("parked deployment: %d", code)
	}
	f.awaiting = parked.ID
	if code := u.api("POST", "/api/v0/identity/users", map[string]any{"name": "alice", "email": "alice@example.com", "memberships": []map[string]string{{"org": "acme", "role": "member"}}}, nil); code != 201 {
		t.Fatalf("user: %d", code)
	}
	var tok struct {
		Token string `json:"token"`
	}
	if code := u.api("POST", "/api/v0/identity/tokens", map[string]any{"name": "laptop", "user": "alice"}, &tok); code != 201 {
		t.Fatalf("token: %d", code)
	}
	f.aliceTok = tok.Token
	u.apiAs(f.aliceTok, "POST", "/api/v0/identity/ssh-keys", map[string]string{"public_key": pubKeyLine(t), "title": "laptop"}, nil)
	u.api("POST", "/api/v0/repos/acme/app/changes/1/comments", map[string]any{"body": "<script>alert(1)</script> looks fine"}, nil)
	u.api("POST", "/api/v0/repos/acme/app/changes/1/reviews", map[string]any{"verdict": "approve", "body": "ship it",
		"findings": []map[string]any{{"severity": "high", "confidence": 0.9, "path": "feature.txt", "message": "consider a newline at EOF"}}}, nil)
	var session struct {
		Session struct{ ID string } `json:"session"`
	}
	if code := u.api("POST", "/api/v0/gateway/sessions", map[string]string{"agent_id": "bot", "repo": "acme/app"}, &session); code != 201 {
		t.Fatalf("session: %d", code)
	}
	f.sessionID = session.Session.ID
	return f
}

func pubKeyLine(t *testing.T) string {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spub, _ := ssh.NewPublicKey(pub)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(spub))) + " alice@test"
}

func pages(f fixture) map[string][]string {
	return map[string][]string{
		"/":                                   {"Repositories", "acme/app"},
		"/acme/app":                           {"acme/app", "Autonomy level", "review policy", "Environments", "staging", "DORA"},
		"/acme/app/issues":                    {"Issues", "Greeting is wrong"},
		"/acme/app/issues/1":                  {"Greeting is wrong", "&lt;b&gt;bold&lt;/b&gt;", "Comments"},
		"/acme/app/changes":                   {"Changes", "Add feature"},
		"/acme/app/changes/1":                 {"Change #1", "Patch sets", "&lt;script&gt;alert(1)&lt;/script&gt;", "consider a newline at EOF", "approve", "feature.txt", "Landable"},
		"/acme/app/changes/1?ps=1":            {"Diff of PS1"},
		"/acme/app/changes/1?from=1&amp;to=1": {"Interdiff PS1"},
		"/acme/app/stacks":                    {"Stacks", "Open stack"},
		"/acme/app/runs":                      {"Pipeline runs", "succeeded"},
		"/acme/app/runs/" + f.run.ID:          {"Pipeline run", "Tasks", "testing", "Search the log"},
		"/acme/app/agent":                     {"Agent runs"},
		"/acme/app/deployments":               {"Deployments", "preview", "staging"},
		"/acme/app/deployments/" + f.awaiting: {"Awaiting approval", "Policy decision", "Approve", "Reject"},
		"/acme/app/environments":              {"staging", "awaiting approval", "Create an environment"},
		"/acme/app/dora":                      {"deployment frequency", "lead time"},
		"/acme/app/github":                    {"GitHub", "Link a GitHub repository"},
		"/acme/app/gateway":                   {"Gateway sessions", f.sessionID, "claude mcp add"},
		"/identity":                           {"human/admin", "administrator"},
		"/identity/tokens":                    {"Create a token"},
		"/identity/keys":                      {"Add a key", "SHA256:"},
		"/identity/agents":                    {"Create an agent"},
		"/identity/users":                     {"alice", "Create a user"},
		"/events":                             {"Ledger", "replay cursor", "io.ore.issue.opened"},
		"/events?type=io.ore.issue.&actor_kind=human": {"io.ore.issue.opened"},
		"/login": {"Sign in"},
	}
}

func TestUIEveryPageRenders(t *testing.T) {
	u := newUI(t)
	f := u.populate()
	u.login(adminTok)
	for path, wants := range pages(f) {
		path = strings.ReplaceAll(path, "&amp;", "&")
		code, body := u.get(path)
		if code != 200 {
			t.Errorf("GET %s: %d %s", path, code, firstLine(body))
			continue
		}
		for _, w := range wants {
			if !strings.Contains(body, w) {
				t.Errorf("GET %s: missing %q", path, w)
			}
		}
		if !strings.Contains(body, "<title>") || !strings.Contains(body, `id="theme"`) {
			t.Errorf("GET %s: not the layout", path)
		}
		if strings.Contains(body, "fonts.googleapis") || strings.Contains(body, "<script src=") || strings.Contains(body, "<link rel=\"stylesheet\"") {
			t.Errorf("GET %s: external asset", path)
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i > 0 {
		return s[:i]
	}
	return s
}

func TestUIUnauthenticatedRedirectsToLogin(t *testing.T) {
	u := newUI(t)
	f := u.populate()
	for path := range pages(f) {
		if path == "/login" {
			continue
		}
		path = strings.ReplaceAll(path, "&amp;", "&")
		code, loc := u.get(path)
		if code != 303 || !strings.HasPrefix(loc, "/login?next=") {
			t.Errorf("GET %s without a session: %d %s", path, code, loc)
		}
	}
	code, _, loc := u.post("/ui/acme/app/issues", url.Values{"title": {"x"}, "csrf": {"x"}})
	if code != 303 || !strings.HasPrefix(loc, "/login") {
		t.Errorf("POST without a session: %d %s", code, loc)
	}
	// the API streams answer 401, not a redirect
	resp, _ := http.Get(u.srv.URL + "/api/v0/events/stream")
	if resp.StatusCode != 401 {
		t.Errorf("stream without a token: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestUILoginLogoutAndIdentityTokens(t *testing.T) {
	u := newUI(t)
	f := u.populate()
	code, body := u.get("/login")
	if code != 200 || !strings.Contains(body, "Sign in") {
		t.Fatalf("login page: %d", code)
	}
	code, body, _ = u.post("/login", url.Values{"token": {"wrong"}, "next": {"/acme/app"}})
	if code != 401 || !strings.Contains(body, "not valid") {
		t.Fatalf("bad token: %d", code)
	}
	code, _, loc := u.post("/login", url.Values{"token": {f.aliceTok}, "next": {"/acme/app"}})
	if code != 303 || loc != "/acme/app" {
		t.Fatalf("alice login: %d %s", code, loc)
	}
	code, body = u.get("/acme/app")
	if code != 200 || !strings.Contains(body, "human/alice") {
		t.Fatalf("alice page: %d", code)
	}
	code, body = u.get("/identity")
	if !strings.Contains(body, "alice@example.com") || strings.Contains(body, "administrator") {
		t.Fatalf("alice identity page: %d %s", code, firstLine(body))
	}
	code, body = u.get("/identity/users")
	if code != 200 || !strings.Contains(body, "needs an administrator") {
		t.Fatalf("alice users page: %d", code)
	}
	// an open redirect is refused
	code, _, loc = u.post("/login", url.Values{"token": {f.aliceTok}, "next": {"//evil.example/x"}})
	if code != 303 || loc != "/" {
		t.Fatalf("open redirect: %d %s", code, loc)
	}
	code, _, loc = u.post("/logout", nil)
	if code != 303 || loc != "/login" {
		t.Fatalf("logout: %d %s", code, loc)
	}
	code, loc = u.get("/acme/app")
	if code != 303 || !strings.HasPrefix(loc, "/login") {
		t.Fatalf("after logout: %d %s", code, loc)
	}
	// the v0 query-string flow still sets the cookie
	code, loc = u.get("/login?token=" + adminTok)
	if code != 302 || loc != "/" {
		t.Fatalf("query login: %d %s", code, loc)
	}
	if code, body = u.get("/identity"); !strings.Contains(body, "administrator") {
		t.Fatalf("admin after query login: %d", code)
	}
}

func TestUIStaleCSRFIsRefused(t *testing.T) {
	u := newUI(t)
	u.populate()
	u.login(adminTok)
	for name, csrf := range map[string]string{"stale": strings.Repeat("0", 32), "missing": "", "short": "abc"} {
		form := url.Values{"title": {"should not exist"}}
		if csrf != "" {
			form.Set("csrf", csrf)
		} else {
			form.Set("csrf", " ")
		}
		code, body, _ := u.post("/ui/acme/app/issues", form)
		if code != 403 || !strings.Contains(body, "security token") {
			t.Errorf("%s csrf: %d %s", name, code, firstLine(body))
		}
	}
	issues, _ := u.db.Issues(context.Background(), u.repo)
	for _, i := range issues {
		if i.Title == "should not exist" {
			t.Fatal("issue created despite a bad CSRF token")
		}
	}
	// logout also needs the token
	if code, _, _ := u.post("/logout", url.Values{"csrf": {strings.Repeat("1", 32)}}); code != 403 {
		t.Errorf("logout with a stale token: %d", code)
	}
	// the right token works
	page := u.follow("/ui/acme/app/issues", url.Values{"title": {"filed from the form"}, "body": {"details"}})
	if !strings.Contains(page, "filed from the form") {
		t.Fatal("issue form did not file")
	}
}

func TestUIFormsWriteThroughTheServices(t *testing.T) {
	u := newUI(t)
	f := u.populate()
	u.login(adminTok)
	ctx := context.Background()

	page := u.follow("/ui/acme/app/level", url.Values{"level": {"3"}, "reason": {"trusting the tests"}})
	if !strings.Contains(page, "Autonomy level set to L3") || !strings.Contains(page, "L3 land") {
		t.Error("level form")
	}
	page = u.follow("/ui/acme/app/issues/1/comments", url.Values{"body": {"<img src=x onerror=alert(1)> plain"}})
	if strings.Contains(page, "<img src=x") || !strings.Contains(page, "&lt;img src=x onerror=alert(1)&gt; plain") {
		t.Error("issue comment not escaped")
	}
	evs, _ := u.db.Events(ctx, ledger.EventsOptions{Type: "io.ore.comment.created"})
	if len(evs) != 1 || evs[0].OreActor.ID != "admin" {
		t.Errorf("comment event: %+v", evs)
	}
	page = u.follow("/ui/acme/app/changes/1/comments", url.Values{"body": {"nit: <em>style</em>"}, "path": {"feature.txt"}, "start_line": {"1"}, "end_line": {"1"}})
	if !strings.Contains(page, "nit: &lt;em&gt;style&lt;/em&gt;") || !strings.Contains(page, "feature.txt:1") {
		t.Error("change comment")
	}
	page = u.follow("/ui/acme/app/changes/1/reviews", url.Values{"verdict": {"request_changes"}, "body": {"not yet"}, "findings": {`[{"severity":"low","confidence":0.5,"path":"feature.txt","message":"typo"}]`}})
	if !strings.Contains(page, "request_changes") || !strings.Contains(page, "typo") || !strings.Contains(page, "Not landable") {
		t.Error("review form")
	}
	code, body, _ := u.post("/ui/acme/app/changes/1/reviews", url.Values{"verdict": {"approve"}, "findings": {"not json"}})
	if code != 303 || body != "" {
		t.Errorf("bad findings: %d", code)
	}
	if _, page = u.get("/acme/app/changes/1?err=x"); !strings.Contains(page, "x") {
		t.Error("flash")
	}
	// approvals: the same reviewer approves, superseding the request
	u.follow("/ui/acme/app/changes/1/reviews", url.Values{"verdict": {"approve"}})
	page = u.follow("/ui/acme/app/changes/1/land", url.Values{"strategy": {"queue"}})
	if !strings.Contains(page, "Landing queue: landed") || !strings.Contains(page, `class="pill ok">merged`) {
		t.Errorf("land: %s", firstLine(page))
	}
	// a second landing of a merged change is a flash, not a crash
	code, _, loc := u.post("/ui/acme/app/changes/1/land", url.Values{"strategy": {"queue"}})
	if code != 303 || !strings.Contains(loc, "err=") {
		t.Errorf("land again: %d %s", code, loc)
	}
	// deployments: approve the parked one (admin is the approver)
	page = u.follow("/ui/acme/app/deployments/"+f.awaiting+"/approve", url.Values{"comment": {"go"}})
	if !strings.Contains(page, "Approved") || strings.Contains(page, "Awaiting approval.") {
		t.Errorf("approve: %s", firstLine(page))
	}
	// environments: create, deploy, stop, delete
	page = u.follow("/ui/acme/app/environments", url.Values{"name": {"qa"}, "kind": {"preview"}, "target_kind": {"none"}, "required_approvals": {"0"}, "health_path": {"/health"}, "auto_rollback": {"on"}})
	if !strings.Contains(page, "Environment qa created") || !strings.Contains(page, "/health") {
		t.Error("environment form")
	}
	page = u.follow("/ui/acme/app/environments/qa/deploy", url.Values{"ref": {"main"}})
	if !strings.Contains(page, "Deployment") || !strings.Contains(page, "Policy decision") {
		t.Errorf("env deploy: %s", firstLine(page))
	}
	if strings.Contains(page, "Preview lifetime") {
		id := page[strings.Index(page, "/deployments/")+len("/deployments/"):]
		id = id[:strings.IndexAny(id, "/\"")]
		page = u.follow("/ui/acme/app/deployments/"+id+"/extend", url.Values{"hours": {"1"}})
		if !strings.Contains(page, "Preview extended") {
			t.Error("extend")
		}
	}
	u.follow("/ui/acme/app/environments/qa/delete", nil)
	// identity: token shown once, key added and removed, agent and user created
	code, body, _ = u.post("/ui/identity/tokens", url.Values{"name": {"ci"}, "scopes": {"read"}, "expires_in": {"1h"}})
	if code != 201 || !strings.Contains(body, "ore_") || !strings.Contains(body, "shown once") {
		t.Errorf("token form: %d", code)
	}
	secret := body[strings.Index(body, "<code>ore_")+6:]
	secret = secret[:strings.Index(secret, "<")]
	if _, page = u.get("/identity/tokens"); strings.Contains(page, secret) {
		t.Error("secret shown again")
	}
	if code := u.apiAs(secret, "GET", "/api/v0/repos/acme/app", nil, nil); code != 200 {
		t.Errorf("new token on the API: %d", code)
	}
	page = u.follow("/ui/identity/keys", url.Values{"public_key": {pubKeyLine(t)}, "title": {"desk"}})
	if !strings.Contains(page, "desk") {
		t.Error("key form")
	}
	code, _, loc = u.post("/ui/identity/keys", url.Values{"public_key": {"garbage"}})
	if code != 303 || !strings.Contains(loc, "err=") {
		t.Errorf("bad key: %d %s", code, loc)
	}
	page = u.follow("/ui/identity/agents", url.Values{"org": {"acme"}, "name": {"fixer"}, "owner": {"alice"}, "budgets": {`{"ci_minutes": 10}`}})
	if !strings.Contains(page, "acme/fixer") || !strings.Contains(page, "ci_minutes") {
		t.Error("agent form")
	}
	page = u.follow("/ui/identity/users", url.Values{"name": {"bob"}, "email": {"b@x"}, "role": {"member"}, "org": {"acme"}, "org_role": {"guest"}})
	if !strings.Contains(page, "bob") {
		t.Error("user form")
	}
	// gateway: open a session through the form (token shown once) and close it
	code, body, _ = u.post("/ui/acme/app/gateway/sessions", url.Values{"agent_id": {"claude-code"}})
	if code != 200 || !strings.Contains(body, "gws_") {
		t.Errorf("gateway session form: %d", code)
	}
	id := body[strings.Index(body, "Session ")+len("Session "):]
	id = id[:strings.IndexByte(id, ' ')]
	page = u.follow("/ui/acme/app/gateway/sessions/"+id+"/close", url.Values{"reason": {"done"}})
	if !strings.Contains(page, "Session closed") {
		t.Error("close session")
	}
	// pipeline and agent runs from the forms
	code, _, loc = u.post("/ui/acme/app/runs", url.Values{"ref": {"main"}})
	if code != 303 || !strings.Contains(loc, "/runs/") {
		t.Errorf("run form: %d %s", code, loc)
	}
	code, _, loc = u.post("/ui/acme/app/agent", url.Values{"prompt": {"do nothing"}})
	if code != 303 || !strings.Contains(loc, "/agent/") {
		t.Errorf("agent form: %d %s", code, loc)
	}
	if code, page = u.get(loc); code != 200 || !strings.Contains(page, "Agent run") {
		t.Errorf("agent run page: %d", code)
	}
	// stacks: open from a fresh branch and land
	u.api("POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "s1", "message": "s1", "files": []map[string]any{{"path": "s1.txt", "content": "1\n"}}}, nil)
	code, _, loc = u.post("/ui/acme/app/stacks", url.Values{"branches": {"s1\n"}})
	if code != 303 || !strings.Contains(loc, "/stacks/") {
		t.Errorf("stack form: %d %s", code, loc)
	}
	if code, page = u.get(loc); code != 200 || !strings.Contains(page, "Land stack") {
		t.Errorf("stack page: %d", code)
	}
	page = u.follow("/ui"+loc[:strings.Index(loc, "?")+0]+"/land", url.Values{"strategy": {"queue"}})
	if !strings.Contains(page, "landed") {
		t.Errorf("stack land: %s", firstLine(page))
	}
}

func TestUIMissingRepositoryIs404Page(t *testing.T) {
	u := newUI(t)
	f := u.populate()
	u.login(adminTok)
	for _, path := range []string{"/nope/none", "/nope/none/issues", "/acme/app/issues/999", "/acme/app/changes/999", "/acme/app/runs/prun_nope", "/acme/app/agent/arun_nope", "/acme/app/deployments/dep_nope", "/acme/app/stacks/stk_nope", "/acme/none/changes/1"} {
		code, body := u.get(path)
		if code != 404 || !strings.Contains(body, "Not found") || !strings.Contains(body, `id="theme"`) {
			t.Errorf("GET %s: %d %s", path, code, firstLine(body))
		}
	}
	// a run of another repository is not reachable through this one
	u.api("POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "other"}, nil)
	if code, _ := u.get("/acme/other/runs/" + f.run.ID); code != 404 {
		t.Errorf("cross-repository run: %d", code)
	}
	// forms on a missing repository redirect back with the error, no panic
	code, _, loc := u.post("/ui/nope/none/issues", url.Values{"title": {"x"}})
	if code != 303 || !strings.Contains(loc, "err=") {
		t.Errorf("POST on a missing repository: %d %s", code, loc)
	}
}

func TestUIChangeWithZeroPatchSets(t *testing.T) {
	u := newUI(t)
	u.populate()
	u.login(adminTok)
	ctx := context.Background()
	c := &ledger.Change{ChangeID: review.NewChangeID(), Title: "no patch sets", SourceRef: "refs/heads/ghost", TargetRef: "refs/heads/main", BaseOID: strings.Repeat("a", 40), HeadOID: strings.Repeat("b", 40)}
	if err := u.db.InsertChange(ctx, u.repo, c, ledger.System); err != nil {
		t.Fatal(err)
	}
	var n int
	u.db.QueryRow(ctx, `SELECT COUNT(1) FROM patch_sets WHERE change_id = ?`, c.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("expected no patch sets before the first read, got %d", n)
	}
	code, body := u.get(fmt.Sprintf("/acme/app/changes/%d", c.Number))
	if code != 200 || !strings.Contains(body, "no patch sets") || !strings.Contains(body, "Diff") {
		t.Fatalf("change with zero patch sets: %d %s", code, firstLine(body))
	}
	// the read backfills patch set 1 from the change row (review v1 semantics)
	// or, when it cannot, the page says so; either way the bogus commits give
	// a diff error, never a crash
	if !strings.Contains(body, "Patch sets (1)") && !strings.Contains(body, "No patch sets recorded") {
		t.Fatalf("patch set section: %s", firstLine(body))
	}
	if !strings.Contains(body, `class="flash bad"`) {
		t.Fatal("expected the diff error for unknown commits")
	}
	if code, body = u.get(fmt.Sprintf("/acme/app/changes/%d?ps=7&from=1&to=2", c.Number)); code != 200 || !strings.Contains(body, "Interdiff:") {
		t.Fatalf("bogus patch set selection: %d", code)
	}
}

// streamRecorder serves a handler into a pipe so a test can read a stream
// line by line and cancel it.
type streamRecorder struct {
	hdr  http.Header
	code int
	w    *io.PipeWriter
	mu   sync.Mutex
}

func (s *streamRecorder) Header() http.Header         { return s.hdr }
func (s *streamRecorder) WriteHeader(c int)           { s.code = c }
func (s *streamRecorder) Write(b []byte) (int, error) { return s.w.Write(b) }
func (s *streamRecorder) Flush()                      {}

// stream opens an SSE request against the handler; lines arrive on the
// channel, cancel ends it, done closes when the handler returned.
func (u *ui) stream(path string) (lines <-chan string, cancel func(), done <-chan struct{}) {
	pr, pw := io.Pipe()
	rec := &streamRecorder{hdr: http.Header{}, w: pw}
	ctx, cancelCtx := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", path, nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+adminTok)
	out := make(chan string, 1000)
	fin := make(chan struct{})
	go func() {
		u.s.Handler().ServeHTTP(rec, req)
		pw.Close()
		close(fin)
	}()
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			out <- sc.Text()
		}
		close(out)
	}()
	return out, cancelCtx, fin
}

func waitLine(t *testing.T, lines <-chan string, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("stream ended before %q", want)
			}
			if strings.Contains(l, want) {
				return l
			}
		case <-deadline:
			t.Fatalf("no line containing %q within %s", want, timeout)
		}
	}
}

func TestUIEventStreamDeliversAndClosesCleanly(t *testing.T) {
	u := newUI(t)
	u.populate()
	lines, cancel, done := u.stream("/api/v0/events/stream?type=io.ore.issue.")
	waitLine(t, lines, ": connected", time.Second)
	var issue ledger.Issue
	u.api("POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": "streamed"}, &issue)
	l := waitLine(t, lines, "io.ore.issue.opened", 2*time.Second)
	if !strings.Contains(l, `"title":"streamed"`) {
		t.Fatalf("event payload: %s", l)
	}
	// a type filter keeps other events out
	u.api("POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "zz", "message": "m", "files": []map[string]any{{"path": "z", "content": "z"}}}, nil)
	select {
	case l := <-lines:
		if strings.Contains(l, "io.ore.ref.updated") {
			t.Fatalf("filtered event leaked: %s", l)
		}
	case <-time.After(150 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after the client went away")
	}
	// Last-Event-ID resumes after a given sequence
	evs, _ := u.db.Events(context.Background(), ledger.EventsOptions{Type: "io.ore.issue.opened"})
	pr, pw := io.Pipe()
	rec := &streamRecorder{hdr: http.Header{}, w: pw}
	ctx, cancel2 := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/v0/events/stream?after=0", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+adminTok)
	req.Header.Set("Last-Event-ID", fmt.Sprint(evs[len(evs)-1].Seq-1))
	go func() { u.s.Handler().ServeHTTP(rec, req); pw.Close() }()
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	found := false
	deadline := time.Now().Add(2 * time.Second)
	for sc.Scan() && time.Now().Before(deadline) {
		if strings.Contains(sc.Text(), fmt.Sprintf("id: %d", evs[len(evs)-1].Seq)) {
			found = true
			break
		}
	}
	cancel2()
	if !found {
		t.Fatal("Last-Event-ID resume did not replay the next event")
	}
}

func TestUILogStreamWaitsForMissingFileThenDelivers(t *testing.T) {
	u := newUI(t)
	u.populate()
	ctx := context.Background()
	run := ledger.PipelineRun{CommitOID: strings.Repeat("c", 40), LogPath: filepath.Join(u.logs, "later.log")}
	if err := u.db.InsertPipelineRun(ctx, u.repo, &run, ledger.System); err != nil {
		t.Fatal(err)
	}
	lines, cancel, done := u.stream("/api/v0/repos/acme/app/runs/" + run.ID + "/log/stream")
	defer cancel()
	waitLine(t, lines, "event: status", time.Second)
	select {
	case l := <-lines:
		if strings.HasPrefix(l, "data: ") && l != "data: queued" {
			t.Fatalf("unexpected data before the file exists: %s", l)
		}
	case <-time.After(100 * time.Millisecond):
	}
	os.WriteFile(run.LogPath, []byte("first line\npartial"), 0o644)
	waitLine(t, lines, "data: first line", 2*time.Second)
	select {
	case l := <-lines:
		if strings.Contains(l, "partial") {
			t.Fatalf("partial line delivered early: %s", l)
		}
	case <-time.After(100 * time.Millisecond):
	}
	f, _ := os.OpenFile(run.LogPath, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(" done\nlast\n")
	f.Close()
	waitLine(t, lines, "data: partial done", 2*time.Second)
	waitLine(t, lines, "data: last", 2*time.Second)
	if err := u.db.UpdatePipelineRun(ctx, u.repo, &run, "succeeded", "", ledger.System); err != nil {
		t.Fatal(err)
	}
	waitLine(t, lines, "event: done", 2*time.Second)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("log stream did not end after done")
	}
	// the page renders the log too, with the live attributes off for a finished run
	u.login(adminTok)
	code, body := u.get("/acme/app/runs/" + run.ID)
	if code != 200 || !strings.Contains(body, "first line") || !strings.Contains(body, `data-live="0"`) {
		t.Fatalf("run page: %d", code)
	}
	// a missing run is 404 on the stream
	resp, _ := http.NewRequest("GET", u.srv.URL+"/api/v0/repos/acme/app/runs/prun_nope/log/stream", nil)
	resp.Header.Set("Authorization", "Bearer "+adminTok)
	res, _ := http.DefaultClient.Do(resp)
	if res.StatusCode != 404 {
		t.Fatalf("missing run stream: %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestUIVeryLongLogLineIsTruncated(t *testing.T) {
	u := newUI(t)
	u.populate()
	ctx := context.Background()
	long := strings.Repeat("x", 100*1024)
	run := ledger.AgentRun{Adapter: "noop", LogPath: filepath.Join(u.logs, "long.log")}
	if err := u.db.InsertAgentRun(ctx, u.repo, &run, ledger.System); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(run.LogPath, []byte("short\n"+long+"\nafter\n"), 0o644)
	u.login(adminTok)
	code, body := u.get("/acme/app/agent/" + run.ID)
	if code != 200 || !strings.Contains(body, "[line truncated: ") || strings.Contains(body, long) || !strings.Contains(body, "after") {
		t.Fatalf("agent run page: %d", code)
	}
	lines, cancel, _ := u.stream("/api/v0/repos/acme/app/agent/runs/" + run.ID + "/log/stream?offset=0")
	defer cancel()
	l := waitLine(t, lines, "[line truncated:", 2*time.Second)
	if len(l) > web.MaxLogLine+100 {
		t.Fatalf("stream line not truncated: %d bytes", len(l))
	}
	if got := web.TruncateLine("héllo" + strings.Repeat("é", web.MaxLogLine)); !strings.HasSuffix(got, "more bytes]") || !strings.HasPrefix(got, "héllo") {
		t.Fatalf("utf-8 safe truncation: %q", got[:20])
	}
}

func TestUIDiffWithBinaryFiles(t *testing.T) {
	diff := "diff --git a/logo.png b/logo.png\nindex 0000000..1111111\nBinary files a/logo.png and b/logo.png differ\ndiff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+new\n"
	html, files, _ := web.RenderDiff(diff)
	s := string(html)
	if files != 2 || !strings.Contains(s, "binary") || !strings.Contains(s, "contents are not shown") || !strings.Contains(s, `<span class="a">+new</span>`) || !strings.Contains(s, "logo.png") {
		t.Fatalf("binary diff: %s", s)
	}
	// through the page: a file with NUL bytes committed on a branch
	u := newUI(t)
	u.populate()
	u.login(adminTok)
	u.api("POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "bin", "message": "binary", "files": []map[string]any{{"path": "blob.bin", "content": "\x00\x01\x02binary\x00"}, {"path": "note.txt", "content": "text\n"}}}, nil)
	var ch ledger.Change
	if code := u.api("POST", "/api/v0/repos/acme/app/changes", map[string]any{"source_ref": "bin", "title": "binary"}, &ch); code != 201 {
		t.Fatalf("change: %d", code)
	}
	code, body := u.get(fmt.Sprintf("/acme/app/changes/%d", ch.Number))
	if code != 200 || !strings.Contains(body, `<span class="pill">binary</span>`) || !strings.Contains(body, "note.txt") || !strings.Contains(body, "1 binary") {
		t.Fatalf("binary change page: %d", code)
	}
	if h, _, _ := web.RenderDiff(""); !strings.Contains(string(h), "No differences") {
		t.Fatal("empty diff")
	}
}

func TestUIDiffBudget20000Lines(t *testing.T) {
	// the renderer alone
	var b strings.Builder
	for f := 0; f < 20; f++ {
		fmt.Fprintf(&b, "diff --git a/f%d.go b/f%d.go\n--- a/f%d.go\n+++ b/f%d.go\n@@ -1,500 +1,500 @@\n", f, f, f, f)
		for i := 0; i < 500; i++ {
			fmt.Fprintf(&b, "-line %d <old & stale>\n+line %d <new & fresh>\n", i, i)
		}
	}
	text := b.String()
	web.RenderDiff(text)
	start := time.Now()
	html, files, collapsed := web.RenderDiff(text)
	elapsed := time.Since(start)
	if files != 20 || collapsed != 20 || !strings.Contains(string(html), "&lt;new &amp; fresh&gt;") {
		t.Fatalf("render: files=%d collapsed=%d", files, collapsed)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("rendering 20,000 diff lines took %s, budget 200ms", elapsed)
	}
	// the whole change page, end to end, with a 20,000-line change
	u := newUI(t)
	u.populate()
	u.login(adminTok)
	var big strings.Builder
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&big, "line %d of the big file with some <markup> & text\n", i)
	}
	u.api("POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "big", "message": "big", "files": []map[string]any{{"path": "big.txt", "content": big.String()}}}, nil)
	var ch ledger.Change
	if code := u.api("POST", "/api/v0/repos/acme/app/changes", map[string]any{"source_ref": "big", "title": "big"}, &ch); code != 201 {
		t.Fatalf("change: %d", code)
	}
	path := fmt.Sprintf("/acme/app/changes/%d", ch.Number)
	if code, _ := u.get(path); code != 200 { // warm the caches (git, sqlite)
		t.Fatalf("big change: %d", code)
	}
	best := time.Hour
	var body string
	for i := 0; i < 3; i++ {
		start := time.Now()
		_, body = u.get(path)
		if d := time.Since(start); d < best {
			best = d
		}
	}
	if !strings.Contains(body, "Expand 20001 lines") && !strings.Contains(body, "Expand 2000") {
		t.Fatalf("big diff not collapsed: %s", firstLine(body))
	}
	if best > 200*time.Millisecond {
		t.Fatalf("the change page with a 20,000-line diff rendered in %s (best of 3), budget 200ms", best)
	}
	t.Logf("20,000-line change page: %s (best of 3); renderer alone: %s", best, elapsed)
}

func TestUIPaginationAndFilters(t *testing.T) {
	u := newUI(t)
	u.populate()
	u.login(adminTok)
	for i := 0; i < 60; i++ {
		u.api("POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": fmt.Sprintf("bulk issue %02d", i)}, nil)
	}
	u.api("POST", "/api/v0/repos/acme/app/issues/3/close", map[string]string{"reason": "dup"}, nil)
	code, body := u.get("/acme/app/issues")
	if code != 200 || !strings.Contains(body, "page 1 of 2") || !strings.Contains(body, "bulk issue 59") || strings.Contains(body, "bulk issue 05") {
		t.Fatalf("page 1: %d", code)
	}
	_, body = u.get("/acme/app/issues?page=2")
	if !strings.Contains(body, "page 2 of 2") || !strings.Contains(body, "bulk issue 05") || !strings.Contains(body, "Greeting is wrong") {
		t.Fatal("page 2")
	}
	_, body = u.get("/acme/app/issues?state=closed")
	if !strings.Contains(body, "bulk issue 01") || strings.Contains(body, "bulk issue 02") {
		t.Fatal("state filter")
	}
	_, body = u.get("/acme/app/issues?page=99&per=10")
	if !strings.Contains(body, "page 7 of 7") {
		t.Fatal("page clamp")
	}
	// the ledger pages by cursor and filters by type, subject prefix and actor kind
	_, body = u.get("/events?limit=10")
	if !strings.Contains(body, "older ›") || !strings.Contains(body, "live tail") {
		t.Fatal("ledger first page")
	}
	i := strings.Index(body, `href="/events?before=`)
	if i < 0 {
		t.Fatal("no older link")
	}
	older := body[i+6:]
	older = older[:strings.Index(older, `"`)]
	_, body = u.get(strings.ReplaceAll(older, "&amp;", "&"))
	if !strings.Contains(body, "newer") || !strings.Contains(body, "live tail is off") {
		t.Fatal("ledger older page")
	}
	_, body = u.get("/events?type=io.ore.issue.closed&subject=acme/app/&actor_kind=human&org=acme")
	if !strings.Contains(body, "io.ore.issue.closed") || strings.Contains(body, "io.ore.issue.opened") {
		t.Fatal("ledger filters")
	}
	if code, _ := u.get("/events?org=nope"); code != 404 {
		t.Fatalf("unknown org filter: %d", code)
	}
}
