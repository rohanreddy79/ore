// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// SecretSource resolves "secret:NAME" key references (the secrets store
// another slice registers as Services["secrets"]).
type SecretSource interface {
	Secret(ctx context.Context, orgID, name string) (string, error)
}

// Service runs deployments into environments with policy: approvals, remote
// targets, health-gated rollouts with automatic rollback, preview lifecycles
// and the DORA view. It is attached to a Deployer as Deployer.Environments so
// the v0 API, the UI and the agent loop go through policy whenever an
// environment record exists.
type Service struct {
	DB       *ledger.DB
	ReposDir string
	// Deployer supplies the executor default and the public host for URLs.
	Deployer *Deployer
	// Secrets resolves secret: key references; nil means file: and env: only.
	Secrets SecretSource
	Log     *slog.Logger
	// Now is the clock (tests).
	Now func() time.Time
	// ReapInterval is the background tick (default 1 minute).
	ReapInterval time.Duration

	mu    sync.Mutex
	locks map[string]*sync.Mutex

	// test seams
	beforeExecute func(dep *ledger.Deployment)
	newEngine     func(ctx context.Context, t Target) (*engine, error)
	probeInterval time.Duration
}

// ErrForbidden marks an actor the policy does not allow to act (HTTP 403).
var ErrForbidden = errors.New("forbidden")

// FailedError reports a deployment that ran and failed its health check; the
// deployment record carries the detail.
type FailedError struct {
	Deployment ledger.Deployment
	Cause      error
}

func (e *FailedError) Error() string {
	return "deployment " + e.Deployment.ID + " failed: " + e.Cause.Error()
}
func (e *FailedError) Unwrap() error { return e.Cause }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// stamp is the clock in the ledger's timestamp format.
func (s *Service) stamp() string { return s.now().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) lock(repoID, env string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks == nil {
		s.locks = map[string]*sync.Mutex{}
	}
	k := repoID + "/" + env
	if s.locks[k] == nil {
		s.locks[k] = &sync.Mutex{}
	}
	return s.locks[k]
}

// DeployRequest asks for a commit to be placed into an environment.
type DeployRequest struct {
	// Ref is the full ref name the commit was resolved from; it is what
	// allowed_refs is matched against. Empty when only a commit is known.
	Ref        string
	CommitOID  string
	RollbackOf *string
	// Bypass waives approvals and the wait timer (automatic rollback).
	Bypass string
}

// Request evaluates the environment's policy and either refuses the
// deployment (recorded, *RefusedError), parks it awaiting approval, or runs
// it to completion. A second request into a busy environment is refused with
// *ConflictError and recorded; never two containers.
func (s *Service) Request(ctx context.Context, repo ledger.Repo, env Environment, in DeployRequest, actor ledger.Actor) (ledger.Deployment, error) {
	if in.CommitOID == "" {
		return ledger.Deployment{}, fmt.Errorf("%w: commit is required", ErrInvalid)
	}
	dep := ledger.Deployment{Environment: env.Name, CommitOID: in.CommitOID, Actor: actor, RollbackOf: in.RollbackOf}
	if cur, err := s.DB.CurrentDeployment(ctx, repo, env.Name); err == nil {
		dep.PreviousID = &cur.ID
	}
	now := s.now()
	preq := PolicyRequest{Ref: in.Ref, CommitOID: in.CommitOID, Actor: actor, RequestedAt: now, Now: now, Bypass: in.Bypass}
	if env.Policy.RequireGreenPipeline {
		g := s.pipelineGreen(ctx, repo, in.CommitOID)
		preq.PipelineGreen = &g
	}
	decision := Evaluate(env, preq)
	if decision.Outcome == OutcomeRefuse {
		// the policy verdict is the useful answer even when the environment is busy
		if err := s.insert(ctx, repo, env, &dep, in.Ref, decision); err != nil {
			return dep, err
		}
		if err := s.transition(ctx, repo, env, &dep, StateRefused, &decision, map[string]any{"reasons": decision.Reasons}); err != nil {
			return dep, err
		}
		return dep, &RefusedError{DeploymentID: dep.ID, Decision: decision}
	}
	lk := s.lock(repo.ID, env.Name)
	if !lk.TryLock() {
		return s.refuseBusy(ctx, repo, env, in, actor, "another deployment into "+env.Name+" is in progress")
	}
	defer lk.Unlock()
	if other, ok := s.inFlight(ctx, repo, env.Name); ok {
		return s.refuseBusy(ctx, repo, env, in, actor, fmt.Sprintf("deployment %s into %s is %s", other.ID, env.Name, other.Status))
	}
	if err := s.insert(ctx, repo, env, &dep, in.Ref, decision); err != nil {
		return dep, err
	}
	switch decision.Outcome {
	case OutcomeAwait:
		if err := s.transition(ctx, repo, env, &dep, StateAwaitingApproval, &decision, nil); err != nil {
			return dep, err
		}
		s.scheduleWait(repo, dep.ID, decision)
		return dep, nil
	}
	return s.run(ctx, repo, env, &dep, decision)
}

