// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// harness is a service over a fresh ledger with one repository.
type harness struct {
	t    *testing.T
	ctx  context.Context
	db   *ledger.DB
	svc  *Service
	dep  *Deployer
	repo ledger.Repo
	rp   string
	now  time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := ledger.Open(ctx, filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	org, err := db.CreateOrg(ctx, "acme", ledger.System)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := db.CreateRepo(ctx, org, "app", "main", 2, 8080, ledger.System)
	if err != nil {
		t.Fatal(err)
	}
	rp := filepath.Join(dir, "acme", "app.git")
	if err := gitx.InitBare(ctx, rp, "main"); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, ctx: ctx, db: db, repo: repo, rp: rp, now: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	h.dep = &Deployer{DB: db, ReposDir: dir, Executor: "none"}
	h.svc = &Service{DB: db, ReposDir: dir, Deployer: h.dep, Now: func() time.Time { return h.now }, probeInterval: 20 * time.Millisecond}
	h.dep.Environments = h.svc
	return h
}

// commit writes a Dockerfile with the given marker on main and returns the oid.
func (h *harness) commit(marker string) string {
	h.t.Helper()
	parent, _ := gitx.RevParse(h.ctx, h.rp, "refs/heads/main")
	old := parent
	if parent == "" {
		old = gitx.ZeroOID
	}
	oid, err := gitx.CommitFiles(h.ctx, h.rp, parent, []gitx.FileEdit{{Path: "Dockerfile", Content: []byte("FROM scratch\n# " + marker + "\n")}}, marker+"\n", gitx.Author{Name: "t", Email: "t@x"})
	if err != nil {
		h.t.Fatal(err)
	}
	if err := gitx.UpdateRef(h.ctx, h.rp, "refs/heads/main", oid, old); err != nil {
		h.t.Fatal(err)
	}
	return oid
}

func (h *harness) env(name, kind string, target Target, p Policy) Environment {
	h.t.Helper()
	env := Environment{Name: name, Kind: kind, Target: target, Policy: p}
	if err := h.svc.CreateEnvironment(h.ctx, h.repo, &env, human); err != nil {
		h.t.Fatal(err)
	}
	return env
}

func (h *harness) events(depID string) []string {
	evs, _ := h.db.Events(h.ctx, ledger.EventsOptions{Subject: h.repo.Org + "/" + h.repo.Name + "/deployments/" + depID, Limit: 100})
	var types []string
	for _, e := range evs {
		types = append(types, strings.TrimPrefix(e.Type, "io.ore.deployment."))
	}
	return types
}

func (h *harness) status(depID string) string {
	d, err := h.db.Deployment(h.ctx, depID)
	if err != nil {
		h.t.Fatal(err)
	}
	return d.Status
}

func (h *harness) issues() []ledger.Issue {
	is, _ := h.db.Issues(h.ctx, h.repo)
	return is
}

// fakeTarget is a fake Docker engine whose containers are one HTTP server that
// answers /health with 500 for images marked unhealthy.
type fakeTarget struct {
	fr        *fakeRunner
	srv       *httptest.Server
	mu        sync.Mutex
	current   string
	unhealthy map[string]bool
	removed   []string
}

func (h *harness) fakeDocker() *fakeTarget {
	ft := &fakeTarget{fr: &fakeRunner{}, unhealthy: map[string]bool{}}
	ft.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ft.mu.Lock()
		bad := ft.unhealthy[ft.current]
		ft.mu.Unlock()
		if bad {
			http.Error(w, "boom", 500)
			return
		}
		w.Write([]byte("ok"))
	}))
	h.t.Cleanup(ft.srv.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(ft.srv.URL, "http://"))
	ft.fr.portOut = "0.0.0.0:" + port
	base := ft.fr
	h.svc.newEngine = func(ctx context.Context, t Target) (*engine, error) {
		return &engine{kind: t.Kind, host: "127.0.0.1", runner: recordingRunner{base: base, ft: ft}}, nil
	}
	return ft
}

type recordingRunner struct {
	base *fakeRunner
	ft   *fakeTarget
}

func (r recordingRunner) run(ctx context.Context, env []string, name string, args ...string) (string, error) {
	out, err := r.base.run(ctx, env, name, args...)
	if len(args) > 0 && args[0] == "run" {
		r.ft.mu.Lock()
		r.ft.current = args[len(args)-1]
		r.ft.mu.Unlock()
	}
	if len(args) > 2 && args[0] == "rm" {
		r.ft.mu.Lock()
		r.ft.removed = append(r.ft.removed, args[2])
		r.ft.mu.Unlock()
	}
	return out, err
}

