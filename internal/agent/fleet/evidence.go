// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

// Thresholds drive the recommender (docs/12 §2 promotion evidence, §5
// demotion signals). Zero values mean the default.
type Thresholds struct {
	PromoteMinChanges         int     `json:"promote_min_changes"`
	PromoteMinAcceptance      float64 `json:"promote_min_acceptance_rate"`
	PromoteMaxFixRate         float64 `json:"promote_max_post_merge_fix_rate"`
	PromoteMaxRollbackRate    float64 `json:"promote_max_rollback_rate"`
	PromoteMaxRecoverySeconds float64 `json:"promote_max_recovery_seconds"`
	DemoteMaxRollbackRate     float64 `json:"demote_max_rollback_rate"`
	DemoteMinDeployments      int     `json:"demote_min_deployments"`
	DemoteMaxFixRate          float64 `json:"demote_max_post_merge_fix_rate"`
	DemoteMinMerged           int     `json:"demote_min_merged"`
	DemoteBudgetOverruns      int     `json:"demote_budget_overruns"`
	MaxAutoPromoteLevel       int     `json:"max_auto_promote_level"`
}

// DefaultThresholds are conservative: ten accepted agent changes with few
// follow-up fixes and no production damage earn a level; one in five
// production deployments failing, or one in three merges needing a revert or
// a flag, loses one. L5 is never reached automatically.
func DefaultThresholds() Thresholds {
	return Thresholds{
		PromoteMinChanges: 10, PromoteMinAcceptance: 0.8, PromoteMaxFixRate: 0.1, PromoteMaxRollbackRate: 0.05, PromoteMaxRecoverySeconds: 3600,
		DemoteMaxRollbackRate: 0.2, DemoteMinDeployments: 2, DemoteMaxFixRate: 0.3, DemoteMinMerged: 3, DemoteBudgetOverruns: 3, MaxAutoPromoteLevel: 4,
	}
}

func (t Thresholds) withDefaults() Thresholds {
	d := DefaultThresholds()
	if t.PromoteMinChanges == 0 {
		t.PromoteMinChanges = d.PromoteMinChanges
	}
	if t.PromoteMinAcceptance == 0 {
		t.PromoteMinAcceptance = d.PromoteMinAcceptance
	}
	if t.PromoteMaxFixRate == 0 {
		t.PromoteMaxFixRate = d.PromoteMaxFixRate
	}
	if t.PromoteMaxRollbackRate == 0 {
		t.PromoteMaxRollbackRate = d.PromoteMaxRollbackRate
	}
	if t.PromoteMaxRecoverySeconds == 0 {
		t.PromoteMaxRecoverySeconds = d.PromoteMaxRecoverySeconds
	}
	if t.DemoteMaxRollbackRate == 0 {
		t.DemoteMaxRollbackRate = d.DemoteMaxRollbackRate
	}
	if t.DemoteMinDeployments == 0 {
		t.DemoteMinDeployments = d.DemoteMinDeployments
	}
	if t.DemoteMaxFixRate == 0 {
		t.DemoteMaxFixRate = d.DemoteMaxFixRate
	}
	if t.DemoteMinMerged == 0 {
		t.DemoteMinMerged = d.DemoteMinMerged
	}
	if t.DemoteBudgetOverruns == 0 {
		t.DemoteBudgetOverruns = d.DemoteBudgetOverruns
	}
	if t.MaxAutoPromoteLevel == 0 {
		t.MaxAutoPromoteLevel = d.MaxAutoPromoteLevel
	}
	return t
}

// Evidence is what the ledger says about a repository's agents over a window.
type Evidence struct {
	Repo              string `json:"repo"`
	Level             int    `json:"autonomy_level"`
	WindowDays        int    `json:"window_days"`
	Since             string `json:"since"`
	Until             string `json:"until"`
	WindowRestartedAt string `json:"window_restarted_at,omitempty"`
	// WindowAfterSeq is the ledger sequence of the last autonomy level change;
	// only entities whose creation event is strictly after it count, so the
	// evidence that caused a level change can never cause it twice, whatever
	// the clock says.
	WindowAfterSeq int64 `json:"window_after_seq,omitempty"`
	Changes        struct {
		Opened           int      `json:"opened"`
		Merged           int      `json:"merged"` // including those later reverted
		Closed           int      `json:"closed"`
		Reverted         int      `json:"reverted"`
		Flagged          int      `json:"flagged"`
		Decided          int      `json:"decided"`
		AcceptanceRate   *float64 `json:"acceptance_rate"`     // merged / decided
		PostMergeFixRate *float64 `json:"post_merge_fix_rate"` // (reverted + flagged) / merged
	} `json:"agent_changes"`
	Production struct {
		Deployments           int      `json:"deployments"` // non-rollback production deployments that ran
		Failed                int      `json:"failed"`      // failed or rolled back
		RollbackRate          *float64 `json:"rollback_rate"`
		RecoverySamples       int      `json:"recovery_samples"`
		RecoveryMedianSeconds float64  `json:"recovery_median_seconds"`
	} `json:"production"`
	Runs struct {
		Total     int `json:"total"`
		Completed int `json:"completed"`
		Failed    int `json:"failed"`
		Blocked   int `json:"blocked"`
	} `json:"agent_runs"`
	BudgetOverruns int `json:"budget_overruns"`
	SecurityAlerts int `json:"security_alerts"`
}

