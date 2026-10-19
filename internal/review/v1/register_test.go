// SPDX-License-Identifier: AGPL-3.0-only

package reviewv1

import (
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

	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/review"
	"github.com/orevcs/ore/internal/web"
)

const (
	humanTok = "tok"
	agentTok = "agent-tok"
)

// newServer builds the v0 server with the host executor and applies the
// review.v1 extension the way `ore serve` does through the registry.
func newServer(t *testing.T) (*httptest.Server, *web.Server, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := ledger.Open(context.Background(), filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repos, logs := filepath.Join(dir, "repos"), filepath.Join(dir, "logs")
	s := &web.Server{DB: db, ReposDir: repos, LogsDir: logs, Token: humanTok, BaseURL: "http://test", Log: slog.Default(),
		Runner:   &pipelines.Runner{DB: db, ReposDir: repos, LogsDir: logs, Executor: "host"},
		Deployer: &delivery.Deployer{DB: db, ReposDir: repos, Executor: "none"},
		Review:   &review.Service{DB: db, ReposDir: repos},
		Services: map[string]any{},
	}
	s.Authenticate = func(r *http.Request) *ledger.Actor {
		switch strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") {
		case humanTok:
			return &ledger.Actor{Kind: "human", ID: "admin"}
		case agentTok:
			return &ledger.Actor{Kind: "agent", ID: "review-bot"}
		}
		return nil
	}
	if err := Extension(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, s, repos
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
	if out != nil {
		if s, ok := out.(*string); ok {
			*s = string(data)
		} else if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s: bad json %v: %s", method, path, err, data)
		}
	}
	return resp.StatusCode
}

const repoAPI = "/api/v0/repos/acme/app"

func setup(t *testing.T, srv *httptest.Server) {
	t.Helper()
	if code := call(t, srv, humanTok, "POST", "/api/v0/orgs", map[string]string{"name": "acme"}, nil); code != 201 {
		t.Fatalf("create org: %d", code)
	}
	if code := call(t, srv, humanTok, "POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "app", "autonomy_level": 3}, nil); code != 201 {
		t.Fatalf("create repo: %d", code)
	}
	commit(t, srv, "main", "init", map[string]string{
		"STATUS": "ok\n", "a.txt": "1\n2\n3\n4\n5\n", "b.txt": "b\n", "c.txt": "c\n",
		".ore/pipeline.yaml": "version: 0\ntasks:\n  - id: check\n    run: grep -qx ok STATUS\n",
	})
}

func commit(t *testing.T, srv *httptest.Server, branch, msg string, files map[string]string) string {
	t.Helper()
	var list []map[string]any
	for p, c := range files {
		list = append(list, map[string]any{"path": p, "content": c})
	}
	var out struct{ Commit string }
	if code := call(t, srv, humanTok, "POST", repoAPI+"/commits", map[string]any{"branch": branch, "message": msg, "files": list}, &out); code != 201 {
		t.Fatalf("commit on %s: %d", branch, code)
	}
	return out.Commit
}

func openChange(t *testing.T, srv *httptest.Server, branch string) ledger.Change {
	t.Helper()
	var ch ledger.Change
	if code := call(t, srv, humanTok, "POST", repoAPI+"/changes", map[string]string{"source_ref": branch}, &ch); code != 201 {
		t.Fatalf("open change on %s: %d", branch, code)
	}
	return ch
}

func changePath(ch ledger.Change, suffix string) string {
	return repoAPI + "/changes/" + itoa(ch.Number) + suffix
}

func itoa(n int) string { return strconv.Itoa(n) }

func refs(t *testing.T, srv *httptest.Server) map[string]string {
	t.Helper()
	var refs map[string]string
	call(t, srv, humanTok, "GET", repoAPI+"/refs", nil, &refs)
	return refs
}