func (r recordingRunner) pipe(ctx context.Context, env []string, a, b []string) (string, error) {
	return r.base.pipe(ctx, env, a, b)
}

func imageFor(repo ledger.Repo, oid string) string {
	return fmt.Sprintf("ore/%s-%s:%s", repo.Org, repo.Name, oid[:12])
}

// ---- tests ----

func TestRequestAwaitsApprovalThenSucceeds(t *testing.T) {
	h := newHarness(t)
	env := h.env("production", KindProduction, Target{Kind: TargetNone}, Policy{RequiredApprovals: 1, Approvers: []string{"alice"}})
	dep, err := h.svc.Request(h.ctx, h.repo, env, DeployRequest{Ref: "refs/heads/main", CommitOID: "aaaa"}, bob)
	if err != nil || dep.Status != StateAwaitingApproval {
		t.Fatalf("request: %v %+v", err, dep)
	}
	// a non-approver is refused and the attempt is on the ledger
	if _, err := h.svc.Approve(h.ctx, h.repo, dep.ID, bob, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-approver: %v", err)
	}
	if _, err := h.svc.Approve(h.ctx, h.repo, dep.ID, agent, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent approver: %v", err)
	}
	got, err := h.svc.Approve(h.ctx, h.repo, dep.ID, human, "lgtm")
	if err != nil || got.Status != StateSucceeded || got.URL != "none://acme/app/production" {
		t.Fatalf("approve: %v %+v", err, got)
	}
	want := []string{"requested", "awaiting_approval", "approval.refused", "approval.refused", "approved", "started", "succeeded"}
	if got := h.events(dep.ID); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events %v", got)
	}
	view, _ := h.svc.View(h.ctx, dep.ID)
	if view.Rollout == nil || view.Rollout.Decision.Approvals != 1 || len(view.Approvals) != 1 || view.Approvals[0].Approver != "alice" || view.Rollout.Health == nil || !view.Rollout.Health.Skipped {
		t.Fatalf("view %+v", view)
	}
}

func TestDeployFromDisallowedRefIsRefusedAndRecorded(t *testing.T) {
	h := newHarness(t)
	env := h.env("production", KindProduction, Target{Kind: TargetNone}, Policy{AllowedRefs: []string{"refs/heads/main"}})
	dep, err := h.svc.Request(h.ctx, h.repo, env, DeployRequest{Ref: "refs/heads/feature", CommitOID: "aaaa"}, human)
	var refused *RefusedError
	if !errors.As(err, &refused) || dep.Status != StateRefused || refused.DeploymentID != dep.ID {
		t.Fatalf("expected refusal, got %v %+v", err, dep)
	}
	if got := h.events(dep.ID); strings.Join(got, ",") != "requested,refused" {
		t.Fatalf("events %v", got)
	}
	evs, _ := h.db.Events(h.ctx, ledger.EventsOptions{Type: "io.ore.deployment.refused"})
	if len(evs) != 1 || !strings.Contains(string(evs[0].Data), `"policy_decision"`) || !strings.Contains(string(evs[0].Data), "allowed refs") {
		t.Fatalf("refused event must carry the decision: %+v", evs)
	}
	if _, err := h.db.CurrentDeployment(h.ctx, h.repo, "production"); err == nil {
		t.Fatal("nothing must be deployed")
	}
}

func TestAgentDeployToEnvironmentThatForbidsAgentsIsRefused(t *testing.T) {
	h := newHarness(t)
	oid := h.commit("v1")
	h.env("production", KindProduction, Target{Kind: TargetNone}, Policy{AllowAgentDeploys: false})
	// through the legacy Deployer entry point the agent loop uses
	dep, err := h.dep.Deploy(h.ctx, h.repo, oid, "production", agent, nil)
	var refused *RefusedError
	if !errors.As(err, &refused) || dep.Status != StateRefused {
		t.Fatalf("agent must be refused: %v %+v", err, dep)
	}
	ro, _ := h.svc.Rollout(h.ctx, dep.ID)
	if ro.SourceRef != "refs/heads/main" {
		t.Fatalf("the ref the commit was deployed from must be inferred: %+v", ro)
	}
	// a human deploying the same commit goes through
	ok, err := h.dep.Deploy(h.ctx, h.repo, oid, "production", human, nil)
	if err != nil || ok.Status != StateSucceeded {
		t.Fatalf("human: %v %+v", err, ok)
	}
	// repositories without a record keep the implicit path (ore demo)
	imp, err := h.dep.Deploy(h.ctx, h.repo, oid, "preview", agent, nil)
	if err != nil || imp.Status != StateSucceeded {
		t.Fatalf("implicit: %v %+v", err, imp)
	}
	if _, err := h.svc.Rollout(h.ctx, imp.ID); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatal("implicit deployments have no rollout record")
	}
}

