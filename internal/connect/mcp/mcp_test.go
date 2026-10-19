// SPDX-License-Identifier: AGPL-3.0-only

package mcp

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
	"github.com/orevcs/ore/internal/connect/gateway"
	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/review"
	"github.com/orevcs/ore/internal/web"
)

const admin = "admin-token"

type harness struct {
	t       *testing.T
	srv     *httptest.Server
	web     *web.Server
	gw      *gateway.Gateway
	repo    ledger.Repo
	other   ledger.Repo
	session gateway.Session
	token   string
}

// newHarness wires the whole server through the registry (connect.gateway
// then connect.mcp), creates acme/app (main + feat) and acme/other, and
// opens a session for agent "bot" on acme/app.
func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	db, err := ledger.Open(context.Background(), filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repos, logs := filepath.Join(dir, "repos"), filepath.Join(dir, "logs")
	s := &web.Server{DB: db, ReposDir: repos, LogsDir: logs, Token: admin, BaseURL: "http://test", Log: slog.Default(),
		Runner:   &pipelines.Runner{DB: db, ReposDir: repos, LogsDir: logs, Executor: "host"},
		Deployer: &delivery.Deployer{DB: db, ReposDir: repos, Executor: "none"},
		Review:   &review.Service{DB: db, ReposDir: repos},
		Services: map[string]any{},
	}
	s.Loop = &agent.Loop{DB: db, ReposDir: repos, LogsDir: logs, Runner: s.Runner, Deployer: s.Deployer, Review: s.Review, AdapterCmd: "true", AdapterName: "noop"}
	if err := registry.Apply(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	gw, ok := gateway.From(s)
	if !ok {
		t.Fatal("gateway missing")
	}
	if _, ok := s.Services[ServiceName].(*Server); !ok {
		t.Fatal("mcp server missing from Services")
	}
	h := &harness{t: t, srv: httptest.NewServer(s.Handler()), web: s, gw: gw}
	t.Cleanup(h.srv.Close)
	h.rest("POST", "/api/v0/orgs", map[string]string{"name": "acme"}, nil)
	h.rest("POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "app"}, &h.repo)
	h.rest("POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "other"}, &h.other)
	for _, r := range []string{"app", "other"} {
		h.rest("POST", "/api/v0/repos/acme/"+r+"/commits", map[string]any{"branch": "main", "message": "init", "files": []map[string]any{
			{"path": "README.md", "content": "# " + r + "\n"},
			{"path": ".ore/pipeline.yaml", "content": "version: 0\ntasks:\n  - id: hello\n    run: echo line1; echo line2; echo line3\n"},
		}}, nil)
	}
	h.rest("POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "feat", "message": "feature", "files": []map[string]any{{"path": "feature.txt", "content": "new\n"}}}, nil)
	var created struct {
		Session gateway.Session
		Token   string
	}
	if code := h.rest("POST", "/api/v0/gateway/sessions", map[string]string{"agent_id": "bot", "repo": "acme/app"}, &created); code != 201 {
		t.Fatalf("session: %d", code)
	}
	h.session, h.token = created.Session, created.Token
	return h
}

func (h *harness) rest(method, path string, in, out any) int {
	h.t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, body)
	req.Header.Set("Authorization", "Bearer "+admin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 400 {
		if err := json.Unmarshal(data, out); err != nil {
			h.t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
	if resp.StatusCode >= 400 {
		h.t.Logf("%s %s -> %d %s", method, path, resp.StatusCode, data)
	}
	return resp.StatusCode
}

type rpcReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  map[string]any  `json:"result"`
	Error   *RPCError       `json:"error"`
}

// raw posts a body to /mcp and returns the HTTP status and the decoded reply (if any).
func (h *harness) raw(token, body string) (int, rpcReply, http.Header) {
	h.t.Helper()
	req, _ := http.NewRequest("POST", h.srv.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var reply rpcReply
	if resp.StatusCode == 200 && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &reply); err != nil {
			h.t.Fatalf("bad reply %v: %s", err, data)
		}
	}
	return resp.StatusCode, reply, resp.Header
}

// rpc sends one JSON-RPC request with id 1 and expects HTTP 200.
func (h *harness) rpc(token, method string, params any) rpcReply {
	h.t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		msg["params"] = params
	}
	b, _ := json.Marshal(msg)
	code, reply, _ := h.raw(token, string(b))
	if code != 200 {
		h.t.Fatalf("%s: HTTP %d", method, code)
	}
	if string(reply.ID) != "1" || reply.JSONRPC != "2.0" {
		h.t.Fatalf("%s: bad envelope id=%s jsonrpc=%s", method, reply.ID, reply.JSONRPC)
	}
	return reply
}

// tool calls a tool and returns (structuredContent, text, isError).
func (h *harness) tool(token, name string, args map[string]any) (map[string]any, string, bool) {
	h.t.Helper()
	reply := h.rpc(token, "tools/call", map[string]any{"name": name, "arguments": args})
	if reply.Error != nil {
		h.t.Fatalf("tools/call %s: %v", name, reply.Error)
	}
	content, _ := reply.Result["content"].([]any)
	text := ""
	if len(content) > 0 {
		c0, _ := content[0].(map[string]any)
		text, _ = c0["text"].(string)
	}
	sc, _ := reply.Result["structuredContent"].(map[string]any)
	isErr, _ := reply.Result["isError"].(bool)
	return sc, text, isErr
}

func (h *harness) gatedCodes() []string {
	h.t.Helper()
	evs, _ := h.web.DB.Events(context.Background(), ledger.EventsOptions{Type: gateway.EventWriteGated, Limit: 1000})
	var codes []string
	for _, ev := range evs {
		var d struct{ Code string }
		json.Unmarshal(ev.Data, &d)
		codes = append(codes, d.Code)
	}
	return codes
}

func toolNames(reply rpcReply) []string {
	list, _ := reply.Result["tools"].([]any)
	var names []string
	for _, t := range list {
		m, _ := t.(map[string]any)
		names = append(names, m["name"].(string))
	}
	return names
}

func TestMCPEndToEnd(t *testing.T) {
	h := newHarness(t)

	// transport: no token -> 401 with a challenge; GET -> 405
	code, _, hdr := h.raw("", `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if code != 401 || !strings.HasPrefix(hdr.Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("unauthenticated: %d %q", code, hdr.Get("WWW-Authenticate"))
	}
	if resp, _ := http.Get(h.srv.URL + "/mcp"); resp.StatusCode != 405 {
		t.Fatalf("GET /mcp: %d", resp.StatusCode)
	}

	// initialize negotiates the protocol version, ping answers
	init := h.rpc(h.token, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "0"}})
	if init.Error != nil || init.Result["protocolVersion"] != "2025-03-26" {
		t.Fatalf("initialize: %+v", init)
	}
	if si, _ := init.Result["serverInfo"].(map[string]any); si["name"] != "ore" {
		t.Fatalf("serverInfo %+v", init.Result["serverInfo"])
	}
	init = h.rpc(h.token, "initialize", map[string]any{"protocolVersion": "1999-01-01"})
	if init.Result["protocolVersion"] != ProtocolVersion {
		t.Fatalf("unknown version negotiated to %v", init.Result["protocolVersion"])
	}
	if code, _, _ := h.raw(h.token, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != 202 {
		t.Fatalf("notification: %d", code)
	}
	if ping := h.rpc(h.token, "ping", nil); ping.Error != nil || len(ping.Result) != 0 {
		t.Fatalf("ping: %+v", ping)
	}

	// tools/list: deterministic order, ttlMs, every tool of the slice
	list := h.rpc(h.token, "tools/list", nil)
	names := toolNames(list)
	want := []string{"changes_diff", "changes_get", "changes_list", "changes_open", "changes_request_review", "context_whoami", "deployments_list", "deployments_request",
		"issues_comment", "issues_get", "issues_list", "issues_open", "ledger_events", "pipelines_run_log", "pipelines_runs", "repos_file", "repos_get", "repos_list", "repos_log", "repos_tree"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tools:\n got %v\nwant %v", names, want)
	}
	if again := toolNames(h.rpc(h.token, "tools/list", map[string]any{"cursor": ""})); strings.Join(again, ",") != strings.Join(names, ",") {
		t.Fatal("tools/list is not deterministic")
	}
	meta, _ := list.Result["_meta"].(map[string]any)
	if ttl, _ := meta["ttlMs"].(float64); ttl != 300000 {
		t.Fatalf("ttlMs %v", meta)
	}
	for _, tl := range list.Result["tools"].([]any) {
		m := tl.(map[string]any)
		if _, ok := m["inputSchema"].(map[string]any)["type"]; !ok || m["description"] == "" {
			t.Fatalf("tool %v lacks schema or description", m["name"])
		}
	}

	// identity and scope
	who, _, isErr := h.tool(h.token, "context_whoami", nil)
	if isErr || who["writes"] != "gateway" || who["actor"].(map[string]any)["kind"] != "agent" {
		t.Fatalf("whoami %+v", who)
	}
	if sess := who["session"].(map[string]any); sess["scope"] != "repo:acme/app" {
		t.Fatalf("whoami session %+v", sess)
	}
	rl := who["rate_limit"].(map[string]any)
	if rl["writes"].(float64) != 20 || rl["window"] != "10m0s" {
		t.Fatalf("rate limit %+v", rl)
	}

	// reads: repository, tree, file, log, scoped repos.list
	repos, _, _ := h.tool(h.token, "repos_list", nil)
	if rs := repos["repos"].([]any); len(rs) != 1 || rs[0].(map[string]any)["name"] != "app" {
		t.Fatalf("scoped repos_list %+v", repos)
	}
	get, _, _ := h.tool(h.token, "repos_get", nil)
	if refs := get["refs"].(map[string]any); refs["refs/heads/main"] == nil || refs["refs/heads/feat"] == nil {
		t.Fatalf("repos_get refs %+v", refs)
	}
	tree, _, _ := h.tool(h.token, "repos_tree", map[string]any{"ref": "feat"})
	if paths := fmt.Sprint(tree["paths"]); !strings.Contains(paths, "feature.txt") || !strings.Contains(paths, "README.md") {
		t.Fatalf("tree %v", paths)
	}
	_, text, isErr := h.tool(h.token, "repos_file", map[string]any{"path": "README.md"})
	if isErr || text != "# app\n" {
		t.Fatalf("file %q %v", text, isErr)
	}
	if _, _, isErr := h.tool(h.token, "repos_file", map[string]any{"path": "../etc/passwd"}); !isErr {
		t.Fatal("path traversal accepted")
	}
	log, _, _ := h.tool(h.token, "repos_log", map[string]any{"ref": "feat", "limit": 10})
	if commits := log["commits"].([]any); len(commits) != 2 || commits[0].(map[string]any)["subject"] != "feature" {
		t.Fatalf("log %+v", log)
	}

	// the ledger, scoped to the session's repository
	evs, _, _ := h.tool(h.token, "ledger_events", map[string]any{"type": "io.ore.repository.created"})
	if list := evs["events"].([]any); len(list) != 1 || list[0].(map[string]any)["subject"] != "acme/app" {
		t.Fatalf("scoped events %+v", evs)
	}

	// write through the gateway: open an issue, see both events
	res, _, isErr := h.tool(h.token, "issues_open", map[string]any{"title": "Flaky retry in uploader", "body": "seen in CI", "labels": []string{"bug"}})
	if isErr || res["status"] != "executed" || res["issue"].(map[string]any)["number"].(float64) != 1 {
		t.Fatalf("issues_open %+v", res)
	}
	evs, _, _ = h.tool(h.token, "ledger_events", map[string]any{"type": "io.ore.issue.opened"})
	opened := evs["events"].([]any)
	if len(opened) != 1 || opened[0].(map[string]any)["oreactor"].(map[string]any)["kind"] != "agent" || opened[0].(map[string]any)["oreactor"].(map[string]any)["id"] != "bot" {
		t.Fatalf("issue.opened %+v", opened)
	}
	evs, _, _ = h.tool(h.token, "ledger_events", map[string]any{"type": gateway.EventWriteExecuted})
	exec := evs["events"].([]any)
	if len(exec) != 1 || !strings.Contains(fmt.Sprint(exec[0].(map[string]any)["data"]), h.session.ID) {
		t.Fatalf("write.executed %+v", exec)
	}
	issue, _, _ := h.tool(h.token, "issues_get", map[string]any{"number": 1})
	if issue["issue"].(map[string]any)["title"] != "Flaky retry in uploader" {
		t.Fatalf("issues_get %+v", issue)
	}
	issues, _, _ := h.tool(h.token, "issues_list", map[string]any{"state": "open"})
	if len(issues["issues"].([]any)) != 1 {
		t.Fatalf("issues_list %+v", issues)
	}

	// comment, change, review request, deployment request
	cm, _, isErr := h.tool(h.token, "issues_comment", map[string]any{"issue": 1, "body": "I can take this."})
	if isErr || cm["comment"].(map[string]any)["target_kind"] != "issue" {
		t.Fatalf("comment %+v", cm)
	}
	issue, _, _ = h.tool(h.token, "issues_get", map[string]any{"number": "1"})
	if len(issue["comments"].([]any)) != 1 {
		t.Fatalf("issue comments %+v", issue)
	}
	ch, _, isErr := h.tool(h.token, "changes_open", map[string]any{"branch": "feat", "title": "Add feature", "body": "rationale", "model": "claude-fable-5-1"})
	if isErr || ch["change"].(map[string]any)["number"].(float64) != 1 || ch["existing"] == true {
		t.Fatalf("changes_open %+v", ch)
	}
	if prov := ch["change"].(map[string]any)["provenance"].(string); !strings.Contains(prov, "claude-fable-5-1") || !strings.Contains(prov, `"agent_id":"bot"`) {
		t.Fatalf("provenance %s", prov)
	}
	chg, _, _ := h.tool(h.token, "changes_get", map[string]any{"number": 1})
	if chg["change"].(map[string]any)["state"] != "open" || !strings.Contains(chg["diffstat"].(string), "feature.txt") {
		t.Fatalf("changes_get %+v", chg)
	}
	_, diff, _ := h.tool(h.token, "changes_diff", map[string]any{"number": 1})
	if !strings.Contains(diff, "+new") {
		t.Fatalf("diff %s", diff)
	}
	chs, _, _ := h.tool(h.token, "changes_list", map[string]any{"state": "open"})
	if len(chs["changes"].([]any)) != 1 {
		t.Fatalf("changes_list %+v", chs)
	}
	rr, _, isErr := h.tool(h.token, "changes_request_review", map[string]any{"number": 1, "reviewers": []string{"alice"}})
	if isErr || rr["review_request"].(map[string]any)["change"].(float64) != 1 {
		t.Fatalf("request_review %+v", rr)
	}
	cm, _, isErr = h.tool(h.token, "issues_comment", map[string]any{"change": 1, "body": "please look at the retry loop"})
	if isErr || cm["comment"].(map[string]any)["target_kind"] != "change" {
		t.Fatalf("change comment %+v", cm)
	}
	dr, _, isErr := h.tool(h.token, "deployments_request", map[string]any{"release": "feat", "environment": "preview"})
	if isErr || dr["deployment_request"].(map[string]any)["environment"] != "preview" {
		t.Fatalf("deployments_request %+v", dr)
	}
	deps, _, _ := h.tool(h.token, "deployments_list", nil)
	if len(deps["deployments"].([]any)) != 0 {
		t.Fatalf("a request must not deploy: %+v", deps)
	}

	// pipelines: run one on main through the REST API, read its log by range
	var run ledger.PipelineRun
	h.rest("POST", "/api/v0/repos/acme/app/runs", map[string]any{"wait": true}, &run)
	runs, _, _ := h.tool(h.token, "pipelines_runs", nil)
	if rs := runs["runs"].([]any); len(rs) != 1 || rs[0].(map[string]any)["id"] != run.ID {
		t.Fatalf("pipelines_runs %+v", runs)
	}
	lg, text, _ := h.tool(h.token, "pipelines_run_log", map[string]any{"run": run.ID, "start": 3, "count": 1})
	if lg["count"].(float64) != 1 || lg["total_lines"].(float64) < 3 || !strings.Contains(text, "line2") || lg["truncated"] != true {
		t.Fatalf("run_log %+v %q", lg, text)
	}
	if _, _, isErr := h.tool(h.token, "pipelines_run_log", map[string]any{"run": "prun_nope"}); !isErr {
		t.Fatal("unknown run accepted")
	}

	// the admin token: reads across repositories, writes need a session
	all, _, _ := h.tool(admin, "repos_list", nil)
	if len(all["repos"].([]any)) != 2 {
		t.Fatalf("admin repos_list %+v", all)
	}
	who, _, _ = h.tool(admin, "context_whoami", nil)
	if who["writes"] != "session_required" || who["actor"].(map[string]any)["kind"] != "human" {
		t.Fatalf("admin whoami %+v", who)
	}
	if _, _, isErr := h.tool(admin, "repos_get", nil); !isErr {
		t.Fatal("admin without repo must be asked for one")
	}
	other, _, _ := h.tool(admin, "repos_get", map[string]any{"repo": "acme/other"})
	if other["repo"].(map[string]any)["name"] != "other" {
		t.Fatalf("admin repos_get %+v", other)
	}
	sc, text, isErr := h.tool(admin, "issues_open", map[string]any{"repo": "acme/other", "title": "x"})
	if !isErr || sc["code"] != "session_required" || !strings.Contains(text, "gateway session") {
		t.Fatalf("admin write %v %q", sc, text)
	}
	if codes := h.gatedCodes(); len(codes) != 0 {
		t.Fatalf("no refusal should have been recorded, got %v", codes)
	}
	cur, _ := h.gw.Session(context.Background(), h.session.ID)
	if cur.Writes != 6 || cur.Refused != 0 {
		t.Fatalf("session counters %+v", cur)
	}
}

func TestMCPMalformedJSONRPC(t *testing.T) {
	h := newHarness(t)
	code, reply, _ := h.raw(h.token, `{"jsonrpc":"2.0","id":1,"method":`)
	if code != 200 || reply.Error == nil || reply.Error.Code != CodeParseError || string(reply.ID) != "null" {
		t.Fatalf("%d %+v", code, reply)
	}
	code, reply, _ = h.raw(h.token, `{"jsonrpc":"2.0","id":1,"method":"ping"} {"extra":1}`)
	if code != 200 || reply.Error == nil || reply.Error.Code != CodeParseError {
		t.Fatalf("trailing data: %d %+v", code, reply)
	}
	// well-formed JSON that is not a JSON-RPC 2.0 request
	code, reply, _ = h.raw(h.token, `{"jsonrpc":"1.0","id":7,"method":"ping"}`)
	if code != 200 || reply.Error == nil || reply.Error.Code != CodeInvalidRequest || string(reply.ID) != "7" {
		t.Fatalf("jsonrpc 1.0: %d %+v", code, reply)
	}
	code, reply, _ = h.raw(h.token, `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`)
	if code != 200 || reply.Error == nil || reply.Error.Code != CodeInvalidRequest {
		t.Fatalf("batch: %d %+v", code, reply)
	}
}

func TestMCPUnknownMethod(t *testing.T) {
	h := newHarness(t)
	reply := h.rpc(h.token, "tools/explode", nil)
	if reply.Error == nil || reply.Error.Code != CodeMethodNotFound || !strings.Contains(reply.Error.Message, "tools/explode") {
		t.Fatalf("%+v", reply)
	}
	if reply := h.rpc(h.token, "resources/list", nil); reply.Error == nil || reply.Error.Code != CodeMethodNotFound {
		t.Fatalf("resources/list: %+v", reply)
	}
}

func TestMCPUnknownTool(t *testing.T) {
	h := newHarness(t)
	reply := h.rpc(h.token, "tools/call", map[string]any{"name": "repos_delete", "arguments": map[string]any{}})
	if reply.Error == nil || reply.Error.Code != CodeInvalidParams || !strings.Contains(reply.Error.Message, "repos_delete") {
		t.Fatalf("%+v", reply)
	}
	if reply := h.rpc(h.token, "tools/call", map[string]any{"arguments": map[string]any{}}); reply.Error == nil || reply.Error.Code != CodeInvalidParams {
		t.Fatalf("missing name: %+v", reply)
	}
}

func TestMCPRequestOutsideSessionRepository(t *testing.T) {
	h := newHarness(t)
	// reads outside the scope are refused by the tool (not a write: not on the ledger)
	sc, text, isErr := h.tool(h.token, "repos_get", map[string]any{"repo": "acme/other"})
	if !isErr || sc["code"] != gateway.CodeOutsideScope || !strings.Contains(text, "outside the session scope") {
		t.Fatalf("read: %v %q", sc, text)
	}
	if _, _, isErr := h.tool(h.token, "repos_file", map[string]any{"repo": h.other.ID, "path": "README.md"}); !isErr {
		t.Fatal("read by id outside scope accepted")
	}
	// a write outside the scope reaches the gateway, is refused and recorded
	sc, text, isErr = h.tool(h.token, "issues_open", map[string]any{"repo": "acme/other", "title": "sneaky"})
	if !isErr || sc["code"] != gateway.CodeOutsideScope || !strings.Contains(text, "refused (outside_scope)") {
		t.Fatalf("write: %v %q", sc, text)
	}
	if codes := h.gatedCodes(); len(codes) != 1 || codes[0] != gateway.CodeOutsideScope {
		t.Fatalf("gated %v", codes)
	}
	var issues []ledger.Issue
	h.rest("GET", "/api/v0/repos/acme/other/issues", nil, &issues)
	if len(issues) != 0 {
		t.Fatal("issue written outside the scope")
	}
}

func TestMCPRateLimitExceeded(t *testing.T) {
	h := newHarness(t)
	h.gw.RateLimit = 2
	for i := 0; i < 2; i++ {
		if _, _, isErr := h.tool(h.token, "issues_open", map[string]any{"title": fmt.Sprint("issue ", i)}); isErr {
			t.Fatalf("write %d refused", i)
		}
	}
	sc, text, isErr := h.tool(h.token, "issues_open", map[string]any{"title": "one too many"})
	if !isErr || sc["code"] != gateway.CodeRateLimited || !strings.Contains(text, "rate limit exceeded: 2 writes per 10m0s") {
		t.Fatalf("%v %q", sc, text)
	}
	if codes := h.gatedCodes(); len(codes) != 1 || codes[0] != gateway.CodeRateLimited {
		t.Fatalf("gated %v", codes)
	}
	// reads are not rate-limited
	if _, _, isErr := h.tool(h.token, "issues_list", nil); isErr {
		t.Fatal("read refused by the write limit")
	}
}

func TestMCPWriteInReadOnlyMode(t *testing.T) {
	t.Setenv("ORE_MCP_READ_ONLY", "1")
	h := newHarness(t)
	names := toolNames(h.rpc(h.token, "tools/list", nil))
	if len(names) != 15 {
		t.Fatalf("read-only tools: %v", names)
	}
	for _, n := range names {
		if strings.HasSuffix(n, "_open") || strings.HasSuffix(n, "_comment") || strings.HasSuffix(n, "_request") || strings.HasSuffix(n, "_request_review") {
			t.Fatalf("write tool listed in read-only mode: %s", n)
		}
	}
	sc, text, isErr := h.tool(h.token, "issues_open", map[string]any{"title": "nope"})
	if !isErr || !strings.Contains(text, "read-only mode") || sc != nil {
		t.Fatalf("%v %q", sc, text)
	}
	who, _, _ := h.tool(h.token, "context_whoami", nil)
	if who["read_only"] != true || who["writes"] != "disabled" {
		t.Fatalf("whoami %+v", who)
	}
	var issues []ledger.Issue
	h.rest("GET", "/api/v0/repos/acme/app/issues", nil, &issues)
	if len(issues) != 0 {
		t.Fatal("issue written in read-only mode")
	}
}

func TestMCPChangesOpenForBranchAlreadyOpen(t *testing.T) {
	h := newHarness(t)
	first, _, isErr := h.tool(h.token, "changes_open", map[string]any{"branch": "feat", "title": "first"})
	if isErr || first["existing"] == true {
		t.Fatalf("first %+v", first)
	}
	second, _, isErr := h.tool(h.token, "changes_open", map[string]any{"branch": "feat", "title": "second"})
	if isErr || second["existing"] != true {
		t.Fatalf("second %+v", second)
	}
	a, b := first["change"].(map[string]any), second["change"].(map[string]any)
	if a["id"] != b["id"] || a["number"] != b["number"] || b["title"] != "first" {
		t.Fatalf("different changes: %v vs %v", a, b)
	}
	chs, _, _ := h.tool(h.token, "changes_list", nil)
	if len(chs["changes"].([]any)) != 1 {
		t.Fatalf("changes %+v", chs)
	}
}

func TestMCPChangesOpenMissingNonDefaultBase(t *testing.T) {
	h := newHarness(t)
	sc, text, isErr := h.tool(h.token, "changes_open", map[string]any{"branch": "feat", "base": "release-2", "title": "t"})
	if !isErr || sc["code"] != "bad_target" || !strings.Contains(text, "release-2 does not exist") {
		t.Fatalf("%v %q", sc, text)
	}
	if codes := h.gatedCodes(); len(codes) != 1 || codes[0] != "bad_target" {
		t.Fatalf("gated %v", codes)
	}
	// opening from the protected default branch is refused too
	sc, _, isErr = h.tool(h.token, "changes_open", map[string]any{"branch": "main", "title": "t"})
	if !isErr || sc["code"] != "protected_branch" {
		t.Fatalf("protected: %v", sc)
	}
	chs, _, _ := h.tool(h.token, "changes_list", nil)
	if len(chs["changes"].([]any)) != 0 {
		t.Fatal("a change was opened")
	}
}

func TestMCPCommentOver65536Bytes(t *testing.T) {
	h := newHarness(t)
	h.tool(h.token, "issues_open", map[string]any{"title": "target"})
	ok, _, isErr := h.tool(h.token, "issues_comment", map[string]any{"issue": 1, "body": strings.Repeat("a", gateway.MaxCommentBytes)})
	if isErr || ok["status"] != "executed" {
		t.Fatalf("exactly 65536 bytes must pass: %+v", ok)
	}
	sc, text, isErr := h.tool(h.token, "issues_comment", map[string]any{"issue": 1, "body": strings.Repeat("a", gateway.MaxCommentBytes+1)})
	if !isErr || sc["code"] != gateway.CodeInvalid || !strings.Contains(text, "65537 bytes, the limit is 65536") {
		t.Fatalf("%v %q", sc, text)
	}
	if codes := h.gatedCodes(); len(codes) != 1 || codes[0] != gateway.CodeInvalid {
		t.Fatalf("gated %v", codes)
	}
	issue, _, _ := h.tool(h.token, "issues_get", map[string]any{"number": 1})
	if len(issue["comments"].([]any)) != 1 {
		t.Fatalf("comments %+v", issue["comments"])
	}
}

func TestMCPSessionTokenAfterClose(t *testing.T) {
	h := newHarness(t)
	if _, _, isErr := h.tool(h.token, "repos_list", nil); isErr {
		t.Fatal("open session refused")
	}
	if code := h.rest("POST", "/api/v0/gateway/"+h.session.ID+"/close", map[string]string{"reason": "finished"}, nil); code != 200 {
		t.Fatalf("close: %d", code)
	}
	for _, method := range []string{"tools/list", "tools/call"} {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":{"name":"issues_open","arguments":{"title":"late"}}}`, method)
		code, _, hdr := h.raw(h.token, body)
		if code != 401 || !strings.Contains(hdr.Get("WWW-Authenticate"), "session closed") {
			t.Fatalf("%s after close: %d %q", method, code, hdr.Get("WWW-Authenticate"))
		}
	}
	if codes := h.gatedCodes(); strings.Join(codes, ",") != "session_closed,session_closed" {
		t.Fatalf("gated %v", codes)
	}
	// an unknown session token is plain 401
	if code, _, _ := h.raw("gws_deadbeef", `{"jsonrpc":"2.0","id":1,"method":"ping"}`); code != 401 {
		t.Fatalf("unknown token: %d", code)
	}
	// a closed session may still be inspected over REST by the admin
	var sess gateway.Session
	if code := h.rest("GET", "/api/v0/gateway/"+h.session.ID, nil, &sess); code != 200 || sess.State != "closed" || sess.Refused != 2 {
		t.Fatalf("closed session %d %+v", code, sess)
	}
}
