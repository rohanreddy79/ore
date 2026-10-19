// SPDX-License-Identifier: AGPL-3.0-only

// Package delivery deploys a commit into an environment. v0 builds the
// repository's Dockerfile and runs it as a container per (repository,
// environment); each deployment and rollback is a ledger entity so the path
// after merge is recorded whether a human or an agent triggered it.
package delivery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// Deployer runs deployments.
type Deployer struct {
	DB       *ledger.DB
	ReposDir string
	// Executor is "docker" (default) or "none" (records deployments without running anything; tests).
	Executor string
	// PublicHost is the host used in deployment URLs (default localhost).
	PublicHost string
	// Environments, when set, routes Deploy, Rollback and Stop through the
	// environment's policy (approvals, allowed refs, remote targets,
	// health-gated rollouts) whenever the repository has a record for the
	// environment. Repositories without records keep the implicit local-docker
	// path below, so `ore demo` is unchanged.
	Environments *Service
}

func docker(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// Deploy places commitOID into env. previous (if any) is stopped after the new container is up.
func (d *Deployer) Deploy(ctx context.Context, repo ledger.Repo, commitOID, env string, actor ledger.Actor, rollbackOf *string) (ledger.Deployment, error) {
	if d.Environments != nil {
		if dep, handled, err := d.Environments.hookDeploy(ctx, repo, commitOID, env, actor, rollbackOf); handled {
			return dep, err
		}
	}
	dep := ledger.Deployment{Environment: env, CommitOID: commitOID, Actor: actor, RollbackOf: rollbackOf}
	if prev, err := d.DB.CurrentDeployment(ctx, repo, env); err == nil {
		dep.PreviousID = &prev.ID
	}
	if err := d.DB.InsertDeployment(ctx, repo, &dep); err != nil {
		return dep, err
	}
	if err := d.DB.UpdateDeployment(ctx, repo, &dep, "started"); err != nil {
		return dep, err
	}
	if err := d.execute(ctx, repo, &dep); err != nil {
		dep.URL = ""
		_ = d.DB.UpdateDeployment(ctx, repo, &dep, "failed")
		return dep, err
	}
	if err := d.DB.UpdateDeployment(ctx, repo, &dep, "succeeded"); err != nil {
		return dep, err
	}
	// Stop the previous container for this environment, now that the new one answers.
	if dep.PreviousID != nil && d.Executor != "none" {
		if prev, err := d.DB.Deployment(ctx, *dep.PreviousID); err == nil && prev.ContainerID != "" && prev.ContainerID != dep.ContainerID {
			_, _ = docker(ctx, "rm", "-f", prev.ContainerID)
		}
	}
	return dep, nil
}

func (d *Deployer) execute(ctx context.Context, repo ledger.Repo, dep *ledger.Deployment) error {
	short := dep.CommitOID
	if len(short) > 12 {
		short = short[:12]
	}
	dep.Image = fmt.Sprintf("ore/%s-%s:%s", repo.Org, repo.Name, short)
	if d.Executor == "none" {
		dep.ContainerID = "none"
		dep.URL = fmt.Sprintf("none://%s/%s/%s", repo.Org, repo.Name, dep.Environment)
		return nil
	}
	work, err := os.MkdirTemp("", "ore-deploy-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	repoPath := filepath.Join(d.ReposDir, repo.Org, repo.Name+".git")
	if err := gitx.Archive(ctx, repoPath, dep.CommitOID, work); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(work, "Dockerfile")); err != nil {
		return errors.New("no Dockerfile at the repository root; v0 deploys container images only")
	}
	if _, err := docker(ctx, "build", "-q", "-t", dep.Image, work); err != nil {
		return err
	}
	name := fmt.Sprintf("ore-%s-%s-%s", repo.Org, repo.Name, dep.Environment)
	// Replace any container holding the environment's name (crash-safe rerun).
	_, _ = docker(ctx, "rm", "-f", name)
	id, err := docker(ctx, "run", "-d", "--name", name,
		"--label", "ore.repo="+repo.ID, "--label", "ore.env="+dep.Environment, "--label", "ore.deployment="+dep.ID,
		"-p", fmt.Sprintf("0:%d", repo.DeployPort), dep.Image)
	if err != nil {
		return err
	}
	dep.ContainerID = id
	portOut, err := docker(ctx, "port", id, fmt.Sprintf("%d/tcp", repo.DeployPort))
	if err != nil {
		return err
	}
	// docker port prints e.g. "0.0.0.0:55000" (possibly two lines for v4/v6)
	hostPort := portOut
	if i := strings.LastIndex(strings.Split(portOut, "\n")[0], ":"); i >= 0 {
		hostPort = strings.Split(portOut, "\n")[0][i+1:]
	}
	host := d.PublicHost
	if host == "" {
		host = "localhost"
	}
	dep.URL = fmt.Sprintf("http://%s:%s", host, hostPort)
	return waitHealthy(ctx, dep.URL, 30*time.Second)
}

func waitHealthy(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	var last error
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
			last = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			last = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("deployment did not become healthy at %s: %v", url, last)
}

// Rollback redeploys the deployment before the current one in env.
func (d *Deployer) Rollback(ctx context.Context, repo ledger.Repo, env string, actor ledger.Actor) (ledger.Deployment, error) {
	if d.Environments != nil {
		if dep, handled, err := d.Environments.hookRollback(ctx, repo, env, actor); handled {
			return dep, err
		}
	}
	cur, err := d.DB.CurrentDeployment(ctx, repo, env)
	if err != nil {
		return ledger.Deployment{}, fmt.Errorf("nothing deployed in %s", env)
	}
	if cur.PreviousID == nil {
		return ledger.Deployment{}, fmt.Errorf("no previous deployment in %s to roll back to", env)
	}
	prev, err := d.DB.Deployment(ctx, *cur.PreviousID)
	if err != nil {
		return ledger.Deployment{}, err
	}
	dep, err := d.Deploy(ctx, repo, prev.CommitOID, env, actor, &cur.ID)
	if err != nil {
		return dep, err
	}
	_ = d.DB.UpdateDeployment(ctx, repo, &dep, "rolled_back")
	return dep, nil
}

// Stop removes the container for an environment and records it.
func (d *Deployer) Stop(ctx context.Context, repo ledger.Repo, env string, actor ledger.Actor) error {
	if d.Environments != nil {
		if handled, err := d.Environments.hookStop(ctx, repo, env, actor); handled {
			return err
		}
	}
	cur, err := d.DB.CurrentDeployment(ctx, repo, env)
	if err != nil {
		return fmt.Errorf("nothing deployed in %s", env)
	}
	if d.Executor != "none" && cur.ContainerID != "" {
		_, _ = docker(ctx, "rm", "-f", cur.ContainerID)
	}
	cur.Actor = actor
	return d.DB.UpdateDeployment(ctx, repo, &cur, "stopped")
}