// Recommendation is the recommender's verdict.
type Recommendation struct {
	Action  string   `json:"action"` // promote | demote | hold
	From    int      `json:"from"`
	To      int      `json:"to"`
	Reasons []string `json:"reasons"`
	// Automatic reports whether the repository's settings would apply it.
	Automatic bool `json:"automatic"`
}

// levelChangeBoundary returns the ledger sequence and time of the last
// io.ore.repository.autonomy.changed event for the repository (0 when the
// level never changed). The evidence window restarts strictly after it.
func (s *Service) levelChangeBoundary(ctx context.Context, repo ledger.Repo) (int64, string, error) {
	var seq int64
	var at string
	err := s.DB.QueryRow(ctx, `SELECT seq, time FROM events WHERE type = 'io.ore.repository.autonomy.changed' AND org_id = ? AND subject = ? ORDER BY seq DESC LIMIT 1`, repo.OrgID, repoSubject(repo)).Scan(&seq, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	return seq, at, err
}

// Evidence computes the evidence and the recommendation for a repository.
// The window is the last evidence_window_days, cut at the last level change:
// an entity counts when its creation event (io.ore.change.opened,
// io.ore.deployment.requested, io.ore.agent.session.started,
// io.ore.change.flagged, ...) has a ledger sequence strictly after that
// change's event. Sequences, not timestamps, decide, so entities created in
// the same millisecond as the level change are not counted twice.
func (s *Service) Evidence(ctx context.Context, repo ledger.Repo) (Evidence, Recommendation, error) {
	st, err := s.Settings(ctx, repo)
	if err != nil {
		return Evidence{}, Recommendation{}, err
	}
	now := s.now()
	since := now.Add(-time.Duration(st.EvidenceWindowDays) * 24 * time.Hour)
	ev := Evidence{Repo: repo.Org + "/" + repo.Name, Level: repo.AutonomyLevel, WindowDays: st.EvidenceWindowDays, Until: stamp(now)}
	boundary, boundaryAt, err := s.levelChangeBoundary(ctx, repo)
	if err != nil {
		return ev, Recommendation{}, err
	}
	ev.WindowAfterSeq = boundary
	if boundary > 0 {
		ev.WindowRestartedAt = boundaryAt // informational: the sequence decides, not the clock
	}
	ev.Since = stamp(since)
	subj := repoSubject(repo)

	// agent changes
	rows, err := s.DB.Query(ctx, `SELECT c.state, c.provenance FROM changes c WHERE c.repo_id = ? AND c.created_at >= ?
		AND EXISTS (SELECT 1 FROM events e WHERE e.type = 'io.ore.change.opened' AND e.seq > ? AND e.subject = ? || '/changes/' || c.number)`, repo.ID, ev.Since, boundary, subj)
	if err != nil {
		return ev, Recommendation{}, err
	}
	for rows.Next() {
		var state, prov string
		if err := rows.Scan(&state, &prov); err != nil {
			rows.Close()
			return ev, Recommendation{}, err
		}
		var p struct {
			AuthorKind string `json:"author_kind"`
		}
		if json.Unmarshal([]byte(prov), &p) != nil || p.AuthorKind != "agent" {
			continue
		}
		ev.Changes.Opened++
		switch state {
		case "merged":
			ev.Changes.Merged++
		case "reverted":
			ev.Changes.Merged++
			ev.Changes.Reverted++
		case "closed":
			ev.Changes.Closed++
		}
	}
	rows.Close()
	if err := s.DB.QueryRow(ctx, `SELECT COUNT(1) FROM events WHERE org_id = ? AND type = 'io.ore.change.flagged' AND seq > ? AND time >= ? AND subject LIKE ?`, repo.OrgID, boundary, ev.Since, subj+"/changes/%").Scan(&ev.Changes.Flagged); err != nil {
		return ev, Recommendation{}, err
	}
	ev.Changes.Decided = ev.Changes.Merged + ev.Changes.Closed
	if ev.Changes.Decided > 0 {
		r := float64(ev.Changes.Merged) / float64(ev.Changes.Decided)
		ev.Changes.AcceptanceRate = &r
	}
	if ev.Changes.Merged > 0 {
		r := float64(ev.Changes.Reverted+ev.Changes.Flagged) / float64(ev.Changes.Merged)
		ev.Changes.PostMergeFixRate = &r
	}

	// production deployments
	envs := []any{"production"}
	if ers, err := s.DB.Query(ctx, `SELECT name FROM environments WHERE repo_id = ? AND kind = 'production'`, repo.ID); err == nil {
		for ers.Next() {
			var n string
			if ers.Scan(&n) == nil && n != "production" {
				envs = append(envs, n)
			}
		}
		ers.Close()
	}
	q := `SELECT d.status, d.rollback_of, d.created_at FROM deployments d WHERE d.repo_id = ? AND d.created_at >= ? AND d.environment IN (` + strings.TrimSuffix(strings.Repeat("?,", len(envs)), ",") + `)
		AND EXISTS (SELECT 1 FROM events e WHERE e.type = 'io.ore.deployment.requested' AND e.seq > ? AND e.subject = ? || '/deployments/' || d.id) ORDER BY d.created_at ASC, d.id ASC`
	args := append([]any{repo.ID, ev.Since}, envs...)
	args = append(args, boundary, subj)
	drows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return ev, Recommendation{}, err
	}
	type dep struct {
		status    string
		rollback  bool
		createdAt time.Time
	}
	var deps []dep
	for drows.Next() {
		var d dep
		var rb *string
		var created string
		if err := drows.Scan(&d.status, &rb, &created); err != nil {
			drows.Close()
			return ev, Recommendation{}, err
		}
		d.rollback, d.createdAt = rb != nil, parseStamp(created)
		deps = append(deps, d)
	}
	drows.Close()
	var recoveries []float64
	for i, d := range deps {
		if d.rollback {
			continue
		}
		switch d.status {
		case "succeeded":
			ev.Production.Deployments++
		case "failed", "rolled_back":
			ev.Production.Deployments++
			ev.Production.Failed++
			for _, later := range deps[i+1:] {
				if later.status == "succeeded" {
					recoveries = append(recoveries, later.createdAt.Sub(d.createdAt).Seconds())
					break
				}
			}
		}
	}
	if ev.Production.Deployments > 0 {
		r := float64(ev.Production.Failed) / float64(ev.Production.Deployments)
		ev.Production.RollbackRate = &r
	}
	if len(recoveries) > 0 {
		sort.Float64s(recoveries)
		ev.Production.RecoverySamples = len(recoveries)
		ev.Production.RecoveryMedianSeconds = recoveries[len(recoveries)/2]
	}

	// runs
	rrows, err := s.DB.Query(ctx, `SELECT a.status FROM agent_runs a WHERE a.repo_id = ? AND a.created_at >= ?
		AND EXISTS (SELECT 1 FROM events e WHERE e.type = 'io.ore.agent.session.started' AND e.seq > ? AND e.subject = ? || '/agent/' || a.id)`, repo.ID, ev.Since, boundary, subj)
	if err != nil {
		return ev, Recommendation{}, err
	}
	for rrows.Next() {
		var status string
		if rrows.Scan(&status) == nil {
			ev.Runs.Total++
			switch status {
			case "completed":
				ev.Runs.Completed++
			case "failed":
				ev.Runs.Failed++
			case "blocked":
				ev.Runs.Blocked++
			}
		}
	}
	rrows.Close()

	// budget overruns and security alerts
	if err := s.DB.QueryRow(ctx, `SELECT COUNT(1) FROM events WHERE org_id = ? AND type = 'io.ore.agent.budget.exceeded' AND seq > ? AND time >= ? AND (subject = ? OR subject LIKE ?)`, repo.OrgID, boundary, ev.Since, subj, subj+"/%").Scan(&ev.BudgetOverruns); err != nil {
		return ev, Recommendation{}, err
	}
	if err := s.DB.QueryRow(ctx, `SELECT COUNT(1) FROM events WHERE org_id = ? AND type LIKE 'io.ore.security.%' AND seq > ? AND time >= ? AND (subject = ? OR subject LIKE ?)`, repo.OrgID, boundary, ev.Since, subj, subj+"/%").Scan(&ev.SecurityAlerts); err != nil {
		return ev, Recommendation{}, err
	}
	return ev, Recommend(ev, st), nil
}