func (s *Service) refuseBusy(ctx context.Context, repo ledger.Repo, env Environment, in DeployRequest, actor ledger.Actor, why string) (ledger.Deployment, error) {
	dep := ledger.Deployment{Environment: env.Name, CommitOID: in.CommitOID, Actor: actor, RollbackOf: in.RollbackOf}
	now := s.now()
	decision := Decision{Outcome: OutcomeRefuse, Reasons: []string{why}, Checks: []Check{{Rule: "concurrency", Pass: false, Detail: why}}, RequiredApprovals: env.Policy.RequiredApprovals, EvaluatedAt: now.UTC().Format(time.RFC3339)}
	if err := s.insert(ctx, repo, env, &dep, in.Ref, decision); err != nil {
		return dep, err
	}
	if err := s.transition(ctx, repo, env, &dep, StateRefused, &decision, map[string]any{"reasons": decision.Reasons}); err != nil {
		return dep, err
	}
	return dep, &ConflictError{Msg: why}
}

// inFlight finds a deployment that blocks new requests for the environment:
// one awaiting approval, or one started in the last hour (a crashed server
// must not block its environment forever; the lock guards live processes).
func (s *Service) inFlight(ctx context.Context, repo ledger.Repo, env string) (ledger.Deployment, bool) {
	cutoff := s.now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05.000Z07:00")
	var id string
	err := s.DB.QueryRow(ctx, `SELECT id FROM deployments WHERE repo_id = ? AND environment = ? AND (status = ? OR (status = ? AND created_at > ?)) ORDER BY created_at DESC LIMIT 1`,
		repo.ID, env, StateAwaitingApproval, StateStarted, cutoff).Scan(&id)
	if err != nil {
		return ledger.Deployment{}, false
	}
	dep, err := s.DB.Deployment(ctx, id)
	return dep, err == nil
}

func (s *Service) pipelineGreen(ctx context.Context, repo ledger.Repo, commit string) bool {
	var n int
	_ = s.DB.QueryRow(ctx, `SELECT COUNT(1) FROM pipeline_runs WHERE repo_id = ? AND commit_oid = ? AND status = 'succeeded'`, repo.ID, commit).Scan(&n)
	return n > 0
}

