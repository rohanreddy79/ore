// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/agent"
	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/review"
	reviewv1 "github.com/orevcs/ore/internal/review/v1"
	"github.com/orevcs/ore/internal/web"
)

const tok = "tok"

// newServer builds the server the way `ore serve` does and applies the fleet
// extension; the watcher runs synchronously and is ticked by the test.
func newServer(t *testing.T) (*httptest.Server, *web.Server, *Service, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := ledger.Open(context.Background(), filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repos, logs := filepath.Join(dir, "repos"), filepath.Join(dir, "logs")
	s := &web.Server{DB: db, ReposDir: repos, LogsDir: logs, Token: tok, BaseURL: "http://test", Log: slog.Default(),
		Runner:   &pipelines.Runner{DB: db, ReposDir: repos, LogsDir: logs, Executor: "host"},
		Deployer: &delivery.Deployer{DB: db, ReposDir: repos, Executor: "none"},
		Review:   &review.Service{DB: db, ReposDir: repos},
		Services: map[string]any{},
	}
	s.Review.Verifier = s.Runner
	s.Loop = &agent.Loop{DB: db, ReposDir: repos, LogsDir: logs, Runner: s.Runner, Deployer: s.Deployer, Review: s.Review, AdapterCmd: fixmeAdapter, AdapterName: "demo-script"}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := reviewv1.Extension(ctx, s); err != nil { // the fleet lands through review v1
		t.Fatal(err)
	}
	if err := Extension(ctx, s); err != nil {
		t.Fatal(err)
	}
	svc := s.Services[Name].(*Service)
	svc.RunSync = true
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, s, svc, dir
}

func call(t *testing.T, srv *httptest.Server, method, path string, in any, out any) int {
	t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+path, body)
	req.Header.Set("Authorization", "Bearer "+tok)
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

func TestExtensionRegistersAndGovernsTheLoop(t *testing.T) {
	found := false
	for _, n := range registry.Names() {
		if n == Name {
			found = true
		}
	}
	if !found {
		t.Fatal("agent.fleet not registered")
	}
	_, s, svc, _ := newServer(t)
	if s.Loop.Governor != svc || s.Services["agent.fleet"] != svc {
		t.Fatal("the fleet should govern the loop and be published as a service")
	}
}

func TestHTTPFleetEndToEnd(t *testing.T) {
	srv, s, svc, dir := newServer(t)
	ctx := context.Background()
	if code := call(t, srv, "POST", "/api/v0/orgs", map[string]string{"name": "acme"}, nil); code != 201 {
		t.Fatalf("org: %d", code)
	}
	var repo ledger.Repo
	if code := call(t, srv, "POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "app", "autonomy_level": 3}, &repo); code != 201 {
		t.Fatalf("repo: %d", code)
	}
	if code := call(t, srv, "POST", repoAPI+"/commits", map[string]any{"branch": "main", "message": "init", "files": []map[string]any{
		{"path": "greeting.txt", "content": "Hello, World!\n"}, {"path": ".ore/pipeline.yaml", "content": pipelineYAML},
	}}, nil); code != 201 {
		t.Fatalf("commit: %d", code)
	}

	// fleet status and defaults
	var st Status
	if code := call(t, srv, "GET", repoAPI+"/fleet", nil, &st); code != 200 || st.Level != 3 || len(st.Roles) != 4 || st.Settings.WatcherRetries != 1 || len(st.Budgets) != 1 {
		t.Fatalf("status: %d %+v", code, st)
	}
	var inst map[string]any
	if code := call(t, srv, "GET", "/api/v0/fleet", nil, &inst); code != 200 || inst["paused"] != false {
		t.Fatalf("instance: %d %v", code, inst)
	}

	// roles: configure the script reviewer
	reviewer := filepath.Join(dir, "reviewer.sh")
	os.WriteFile(reviewer, []byte(ScriptReviewer), 0o755)
	var role Role
	if code := call(t, srv, "PUT", repoAPI+"/fleet/roles/reviewer", map[string]any{"adapter_cmd": "sh " + reviewer, "adapter_name": "script-reviewer"}, &role); code != 200 || !role.Enabled || role.Default {
		t.Fatalf("set reviewer: %d %+v", code, role)
	}
	if code := call(t, srv, "PUT", repoAPI+"/fleet/roles/janitor", map[string]any{"enabled": true}, nil); code != 400 {
		t.Fatalf("unknown role: %d", code)
	}
	var roles []Role
	call(t, srv, "GET", repoAPI+"/fleet/roles", nil, &roles)
	if len(roles) != 4 || roles[1].AdapterName != "script-reviewer" {
		t.Fatalf("roles: %+v", roles)
	}

	// budgets
	var bs BudgetStatus
	if code := call(t, srv, "PUT", repoAPI+"/fleet/budgets", map[string]any{"principal": "", "budget": map[string]int{"runs_per_day": 3, "ci_minutes_per_day": 0, "open_changes": 0, "writes_per_hour": 0}}, &bs); code != 200 || bs.Budget.RunsPerDay != 3 {
		t.Fatalf("set budget: %d %+v", code, bs)
	}
	if code := call(t, srv, "PUT", repoAPI+"/fleet/budgets", map[string]any{"budget": map[string]int{"runs_per_day": -1}}, nil); code != 400 {
		t.Fatalf("bad budget: %d", code)
	}

	// an L3 run through the API: reviewer blocks the FIXME, the retry fixes it, land_when_green lands
	var issue ledger.Issue
	call(t, srv, "POST", repoAPI+"/issues", map[string]string{"title": "fix greeting"}, &issue)
	var run ledger.AgentRun
	if code := call(t, srv, "POST", repoAPI+"/agent/runs", map[string]any{"issue": issue.Number, "wait": true}, &run); code != 200 || run.Status != "completed" {
		var log string
		call(t, srv, "GET", repoAPI+"/agent/runs/"+run.ID+"/log", nil, &log)
		t.Fatalf("run: %d %s %q\n%s", code, run.Status, run.Error, log)
	}
	var ch ledger.Change
	call(t, srv, "GET", repoAPI+"/changes/1", nil, &ch)
	if ch.State != "merged" {
		t.Fatalf("change: %+v", ch)
	}
	var reviews []review.Review
	call(t, srv, "GET", repoAPI+"/changes/1/reviews", nil, &reviews)
	if len(reviews) != 2 || reviews[0].Lane != "agent" || reviews[0].Verdict != "request_changes" || reviews[1].Verdict != "approve" {
		t.Fatalf("reviews: %+v", reviews)
	}
	var landings []review.Landing
	call(t, srv, "GET", repoAPI+"/changes/1/landings", nil, &landings)
	if len(landings) != 1 || landings[0].Strategy != "land_when_green" {
		t.Fatalf("landings: %+v", landings)
	}
	var file string
	call(t, srv, "GET", repoAPI+"/file?path=greeting.txt", nil, &file)
	if file != "Hello, Ore!\n" {
		t.Fatalf("main: %q", file)
	}

	// evidence and recommendation
	var evOut struct {
		Evidence       Evidence
		Recommendation Recommendation
	}
	if code := call(t, srv, "GET", repoAPI+"/fleet/evidence", nil, &evOut); code != 200 || evOut.Evidence.Changes.Merged != 1 || evOut.Recommendation.Action != "hold" {
		t.Fatalf("evidence: %d %+v", code, evOut)
	}
	var budgets []BudgetStatus
	call(t, srv, "GET", repoAPI+"/fleet/budgets", nil, &budgets)
	if len(budgets) != 2 || budgets[0].Usage.Runs != 1 || budgets[1].Principal != "demo-script" {
		t.Fatalf("budgets: %+v", budgets)
	}

	// kill switch: a paused repository refuses runs
	if code := call(t, srv, "POST", repoAPI+"/fleet/pause", map[string]string{"reason": "incident"}, nil); code != 200 {
		t.Fatalf("pause: %d", code)
	}
	var errBody map[string]any
	if code := call(t, srv, "POST", repoAPI+"/agent/runs", map[string]any{"prompt": "again", "wait": true}, &errBody); code < 400 || !strings.Contains(errBody["error"].(string), "paused") {
		t.Fatalf("paused run: %d %v", code, errBody)
	}
	call(t, srv, "GET", "/api/v0/fleet", nil, &inst)
	if list, _ := inst["paused_repositories"].([]any); len(list) != 1 {
		t.Fatalf("instance status should list the paused repo: %v", inst)
	}
	if code := call(t, srv, "POST", repoAPI+"/fleet/resume", nil, nil); code != 200 {
		t.Fatalf("resume: %d", code)
	}
	if code := call(t, srv, "POST", "/api/v0/fleet/pause", map[string]string{"reason": "maintenance"}, nil); code != 200 {
		t.Fatalf("instance pause: %d", code)
	}
	if code := call(t, srv, "POST", repoAPI+"/agent/runs", map[string]any{"prompt": "again", "wait": true}, &errBody); code < 400 || !strings.Contains(errBody["error"].(string), "instance paused") {
		t.Fatalf("instance-paused run: %d %v", code, errBody)
	}
	call(t, srv, "POST", "/api/v0/fleet/resume", nil, nil)

	// settings
	var settings Settings
	if code := call(t, srv, "PUT", repoAPI+"/fleet/settings", map[string]any{"watcher_retries": 2, "thresholds": map[string]any{"promote_min_changes": 1}}, &settings); code != 200 || settings.WatcherRetries != 2 || settings.Thresholds.PromoteMinChanges != 1 || settings.Thresholds.DemoteMaxRollbackRate != 0.2 {
		t.Fatalf("settings: %d %+v", code, settings)
	}
	if code := call(t, srv, "PUT", repoAPI+"/fleet/settings", map[string]any{"watcher_retries": -1}, nil); code != 400 {
		t.Fatalf("bad settings: %d", code)
	}
	// with the lowered threshold the one accepted change recommends L4; auto_promote is off so nothing moves
	call(t, srv, "GET", repoAPI+"/fleet/evidence", nil, &evOut)
	if evOut.Recommendation.Action != "promote" || evOut.Recommendation.To != 4 || evOut.Recommendation.Automatic {
		t.Fatalf("recommendation: %+v", evOut.Recommendation)
	}
	call(t, srv, "GET", repoAPI, nil, &repo)
	if repo.AutonomyLevel != 3 {
		t.Fatal("promotion must not apply by itself")
	}
	var applied struct {
		Applied bool
		Level   int `json:"autonomy_level"`
	}
	if code := call(t, srv, "POST", repoAPI+"/fleet/evidence/apply", nil, &applied); code != 200 || !applied.Applied || applied.Level != 4 {
		t.Fatalf("apply: %d %+v", code, applied)
	}

	// a human flags the landed change: demoted to L3 with the evidence on the event
	var flagged struct {
		Flag    Flag
		Applied bool
		Level   int `json:"autonomy_level"`
	}
	if code := call(t, srv, "POST", repoAPI+"/changes/1/flag", map[string]string{"reason": "should not have landed"}, &flagged); code != 201 || !flagged.Applied || flagged.Level != 3 || flagged.Flag.Number != 1 {
		t.Fatalf("flag: %d %+v", code, flagged)
	}
	if code := call(t, srv, "POST", repoAPI+"/fleet/changes/1/flag", nil, nil); code != 409 {
		t.Fatalf("double flag: %d", code)
	}
	var evs []ledger.Event
	call(t, srv, "GET", "/api/v0/events?type=io.ore.repository.autonomy.changed", nil, &evs)
	if len(evs) != 2 || !strings.Contains(string(evs[1].Data), "evidence: {") {
		t.Fatalf("autonomy events: %+v", evs)
	}

	// the watcher reacts to a delivery failure issue (signals endpoint)
	s.Loop.AdapterCmd = `printf 'Hello, Ore!\n' > greeting.txt; echo touched >> notes.txt`
	if _, err := s.DB.CreateIssue(ctx, repo, "Deployment dep_x to production failed health check", "probe failed", ledger.System); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if n, _ := svc.Tick(ctx); n == 0 {
			break
		}
	}
	var sigs []Signal
	if code := call(t, srv, "GET", repoAPI+"/fleet/signals", nil, &sigs); code != 200 || len(sigs) != 1 || sigs[0].Decision != "started" {
		t.Fatalf("signals: %d %+v", code, sigs)
	}
	var runs []ledger.AgentRun
	call(t, srv, "GET", repoAPI+"/agent/runs", nil, &runs)
	if len(runs) != 2 || runs[0].Status != "completed" {
		t.Fatalf("runs: %+v", runs)
	}
	if code := call(t, srv, "GET", "/api/v0/repos/acme/nope/fleet", nil, nil); code != 404 {
		t.Fatalf("missing repo: %d", code)
	}
	_ = strconv.Itoa
}
