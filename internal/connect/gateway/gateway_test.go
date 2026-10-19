// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/review"
)

type fixture struct {
	g     *Gateway
	db    *ledger.DB
	repo  ledger.Repo // acme/app with main and feat
	other ledger.Repo // acme/other
	admin ledger.Actor
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := ledger.Open(ctx, filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repos := filepath.Join(dir, "repos")
	admin := ledger.Actor{Kind: "human", ID: "admin"}
	org, err := db.CreateOrg(ctx, "acme", admin)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name string) ledger.Repo {
		r, err := db.CreateRepo(ctx, org, name, "main", 2, 0, admin)
		if err != nil {
			t.Fatal(err)
		}
		rp := filepath.Join(repos, r.Org, r.Name+".git")
		if err := gitx.InitBare(ctx, rp, "main"); err != nil {
			t.Fatal(err)
		}
		author := gitx.Author{Name: "t", Email: "t@acme.invalid"}
		c1, err := gitx.CommitFiles(ctx, rp, "", []gitx.FileEdit{{Path: "README.md", Content: []byte("hello\n")}}, "init\n", author)
		if err != nil {
			t.Fatal(err)
		}
		if err := gitx.UpdateRef(ctx, rp, "refs/heads/main", c1, gitx.ZeroOID); err != nil {
			t.Fatal(err)
		}
		c2, err := gitx.CommitFiles(ctx, rp, c1, []gitx.FileEdit{{Path: "feature.txt", Content: []byte("new\n")}}, "feature\n", author)
		if err != nil {
			t.Fatal(err)
		}
		if err := gitx.UpdateRef(ctx, rp, "refs/heads/feat", c2, gitx.ZeroOID); err != nil {
			t.Fatal(err)
		}
		return r
	}
	f := &fixture{db: db, admin: admin, repo: mk("app"), other: mk("other")}
	f.g = &Gateway{DB: db, ReposDir: repos, Review: &review.Service{DB: db, ReposDir: repos}}
	return f
}

func (f *fixture) session(t *testing.T) (Session, string) {
	t.Helper()
	s, tok, err := f.g.CreateSession(context.Background(), f.repo, "bot", f.admin)
	if err != nil {
		t.Fatal(err)
	}
	return s, tok
}

func envelope(s Session, repoID string, reqs ...string) []byte {
	return []byte(fmt.Sprintf(`{"version":"0","session":{"agent_id":%q,"session_id":%q,"repository_id":%q},"requests":[%s]}`, s.AgentID, s.ID, repoID, strings.Join(reqs, ",")))
}