func (s *Service) insert(ctx context.Context, repo ledger.Repo, env Environment, dep *ledger.Deployment, ref string, decision Decision) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.stamp()
	dep.ID, dep.OrgID, dep.RepoID, dep.Status, dep.CreatedAt = ledger.NewID("dep"), repo.OrgID, repo.ID, StateRequested, now
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployments(id, org_id, repo_id, environment, commit_oid, image, container_id, url, status, previous_id, rollback_of, actor_kind, actor_id, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, dep.ID, dep.OrgID, dep.RepoID, dep.Environment, dep.CommitOID, dep.Image, dep.ContainerID, dep.URL, dep.Status, dep.PreviousID, dep.RollbackOf, dep.Actor.Kind, dep.Actor.ID, dep.CreatedAt); err != nil {
		return err
	}
	dj, _ := json.Marshal(decision)
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployment_rollouts(deployment_id, org_id, repo_id, environment_id, source_ref, decision, health, failure, recovery_seconds, created_at, updated_at)
		VALUES(?,?,?,?,?,?,'{}','',NULL,?,?)`, dep.ID, repo.OrgID, repo.ID, env.ID, ref, string(dj), now, now); err != nil {
		return err
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.deployment.requested", depSubject(repo, dep.ID), dep.Actor, eventData(dep, env, &decision, map[string]any{"ref": ref})); err != nil {
		return err
	}
	return tx.Commit()
}

func depSubject(repo ledger.Repo, id string) string {
	return repo.Org + "/" + repo.Name + "/deployments/" + id
}

// eventData is the deployment plus the policy decision and transition detail.
func eventData(dep *ledger.Deployment, env Environment, decision *Decision, extra map[string]any) map[string]any {
	data := map[string]any{}
	b, _ := json.Marshal(dep)
	_ = json.Unmarshal(b, &data)
	data["environment_kind"] = env.Kind
	data["target_kind"] = env.Target.Kind
	if env.ID != "" {
		data["environment_id"] = env.ID
	}
	if decision != nil {
		data["policy_decision"] = decision
	}
	for k, v := range extra {
		data[k] = v
	}
	return data
}

// transition moves a deployment to a new state, updates its rollout record and
// emits io.ore.deployment.<state> with the policy decision attached, all in one
// transaction.
func (s *Service) transition(ctx context.Context, repo ledger.Repo, env Environment, dep *ledger.Deployment, to string, decision *Decision, extra map[string]any) error {
	if !CanTransition(dep.Status, to) {
		return &TransitionError{From: dep.Status, To: to}
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.stamp()
	dep.Status = to
	switch to {
	case StateSucceeded, StateFailed, StateRefused, StateRejected, StateStopped:
		dep.FinishedAt = &now
	}
	if _, err := tx.ExecContext(ctx, `UPDATE deployments SET status = ?, image = ?, container_id = ?, url = ?, finished_at = ? WHERE id = ?`, dep.Status, dep.Image, dep.ContainerID, dep.URL, dep.FinishedAt, dep.ID); err != nil {
		return err
	}
	set, args := []string{"updated_at = ?"}, []any{now}
	if decision != nil {
		dj, _ := json.Marshal(decision)
		set, args = append(set, "decision = ?"), append(args, string(dj))
	}
	if h, ok := extra["health"]; ok && h != nil {
		hj, _ := json.Marshal(h)
		set, args = append(set, "health = ?"), append(args, string(hj))
	}
	if f, ok := extra["failure"].(string); ok {
		set, args = append(set, "failure = ?"), append(args, f)
	}
	if r, ok := extra["recovery_seconds"].(float64); ok {
		set, args = append(set, "recovery_seconds = ?"), append(args, r)
	}
	args = append(args, dep.ID)
	if _, err := tx.ExecContext(ctx, `UPDATE deployment_rollouts SET `+strings.Join(set, ", ")+` WHERE deployment_id = ?`, args...); err != nil {
		return err
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.deployment."+to, depSubject(repo, dep.ID), dep.Actor, eventData(dep, env, decision, extra)); err != nil {
		return err
	}
	return tx.Commit()
}

// Approve records an approval and starts the deployment once the policy is
// satisfied. Non-approvers are refused (ErrForbidden); approving a deployment
// that is not awaiting approval is a conflict; the same approver counts once.
func (s *Service) Approve(ctx context.Context, repo ledger.Repo, deploymentID string, actor ledger.Actor, comment string) (ledger.Deployment, error) {
	dep, env, ro, err := s.load(ctx, repo, deploymentID)
	if err != nil {
		return dep, err
	}
	if dep.Status != StateAwaitingApproval {
		return dep, &ConflictError{Msg: fmt.Sprintf("deployment %s is %s, not awaiting approval", dep.ID, dep.Status)}
	}
	if !CanApprove(env, actor) {
		s.emit(ctx, repo, "io.ore.deployment.approval.refused", dep.ID, actor, map[string]any{"approver": actor.ID, "reason": "not an approver for " + env.Name})
		return dep, fmt.Errorf("%w: %s/%s is not an approver for %s", ErrForbidden, actor.Kind, actor.ID, env.Name)
	}
	decision, err := s.recordApproval(ctx, repo, env, &dep, ro, actor, "approve", comment)
	if err != nil {
		return dep, err
	}
	switch decision.Outcome {
	case OutcomeRefuse:
		if err := s.transition(ctx, repo, env, &dep, StateRefused, &decision, map[string]any{"reasons": decision.Reasons}); err != nil {
			return dep, err
		}
		return dep, &RefusedError{DeploymentID: dep.ID, Decision: decision}
	case OutcomeAwait:
		s.scheduleWait(repo, dep.ID, decision)
		return dep, nil
	}
	lk := s.lock(repo.ID, env.Name)
	if !lk.TryLock() {
		return dep, &ConflictError{Msg: "approval recorded; another deployment into " + env.Name + " is in progress, approve again when it finishes"}
	}
	defer lk.Unlock()
	return s.run(ctx, repo, env, &dep, decision)
}

// Reject ends a deployment awaiting approval.
func (s *Service) Reject(ctx context.Context, repo ledger.Repo, deploymentID string, actor ledger.Actor, comment string) (ledger.Deployment, error) {
	dep, env, ro, err := s.load(ctx, repo, deploymentID)
	if err != nil {
		return dep, err
	}
	if dep.Status != StateAwaitingApproval {
		return dep, &ConflictError{Msg: fmt.Sprintf("deployment %s is %s, not awaiting approval", dep.ID, dep.Status)}
	}
	if !CanApprove(env, actor) {
		s.emit(ctx, repo, "io.ore.deployment.approval.refused", dep.ID, actor, map[string]any{"approver": actor.ID, "reason": "not an approver for " + env.Name})
		return dep, fmt.Errorf("%w: %s/%s is not an approver for %s", ErrForbidden, actor.Kind, actor.ID, env.Name)
	}
	decision, err := s.recordApproval(ctx, repo, env, &dep, ro, actor, "reject", comment)
	if err != nil {
		return dep, err
	}
	if err := s.transition(ctx, repo, env, &dep, StateRejected, &decision, map[string]any{"rejected_by": actor.ID, "comment": comment}); err != nil {
		return dep, err
	}
	return dep, nil
}

// recordApproval inserts the decision, re-evaluates the policy with every
// approval so far, stores the decision and emits io.ore.deployment.approved
// (rejections emit their own transition event).
func (s *Service) recordApproval(ctx context.Context, repo ledger.Repo, env Environment, dep *ledger.Deployment, ro Rollout, actor ledger.Actor, verdict, comment string) (Decision, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Decision{}, err
	}
	defer tx.Rollback()
	now := s.stamp()
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployment_approvals(id, org_id, deployment_id, approver, decision, comment, at) VALUES(?,?,?,?,?,?,?)`,
		ledger.NewID("apr"), repo.OrgID, dep.ID, actor.ID, verdict, comment, now); err != nil {
		return Decision{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT approver, decision FROM deployment_approvals WHERE deployment_id = ? ORDER BY at, id`, dep.ID)
	if err != nil {
		return Decision{}, err
	}
	var approvals []Approval
	for rows.Next() {
		var a Approval
		if err := rows.Scan(&a.Approver, &a.Decision); err != nil {
			rows.Close()
			return Decision{}, err
		}
		approvals = append(approvals, a)
	}
	rows.Close()
	preq := PolicyRequest{Ref: ro.SourceRef, CommitOID: dep.CommitOID, Actor: dep.Actor, Approvals: approvals, RequestedAt: parseTime(dep.CreatedAt), Now: s.now()}
	if env.Policy.RequireGreenPipeline {
		var n int
		_ = tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM pipeline_runs WHERE repo_id = ? AND commit_oid = ? AND status = 'succeeded'`, repo.ID, dep.CommitOID).Scan(&n)
		g := n > 0
		preq.PipelineGreen = &g
	}
	decision := Evaluate(env, preq)
	dj, _ := json.Marshal(decision)
	if _, err := tx.ExecContext(ctx, `UPDATE deployment_rollouts SET decision = ?, updated_at = ? WHERE deployment_id = ?`, string(dj), now, dep.ID); err != nil {
		return Decision{}, err
	}
	if verdict == "approve" {
		if _, err := tx.Emit(repo.OrgID, "io.ore.deployment.approved", depSubject(repo, dep.ID), actor, eventData(dep, env, &decision, map[string]any{"approver": actor.ID, "comment": comment})); err != nil {
			return Decision{}, err
		}
	}
	return decision, tx.Commit()
}