func TestDoubleApprovalBySameUserCountsOnce(t *testing.T) {
	h := newHarness(t)
	env := h.env("production", KindProduction, Target{Kind: TargetNone}, Policy{RequiredApprovals: 2, Approvers: []string{"alice", "bob"}})
	dep, _ := h.svc.Request(h.ctx, h.repo, env, DeployRequest{Ref: "refs/heads/main", CommitOID: "aaaa"}, human)
	for i := 0; i < 2; i++ {
		got, err := h.svc.Approve(h.ctx, h.repo, dep.ID, human, "")
		if err != nil || got.Status != StateAwaitingApproval {
			t.Fatalf("alice #%d: %v %s", i, err, got.Status)
		}
	}
	ro, _ := h.svc.Rollout(h.ctx, dep.ID)
	if ro.Decision.Approvals != 1 || ro.Decision.Outcome != OutcomeAwait {
		t.Fatalf("decision %+v", ro.Decision)
	}
	got, err := h.svc.Approve(h.ctx, h.repo, dep.ID, bob, "")
	if err != nil || got.Status != StateSucceeded {
		t.Fatalf("bob: %v %s", err, got.Status)
	}
	aps, _ := h.svc.Approvals(h.ctx, dep.ID)
	if len(aps) != 3 {
		t.Fatalf("all decisions are kept: %d", len(aps))
	}
}

func TestApprovalAfterRejectionConflicts(t *testing.T) {
	h := newHarness(t)
	env := h.env("production", KindProduction, Target{Kind: TargetNone}, Policy{RequiredApprovals: 1})
	dep, _ := h.svc.Request(h.ctx, h.repo, env, DeployRequest{Ref: "refs/heads/main", CommitOID: "aaaa"}, human)
	if _, err := h.svc.Reject(h.ctx, h.repo, dep.ID, agent, "no"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agents cannot reject: %v", err)
	}
	got, err := h.svc.Reject(h.ctx, h.repo, dep.ID, bob, "not now")
	if err != nil || got.Status != StateRejected {
		t.Fatalf("reject: %v %s", err, got.Status)
	}
	var conflict *ConflictError
	if _, err := h.svc.Approve(h.ctx, h.repo, dep.ID, human, ""); !errors.As(err, &conflict) {
		t.Fatalf("approve after reject must conflict: %v", err)
	}
	if _, err := h.svc.Reject(h.ctx, h.repo, dep.ID, human, ""); !errors.As(err, &conflict) {
		t.Fatalf("reject twice must conflict: %v", err)
	}
	if got := h.events(dep.ID); strings.Join(got, ",") != "requested,awaiting_approval,approval.refused,rejected" {
		t.Fatalf("events %v", got)
	}
	// the environment is free again
	next, err := h.svc.Request(h.ctx, h.repo, env, DeployRequest{Ref: "refs/heads/main", CommitOID: "bbbb"}, human)
	if err != nil || next.Status != StateAwaitingApproval {
		t.Fatalf("next: %v %s", err, next.Status)
	}
	// and a second request while one awaits approval is refused, recorded
	busy, err := h.svc.Request(h.ctx, h.repo, env, DeployRequest{Ref: "refs/heads/main", CommitOID: "cccc"}, human)
	if !errors.As(err, &conflict) || busy.Status != StateRefused {
		t.Fatalf("busy: %v %s", err, busy.Status)
	}
}