// Recommend is the pure recommender: demotion signals (docs/12 §5) win over
// promotion evidence (§2). Demotion goes one level down to L2 at most, since
// below L2 agents do not land anyway; promotion goes one level up, never
// past MaxAutoPromoteLevel.
func Recommend(ev Evidence, st Settings) Recommendation {
	th := st.Thresholds.withDefaults()
	rec := Recommendation{Action: "hold", From: ev.Level, To: ev.Level, Reasons: []string{}}
	if ev.Level >= 3 {
		if r := ev.Production.RollbackRate; r != nil && *r > th.DemoteMaxRollbackRate && ev.Production.Deployments >= th.DemoteMinDeployments {
			rec.Reasons = append(rec.Reasons, fmt.Sprintf("rollback_rate %.2f > %.2f over %d production deployments", *r, th.DemoteMaxRollbackRate, ev.Production.Deployments))
		}
		if r := ev.Changes.PostMergeFixRate; r != nil && *r > th.DemoteMaxFixRate && ev.Changes.Merged >= th.DemoteMinMerged {
			rec.Reasons = append(rec.Reasons, fmt.Sprintf("post_merge_fix_rate %.2f > %.2f over %d merged agent changes", *r, th.DemoteMaxFixRate, ev.Changes.Merged))
		}
		if ev.BudgetOverruns >= th.DemoteBudgetOverruns {
			rec.Reasons = append(rec.Reasons, fmt.Sprintf("budget_overruns %d >= %d", ev.BudgetOverruns, th.DemoteBudgetOverruns))
		}
		if ev.SecurityAlerts > 0 {
			rec.Reasons = append(rec.Reasons, fmt.Sprintf("security_alerts %d", ev.SecurityAlerts))
		}
		if ev.Changes.Flagged > 0 {
			rec.Reasons = append(rec.Reasons, fmt.Sprintf("flagged %d change(s) as should not have landed", ev.Changes.Flagged))
		}
		if len(rec.Reasons) > 0 {
			rec.Action, rec.To = "demote", max(ev.Level-1, 2)
			rec.Automatic = st.AutoDemote
			return rec
		}
	}
	if ev.Level < th.MaxAutoPromoteLevel {
		ok := ev.Changes.Decided >= th.PromoteMinChanges &&
			ev.Changes.AcceptanceRate != nil && *ev.Changes.AcceptanceRate >= th.PromoteMinAcceptance &&
			(ev.Changes.PostMergeFixRate == nil || *ev.Changes.PostMergeFixRate <= th.PromoteMaxFixRate) &&
			(ev.Production.RollbackRate == nil || *ev.Production.RollbackRate <= th.PromoteMaxRollbackRate) &&
			(ev.Production.RecoverySamples == 0 || ev.Production.RecoveryMedianSeconds <= th.PromoteMaxRecoverySeconds) &&
			ev.BudgetOverruns == 0 && ev.SecurityAlerts == 0 && ev.Changes.Flagged == 0
		if ok {
			rec.Action, rec.To = "promote", ev.Level+1
			rec.Automatic = st.AutoPromote
			rec.Reasons = append(rec.Reasons, fmt.Sprintf("acceptance_rate %.2f >= %.2f over %d decided agent changes", *ev.Changes.AcceptanceRate, th.PromoteMinAcceptance, ev.Changes.Decided))
			if ev.Changes.PostMergeFixRate != nil {
				rec.Reasons = append(rec.Reasons, fmt.Sprintf("post_merge_fix_rate %.2f <= %.2f", *ev.Changes.PostMergeFixRate, th.PromoteMaxFixRate))
			}
			if ev.Production.RollbackRate != nil {
				rec.Reasons = append(rec.Reasons, fmt.Sprintf("rollback_rate %.2f <= %.2f", *ev.Production.RollbackRate, th.PromoteMaxRollbackRate))
			}
			rec.Reasons = append(rec.Reasons, "no budget overruns, security alerts or flags in the window")
			return rec
		}
	}
	return rec
}