func (s *Service) emit(ctx context.Context, repo ledger.Repo, typ, depID string, actor ledger.Actor, data any) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback()
	if _, err := tx.Emit(repo.OrgID, typ, depSubject(repo, depID), actor, data); err == nil {
		_ = tx.Commit()
	}
}

// load fetches a deployment of the repository with its environment and rollout record.
func (s *Service) load(ctx context.Context, repo ledger.Repo, deploymentID string) (ledger.Deployment, Environment, Rollout, error) {
	dep, err := s.DB.Deployment(ctx, deploymentID)
	if err != nil {
		return dep, Environment{}, Rollout{}, err
	}
	if dep.RepoID != repo.ID {
		return dep, Environment{}, Rollout{}, ledger.ErrNotFound
	}
	ro, _ := s.Rollout(ctx, dep.ID)
	env := s.envFor(ctx, repo, dep, ro)
	return dep, env, ro, nil
}

// envFor resolves the environment a deployment belongs to: by id from the
// rollout record, by name, else the implicit environment of that name.
func (s *Service) envFor(ctx context.Context, repo ledger.Repo, dep ledger.Deployment, ro Rollout) Environment {
	if ro.EnvironmentID != "" {
		if env, err := s.EnvironmentByID(ctx, ro.EnvironmentID); err == nil {
			return env
		}
	}
	if env, err := s.Environment(ctx, repo, dep.Environment); err == nil {
		return env
	}
	return s.ImplicitEnvironment(repo, dep.Environment)
}

