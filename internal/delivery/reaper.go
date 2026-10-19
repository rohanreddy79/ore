// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

// Run ticks until ctx ends: expired previews are stopped and deployments whose
// wait timer elapsed are started.
func (s *Service) Run(ctx context.Context) {
	iv := s.ReapInterval
	if iv <= 0 {
		iv = time.Minute
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.Tick(ctx, s.now()); err != nil {
				s.log().Warn("delivery: tick", "err", err)
			}
		}
	}
}

// Tick runs one reaper pass at the given time and returns how many previews it
// stopped. With nothing expired it writes nothing to the ledger.
func (s *Service) Tick(ctx context.Context, now time.Time) (int, error) {
	n, err := s.ReapOnce(ctx, now)
	if err != nil {
		return n, err
	}
	s.startWaiting(ctx)
	return n, nil
}

// ReapOnce stops previews whose TTL expired (and implicit "preview"
// deployments older than DefaultPreviewTTL without a record), recording
// io.ore.deployment.stopped with reason "ttl".
func (s *Service) ReapOnce(ctx context.Context, now time.Time) (int, error) {
	ts := now.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	implicitCutoff := now.Add(-DefaultPreviewTTL).UTC().Format("2006-01-02T15:04:05.000Z07:00")
	rows, err := s.DB.Query(ctx, `
		SELECT d.id FROM preview_ttls t JOIN deployments d ON d.id = t.deployment_id WHERE t.expires_at <= ? AND d.status = 'succeeded'
		UNION
		SELECT d.id FROM deployments d
		  LEFT JOIN preview_ttls t ON t.deployment_id = d.id
		  LEFT JOIN environments e ON e.repo_id = d.repo_id AND e.name = d.environment
		 WHERE d.status = 'succeeded' AND t.deployment_id IS NULL AND e.id IS NULL AND d.environment = 'preview' AND d.created_at <= ?`, ts, implicitCutoff)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	stopped := 0
	for _, id := range ids {
		dep, err := s.DB.Deployment(ctx, id)
		if err != nil {
			continue
		}
		repo, err := s.DB.RepoByID(ctx, dep.RepoID)
		if err != nil {
			continue
		}
		ro, _ := s.Rollout(ctx, dep.ID)
		env := s.envFor(ctx, repo, dep, ro)
		lk := s.lock(repo.ID, env.Name)
		if !lk.TryLock() {
			continue // a deployment is in progress; next tick
		}
		err = s.stopDeployment(ctx, repo, env, dep, ledger.System, "ttl")
		lk.Unlock()
		if err != nil {
			s.log().Warn("delivery: reap", "deployment", dep.ID, "err", err)
			continue
		}
		stopped++
	}
	return stopped, nil
}

// startWaiting starts deployments awaiting approval whose policy is now satisfied.
func (s *Service) startWaiting(ctx context.Context) {
	rows, err := s.DB.Query(ctx, `SELECT id, repo_id FROM deployments WHERE status = ? ORDER BY created_at`, StateAwaitingApproval)
	if err != nil {
		return
	}
	type pair struct{ id, repo string }
	var waiting []pair
	for rows.Next() {
		var p pair
		if rows.Scan(&p.id, &p.repo) == nil {
			waiting = append(waiting, p)
		}
	}
	rows.Close()
	for _, p := range waiting {
		repo, err := s.DB.RepoByID(ctx, p.repo)
		if err != nil {
			continue
		}
		if _, err := s.TryStart(ctx, repo, p.id); err != nil {
			s.log().Warn("delivery: start waiting", "deployment", p.id, "err", err)
		}
	}
}