func TestExtensionRegistersThroughTheRegistry(t *testing.T) {
	found := false
	for _, n := range registry.Names() {
		if n == "review.v1" {
			found = true
		}
	}
	if !found {
		t.Fatal("review.v1 not registered")
	}
	_, s, _ := newServer(t)
	if _, ok := s.Services["review.landing"].(*review.Service); !ok {
		t.Fatalf("review.landing service missing: %T", s.Services["review.landing"])
	}
	if s.Review.Verifier == nil {
		t.Fatal("the extension should wire the pipeline runner as the verifier")
	}
}

func TestHTTPPatchSetsInterdiffAndComments(t *testing.T) {
	srv, _, _ := newServer(t)
	setup(t, srv)
	commit(t, srv, "feat", "feat 1", map[string]string{"a.txt": "1\n2\nX\n4\n5\n"})
	ch := openChange(t, srv, "feat")
	if code := call(t, srv, "", "GET", changePath(ch, "/patchsets"), nil, nil); code != 401 {
		t.Fatalf("expected 401, got %d", code)
	}
	var sets []review.PatchSet
	if code := call(t, srv, humanTok, "GET", changePath(ch, "/patchsets"), nil, &sets); code != 200 || len(sets) != 1 || sets[0].PushedBy.ID != "admin" {
		t.Fatalf("patch sets: %d %+v", code, sets)
	}
	// comment on patch set 1, line 3 (X) and line 5
	var onX, on5 review.Comment
	if code := call(t, srv, humanTok, "POST", changePath(ch, "/comments"), map[string]any{"path": "a.txt", "start_line": 3, "body": "why X?"}, &onX); code != 201 || onX.Anchor == nil {
		t.Fatalf("comment: %d %+v", code, onX)
	}
	if code := call(t, srv, agentTok, "POST", changePath(ch, "/comments"), map[string]any{"path": "a.txt", "start_line": 5, "end_line": 5, "body": "trailing"}, &on5); code != 201 || on5.Lane != "agent" {
		t.Fatalf("agent comment: %d %+v", code, on5)
	}
	// a push through the API (no change call) becomes patch set 2: X deleted
	commit(t, srv, "feat", "feat 2", map[string]string{"a.txt": "1\n2\n4\n5\n"})
	if call(t, srv, humanTok, "GET", changePath(ch, "/patchsets"), nil, &sets); len(sets) != 2 || sets[1].Number != 2 {
		t.Fatalf("patch sets after push: %+v", sets)
	}
	var id review.Interdiff
	if code := call(t, srv, humanTok, "GET", changePath(ch, "/interdiff?from=1&to=2"), nil, &id); code != 200 || id.Identical || !strings.Contains(id.Diff, "-X") {
		t.Fatalf("interdiff: %d %+v", code, id)
	}
	if call(t, srv, humanTok, "GET", changePath(ch, "/interdiff?from=2&to=2"), nil, &id); !id.Identical || id.Diff != "" {
		t.Fatalf("identical interdiff: %+v", id)
	}
	var raw string
	if code := call(t, srv, humanTok, "GET", changePath(ch, "/interdiff?from=1&to=2&format=range-diff"), nil, &raw); code != 200 || !strings.Contains(raw, "feat") {
		t.Fatalf("range-diff: %d %q", code, raw)
	}
	if code := call(t, srv, humanTok, "GET", changePath(ch, "/interdiff?to=2"), nil, nil); code != 400 {
		t.Fatalf("missing from: %d", code)
	}
	if code := call(t, srv, humanTok, "GET", changePath(ch, "/interdiff?from=1&to=9"), nil, nil); code != 404 {
		t.Fatalf("unknown patch set: %d", code)
	}
	var comments []review.Comment
	call(t, srv, humanTok, "GET", changePath(ch, "/comments"), nil, &comments)
	if len(comments) != 2 {
		t.Fatalf("comments: %+v", comments)
	}
	for _, c := range comments {
		switch c.ID {
		case onX.ID:
			if !c.Outdated || c.Anchor != nil {
				t.Fatalf("comment on the deleted line should be outdated: %+v", c)
			}
		case on5.ID:
			if c.Outdated || c.Anchor == nil || c.Anchor.PatchSet != 2 || c.Anchor.StartLine != 4 {
				t.Fatalf("comment on line 5 should move to line 4: %+v %+v", c, c.Anchor)
			}
		}
	}
	var resolved review.Comment
	if code := call(t, srv, humanTok, "POST", changePath(ch, "/comments/"+onX.ID+"/resolve"), nil, &resolved); code != 200 || !resolved.Resolved {
		t.Fatalf("resolve: %d %+v", code, resolved)
	}
	if code := call(t, srv, humanTok, "POST", changePath(ch, "/comments"), map[string]any{"body": ""}, nil); code != 400 {
		t.Fatalf("empty body: %d", code)
	}
	if code := call(t, srv, humanTok, "POST", changePath(ch, "/comments"), map[string]any{"body": "x", "bogus": 1}, nil); code != 400 {
		t.Fatalf("unknown field: %d", code)
	}
}