// scheduleWait starts the deployment when its wait timer elapses, if approvals
// are already satisfied. Tick re-checks waiting deployments as well, so a
// restart does not lose them.
func (s *Service) scheduleWait(repo ledger.Repo, depID string, d Decision) {
	if d.WaitUntil == "" || d.Approvals < d.RequiredApprovals {
		return
	}
	until := parseTime(d.WaitUntil)
	wait := until.Sub(s.now())
	if wait < 0 {
		wait = 0
	}
	time.AfterFunc(wait+50*time.Millisecond, func() {
		if _, err := s.TryStart(context.Background(), repo, depID); err != nil && !errors.As(err, new(*ConflictError)) {
			s.log().Warn("delivery: wait timer", "deployment", depID, "err", err)
		}
	})
}

// TryStart starts a deployment awaiting approval if its policy is now
// satisfied (approvals in, wait timer elapsed); otherwise it returns the
// deployment unchanged.
func (s *Service) TryStart(ctx context.Context, repo ledger.Repo, deploymentID string) (ledger.Deployment, error) {
	dep, env, ro, err := s.load(ctx, repo, deploymentID)
	if err != nil {
		return dep, err
	}
	if dep.Status != StateAwaitingApproval {
		return dep, nil
	}
	approvals, _ := s.Approvals(ctx, dep.ID)
	preq := PolicyRequest{Ref: ro.SourceRef, CommitOID: dep.CommitOID, Actor: dep.Actor, Approvals: approvals, RequestedAt: parseTime(dep.CreatedAt), Now: s.now()}
	if env.Policy.RequireGreenPipeline {
		g := s.pipelineGreen(ctx, repo, dep.CommitOID)
		preq.PipelineGreen = &g
	}
	decision := Evaluate(env, preq)
	if decision.Outcome != OutcomeStart {
		return dep, nil
	}
	lk := s.lock(repo.ID, env.Name)
	if !lk.TryLock() {
		return dep, &ConflictError{Msg: "another deployment into " + env.Name + " is in progress"}
	}
	defer lk.Unlock()
	return s.run(ctx, repo, env, &dep, decision)
}