// Evaluate computes the evidence, records the evaluation and applies the
// recommendation when the repository's settings say so (auto_demote on by
// default, auto_promote off). It returns whether a level change was applied.
func (s *Service) Evaluate(ctx context.Context, repo ledger.Repo, trigger string, actor ledger.Actor) (Evidence, Recommendation, bool, error) {
	ev, rec, err := s.Evidence(ctx, repo)
	if err != nil {
		return ev, rec, false, err
	}
	if err := s.emit(ctx, repo.OrgID, "io.ore.fleet.evidence.evaluated", repoSubject(repo), actor, map[string]any{"trigger": trigger, "evidence": ev, "recommendation": rec}); err != nil {
		return ev, rec, false, err
	}
	if rec.Action == "hold" || !rec.Automatic {
		return ev, rec, false, nil
	}
	if err := s.applyLevel(ctx, repo, ev, rec, "automatic ("+trigger+")", actor); err != nil {
		return ev, rec, false, err
	}
	return ev, rec, true, nil
}

// ApplyRecommendation applies the current recommendation regardless of the
// automatic flags (a human pressing the button).
func (s *Service) ApplyRecommendation(ctx context.Context, repo ledger.Repo, actor ledger.Actor) (Evidence, Recommendation, bool, error) {
	ev, rec, err := s.Evidence(ctx, repo)
	if err != nil {
		return ev, rec, false, err
	}
	if rec.Action == "hold" {
		return ev, rec, false, nil
	}
	if err := s.applyLevel(ctx, repo, ev, rec, "applied by "+actor.Kind+"/"+actor.ID, actor); err != nil {
		return ev, rec, false, err
	}
	return ev, rec, true, nil
}

