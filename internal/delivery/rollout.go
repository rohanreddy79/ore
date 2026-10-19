// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// run executes a deployment whose policy allows it to start. The caller holds
// the environment lock. On a failed health check it rolls back automatically
// when the policy says so and opens an issue for the agent loop.
func (s *Service) run(ctx context.Context, repo ledger.Repo, env Environment, dep *ledger.Deployment, decision Decision) (ledger.Deployment, error) {
	if err := s.transition(ctx, repo, env, dep, StateStarted, &decision, nil); err != nil {
		return *dep, err
	}
	if s.beforeExecute != nil {
		s.beforeExecute(dep)
	}
	health, logs, err := s.execute(ctx, repo, env, dep)
	if err == nil {
		if err := s.transition(ctx, repo, env, dep, StateSucceeded, &decision, map[string]any{"health": health}); err != nil {
			return *dep, err
		}
		s.afterSuccess(ctx, repo, env, dep)
		return *dep, nil
	}
	failedAt := s.now()
	if terr := s.transition(ctx, repo, env, dep, StateFailed, &decision, map[string]any{"health": health, "failure": err.Error(), "log_excerpt": logs}); terr != nil {
		return *dep, terr
	}
	var rb *ledger.Deployment
	var rbErr error
	if env.Policy.AutoRollback && dep.PreviousID != nil {
		if prev, perr := s.DB.Deployment(ctx, *dep.PreviousID); perr == nil {
			d, e := s.rollbackTo(ctx, repo, env, prev, dep, ledger.System, "auto-rollback after failed health check", failedAt)
			rb, rbErr = &d, e
		}
	} else if env.Policy.AutoRollback {
		rbErr = errors.New("auto-rollback: no previous succeeded deployment in " + env.Name)
	}
	s.openFailureIssue(ctx, repo, env, *dep, err, health, logs, rb, rbErr)
	return *dep, &FailedError{Deployment: *dep, Cause: err}
}

// execute builds and starts the container on the environment's target and
// probes the health path. It fills dep.Image, ContainerID and URL.
func (s *Service) execute(ctx context.Context, repo ledger.Repo, env Environment, dep *ledger.Deployment) (Health, string, error) {
	dep.Image = fmt.Sprintf("ore/%s-%s:%s", repo.Org, repo.Name, short(dep.CommitOID))
	timeout := time.Duration(env.Policy.HealthTimeoutSeconds) * time.Second
	if env.Target.Kind == TargetNone {
		dep.ContainerID = "none"
		dep.URL = fmt.Sprintf("none://%s/%s/%s", repo.Org, repo.Name, env.Name)
		return Health{URL: dep.URL + env.Policy.HealthPath, Healthy: true, Skipped: true, TimeoutSeconds: int(timeout / time.Second)}, "", nil
	}
	eng, err := s.engine(ctx, repo.OrgID, env.Target)
	if err != nil {
		return Health{}, "", err
	}
	defer eng.close()
	work, err := os.MkdirTemp("", "ore-deploy-*")
	if err != nil {
		return Health{}, "", err
	}
	defer os.RemoveAll(work)
	if err := gitx.Archive(ctx, s.repoPath(repo), dep.CommitOID, work); err != nil {
		return Health{}, "", err
	}
	if _, err := os.Stat(filepath.Join(work, "Dockerfile")); err != nil {
		return Health{}, "", errors.New("no Dockerfile at the repository root; v0 deploys container images only")
	}
	if err := eng.build(ctx, dep.Image, work); err != nil {
		return Health{}, "", err
	}
	if err := eng.transfer(ctx, dep.Image); err != nil {
		return Health{}, "", err
	}
	name := fmt.Sprintf("ore-%s-%s-%s", repo.Org, repo.Name, env.Name)
	id, err := eng.run(ctx, name, map[string]string{"ore.repo": repo.ID, "ore.env": env.Name, "ore.deployment": dep.ID}, repo.DeployPort, dep.Image)
	if err != nil {
		return Health{}, "", err
	}
	dep.ContainerID = id
	hostPort, err := eng.port(ctx, id, repo.DeployPort)
	if err != nil {
		logs := eng.logs(ctx, id, 60)
		_ = eng.rm(ctx, id)
		return Health{}, logs, err
	}
	dep.URL = fmt.Sprintf("http://%s:%s", eng.host, hostPort)
	h := probe(ctx, dep.URL+env.Policy.HealthPath, timeout, s.probeInterval)
	if !h.Healthy {
		logs := eng.logs(ctx, id, 60)
		_ = eng.rm(ctx, id)
		return h, logs, errors.New(h.String())
	}
	return h, "", nil
}

// afterSuccess removes the container the new one replaced and gives previews a TTL.
func (s *Service) afterSuccess(ctx context.Context, repo ledger.Repo, env Environment, dep *ledger.Deployment) {
	if dep.PreviousID != nil && env.Target.Kind != TargetNone {
		if prev, err := s.DB.Deployment(ctx, *dep.PreviousID); err == nil && prev.ContainerID != "" && prev.ContainerID != "none" && prev.ContainerID != dep.ContainerID {
			if eng, err := s.engine(ctx, repo.OrgID, env.Target); err == nil {
				_ = eng.rm(ctx, prev.ContainerID)
				eng.close()
			}
		}
	}
	if env.Expires() {
		if err := s.setTTL(ctx, repo, dep.ID, s.now().Add(env.PreviewTTL())); err != nil {
			s.log().Warn("delivery: ttl", "deployment", dep.ID, "err", err)
		}
	}
}