// Rollback redeploys the release before the one in effect (the latest
// deployment that succeeded or failed) and marks that one rolled_back.
// Approvals and the wait timer are waived; the other rules still apply.
func (s *Service) Rollback(ctx context.Context, repo ledger.Repo, env Environment, actor ledger.Actor) (ledger.Deployment, error) {
	lk := s.lock(repo.ID, env.Name)
	if !lk.TryLock() {
		return ledger.Deployment{}, &ConflictError{Msg: "another deployment into " + env.Name + " is in progress"}
	}
	defer lk.Unlock()
	cur, err := s.effective(ctx, repo, env.Name)
	if err != nil {
		return ledger.Deployment{}, &ConflictError{Msg: "nothing deployed in " + env.Name}
	}
	if cur.PreviousID == nil {
		return ledger.Deployment{}, &ConflictError{Msg: "no previous deployment in " + env.Name + " to roll back to"}
	}
	prev, err := s.DB.Deployment(ctx, *cur.PreviousID)
	if err != nil {
		return ledger.Deployment{}, err
	}
	var failedAt time.Time
	if cur.Status == StateFailed && cur.FinishedAt != nil {
		failedAt = parseTime(*cur.FinishedAt)
	}
	return s.rollbackTo(ctx, repo, env, prev, &cur, actor, "manual rollback", failedAt)
}

// effective is the deployment in effect in an environment: the newest that
// reached succeeded or failed.
func (s *Service) effective(ctx context.Context, repo ledger.Repo, env string) (ledger.Deployment, error) {
	var id string
	err := s.DB.QueryRow(ctx, `SELECT id FROM deployments WHERE repo_id = ? AND environment = ? AND status IN ('succeeded','failed') ORDER BY created_at DESC LIMIT 1`, repo.ID, env).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ledger.Deployment{}, ledger.ErrNotFound
	}
	if err != nil {
		return ledger.Deployment{}, err
	}
	return s.DB.Deployment(ctx, id)
}

// Stop removes the environment's container and records io.ore.deployment.stopped with a reason.
func (s *Service) Stop(ctx context.Context, repo ledger.Repo, env Environment, actor ledger.Actor, reason string) error {
	cur, err := s.DB.CurrentDeployment(ctx, repo, env.Name)
	if err != nil {
		return &ConflictError{Msg: "nothing deployed in " + env.Name}
	}
	return s.stopDeployment(ctx, repo, env, cur, actor, reason)
}

func (s *Service) stopDeployment(ctx context.Context, repo ledger.Repo, env Environment, dep ledger.Deployment, actor ledger.Actor, reason string) error {
	if env.Target.Kind != TargetNone && dep.ContainerID != "" && dep.ContainerID != "none" {
		if eng, err := s.engine(ctx, repo.OrgID, env.Target); err == nil {
			_ = eng.rm(ctx, dep.ContainerID)
			eng.close()
		} else {
			s.log().Warn("delivery: stop", "deployment", dep.ID, "err", err)
		}
	}
	dep.Actor = actor
	if err := s.transition(ctx, repo, env, &dep, StateStopped, nil, map[string]any{"reason": reason}); err != nil {
		return err
	}
	_, _ = s.DB.Exec(ctx, `DELETE FROM preview_ttls WHERE deployment_id = ?`, dep.ID)
	return nil
}

// Extend moves a preview's expiry to now+d, clamped to MaxPreviewTTL from now.
func (s *Service) Extend(ctx context.Context, repo ledger.Repo, deploymentID string, d time.Duration, actor ledger.Actor) (TTL, bool, error) {
	if d <= 0 {
		return TTL{}, false, fmt.Errorf("%w: extension must be positive", ErrInvalid)
	}
	dep, env, _, err := s.load(ctx, repo, deploymentID)
	if err != nil {
		return TTL{}, false, err
	}
	if dep.Status != StateSucceeded {
		return TTL{}, false, &ConflictError{Msg: fmt.Sprintf("deployment %s is %s; only running previews can be extended", dep.ID, dep.Status)}
	}
	if !env.Expires() {
		return TTL{}, false, &ConflictError{Msg: env.Name + " is a " + env.Kind + " environment; only previews expire"}
	}
	now := s.now()
	clamped := false
	exp := now.Add(d)
	if max := now.Add(MaxPreviewTTL); exp.After(max) {
		exp, clamped = max, true
	}
	t := TTL{DeploymentID: dep.ID, ExpiresAt: exp.UTC().Format("2006-01-02T15:04:05.000Z07:00")}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return t, clamped, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO preview_ttls(deployment_id, org_id, repo_id, expires_at, extensions) VALUES(?,?,?,?,1)
		ON CONFLICT(deployment_id) DO UPDATE SET expires_at = excluded.expires_at, extensions = preview_ttls.extensions + 1`, dep.ID, repo.OrgID, repo.ID, t.ExpiresAt); err != nil {
		return t, clamped, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT extensions FROM preview_ttls WHERE deployment_id = ?`, dep.ID).Scan(&t.Extensions); err != nil {
		return t, clamped, err
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.deployment.extended", depSubject(repo, dep.ID), actor, map[string]any{"deployment_id": dep.ID, "expires_at": t.ExpiresAt, "requested_seconds": int(d / time.Second), "clamped": clamped, "max_seconds": int(MaxPreviewTTL / time.Second)}); err != nil {
		return t, clamped, err
	}
	return t, clamped, tx.Commit()
}

