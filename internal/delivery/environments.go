// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

// Environment kinds (docs/04 §B.2).
const (
	KindProduction = "production"
	KindStaging    = "staging"
	KindPreview    = "preview"
	KindEphemeral  = "ephemeral"
)

// Target kinds.
const (
	TargetLocalDocker = "local-docker"
	TargetSSHDocker   = "ssh-docker"
	TargetNone        = "none"
)

// Target is where an environment's containers run.
type Target struct {
	Kind string `json:"kind"` // local-docker | ssh-docker | none
	// ssh-docker: the remote Docker engine reached as ssh://user@host:port.
	Host string `json:"host,omitempty"`
	User string `json:"user,omitempty"`
	Port int    `json:"port,omitempty"`
	// KeyRef names the SSH private key: "file:/path/to/key", "secret:NAME"
	// (resolved through the secrets store registered as Services["secrets"])
	// or "env:VAR". Empty uses the server's default ssh configuration.
	KeyRef string `json:"key_ref,omitempty"`
}

// Policy is the deploy-time protection of an environment (docs/04 §B.7). It
// is a small built-in rule set for now; ADR 0011 moves it to Cedar later.
type Policy struct {
	// AllowedRefs are glob patterns over full ref names (refs/heads/main,
	// refs/tags/v*). Empty allows any ref.
	AllowedRefs []string `json:"allowed_refs"`
	// RequiredApprovals is N; 0 starts deployments immediately.
	RequiredApprovals int `json:"required_approvals"`
	// Approvers are actor ids that may approve or reject. Empty means any
	// non-agent actor.
	Approvers []string `json:"approvers"`
	// WaitSeconds is a timer between the request and the start.
	WaitSeconds int `json:"wait_seconds"`
	// RequireGreenPipeline refuses commits without a succeeded pipeline run.
	RequireGreenPipeline bool `json:"require_green_pipeline"`
	// AllowAgentDeploys lets actors of kind "agent" request deployments.
	AllowAgentDeploys bool `json:"allow_agent_deploys"`
	// HealthPath is probed after the container starts ("/" by default).
	HealthPath string `json:"health_path"`
	// HealthTimeoutSeconds bounds the probe (60 by default).
	HealthTimeoutSeconds int `json:"health_timeout_seconds"`
	// AutoRollback redeploys the previous succeeded deployment on health failure.
	AutoRollback bool `json:"auto_rollback"`
	// PreviewTTLSeconds is the lifetime of preview/ephemeral deployments (2 h by default).
	PreviewTTLSeconds int `json:"preview_ttl_seconds,omitempty"`
}

// Environment is a first-class deployment target with policy.
type Environment struct {
	ID        string `json:"id"`
	OrgID     string `json:"org_id"`
	RepoID    string `json:"repo_id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Target    Target `json:"target"`
	Policy    Policy `json:"policy"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	// Implicit is set for environments synthesized from a name alone
	// (repositories without environment records keep the v0 behaviour).
	Implicit bool `json:"implicit,omitempty"`
}

// Approval is one approve/reject decision on a deployment.
type Approval struct {
	ID           string `json:"id"`
	DeploymentID string `json:"deployment_id"`
	Approver     string `json:"approver"`
	Decision     string `json:"decision"` // approve | reject
	Comment      string `json:"comment,omitempty"`
	At           string `json:"at"`
}

// TTL is the expiry of a preview deployment.
type TTL struct {
	DeploymentID string `json:"deployment_id"`
	ExpiresAt    string `json:"expires_at"`
	Extensions   int    `json:"extensions"`
}

// Rollout is the per-deployment record of the policy decision, the health
// result and the recovery time; it extends ledger.Deployment one-to-one.
type Rollout struct {
	DeploymentID    string   `json:"deployment_id"`
	EnvironmentID   string   `json:"environment_id"`
	SourceRef       string   `json:"source_ref"`
	Decision        Decision `json:"decision"`
	Health          *Health  `json:"health,omitempty"`
	Failure         string   `json:"failure,omitempty"`
	RecoverySeconds *float64 `json:"recovery_seconds,omitempty"`
}

// DefaultPreviewTTL is the lifetime of a preview without a policy override.
const DefaultPreviewTTL = 2 * time.Hour

// MaxPreviewTTL bounds extensions: an extension never pushes the expiry
// further than this from the moment of the request.
const MaxPreviewTTL = 24 * time.Hour

// ValidKind reports whether k is an environment kind.
func ValidKind(k string) bool {
	switch k {
	case KindProduction, KindStaging, KindPreview, KindEphemeral:
		return true
	}
	return false
}

// KindForName infers the kind of an implicit environment from its name.
func KindForName(name string) string {
	switch name {
	case "production", "prod":
		return KindProduction
	case "staging":
		return KindStaging
	default:
		return KindPreview
	}
}

