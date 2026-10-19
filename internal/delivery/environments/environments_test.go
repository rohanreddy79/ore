// SPDX-License-Identifier: AGPL-3.0-only

package environments

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

	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/review"
	"github.com/orevcs/ore/internal/web"
)

// newTestServer builds the server the way `ore serve` does, with the none
// executor, and applies the registered extensions (this slice included).
func newTestServer(t *testing.T) (*httptest.Server, *web.Server) {
	t.Helper()
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
	if s.Deployer.Environments == nil || s.Services[Name] == nil {
		t.Fatal("extension must attach the service")
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, s
}

// as returns a caller that authenticates as user with the shared token.
type caller struct {
	t    *testing.T
	srv  *httptest.Server
	user string
}

func (c caller) do(method, path string, in any, out any) (int, string) {
	c.t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.srv.URL+path, body)
	if c.user == "" {
		req.Header.Set("Authorization", "Bearer tok")
	} else {
		req.SetBasicAuth(c.user, "tok")
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 400 {
		if err := json.Unmarshal(data, out); err != nil {
			c.t.Fatalf("%s %s: bad json %v: %s", method, path, err, data)
		}
	}
	return resp.StatusCode, string(data)
}

func TestEnvironmentsEndToEnd(t *testing.T) {
	srv, _ := newTestServer(t)
	admin := caller{t, srv, ""}
	alice := caller{t, srv, "alice"}
	mallory := caller{t, srv, "mallory"}
	const base = "/api/v0/repos/acme/app"

	if code, _ := admin.do("POST", "/api/v0/orgs", map[string]string{"name": "acme"}, nil); code != 201 {
		t.Fatal(code)
	}
	if code, _ := admin.do("POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "app", "autonomy_level": 3}, nil); code != 201 {
		t.Fatal(code)
	}
	var commit struct{ Commit string }
	admin.do("POST", base+"/commits", map[string]any{"branch": "main", "message": "v1", "files": []map[string]any{{"path": "Dockerfile", "content": "FROM scratch\n"}}}, &commit)
	admin.do("POST", base+"/commits", map[string]any{"branch": "feature", "message": "wip", "files": []map[string]any{{"path": "f", "content": "x"}}}, nil)

	// 1. a production environment requiring one approval from alice, target none
	var env delivery.Environment
	code, body := admin.do("POST", base+"/environments", map[string]any{"name": "production", "kind": "production",
		"target": map[string]any{"kind": "none"},
		"policy": map[string]any{"required_approvals": 1, "approvers": []string{"alice"}, "allowed_refs": []string{"refs/heads/main"}}}, &env)
	if code != 201 || env.Kind != "production" || env.Policy.RequiredApprovals != 1 || env.Policy.AutoRollback != true || env.Policy.AllowAgentDeploys {
		t.Fatalf("create env: %d %s", code, body)
	}
	if code, _ := admin.do("POST", base+"/environments", map[string]any{"name": "production", "kind": "production"}, nil); code != 409 {
		t.Fatalf("duplicate env: %d", code)
	}
	if code, body := admin.do("POST", base+"/environments", map[string]any{"name": "qa", "kind": "qa"}, nil); code != 400 || !strings.Contains(body, "kind") {
		t.Fatalf("bad kind: %d %s", code, body)
	}
	if code, body := admin.do("POST", base+"/environments", map[string]any{"name": "p2", "policy": map[string]any{"approvalz": 1}}, nil); code != 400 || !strings.Contains(body, "approvalz") {
		t.Fatalf("unknown policy field: %d %s", code, body)
	}
	var envs []delivery.Environment
	if code, _ := admin.do("GET", base+"/environments", nil, &envs); code != 200 || len(envs) != 1 {
		t.Fatalf("list: %d %v", code, envs)
	}
	if code, _ := admin.do("PUT", base+"/environments/production", map[string]any{"policy": map[string]any{"wait_seconds": 0}}, &env); code != 200 || env.Policy.RequiredApprovals != 1 {
		t.Fatalf("update must merge: %d %+v", code, env.Policy)
	}

	// 2. request a deployment: awaiting approval (202)
	var dep delivery.DeploymentView
	code, body = admin.do("POST", base+"/environments/production/deployments", map[string]string{"ref": "main"}, &dep)
	if code != 202 || dep.Status != "awaiting_approval" || dep.CommitOID != commit.Commit || dep.Rollout == nil || dep.Rollout.SourceRef != "refs/heads/main" || dep.Rollout.Decision.Outcome != "await" {
		t.Fatalf("request: %d %s", code, body)
	}
	// a disallowed ref is refused (403) and recorded; so is a second request while one awaits (409)
	code, body = admin.do("POST", base+"/environments/production/deployments", map[string]string{"ref": "feature"}, nil)
	if code != 403 || !strings.Contains(body, "allowed refs") || !strings.Contains(body, `"policy_decision"`) {
		t.Fatalf("disallowed ref: %d %s", code, body)
	}
	if code, body := admin.do("POST", base+"/environments/production/deployments", map[string]string{"ref": "main"}, nil); code != 409 && code != 403 {
		t.Fatalf("second request: %d %s", code, body)
	}
	// 3. approve: a non-approver gets 403, alice's approval runs the deployment
	if code, body := mallory.do("POST", base+"/deployments/"+dep.ID+"/approve", nil, nil); code != 403 {
		t.Fatalf("non-approver: %d %s", code, body)
	}
	code, body = alice.do("POST", base+"/deployments/"+dep.ID+"/approve", map[string]string{"comment": "ship it"}, &dep)
	if code != 200 || dep.Status != "succeeded" || len(dep.Approvals) != 1 || dep.Approvals[0].Approver != "alice" {
		t.Fatalf("approve: %d %s", code, body)
	}
	if code, _ := alice.do("POST", base+"/deployments/"+dep.ID+"/approve", nil, nil); code != 409 {
		t.Fatalf("approving a succeeded deployment: %d", code)
	}
	// 4. roll back with no previous deployment: 409
	if code, body := admin.do("POST", base+"/environments/production/rollback", nil, nil); code != 409 || !strings.Contains(body, "no previous") {
		t.Fatalf("rollback without previous: %d %s", code, body)
	}
	// 5. a second release through the v0 deployments route goes through the hook: awaiting, then reject, then 409 on approve
	admin.do("POST", base+"/commits", map[string]any{"branch": "main", "message": "v2", "files": []map[string]any{{"path": "Dockerfile", "content": "FROM scratch\n# v2\n"}}}, &commit)
	var legacy ledger.Deployment
	if code, body := admin.do("POST", base+"/deployments", map[string]string{"ref": "refs/heads/main", "environment": "production"}, &legacy); code != 201 || legacy.Status != "awaiting_approval" {
		t.Fatalf("legacy route: %d %s", code, body)
	}
	if code, body := alice.do("POST", base+"/deployments/"+legacy.ID+"/reject", map[string]string{"comment": "not yet"}, &dep); code != 200 || dep.Status != "rejected" {
		t.Fatalf("reject: %d %s", code, body)
	}
	if code, body := alice.do("POST", base+"/deployments/"+legacy.ID+"/approve", nil, nil); code != 409 {
		t.Fatalf("approve after reject: %d %s", code, body)
	}
	// 6. second deployment approved, then rolled back
	var second delivery.DeploymentView
	admin.do("POST", base+"/environments/production/deployments", map[string]string{"ref": "refs/heads/main"}, &second)
	if code, body := alice.do("POST", base+"/deployments/"+second.ID+"/approve", nil, &second); code != 200 || second.Status != "succeeded" {
		t.Fatalf("approve second: %d %s", code, body)
	}
	var rb delivery.DeploymentView
	code, body = admin.do("POST", base+"/environments/production/rollback", nil, &rb)
	if code != 201 || rb.Status != "succeeded" || rb.RollbackOf == nil || *rb.RollbackOf != second.ID || rb.CommitOID == second.CommitOID {
		t.Fatalf("rollback: %d %s", code, body)
	}
	var view delivery.DeploymentView
	if code, _ := admin.do("GET", base+"/deployments/"+second.ID, nil, &view); code != 200 || view.Status != "rolled_back" {
		t.Fatalf("rolled back view: %d %+v", code, view)
	}
	if code, _ := admin.do("GET", base+"/deployments/dep_nope", nil, nil); code != 404 {
		t.Fatalf("unknown deployment: %d", code)
	}
	// 7. DORA from the ledger
	var dora delivery.DORA
	if code, body := admin.do("GET", base+"/dora?days=30", nil, &dora); code != 200 {
		t.Fatalf("dora: %d %s", code, body)
	}
	if dora.Days != 30 || dora.Deploys.Total != 3 || dora.Deploys.Rollbacks != 1 || dora.ChangeFailure.Total != 2 || dora.ChangeFailure.Failed != 1 || dora.ChangeFailure.Rate != 0.5 {
		t.Fatalf("dora %+v", dora)
	}
	// 8. the ledger has every transition with the decision attached
	var evs []ledger.Event
	admin.do("GET", "/api/v0/events?type=io.ore.deployment.&limit=500", nil, &evs)
	seen := map[string]bool{}
	for _, e := range evs {
		seen[e.Type] = true
		if strings.HasSuffix(e.Type, ".started") && !strings.Contains(string(e.Data), `"policy_decision"`) {
			t.Fatalf("started without decision: %s", e.Data)
		}
	}
	for _, want := range []string{"requested", "awaiting_approval", "approved", "approval.refused", "started", "succeeded", "refused", "rejected", "rolled_back"} {
		if !seen["io.ore.deployment."+want] {
			t.Fatalf("missing event io.ore.deployment.%s in %v", want, seen)
		}
	}
	// 9. previews expire; extension past the maximum is clamped
	admin.do("POST", base+"/environments", map[string]any{"name": "preview", "target": map[string]any{"kind": "none"}}, &env)
	if env.Kind != "preview" || env.Policy.PreviewTTLSeconds != 7200 {
		t.Fatalf("preview defaults %+v", env)
	}
	var pv delivery.DeploymentView
	if code, body := admin.do("POST", base+"/environments/preview/deployments", map[string]string{"ref": "feature"}, &pv); code != 201 || pv.TTL == nil {
		t.Fatalf("preview deploy: %d %s", code, body)
	}
	var ext struct {
		ExpiresAt string `json:"expires_at"`
		Clamped   bool   `json:"clamped"`
	}
	if code, body := admin.do("POST", base+"/deployments/"+pv.ID+"/extend", map[string]int{"hours": 100}, &ext); code != 200 || !ext.Clamped {
		t.Fatalf("extend: %d %s", code, body)
	}
	if code, _ := admin.do("POST", base+"/deployments/"+dep.ID+"/extend", map[string]int{"hours": 1}, nil); code != 409 {
		t.Fatalf("extending a rejected deployment: %d", code)
	}
	if code, _ := admin.do("POST", base+"/environments/preview/stop", map[string]string{"reason": "done"}, nil); code != 200 {
		t.Fatalf("stop: %d", code)
	}
	if code, _ := admin.do("DELETE", base+"/environments/preview", nil, nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := admin.do("GET", base+"/environments/preview", nil, nil); code != 404 {
		t.Fatalf("deleted env: %d", code)
	}
	// unauthenticated
	req, _ := http.NewRequest("GET", srv.URL+base+"/environments", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 401 {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
}

// TestLegacyPathsWithoutRecordsAreUnchanged keeps `ore demo` honest: a
// repository with no environment record deploys the implicit way.
func TestLegacyPathsWithoutRecordsAreUnchanged(t *testing.T) {
	srv, _ := newTestServer(t)
	admin := caller{t, srv, ""}
	admin.do("POST", "/api/v0/orgs", map[string]string{"name": "demo"}, nil)
	admin.do("POST", "/api/v0/repos", map[string]any{"org": "demo", "name": "hello", "autonomy_level": 4}, nil)
	admin.do("POST", "/api/v0/repos/demo/hello/commits", map[string]any{"branch": "main", "message": "v1", "files": []map[string]any{{"path": "Dockerfile", "content": "FROM scratch\n"}}}, nil)
	var dep ledger.Deployment
	for _, env := range []string{"preview", "production"} {
		if code, body := admin.do("POST", "/api/v0/repos/demo/hello/deployments", map[string]string{"environment": env}, &dep); code != 201 || dep.Status != "succeeded" {
			t.Fatalf("%s: %d %s", env, code, body)
		}
	}
	var view delivery.DeploymentView
	if code, _ := admin.do("GET", "/api/v0/repos/demo/hello/deployments/"+dep.ID, nil, &view); code != 200 || view.Rollout != nil {
		t.Fatalf("implicit deployments carry no rollout record: %d %+v", code, view)
	}
	if code, _ := admin.do("POST", "/api/v0/repos/demo/hello/deployments/stop", map[string]string{"environment": "preview"}, nil); code != 200 {
		t.Fatalf("stop: %d", code)
	}
	var evs []ledger.Event
	admin.do("GET", "/api/v0/events?type=io.ore.deployment.", nil, &evs)
	if len(evs) != 7 { // requested, started, succeeded ×2 + stopped
		t.Fatalf("expected 7 deployment events, got %d", len(evs))
	}
}