func TestHTTPReviewsPolicyAndApprovals(t *testing.T) {
	srv, _, _ := newServer(t)
	setup(t, srv)
	var pol review.Policy
	if code := call(t, srv, humanTok, "GET", repoAPI+"/review/policy", nil, &pol); code != 200 || pol.RequiredApprovals != 0 || pol.DefaultStrategy != "queue" {
		t.Fatalf("default policy: %d %+v", code, pol)
	}
	if code := call(t, srv, humanTok, "PUT", repoAPI+"/review/policy", map[string]any{"required_approvals": 1, "allow_agent_approvals": false, "default_strategy": "queue"}, &pol); code != 200 || pol.RequiredApprovals != 1 {
		t.Fatalf("set policy: %d %+v", code, pol)
	}
	commit(t, srv, "feat", "feat 1", map[string]string{"a.txt": "A\n"})
	ch := openChange(t, srv, "feat")
	// agent approval with findings: recorded in the agent lane, does not count
	var rv review.Review
	findings := []map[string]any{{"severity": "medium", "confidence": 0.8, "path": "a.txt", "range": map[string]int{"start_line": 1, "end_line": 1}, "message": "consider lowercase", "fingerprint": "abc"}}
	if code := call(t, srv, agentTok, "POST", changePath(ch, "/reviews"), map[string]any{"verdict": "approve", "findings": findings}, &rv); code != 201 || rv.Lane != "agent" || rv.Counts || len(rv.Findings) != 1 {
		t.Fatalf("agent review: %d %+v", code, rv)
	}
	var body map[string]any
	if code := call(t, srv, humanTok, "POST", changePath(ch, "/land"), map[string]any{"strategy": "queue"}, &body); code != 409 || body["approvals"] == nil {
		t.Fatalf("land on agent approval: %d %v", code, body)
	}
	var st review.ApprovalStatus
	call(t, srv, humanTok, "GET", changePath(ch, "/approvals"), nil, &st)
	if st.Landable || st.Approvals != 0 || len(st.Ignored) != 1 || st.Required != 1 {
		t.Fatalf("approvals: %+v", st)
	}
	// bad findings are a 400
	if code := call(t, srv, humanTok, "POST", changePath(ch, "/reviews"), map[string]any{"verdict": "approve", "findings": []map[string]any{{"severity": "nope", "confidence": 1, "path": "a", "message": "m"}}}, nil); code != 400 {
		t.Fatalf("bad finding: %d", code)
	}
	// human approval counts
	if code := call(t, srv, humanTok, "POST", changePath(ch, "/reviews"), map[string]any{"verdict": "approve", "body": "ship it"}, &rv); code != 201 || !rv.Counts {
		t.Fatalf("human review: %d %+v", code, rv)
	}
	call(t, srv, humanTok, "GET", changePath(ch, "/approvals"), nil, &st)
	if !st.Landable || st.Approvals != 1 {
		t.Fatalf("after human approval: %+v", st)
	}
	// a push after approval resets approvals
	commit(t, srv, "feat", "feat 2", map[string]string{"a.txt": "A\nA\n"})
	call(t, srv, humanTok, "GET", changePath(ch, "/approvals"), nil, &st)
	if st.Landable || st.PatchSet != 2 || st.Approvals != 0 {
		t.Fatalf("after push: %+v", st)
	}
	if code := call(t, srv, humanTok, "POST", changePath(ch, "/land"), nil, &body); code != 409 {
		t.Fatalf("land after push: %d %v", code, body)
	}
	var reviews []review.Review
	call(t, srv, humanTok, "GET", changePath(ch, "/reviews"), nil, &reviews)
	if len(reviews) != 2 || reviews[1].Counts {
		t.Fatalf("stale reviews still count: %+v", reviews)
	}
	call(t, srv, humanTok, "POST", changePath(ch, "/reviews"), map[string]any{"verdict": "approve"}, nil)
	var landed struct {
		Landing review.Landing
		Change  ledger.Change
	}
	if code := call(t, srv, humanTok, "POST", changePath(ch, "/land"), map[string]any{"strategy": "land_when_green"}, &landed); code != 200 || landed.Landing.Status != "landed" || landed.Change.State != "merged" {
		t.Fatalf("land: %d %+v", code, landed)
	}
	if refs(t, srv)["refs/heads/main"] != *landed.Landing.MergeOID {
		t.Fatal("main should be the merge")
	}
}