func (f *fixture) submit(t *testing.T, s Session, repoID string, reqs ...string) Response {
	t.Helper()
	resp, err := f.g.Submit(context.Background(), s, envelope(s, repoID, reqs...))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func (f *fixture) events(t *testing.T, typ string) []ledger.Event {
	t.Helper()
	evs, err := f.db.Events(context.Background(), ledger.EventsOptions{Type: typ, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func gatedCodes(t *testing.T, f *fixture) []string {
	t.Helper()
	var codes []string
	for _, ev := range f.events(t, EventWriteGated) {
		var d struct{ Code string }
		json.Unmarshal(ev.Data, &d)
		codes = append(codes, d.Code)
	}
	return codes
}

func TestSessionTokenRoundTrip(t *testing.T) {
	f := newFixture(t)
	s, tok := f.session(t)
	if !IsSessionToken(tok) || s.Scope != "repo:acme/app" || s.State != "open" || s.Repo != "acme/app" {
		t.Fatalf("session %+v token %q", s, tok)
	}
	got, err := f.g.SessionByToken(context.Background(), tok)
	if err != nil || got.ID != s.ID {
		t.Fatalf("by token: %v %+v", err, got)
	}
	if _, err := f.g.SessionByToken(context.Background(), "gws_nope"); err != ledger.ErrNotFound {
		t.Fatalf("bad token: %v", err)
	}
	if _, err := f.g.SessionByToken(context.Background(), "admin-token"); err != ledger.ErrNotFound {
		t.Fatalf("non-session token: %v", err)
	}
	if _, _, err := f.g.CreateSession(context.Background(), f.repo, " ", f.admin); !IsValidationError(err) {
		t.Fatalf("blank agent: %v", err)
	}
	if n := len(f.events(t, EventSessionOpened)); n != 1 {
		t.Fatalf("opened events: %d", n)
	}
}

func TestScopeRefusesOtherRepository(t *testing.T) {
	f := newFixture(t)
	s, _ := f.session(t)
	resp := f.submit(t, s, f.other.ID, `{"kind":"open_issue","title":"x","body":""}`)
	if resp.Refused != 1 || resp.Results[0].Code != CodeOutsideScope || resp.Results[0].EventID == "" {
		t.Fatalf("resp %+v", resp)
	}
	if codes := gatedCodes(t, f); len(codes) != 1 || codes[0] != CodeOutsideScope {
		t.Fatalf("gated events: %v", codes)
	}
	// a wrong agent or session id in the envelope is also out of scope
	body := []byte(fmt.Sprintf(`{"version":"0","session":{"agent_id":"other-bot","session_id":%q,"repository_id":%q},"requests":[{"kind":"open_issue","title":"x","body":""}]}`, s.ID, f.repo.ID))
	resp, err := f.g.Submit(context.Background(), s, body)
	if err != nil || resp.Refused != 1 || resp.Results[0].Code != CodeOutsideScope {
		t.Fatalf("mismatched agent: %v %+v", err, resp)
	}
	issues, _ := f.db.Issues(context.Background(), f.other)
	if len(issues) != 0 {
		t.Fatal("an issue was written outside the scope")
	}
	cur, _ := f.g.Session(context.Background(), s.ID)
	if cur.Refused != 2 || cur.Writes != 0 {
		t.Fatalf("counters %+v", cur)
	}
	// the repository may be named as org/name too
	resp = f.submit(t, s, "acme/app", `{"kind":"open_issue","title":"x","body":""}`)
	if resp.Executed != 1 {
		t.Fatalf("org/name scope: %+v", resp)
	}
}

func TestRateLimit(t *testing.T) {
	f := newFixture(t)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	f.g.RateLimit, f.g.RateWindow, f.g.Now = 3, time.Minute, func() time.Time { return now }
	s, _ := f.session(t)
	for i := 0; i < 3; i++ {
		resp := f.submit(t, s, f.repo.ID, fmt.Sprintf(`{"kind":"open_issue","title":"issue %d","body":""}`, i))
		if resp.Executed != 1 {
			t.Fatalf("write %d: %+v", i, resp.Results[0])
		}
	}
	resp := f.submit(t, s, f.repo.ID, `{"kind":"open_issue","title":"too many","body":""}`)
	if resp.Refused != 1 || resp.Results[0].Code != CodeRateLimited || !strings.Contains(resp.Results[0].Reason, "3 writes per 1m0s") {
		t.Fatalf("over limit: %+v", resp.Results[0])
	}
	if codes := gatedCodes(t, f); len(codes) != 1 || codes[0] != CodeRateLimited {
		t.Fatalf("gated: %v", codes)
	}
	cur, _ := f.g.Session(context.Background(), s.ID)
	if cur.Writes != 3 || cur.WindowWrites != 3 || cur.Refused != 1 {
		t.Fatalf("counters %+v", cur)
	}
	// a batch is gated request by request: the first slot of a new window executes, the rest are refused
	now = now.Add(time.Minute)
	resp = f.submit(t, s, f.repo.ID, `{"kind":"open_issue","title":"a","body":""}`, `{"kind":"open_issue","title":"b","body":""}`, `{"kind":"open_issue","title":"c","body":""}`, `{"kind":"open_issue","title":"d","body":""}`)
	if resp.Executed != 3 || resp.Refused != 1 || resp.Results[3].Code != CodeRateLimited {
		t.Fatalf("new window: executed %d refused %d", resp.Executed, resp.Refused)
	}
	// refusals never consume a slot and invalid requests are refused before the limiter
	now = now.Add(time.Minute)
	resp = f.submit(t, s, f.repo.ID, `{"kind":"bogus"}`, `{"kind":"open_issue","title":"e","body":""}`)
	if resp.Results[0].Code != CodeInvalid || resp.Results[1].Status != "executed" {
		t.Fatalf("invalid then valid: %+v", resp.Results)
	}
	cur, _ = f.g.Session(context.Background(), s.ID)
	if cur.WindowWrites != 1 || cur.Writes != 7 {
		t.Fatalf("counters after reset %+v", cur)
	}
}

func TestDefaultRateLimitIsTwentyPerTenMinutes(t *testing.T) {
	g := &Gateway{}
	n, w := g.limit()
	if n != 20 || w != 10*time.Minute {
		t.Fatalf("defaults %d %s", n, w)
	}
}

func TestProtectedDefaultBranch(t *testing.T) {
	f := newFixture(t)
	s, _ := f.session(t)
	prov := `"provenance":{"author_kind":"agent","agent_id":"bot","model":"m"}`
	for _, branch := range []string{"main", "refs/heads/main"} {
		resp := f.submit(t, s, f.repo.ID, `{"kind":"open_change","branch":"`+branch+`","title":"t","body":"b",`+prov+`}`)
		if resp.Refused != 1 || resp.Results[0].Code != "protected_branch" {
			t.Fatalf("%s: %+v", branch, resp.Results[0])
		}
	}
	// push, label and review are schema-valid but not executable in v0
	resp := f.submit(t, s, f.repo.ID, `{"kind":"push","branch":"feat","bundle":"obj://b",`+prov+`}`)
	if resp.Results[0].Code != "unsupported" {
		t.Fatalf("push: %+v", resp.Results[0])
	}
	changes, _ := f.db.Changes(context.Background(), f.repo)
	if len(changes) != 0 {
		t.Fatal("a change was opened from the protected branch")
	}
}

func TestOpenChangeExistingAndMissingBase(t *testing.T) {
	f := newFixture(t)
	s, _ := f.session(t)
	prov := `"provenance":{"author_kind":"agent","agent_id":"bot","model":"claude-fable-5-1"}`
	resp := f.submit(t, s, f.repo.ID, `{"kind":"open_change","branch":"feat","title":"Add feature","body":"why",`+prov+`}`)
	if resp.Executed != 1 || resp.Results[0].Change == nil || resp.Results[0].Change.Number != 1 || resp.Results[0].Existing {
		t.Fatalf("open: %+v", resp.Results[0])
	}
	ch := resp.Results[0].Change
	if !strings.Contains(ch.Provenance, `"session_id":"`+s.ID+`"`) || !strings.Contains(ch.Provenance, "claude-fable-5-1") {
		t.Fatalf("provenance %s", ch.Provenance)
	}
	// the same branch again returns the existing open change
	resp = f.submit(t, s, f.repo.ID, `{"kind":"open_change","branch":"feat","title":"again","body":"",`+prov+`}`)
	if resp.Executed != 1 || !resp.Results[0].Existing || resp.Results[0].Change.ID != ch.ID {
		t.Fatalf("existing: %+v", resp.Results[0])
	}
	// a non-default base that does not exist is refused before anything runs
	resp = f.submit(t, s, f.repo.ID, `{"kind":"open_change","branch":"feat","base":"release-9","title":"t","body":"",`+prov+`}`)
	if resp.Refused != 1 || resp.Results[0].Code != "bad_target" || !strings.Contains(resp.Results[0].Reason, "release-9 does not exist") {
		t.Fatalf("missing base: %+v", resp.Results[0])
	}
	// and so is a source branch that does not exist
	resp = f.submit(t, s, f.repo.ID, `{"kind":"open_change","branch":"nope","title":"t","body":"",`+prov+`}`)
	if resp.Results[0].Code != "bad_target" {
		t.Fatalf("missing branch: %+v", resp.Results[0])
	}
	// the change was opened by the agent actor and the executed event carries the session
	opened := f.events(t, "io.ore.change.opened")
	if len(opened) != 1 || opened[0].OreActor.Kind != "agent" || opened[0].OreActor.ID != "bot" {
		t.Fatalf("opened events: %+v", opened)
	}
	exec := f.events(t, EventWriteExecuted)
	if len(exec) != 2 || !strings.Contains(string(exec[0].Data), `"session_id":"`+s.ID+`"`) {
		t.Fatalf("executed events: %d %s", len(exec), exec[0].Data)
	}
}

func TestCommentReviewAndDeployment(t *testing.T) {
	f := newFixture(t)
	s, _ := f.session(t)
	ctx := context.Background()
	issue, _ := f.db.CreateIssue(ctx, f.repo, "bug", "", f.admin)
	prov := `"provenance":{"author_kind":"agent","agent_id":"bot","model":"m"}`
	f.submit(t, s, f.repo.ID, `{"kind":"open_change","branch":"feat","title":"t","body":"",`+prov+`}`)

	resp := f.submit(t, s, f.repo.ID, `{"kind":"comment","target":"issues/1","body":"looking"}`, `{"kind":"comment","target":"changes/1","body":"nit"}`, `{"kind":"comment","target":"`+issue.ID+`","body":"by id"}`)
	if resp.Executed != 3 {
		t.Fatalf("comments: %+v", resp.Results)
	}
	c := resp.Results[0].Comment
	if c == nil || c.TargetKind != "issue" || c.TargetID != issue.ID || c.SessionID != s.ID || c.Actor.ID != "bot" {
		t.Fatalf("comment %+v", c)
	}
	got, _ := f.g.Comments(ctx, f.repo, "issue", issue.ID)
	if len(got) != 2 || got[0].Body != "looking" || got[1].Body != "by id" {
		t.Fatalf("stored comments %+v", got)
	}
	// issue comments are the gateway's; the change comment went to the review store in the agent lane
	if n := len(f.events(t, EventCommentCreated)); n != 2 {
		t.Fatalf("comment events %d", n)
	}
	ch, _ := f.db.Change(ctx, f.repo, 1)
	rc, err := f.g.Review.Comments(ctx, f.repo, ch)
	if err != nil || len(rc) != 1 || rc[0].Body != "nit" || rc[0].Lane != "agent" || rc[0].Author.ID != "bot" {
		t.Fatalf("review-store comment: %v %+v", err, rc)
	}
	resp = f.submit(t, s, f.repo.ID, `{"kind":"comment","target":"issues/99","body":"x"}`, `{"kind":"comment","target":"42","body":"x"}`)
	if resp.Refused != 2 || resp.Results[0].Code != "bad_target" || resp.Results[1].Code != "bad_target" {
		t.Fatalf("bad targets: %+v", resp.Results)
	}
	// request_review must target a change
	resp = f.submit(t, s, f.repo.ID, `{"kind":"request_review","target":"changes/1","reviewers":["alice","bob"]}`, `{"kind":"request_review","target":"issues/1","reviewers":["alice"]}`)
	if resp.Executed != 1 || resp.Refused != 1 || resp.Results[0].Review["change"] != 1 {
		t.Fatalf("review: %+v", resp.Results)
	}
	if evs := f.events(t, EventReviewRequested); len(evs) != 1 || !strings.Contains(string(evs[0].Data), `"reviewers":["alice","bob"]`) {
		t.Fatalf("review events %+v", evs)
	}
	// request_deployment resolves the release and records a request, no deployment runs
	resp = f.submit(t, s, f.repo.ID, `{"kind":"request_deployment","release":"feat","environment":"preview"}`, `{"kind":"request_deployment","release":"v9","environment":"preview"}`)
	if resp.Executed != 1 || resp.Refused != 1 || resp.Results[1].Code != "bad_target" {
		t.Fatalf("deployment: %+v", resp.Results)
	}
	if oid, _ := resp.Results[0].Deployment["commit_oid"].(string); len(oid) != 40 {
		t.Fatalf("commit oid %v", resp.Results[0].Deployment)
	}
	if evs := f.events(t, EventDeployRequested); len(evs) != 1 || !strings.HasPrefix(evs[0].Subject, "acme/app/deployments/requests/") {
		t.Fatalf("deployment events %+v", evs)
	}
	deps, _ := f.db.Deployments(ctx, f.repo, 10)
	if len(deps) != 0 {
		t.Fatal("a deployment was started by a request")
	}
}

func TestClosedSession(t *testing.T) {
	f := newFixture(t)
	s, tok := f.session(t)
	ctx := context.Background()
	closed, err := f.g.CloseSession(ctx, s, "done", f.admin)
	if err != nil || closed.State != "closed" || closed.ClosedAt == nil {
		t.Fatalf("close: %v %+v", err, closed)
	}
	if _, err := f.g.CloseSession(ctx, closed, "again", f.admin); err != ErrSessionClosed {
		t.Fatalf("second close: %v", err)
	}
	// the token still resolves (so the use can be refused and recorded) but no write passes
	got, err := f.g.SessionByToken(ctx, tok)
	if err != nil || got.State != "closed" {
		t.Fatalf("by token after close: %v %+v", err, got)
	}
	resp := f.submit(t, got, f.repo.ID, `{"kind":"open_issue","title":"x","body":""}`)
	if resp.Refused != 1 || resp.Results[0].Code != CodeSessionClosed {
		t.Fatalf("closed submit: %+v", resp)
	}
	if codes := gatedCodes(t, f); len(codes) != 1 || codes[0] != CodeSessionClosed {
		t.Fatalf("gated: %v", codes)
	}
	if n := len(f.events(t, EventSessionClosed)); n != 1 {
		t.Fatalf("closed events %d", n)
	}
}

func TestInvalidEnvelopeIsRecorded(t *testing.T) {
	f := newFixture(t)
	s, _ := f.session(t)
	resp, err := f.g.Submit(context.Background(), s, []byte(`{"version":"7"}`))
	if err != nil || resp.Refused != 1 || resp.Results[0].Code != CodeInvalid {
		t.Fatalf("%v %+v", err, resp)
	}
	gated := f.events(t, EventWriteGated)
	if len(gated) != 1 || gated[0].OreActor.Kind != "agent" || !strings.Contains(string(gated[0].Data), `"decision":"refused"`) {
		t.Fatalf("gated %+v", gated)
	}
	// a 64 KiB comment is refused and the ledger keeps a truncated summary, not the body
	big := strings.Repeat("y", MaxCommentBytes+1)
	resp = f.submit(t, s, f.repo.ID, `{"kind":"comment","target":"issues/1","body":"`+big+`"}`)
	if resp.Results[0].Code != CodeInvalid || !strings.Contains(resp.Results[0].Reason, "65537 bytes") {
		t.Fatalf("big comment: %+v", resp.Results[0])
	}
	gated = f.events(t, EventWriteGated)
	if len(gated) != 2 || len(gated[1].Data) > 2048 {
		t.Fatalf("gated event stored the body: %d bytes", len(gated[1].Data))
	}
}