// rollbackTo deploys prev's commit as a rollback of replaced and marks replaced
// rolled_back, recording the recovery time when replaced had failed. The caller
// holds the environment lock.
func (s *Service) rollbackTo(ctx context.Context, repo ledger.Repo, env Environment, prev ledger.Deployment, replaced *ledger.Deployment, actor ledger.Actor, reason string, failedAt time.Time) (ledger.Deployment, error) {
	dep := ledger.Deployment{Environment: env.Name, CommitOID: prev.CommitOID, Actor: actor, RollbackOf: &replaced.ID}
	if cur, err := s.DB.CurrentDeployment(ctx, repo, env.Name); err == nil {
		dep.PreviousID = &cur.ID
	}
	ref := ""
	if ro, err := s.Rollout(ctx, prev.ID); err == nil {
		ref = ro.SourceRef
	}
	if ref == "" {
		ref = s.RefFor(ctx, repo, prev.CommitOID)
	}
	now := s.now()
	decision := Evaluate(env, PolicyRequest{Ref: ref, CommitOID: prev.CommitOID, Actor: actor, RequestedAt: now, Now: now, Bypass: reason})
	if env.Policy.RequireGreenPipeline {
		// the previous release already ran; its pipeline state is what it was
		g := s.pipelineGreen(ctx, repo, prev.CommitOID)
		decision = Evaluate(env, PolicyRequest{Ref: ref, CommitOID: prev.CommitOID, Actor: actor, PipelineGreen: &g, RequestedAt: now, Now: now, Bypass: reason})
	}
	if err := s.insert(ctx, repo, env, &dep, ref, decision); err != nil {
		return dep, err
	}
	if decision.Outcome == OutcomeRefuse {
		if err := s.transition(ctx, repo, env, &dep, StateRefused, &decision, map[string]any{"reasons": decision.Reasons}); err != nil {
			return dep, err
		}
		return dep, &RefusedError{DeploymentID: dep.ID, Decision: decision}
	}
	if err := s.transition(ctx, repo, env, &dep, StateStarted, &decision, map[string]any{"reason": reason}); err != nil {
		return dep, err
	}
	health, logs, err := s.execute(ctx, repo, env, &dep)
	if err != nil {
		_ = s.transition(ctx, repo, env, &dep, StateFailed, &decision, map[string]any{"health": health, "failure": err.Error(), "log_excerpt": logs, "reason": reason})
		return dep, &FailedError{Deployment: dep, Cause: err}
	}
	if err := s.transition(ctx, repo, env, &dep, StateSucceeded, &decision, map[string]any{"health": health, "reason": reason}); err != nil {
		return dep, err
	}
	s.afterSuccess(ctx, repo, env, &dep)
	extra := map[string]any{"rollback_deployment": dep.ID, "reason": reason}
	if !failedAt.IsZero() {
		extra["recovery_seconds"] = s.now().Sub(failedAt).Seconds()
	}
	if err := s.transition(ctx, repo, env, replaced, StateRolledBack, nil, extra); err != nil {
		return dep, err
	}
	return dep, nil
}

// openFailureIssue files the signal the agent loop picks up.
func (s *Service) openFailureIssue(ctx context.Context, repo ledger.Repo, env Environment, dep ledger.Deployment, cause error, health Health, logs string, rb *ledger.Deployment, rbErr error) {
	title := fmt.Sprintf("Deployment %s to %s failed health check", dep.ID, env.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "Deployment `%s` of commit `%s` into **%s** (%s, target %s) failed.\n\n", dep.ID, short(dep.CommitOID), env.Name, env.Kind, env.Target.Kind)
	fmt.Fprintf(&b, "- cause: %s\n- health: %s\n", cause.Error(), health.String())
	if health.URL != "" {
		fmt.Fprintf(&b, "- probe: GET %s (timeout %ds)\n", health.URL, health.TimeoutSeconds)
	}
	switch {
	case rb != nil && rbErr == nil:
		fmt.Fprintf(&b, "- auto-rollback: succeeded, deployment `%s` restored commit `%s`\n", rb.ID, short(rb.CommitOID))
	case rbErr != nil:
		fmt.Fprintf(&b, "- auto-rollback: failed: %s\n", rbErr.Error())
	case env.Policy.AutoRollback:
		b.WriteString("- auto-rollback: nothing to roll back to\n")
	default:
		b.WriteString("- auto-rollback: off; the deployment stays failed\n")
	}
	if strings.TrimSpace(logs) != "" {
		b.WriteString("\nContainer log excerpt:\n\n```\n" + strings.TrimSpace(logs) + "\n```\n")
	}
	if _, err := s.DB.CreateIssue(ctx, repo, title, b.String(), ledger.System); err != nil {
		s.log().Warn("delivery: open issue", "deployment", dep.ID, "err", err)
	}
}