func TestHTTPLandingStrategiesEndToEnd(t *testing.T) {
	srv, _, reposDir := newServer(t)
	setup(t, srv)
	rp := filepath.Join(reposDir, "acme", "app.git")
	ctx := context.Background()
	type result struct {
		Error   string
		Landing review.Landing
		Change  ledger.Change
	}

	// queue: the default
	commit(t, srv, "q", "queue change", map[string]string{"a.txt": "Q\n"})
	q := openChange(t, srv, "q")
	var res result
	if code := call(t, srv, humanTok, "POST", changePath(q, "/land"), nil, &res); code != 200 || res.Landing.Strategy != "queue" || res.Landing.Status != "landed" {
		t.Fatalf("queue: %d %+v", code, res)
	}

	// land_when_green: red head refused, nothing merged
	commit(t, srv, "red", "break", map[string]string{"STATUS": "broken\n"})
	red := openChange(t, srv, "red")
	before := refs(t, srv)["refs/heads/main"]
	if code := call(t, srv, humanTok, "POST", changePath(red, "/land"), map[string]string{"strategy": "land_when_green"}, &res); code != 409 || res.Landing.Status != "failed" || res.Landing.VerificationRunID == nil || !strings.Contains(res.Error, "verification failed") {
		t.Fatalf("land_when_green red: %d %+v", code, res)
	}
	if refs(t, srv)["refs/heads/main"] != before || res.Change.State != "open" {
		t.Fatal("red change must not merge")
	}
	var landings []review.Landing
	call(t, srv, humanTok, "GET", changePath(red, "/landings"), nil, &landings)
	if len(landings) != 1 || landings[0].Status != "failed" {
		t.Fatalf("landings: %+v", landings)
	}
	// land_when_green: green head lands
	commit(t, srv, "green", "fine", map[string]string{"b.txt": "G\n"})
	green := openChange(t, srv, "green")
	if code := call(t, srv, humanTok, "POST", changePath(green, "/land"), map[string]string{"strategy": "land_when_green"}, &res); code != 200 || res.Landing.Status != "landed" || res.Change.State != "merged" {
		t.Fatalf("land_when_green green: %d %+v", code, res)
	}

	// optimistic: green, no revert
	commit(t, srv, "opt-ok", "optimistic fine", map[string]string{"c.txt": "O\n"})
	optOK := openChange(t, srv, "opt-ok")
	if code := call(t, srv, agentTok, "POST", changePath(optOK, "/land"), map[string]string{"strategy": "optimistic"}, &res); code != 200 || res.Landing.Status != "landed" || res.Landing.RevertOID != nil {
		t.Fatalf("optimistic green: %d %+v", code, res)
	}
	if refs(t, srv)["refs/heads/main"] != *res.Landing.MergeOID {
		t.Fatal("main should be the optimistic merge")
	}

	// optimistic: red, reverted, issue opened, target points at the revert
	commit(t, srv, "opt-bad", "optimistic broken", map[string]string{"STATUS": "broken\n", "a.txt": "BAD\n"})
	optBad := openChange(t, srv, "opt-bad")
	if code := call(t, srv, agentTok, "POST", changePath(optBad, "/land"), map[string]string{"strategy": "optimistic"}, &res); code != 409 || res.Landing.Status != "reverted" || res.Landing.RevertOID == nil || res.Landing.IssueNumber == nil {
		t.Fatalf("optimistic red: %d %+v", code, res)
	}
	if refs(t, srv)["refs/heads/main"] != *res.Landing.RevertOID || res.Change.State != "reverted" {
		t.Fatalf("main should point at the revert: %+v", res)
	}
	var file string
	call(t, srv, humanTok, "GET", repoAPI+"/file?path=STATUS", nil, &file)
	if file != "ok\n" {
		t.Fatalf("STATUS after revert: %q", file)
	}
	var issues []ledger.Issue
	call(t, srv, humanTok, "GET", repoAPI+"/issues", nil, &issues)
	if len(issues) != 1 || issues[0].Title != "Change #"+itoa(optBad.Number)+" reverted: verification failed" || !strings.Contains(issues[0].Body, "[check]") {
		t.Fatalf("issue: %+v", issues)
	}
	var evs []ledger.Event
	call(t, srv, humanTok, "GET", "/api/v0/events?type=io.ore.change.reverted", nil, &evs)
	if len(evs) != 1 {
		t.Fatalf("reverted events: %d", len(evs))
	}

	// a late comment on a merged change is allowed and keeps its lane
	var cm review.Comment
	if code := call(t, srv, agentTok, "POST", changePath(q, "/comments"), map[string]any{"body": "post-merge note"}, &cm); code != 201 || cm.Lane != "agent" {
		t.Fatalf("late comment: %d %+v", code, cm)
	}

	// landing a change whose source branch was deleted is a 409
	commit(t, srv, "gone", "to be deleted", map[string]string{"b.txt": "gone\n"})
	gone := openChange(t, srv, "gone")
	if err := gitx.DeleteRef(ctx, rp, "refs/heads/gone", gone.HeadOID); err != nil {
		t.Fatal(err)
	}
	var errBody map[string]any
	if code := call(t, srv, humanTok, "POST", changePath(gone, "/land"), map[string]string{"strategy": "optimistic"}, &errBody); code != 409 || !strings.Contains(errBody["error"].(string), "source branch") {
		t.Fatalf("deleted source: %d %v", code, errBody)
	}
	// unknown strategy is a 400, unknown change a 404
	if code := call(t, srv, humanTok, "POST", changePath(q, "/land"), map[string]string{"strategy": "yolo"}, nil); code != 400 {
		t.Fatalf("bad strategy: %d", code)
	}
	if code := call(t, srv, humanTok, "POST", repoAPI+"/changes/99/land", nil, nil); code != 404 {
		t.Fatalf("missing change: %d", code)
	}
}