// DefaultPolicy returns the policy a new environment of that kind starts with.
// Production forbids agent deploys and rolls back automatically; previews are
// open and expire.
func DefaultPolicy(kind string) Policy {
	p := Policy{AllowedRefs: []string{}, Approvers: []string{}, HealthPath: "/", HealthTimeoutSeconds: 60, AllowAgentDeploys: true}
	switch kind {
	case KindProduction:
		p.AllowAgentDeploys = false
		p.AutoRollback = true
	case KindStaging:
		p.AutoRollback = true
	default:
		p.PreviewTTLSeconds = int(DefaultPreviewTTL / time.Second)
	}
	return p
}

// Normalize fills defaults and validates.
func (p *Policy) Normalize() error {
	if p.AllowedRefs == nil {
		p.AllowedRefs = []string{}
	}
	if p.Approvers == nil {
		p.Approvers = []string{}
	}
	if p.HealthPath == "" {
		p.HealthPath = "/"
	}
	if !strings.HasPrefix(p.HealthPath, "/") {
		return fmt.Errorf("health_path must start with /")
	}
	if p.HealthTimeoutSeconds <= 0 {
		p.HealthTimeoutSeconds = 60
	}
	if p.RequiredApprovals < 0 || p.WaitSeconds < 0 || p.PreviewTTLSeconds < 0 {
		return fmt.Errorf("required_approvals, wait_seconds and preview_ttl_seconds must be >= 0")
	}
	return nil
}

// Validate checks a target.
func (t *Target) Validate() error {
	switch t.Kind {
	case "":
		t.Kind = TargetLocalDocker
	case TargetLocalDocker, TargetNone:
	case TargetSSHDocker:
		if t.Host == "" {
			return fmt.Errorf("ssh-docker target needs a host")
		}
		if t.User == "" {
			t.User = "root"
		}
		if t.Port == 0 {
			t.Port = 22
		}
	default:
		return fmt.Errorf("unknown target kind %q (local-docker | ssh-docker | none)", t.Kind)
	}
	return nil
}

// PreviewTTL is the configured lifetime for previews in this environment.
func (e Environment) PreviewTTL() time.Duration {
	if e.Policy.PreviewTTLSeconds > 0 {
		return time.Duration(e.Policy.PreviewTTLSeconds) * time.Second
	}
	return DefaultPreviewTTL
}

// Expires reports whether deployments into this environment get a TTL.
func (e Environment) Expires() bool { return e.Kind == KindPreview || e.Kind == KindEphemeral }

// ---- store ----

func (e Environment) subject(r ledger.Repo) string {
	return r.Org + "/" + r.Name + "/environments/" + e.Name
}