func TestRollbackWithoutPreviousDeploymentConflicts(t *testing.T) {
	h := newHarness(t)
	env := h.env("production", KindProduction, Target{Kind: TargetNone}, Policy{})
	var conflict *ConflictError
	if _, err := h.svc.Rollback(h.ctx, h.repo, env, human); !errors.As(err, &conflict) {
		t.Fatalf("nothing deployed: %v", err)
	}
	first, err := h.svc.Request(h.ctx, h.repo, env, DeployRequest{Ref: "refs/heads/main", CommitOID: "aaaa"}, human)
	if err != nil || first.Status != StateSucceeded {
		t.Fatal(err)
	}
	if _, err := h.svc.Rollback(h.ctx, h.repo, env, human); !errors.As(err, &conflict) {
		t.Fatalf("no previous: %v", err)
	}
	second, _ := h.svc.Request(h.ctx, h.repo, env, DeployRequest{Ref: "refs/heads/main", CommitOID: "bbbb"}, human)
	rb, err := h.svc.Rollback(h.ctx, h.repo, env, human)
	if err != nil || rb.Status != StateSucceeded || rb.CommitOID != "aaaa" || rb.RollbackOf == nil || *rb.RollbackOf != second.ID {
		t.Fatalf("rollback: %v %+v", err, rb)
	}
	if h.status(second.ID) != StateRolledBack || h.status(first.ID) != StateSucceeded {
		t.Fatalf("second %s first %s", h.status(second.ID), h.status(first.ID))
	}
	if got := h.events(second.ID); got[len(got)-1] != "rolled_back" {
		t.Fatalf("events %v", got)
	}
	// through the legacy entry point too
	if _, err := h.dep.Rollback(h.ctx, h.repo, "production", human); err != nil {
		t.Fatalf("legacy rollback: %v", err)
	}
}

func TestHealthTimeoutWithAutoRollbackOffStaysFailedAndOpensIssue(t *testing.T) {
	h := newHarness(t)
	ft := h.fakeDocker()
	oid := h.commit("v1")
	env := h.env("production", KindProduction, Target{Kind: TargetLocalDocker}, Policy{HealthPath: "/health", HealthTimeoutSeconds: 1, AutoRollback: false})
	ft.unhealthy[imageFor(h.repo, oid)] = true
	dep, err := h.svc.Request(h.ctx, h.repo, env, DeployRequest{Ref: "refs/heads/main", CommitOID: oid}, human)
	var failed *FailedError
	if !errors.As(err, &failed) || dep.Status != StateFailed {
		t.Fatalf("expected failure, got %v %+v", err, dep)
	}
	if got := h.events(dep.ID); strings.Join(got, ",") != "requested,started,failed" {
		t.Fatalf("events %v", got)
	}
	ro, _ := h.svc.Rollout(h.ctx, dep.ID)
	if ro.Health == nil || ro.Health.Healthy || ro.Health.LastStatus != 500 || ro.Health.Attempts < 2 || ro.Failure == "" {
		t.Fatalf("rollout %+v health %+v", ro, ro.Health)
	}
	// the failed container is removed; no rollback deployment exists
	if len(ft.removed) == 0 || ft.removed[len(ft.removed)-1] != dep.ContainerID {
		t.Fatalf("failed container must be removed: %v", ft.removed)
	}
	deps, _ := h.db.Deployments(h.ctx, h.repo, 10)
	if len(deps) != 1 {
		t.Fatalf("no rollback expected: %d deployments", len(deps))
	}
	is := h.issues()
	if len(is) != 1 || is[0].Title != fmt.Sprintf("Deployment %s to production failed health check", dep.ID) {
		t.Fatalf("issue %+v", is)
	}
	if !strings.Contains(is[0].Body, "log line 1") || !strings.Contains(is[0].Body, "auto-rollback: off") || !strings.Contains(is[0].Body, "status 500") {
		t.Fatalf("issue body:\n%s", is[0].Body)
	}
}