func TestHTTPStacks(t *testing.T) {
	srv, _, reposDir := newServer(t)
	setup(t, srv)
	rp := filepath.Join(reposDir, "acme", "app.git")
	ctx := context.Background()
	au := gitx.Author{Name: "t", Email: "t@t"}
	stackOn := func(name, parent string, path, content string) string {
		oid, err := gitx.CommitFiles(ctx, rp, parent, []gitx.FileEdit{{Path: path, Content: []byte(content)}}, "edit "+path+"\n", au)
		if err != nil {
			t.Fatal(err)
		}
		if err := gitx.UpdateRef(ctx, rp, "refs/heads/"+name, oid, gitx.ZeroOID); err != nil {
			t.Fatal(err)
		}
		return oid
	}
	s1 := commit(t, srv, "s1", "edit a", map[string]string{"a.txt": "A\n"})
	s2 := stackOn("s2", s1, "b.txt", "B\n")
	stackOn("s3", s2, "c.txt", "C\n")

	var st review.Stack
	if code := call(t, srv, humanTok, "POST", repoAPI+"/stacks", map[string]any{"branches": []string{"s1", "s2", "s3"}}, &st); code != 201 || len(st.Changes) != 3 || st.Changes[1].TargetRef != "refs/heads/s1" {
		t.Fatalf("open stack: %d %+v", code, st)
	}
	if code := call(t, srv, humanTok, "POST", repoAPI+"/stacks", map[string]any{"branches": []string{"s3", "s1"}}, nil); code != 400 {
		t.Fatalf("bad order: %d", code)
	}
	var got review.Stack
	if code := call(t, srv, humanTok, "GET", repoAPI+"/stacks/"+st.ID, nil, &got); code != 200 || got.State != "open" || len(got.ChangeIDs) != 3 {
		t.Fatalf("get stack: %d %+v", code, got)
	}
	var list []review.Stack
	if code := call(t, srv, humanTok, "GET", repoAPI+"/stacks", nil, &list); code != 200 || len(list) != 1 {
		t.Fatalf("list stacks: %d %+v", code, list)
	}
	// main moves and edits b.txt: the middle change conflicts on rebase
	commit(t, srv, "main", "main edits b", map[string]string{"b.txt": "main's b\n"})
	var out struct {
		Error    string
		Stack    review.Stack
		Landings []review.Landing
	}
	if code := call(t, srv, humanTok, "POST", repoAPI+"/stacks/"+st.ID+"/land", nil, &out); code != 409 || out.Stack.State != "aborted" || out.Stack.FailedChangeNumber != st.Changes[1].Number || !strings.Contains(out.Error, "b.txt") {
		t.Fatalf("conflicting stack: %d %+v", code, out)
	}
	states := []string{}
	for _, c := range out.Stack.Changes {
		states = append(states, c.State)
	}
	if strings.Join(states, ",") != "merged,open,open" || len(out.Landings) != 1 {
		t.Fatalf("after abort: %v %+v", states, out.Landings)
	}
	// a clean stack lands bottom-up with server-side rebases
	main := refs(t, srv)["refs/heads/main"]
	u1 := stackOn("u1", main, "c.txt", "U1\n")
	stackOn("u2", u1, "a.txt", "U2\n")
	if code := call(t, srv, humanTok, "POST", repoAPI+"/stacks", map[string]any{"branches": []string{"u1", "u2"}}, &st); code != 201 {
		t.Fatalf("open stack 2: %d", code)
	}
	commit(t, srv, "main", "main moves again", map[string]string{"d.txt": "d\n"})
	if code := call(t, srv, humanTok, "POST", repoAPI+"/stacks/"+st.ID+"/land", map[string]string{"strategy": "land_when_green"}, &out); code != 200 || out.Stack.State != "landed" || len(out.Landings) != 2 {
		t.Fatalf("land stack: %d %+v", code, out)
	}
	for _, l := range out.Landings {
		if l.Strategy != "land_when_green" || l.Status != "landed" {
			t.Fatalf("landing: %+v", l)
		}
	}
	var sets []review.PatchSet
	call(t, srv, humanTok, "GET", changePath(out.Stack.Changes[1], "/patchsets"), nil, &sets)
	if len(sets) != 2 {
		t.Fatalf("the rebase should be patch set 2: %+v", sets)
	}
	var file string
	call(t, srv, humanTok, "GET", repoAPI+"/file?path=a.txt", nil, &file)
	if file != "U2\n" {
		t.Fatalf("a.txt on main: %q", file)
	}
	if code := call(t, srv, humanTok, "GET", repoAPI+"/stacks/stk_nope", nil, nil); code != 404 {
		t.Fatalf("missing stack: %d", code)
	}
}