// applyLevel changes the level through ledger.SetAutonomyLevel with the
// evidence attached to the reason. The evidence window restarts at that
// event's ledger sequence (see Evidence); the timestamp kept in the settings
// is informational.
func (s *Service) applyLevel(ctx context.Context, repo ledger.Repo, ev Evidence, rec Recommendation, how string, actor ledger.Actor) error {
	evJSON, _ := json.Marshal(ev)
	reason := fmt.Sprintf("fleet %s L%d -> L%d, %s: %s; evidence: %s", rec.Action, rec.From, rec.To, how, strings.Join(rec.Reasons, "; "), evJSON)
	if err := s.DB.SetAutonomyLevel(ctx, repo, rec.To, reason, actor); err != nil {
		return err
	}
	st, err := s.Settings(ctx, repo)
	if err != nil {
		return err
	}
	st.LastLevelChangeAt = stamp(s.now())
	return s.saveSettings(ctx, repo, &st, "", actor, nil)
}

// FlagChange records a human's "should not have landed" on a change and, at
// L3 and above with automatic demotion on, demotes the repository one level
// with the evidence attached (docs/12 §5).
func (s *Service) FlagChange(ctx context.Context, repo ledger.Repo, ch ledger.Change, reason string, actor ledger.Actor) (Flag, Recommendation, bool, error) {
	if actor.Kind != "human" {
		return Flag{}, Recommendation{}, false, fmt.Errorf("%w: only a human can flag a change", ErrInvalid)
	}
	f := Flag{Reason: reason, Actor: actor}
	if err := s.insertFlag(ctx, repo, ch, &f); err != nil {
		return f, Recommendation{}, false, err
	}
	st, err := s.Settings(ctx, repo)
	if err != nil {
		return f, Recommendation{}, false, err
	}
	rec := Recommendation{Action: "hold", From: repo.AutonomyLevel, To: repo.AutonomyLevel, Reasons: []string{}}
	if repo.AutonomyLevel >= 3 {
		rec = Recommendation{Action: "demote", From: repo.AutonomyLevel, To: max(repo.AutonomyLevel-1, 2), Automatic: st.AutoDemote,
			Reasons: []string{fmt.Sprintf("%s/%s flagged change #%d as should not have landed: %s", actor.Kind, actor.ID, ch.Number, orDefault(reason, "no reason given"))}}
	}
	ev, _, err := s.Evidence(ctx, repo)
	if err != nil {
		return f, rec, false, err
	}
	if err := s.emit(ctx, repo.OrgID, "io.ore.fleet.evidence.evaluated", repoSubject(repo), actor, map[string]any{"trigger": "change flagged", "evidence": ev, "recommendation": rec}); err != nil {
		return f, rec, false, err
	}
	if rec.Action != "demote" || !rec.Automatic {
		return f, rec, false, nil
	}
	if err := s.applyLevel(ctx, repo, ev, rec, "automatic (change flagged)", actor); err != nil {
		return f, rec, false, err
	}
	return f, rec, true, nil
}