func TestHealthFailureAutoRollsBackAndRecordsRecoveryTime(t *testing.T) {
	h := newHarness(t)
	ft := h.fakeDocker()
	good := h.commit("v1")
	bad := h.commit("v2")
	env := h.env("production", KindProduction, Target{Kind: TargetLocalDocker}, Policy{HealthPath: "/health", HealthTimeoutSeconds: 1, AutoRollback: true})
	first, err := h.svc.Request(h.ctx, h.repo, env, DeployRequest{Ref: "refs/heads/main", CommitOID: good}, human)
	if err != nil || first.Status != StateSucceeded || !strings.HasPrefix(first.URL, "http://127.0.0.1:") {
		t.Fatalf("first: %v %+v", err, first)
	}
	ft.unhealthy[imageFor(h.repo, bad)] = true
	h.now = h.now.Add(time.Hour)
	second, err := h.svc.Request(h.ctx, h.repo, env, DeployRequest{Ref: "refs/heads/main", CommitOID: bad}, human)
	var failed *FailedError
	if !errors.As(err, &failed) {
		t.Fatalf("expected FailedError, got %v", err)
	}
	if h.status(second.ID) != StateRolledBack {
		t.Fatalf("failed deployment must be rolled back, is %s", h.status(second.ID))
	}
	deps, _ := h.db.Deployments(h.ctx, h.repo, 10)
	if len(deps) != 3 {
		t.Fatalf("expected a rollback deployment: %d", len(deps))
	}
	var rb ledger.Deployment
	for _, d := range deps {
		if d.RollbackOf != nil {
			rb = d
		}
	}
	if rb.Status != StateSucceeded || rb.CommitOID != good || rb.RollbackOf == nil || *rb.RollbackOf != second.ID || rb.Actor != ledger.System {
		t.Fatalf("rollback %+v", rb)
	}
	cur, _ := h.db.CurrentDeployment(h.ctx, h.repo, "production")
	if cur.ID != rb.ID {
		t.Fatalf("current must be the rollback, is %s", cur.ID)
	}
	if got := h.events(second.ID); strings.Join(got, ",") != "requested,started,failed,rolled_back" {
		t.Fatalf("events %v", got)
	}
	evs, _ := h.db.Events(h.ctx, ledger.EventsOptions{Type: "io.ore.deployment.rolled_back"})
	if len(evs) != 1 || !strings.Contains(string(evs[0].Data), `"recovery_seconds"`) || !strings.Contains(string(evs[0].Data), `"rollback_deployment":"`+rb.ID+`"`) {
		t.Fatalf("rolled_back event must record recovery time: %s", evs[0].Data)
	}
	ro, _ := h.svc.Rollout(h.ctx, second.ID)
	if ro.RecoverySeconds == nil || *ro.RecoverySeconds < 0 {
		t.Fatalf("recovery %+v", ro)
	}
	rbro, _ := h.svc.Rollout(h.ctx, rb.ID)
	if rbro.Decision.Bypass == "" || rbro.Decision.Outcome != OutcomeStart {
		t.Fatalf("rollback decision must record the waiver: %+v", rbro.Decision)
	}
	is := h.issues()
	if len(is) != 1 || !strings.Contains(is[0].Body, "auto-rollback: succeeded, deployment `"+rb.ID+"`") {
		t.Fatalf("issue %+v", is)
	}
	// only the failed container and the one it replaced were removed; the rollback's is running
	for _, r := range ft.removed {
		if r == rb.ContainerID {
			t.Fatal("the rollback container must stay up")
		}
	}
	// the DORA view sees one failure out of two changes and one recovery sample
	d, err := h.svc.ComputeDORA(h.ctx, h.repo, 30)
	if err != nil {
		t.Fatal(err)
	}
	if d.Deploys.Total != 3 || d.Deploys.Rollbacks != 1 || d.ChangeFailure.Total != 2 || d.ChangeFailure.Failed != 1 || d.ChangeFailure.Rate != 0.5 || d.Recovery.Samples != 1 {
		t.Fatalf("dora %+v", d)
	}
}

func TestTwoConcurrentDeploymentsNeverRunTwoContainers(t *testing.T) {
	h := newHarness(t)
	ft := h.fakeDocker()
	oid := h.commit("v1")
	env := h.env("production", KindProduction, Target{Kind: TargetLocalDocker}, Policy{HealthPath: "/", HealthTimeoutSeconds: 2})
	started, release := make(chan struct{}), make(chan struct{})
	h.svc.beforeExecute = func(dep *ledger.Deployment) { close(started); <-release }
	var first ledger.Deployment
	var firstErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		first, firstErr = h.svc.Request(h.ctx, h.repo, env, DeployRequest{Ref: "refs/heads/main", CommitOID: oid}, human)
	}()
	<-started
	h.svc.beforeExecute = nil
	second, err := h.svc.Request(h.ctx, h.repo, env, DeployRequest{Ref: "refs/heads/main", CommitOID: oid}, bob)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || second.Status != StateRefused {
		t.Fatalf("second must be refused: %v %+v", err, second)
	}
	if got := h.events(second.ID); strings.Join(got, ",") != "requested,refused" {
		t.Fatalf("second's events %v", got)
	}
	close(release)
	<-done
	if firstErr != nil || first.Status != StateSucceeded {
		t.Fatalf("first: %v %+v", firstErr, first)
	}
	if ft.fr.runs != 1 {
		t.Fatalf("exactly one container must have been started, got %d", ft.fr.runs)
	}
}