func (s *Service) setTTL(ctx context.Context, repo ledger.Repo, depID string, expires time.Time) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO preview_ttls(deployment_id, org_id, repo_id, expires_at, extensions) VALUES(?,?,?,?,0)
		ON CONFLICT(deployment_id) DO UPDATE SET expires_at = excluded.expires_at`, depID, repo.OrgID, repo.ID, expires.UTC().Format("2006-01-02T15:04:05.000Z07:00"))
	return err
}

// RefFor names the ref a commit was deployed from: the default branch if it
// points at the commit, else another branch, else a tag; "" when none does.
func (s *Service) RefFor(ctx context.Context, repo ledger.Repo, commitOID string) string {
	rp := s.repoPath(repo)
	out, err := gitx.Run(ctx, rp, nil, "for-each-ref", "--points-at", commitOID, "--format=%(refname)")
	if err != nil {
		return ""
	}
	var heads, tags, other []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		switch {
		case l == "":
		case l == "refs/heads/"+repo.DefaultBranch:
			return l
		case strings.HasPrefix(l, "refs/heads/"):
			heads = append(heads, l)
		case strings.HasPrefix(l, "refs/tags/"):
			tags = append(tags, l)
		default:
			other = append(other, l)
		}
	}
	for _, c := range [][]string{heads, tags, other} {
		if len(c) > 0 {
			return c[0]
		}
	}
	return ""
}

func (s *Service) repoPath(repo ledger.Repo) string {
	dir := s.ReposDir
	if dir == "" && s.Deployer != nil {
		dir = s.Deployer.ReposDir
	}
	return dir + "/" + repo.Org + "/" + repo.Name + ".git"
}

// ---- hooks used by Deployer.Deploy / Rollback / Stop ----

// hookDeploy routes a Deployer.Deploy call through policy when the
// environment has a record; handled is false for implicit environments.
func (s *Service) hookDeploy(ctx context.Context, repo ledger.Repo, commitOID, envName string, actor ledger.Actor, rollbackOf *string) (ledger.Deployment, bool, error) {
	env, err := s.Environment(ctx, repo, envName)
	if errors.Is(err, ledger.ErrNotFound) {
		return ledger.Deployment{}, false, nil
	}
	if err != nil {
		return ledger.Deployment{}, true, err
	}
	dep, err := s.Request(ctx, repo, env, DeployRequest{Ref: s.RefFor(ctx, repo, commitOID), CommitOID: commitOID, RollbackOf: rollbackOf}, actor)
	return dep, true, err
}

func (s *Service) hookRollback(ctx context.Context, repo ledger.Repo, envName string, actor ledger.Actor) (ledger.Deployment, bool, error) {
	env, err := s.Environment(ctx, repo, envName)
	if errors.Is(err, ledger.ErrNotFound) {
		return ledger.Deployment{}, false, nil
	}
	if err != nil {
		return ledger.Deployment{}, true, err
	}
	dep, err := s.Rollback(ctx, repo, env, actor)
	return dep, true, err
}

func (s *Service) hookStop(ctx context.Context, repo ledger.Repo, envName string, actor ledger.Actor) (bool, error) {
	env, err := s.Environment(ctx, repo, envName)
	if errors.Is(err, ledger.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	return true, s.Stop(ctx, repo, env, actor, "manual")
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