// CreateEnvironment records an environment and emits io.ore.environment.created.
func (s *Service) CreateEnvironment(ctx context.Context, r ledger.Repo, env *Environment, actor ledger.Actor) error {
	if !ValidKind(env.Kind) {
		return fmt.Errorf("%w: kind must be production|staging|preview|ephemeral", ErrInvalid)
	}
	if err := env.Target.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := env.Policy.Normalize(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	target, _ := json.Marshal(env.Target)
	policy, _ := json.Marshal(env.Policy)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := ledger.Now()
	env.ID, env.OrgID, env.RepoID, env.CreatedAt, env.UpdatedAt = ledger.NewID("env"), r.OrgID, r.ID, now, now
	if _, err := tx.ExecContext(ctx, `INSERT INTO environments(id, org_id, repo_id, name, kind, target, policy, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		env.ID, env.OrgID, env.RepoID, env.Name, env.Kind, string(target), string(policy), now, now); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return &ConflictError{Msg: "environment " + env.Name + " already exists"}
		}
		return err
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.environment.created", env.subject(r), actor, env); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateEnvironment replaces kind, target and policy and emits io.ore.environment.updated.
func (s *Service) UpdateEnvironment(ctx context.Context, r ledger.Repo, env *Environment, actor ledger.Actor) error {
	if !ValidKind(env.Kind) {
		return fmt.Errorf("%w: kind must be production|staging|preview|ephemeral", ErrInvalid)
	}
	if err := env.Target.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := env.Policy.Normalize(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	target, _ := json.Marshal(env.Target)
	policy, _ := json.Marshal(env.Policy)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	env.UpdatedAt = ledger.Now()
	if _, err := tx.ExecContext(ctx, `UPDATE environments SET kind = ?, target = ?, policy = ?, updated_at = ? WHERE id = ?`, env.Kind, string(target), string(policy), env.UpdatedAt, env.ID); err != nil {
		return err
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.environment.updated", env.subject(r), actor, env); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteEnvironment removes the record (deployments stay on the ledger) and emits io.ore.environment.deleted.
func (s *Service) DeleteEnvironment(ctx context.Context, r ledger.Repo, env Environment, actor ledger.Actor) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM environments WHERE id = ?`, env.ID); err != nil {
		return err
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.environment.deleted", env.subject(r), actor, map[string]string{"id": env.ID, "name": env.Name}); err != nil {
		return err
	}
	return tx.Commit()
}

const envCols = `id, org_id, repo_id, name, kind, target, policy, created_at, updated_at`

func scanEnv(s interface{ Scan(...any) error }) (Environment, error) {
	var e Environment
	var target, policy string
	err := s.Scan(&e.ID, &e.OrgID, &e.RepoID, &e.Name, &e.Kind, &target, &policy, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ledger.ErrNotFound
	}
	if err != nil {
		return e, err
	}
	if err := json.Unmarshal([]byte(target), &e.Target); err != nil {
		return e, fmt.Errorf("environment %s: bad target: %w", e.Name, err)
	}
	if err := json.Unmarshal([]byte(policy), &e.Policy); err != nil {
		return e, fmt.Errorf("environment %s: bad policy: %w", e.Name, err)
	}
	_ = e.Policy.Normalize()
	return e, nil
}

// Environment fetches a repository's environment by name.
func (s *Service) Environment(ctx context.Context, r ledger.Repo, name string) (Environment, error) {
	return scanEnv(s.DB.QueryRow(ctx, `SELECT `+envCols+` FROM environments WHERE repo_id = ? AND name = ?`, r.ID, name))
}

// EnvironmentByID fetches by id.
func (s *Service) EnvironmentByID(ctx context.Context, id string) (Environment, error) {
	return scanEnv(s.DB.QueryRow(ctx, `SELECT `+envCols+` FROM environments WHERE id = ?`, id))
}

// Environments lists a repository's environments.
func (s *Service) Environments(ctx context.Context, r ledger.Repo) ([]Environment, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+envCols+` FROM environments WHERE repo_id = ? ORDER BY name`, r.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Environment{}
	for rows.Next() {
		e, err := scanEnv(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ImplicitEnvironment synthesizes the environment a name denotes when no
// record exists: local-docker (or none, following the Deployer's executor),
// permissive policy, previews expire.
func (s *Service) ImplicitEnvironment(r ledger.Repo, name string) Environment {
	kind := KindForName(name)
	pol := DefaultPolicy(kind)
	pol.AllowAgentDeploys = true
	pol.AutoRollback = false
	pol.HealthTimeoutSeconds = 30
	target := Target{Kind: TargetLocalDocker}
	if s.Deployer != nil && s.Deployer.Executor == "none" {
		target.Kind = TargetNone
	}
	return Environment{ID: "", OrgID: r.OrgID, RepoID: r.ID, Name: name, Kind: kind, Target: target, Policy: pol, Implicit: true}
}

// Approvals lists the decisions recorded on a deployment, oldest first.
func (s *Service) Approvals(ctx context.Context, deploymentID string) ([]Approval, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, deployment_id, approver, decision, comment, at FROM deployment_approvals WHERE deployment_id = ? ORDER BY at, id`, deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Approval{}
	for rows.Next() {
		var a Approval
		if err := rows.Scan(&a.ID, &a.DeploymentID, &a.Approver, &a.Decision, &a.Comment, &a.At); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// TTL returns the expiry of a preview deployment, or ledger.ErrNotFound.
func (s *Service) TTL(ctx context.Context, deploymentID string) (TTL, error) {
	var t TTL
	err := s.DB.QueryRow(ctx, `SELECT deployment_id, expires_at, extensions FROM preview_ttls WHERE deployment_id = ?`, deploymentID).Scan(&t.DeploymentID, &t.ExpiresAt, &t.Extensions)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ledger.ErrNotFound
	}
	return t, err
}

// Rollout returns the policy/health record of a deployment, or ledger.ErrNotFound.
func (s *Service) Rollout(ctx context.Context, deploymentID string) (Rollout, error) {
	var ro Rollout
	var decision, health string
	err := s.DB.QueryRow(ctx, `SELECT deployment_id, environment_id, source_ref, decision, health, failure, recovery_seconds FROM deployment_rollouts WHERE deployment_id = ?`, deploymentID).
		Scan(&ro.DeploymentID, &ro.EnvironmentID, &ro.SourceRef, &decision, &health, &ro.Failure, &ro.RecoverySeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return ro, ledger.ErrNotFound
	}
	if err != nil {
		return ro, err
	}
	_ = json.Unmarshal([]byte(decision), &ro.Decision)
	if health != "" && health != "{}" {
		var h Health
		if json.Unmarshal([]byte(health), &h) == nil {
			ro.Health = &h
		}
	}
	return ro, nil
}

// DeploymentView is a deployment with its rollout record, approvals and TTL.
type DeploymentView struct {
	ledger.Deployment
	Rollout   *Rollout   `json:"rollout,omitempty"`
	Approvals []Approval `json:"approvals,omitempty"`
	TTL       *TTL       `json:"ttl,omitempty"`
}

// View assembles the full picture of a deployment.
func (s *Service) View(ctx context.Context, deploymentID string) (DeploymentView, error) {
	dep, err := s.DB.Deployment(ctx, deploymentID)
	if err != nil {
		return DeploymentView{}, err
	}
	v := DeploymentView{Deployment: dep}
	if ro, err := s.Rollout(ctx, dep.ID); err == nil {
		v.Rollout = &ro
	}
	v.Approvals, _ = s.Approvals(ctx, dep.ID)
	if t, err := s.TTL(ctx, dep.ID); err == nil {
		v.TTL = &t
	}
	return v, nil
}