func TestPreviewTTLReaperAndExtension(t *testing.T) {
	h := newHarness(t)
	env := h.env("preview", KindPreview, Target{Kind: TargetNone}, Policy{AllowAgentDeploys: true})
	dep, err := h.svc.Request(h.ctx, h.repo, env, DeployRequest{Ref: "refs/heads/feature", CommitOID: "aaaa"}, agent)
	if err != nil || dep.Status != StateSucceeded {
		t.Fatalf("preview: %v %+v", err, dep)
	}
	ttl, err := h.svc.TTL(h.ctx, dep.ID)
	if err != nil || parseTime(ttl.ExpiresAt) != h.now.Add(DefaultPreviewTTL) {
		t.Fatalf("ttl %+v %v", ttl, err)
	}
	// a tick with nothing expired writes nothing
	before, _ := h.db.Events(h.ctx, ledger.EventsOptions{Limit: 1000})
	if n, err := h.svc.Tick(h.ctx, h.now.Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("tick: %d %v", n, err)
	}
	after, _ := h.db.Events(h.ctx, ledger.EventsOptions{Limit: 1000})
	if len(after) != len(before) {
		t.Fatalf("reaper with nothing expired must not write events: %d -> %d", len(before), len(after))
	}
	// extension past the maximum is clamped to 24 h from now
	got, clamped, err := h.svc.Extend(h.ctx, h.repo, dep.ID, 100*time.Hour, human)
	if err != nil || !clamped || parseTime(got.ExpiresAt) != h.now.Add(MaxPreviewTTL) || got.Extensions != 1 {
		t.Fatalf("extend: %+v %v %v", got, clamped, err)
	}
	got, clamped, err = h.svc.Extend(h.ctx, h.repo, dep.ID, 3*time.Hour, human)
	if err != nil || clamped || parseTime(got.ExpiresAt) != h.now.Add(3*time.Hour) || got.Extensions != 2 {
		t.Fatalf("extend 3h: %+v %v %v", got, clamped, err)
	}
	if n, _ := h.svc.Tick(h.ctx, h.now.Add(2*time.Hour)); n != 0 {
		t.Fatal("extended preview must survive its original expiry")
	}
	n, err := h.svc.Tick(h.ctx, h.now.Add(3*time.Hour+time.Second))
	if err != nil || n != 1 || h.status(dep.ID) != StateStopped {
		t.Fatalf("reap: %d %v %s", n, err, h.status(dep.ID))
	}
	evs, _ := h.db.Events(h.ctx, ledger.EventsOptions{Type: "io.ore.deployment.stopped"})
	if len(evs) != 1 || !strings.Contains(string(evs[0].Data), `"reason":"ttl"`) {
		t.Fatalf("stopped event %v", evs)
	}
	if _, err := h.svc.TTL(h.ctx, dep.ID); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatal("ttl row must be gone")
	}
	var conflict *ConflictError
	if _, _, err := h.svc.Extend(h.ctx, h.repo, dep.ID, time.Hour, human); !errors.As(err, &conflict) {
		t.Fatalf("extending a stopped preview: %v", err)
	}
	if _, _, err := h.svc.Extend(h.ctx, h.repo, dep.ID, -time.Hour, human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative extension: %v", err)
	}
	// production never expires
	prod := h.env("production", KindProduction, Target{Kind: TargetNone}, Policy{})
	p, _ := h.svc.Request(h.ctx, h.repo, prod, DeployRequest{Ref: "refs/heads/main", CommitOID: "bbbb"}, human)
	if _, err := h.svc.TTL(h.ctx, p.ID); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatal("production has no ttl")
	}
	if _, _, err := h.svc.Extend(h.ctx, h.repo, p.ID, time.Hour, human); !errors.As(err, &conflict) {
		t.Fatalf("extending production: %v", err)
	}
}

func TestImplicitPreviewsExpireAfterTheDefaultTTL(t *testing.T) {
	h := newHarness(t)
	oid := h.commit("v1")
	dep, err := h.dep.Deploy(h.ctx, h.repo, oid, "preview", agent, nil) // no record: implicit path
	if err != nil || dep.Status != StateSucceeded {
		t.Fatal(err)
	}
	if n, _ := h.svc.ReapOnce(h.ctx, time.Now().Add(time.Hour)); n != 0 {
		t.Fatal("not yet")
	}
	n, err := h.svc.ReapOnce(h.ctx, time.Now().Add(DefaultPreviewTTL+time.Minute))
	if err != nil || n != 1 || h.status(dep.ID) != StateStopped {
		t.Fatalf("implicit preview reap: %d %v %s", n, err, h.status(dep.ID))
	}
}

func TestWaitTimerStartsTheDeploymentOnTick(t *testing.T) {
	h := newHarness(t)
	env := h.env("staging", KindStaging, Target{Kind: TargetNone}, Policy{WaitSeconds: 3600})
	dep, err := h.svc.Request(h.ctx, h.repo, env, DeployRequest{Ref: "refs/heads/main", CommitOID: "aaaa"}, human)
	if err != nil || dep.Status != StateAwaitingApproval {
		t.Fatalf("%v %+v", err, dep)
	}
	ro, _ := h.svc.Rollout(h.ctx, dep.ID)
	if ro.Decision.WaitUntil != h.now.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("wait until %q", ro.Decision.WaitUntil)
	}
	h.now = h.now.Add(30 * time.Minute)
	h.svc.Tick(h.ctx, h.now)
	if h.status(dep.ID) != StateAwaitingApproval {
		t.Fatal("too early")
	}
	h.now = h.now.Add(31 * time.Minute)
	h.svc.Tick(h.ctx, h.now)
	if h.status(dep.ID) != StateSucceeded {
		t.Fatalf("after the timer: %s", h.status(dep.ID))
	}
}

func TestEnvironmentCRUDAndEvents(t *testing.T) {
	h := newHarness(t)
	env := h.env("staging", KindStaging, Target{Kind: TargetSSHDocker, Host: "h.example", KeyRef: "file:/k"}, Policy{})
	if env.Target.User != "root" || env.Target.Port != 22 || env.Policy.HealthPath != "/" {
		t.Fatalf("defaults %+v", env)
	}
	dup := Environment{Name: "staging", Kind: KindStaging, Target: Target{Kind: TargetNone}}
	var conflict *ConflictError
	if err := h.svc.CreateEnvironment(h.ctx, h.repo, &dup, human); !errors.As(err, &conflict) {
		t.Fatalf("duplicate: %v", err)
	}
	badKind := Environment{Name: "x", Kind: "qa", Target: Target{Kind: TargetNone}}
	if err := h.svc.CreateEnvironment(h.ctx, h.repo, &badKind, human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad kind: %v", err)
	}
	env.Policy.RequiredApprovals = 2
	if err := h.svc.UpdateEnvironment(h.ctx, h.repo, &env, human); err != nil {
		t.Fatal(err)
	}
	got, err := h.svc.Environment(h.ctx, h.repo, "staging")
	if err != nil || got.Policy.RequiredApprovals != 2 || got.Target.Host != "h.example" {
		t.Fatalf("%v %+v", err, got)
	}
	list, _ := h.svc.Environments(h.ctx, h.repo)
	if len(list) != 1 {
		t.Fatal(len(list))
	}
	if err := h.svc.DeleteEnvironment(h.ctx, h.repo, got, human); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Environment(h.ctx, h.repo, "staging"); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatal("deleted")
	}
	evs, _ := h.db.Events(h.ctx, ledger.EventsOptions{Type: "io.ore.environment."})
	var types []string
	for _, e := range evs {
		types = append(types, e.Type)
	}
	if strings.Join(types, ",") != "io.ore.environment.created,io.ore.environment.updated,io.ore.environment.deleted" {
		t.Fatalf("events %v", types)
	}
	imp := h.svc.ImplicitEnvironment(h.repo, "production")
	if !imp.Implicit || imp.Kind != KindProduction || imp.Target.Kind != TargetNone || !imp.Policy.AllowAgentDeploys {
		t.Fatalf("implicit %+v", imp)
	}
}

func TestPercentile(t *testing.T) {
	v := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	if percentile(v, 0.5) != 50 || percentile(v, 0.9) != 90 || percentile(nil, 0.5) != 0 || percentile([]float64{7}, 0.9) != 7 {
		t.Fatalf("%v %v", percentile(v, 0.5), percentile(v, 0.9))
	}
}
