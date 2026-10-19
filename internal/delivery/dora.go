// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

// DORA is the four-keys view computed from the ledger for one repository
// (docs/04 §B.8): medians and p90, never means alone.
type DORA struct {
	Repo    string `json:"repo"`
	Days    int    `json:"days"`
	Since   string `json:"since"`
	Until   string `json:"until"`
	Deploys struct {
		Total     int     `json:"total"`     // production deployments that started (succeeded, failed, rolled back, stopped)
		Succeeded int     `json:"succeeded"` // including those later rolled back
		Rollbacks int     `json:"rollbacks"` // rollback deployments
		PerDay    float64 `json:"per_day"`
		PerWeek   float64 `json:"per_week"`
	} `json:"deployment_frequency"`
	LeadTime struct {
		Samples       int     `json:"samples"`
		MedianSeconds float64 `json:"median_seconds"`
		P90Seconds    float64 `json:"p90_seconds"`
	} `json:"lead_time"`
	ChangeFailure struct {
		Failed int     `json:"failed"`
		Total  int     `json:"total"`
		Rate   float64 `json:"rate"`
	} `json:"change_failure_rate"`
	Recovery struct {
		Samples       int     `json:"samples"`
		MedianSeconds float64 `json:"median_seconds"`
		P90Seconds    float64 `json:"p90_seconds"`
	} `json:"failed_deployment_recovery_time"`
}

type doraDep struct {
	id, status, created, finished string
	rollbackOf                    *string
	recovery                      *float64
}

// ComputeDORA reads production deployments and merged changes in the window.
func (s *Service) ComputeDORA(ctx context.Context, repo ledger.Repo, days int) (DORA, error) {
	if days <= 0 {
		days = 30
	}
	if days > 365 {
		days = 365
	}
	now := s.now()
	since := now.Add(-time.Duration(days) * 24 * time.Hour)
	out := DORA{Repo: repo.Org + "/" + repo.Name, Days: days, Since: since.UTC().Format(time.RFC3339), Until: now.UTC().Format(time.RFC3339)}
	rows, err := s.DB.Query(ctx, `
		SELECT d.id, d.status, d.created_at, COALESCE(d.finished_at, ''), d.rollback_of, r.recovery_seconds
		  FROM deployments d
		  LEFT JOIN environments e ON e.repo_id = d.repo_id AND e.name = d.environment
		  LEFT JOIN deployment_rollouts r ON r.deployment_id = d.id
		 WHERE d.repo_id = ?
		   AND COALESCE(e.kind, CASE WHEN d.environment IN ('production','prod') THEN 'production' ELSE 'other' END) = 'production'
		   AND d.status IN ('succeeded','failed','rolled_back','stopped')
		   AND d.created_at >= ?
		 ORDER BY d.created_at`, repo.ID, since.UTC().Format("2006-01-02T15:04:05.000Z07:00"))
	if err != nil {
		return out, err
	}
	var deps []doraDep
	for rows.Next() {
		var d doraDep
		if err := rows.Scan(&d.id, &d.status, &d.created, &d.finished, &d.rollbackOf, &d.recovery); err != nil {
			rows.Close()
			return out, err
		}
		deps = append(deps, d)
	}
	rows.Close()

	rolledBackBy := map[string]bool{}
	for _, d := range deps {
		if d.rollbackOf != nil {
			rolledBackBy[*d.rollbackOf] = true
		}
	}
	// frequency and failures
	for _, d := range deps {
		out.Deploys.Total++
		if d.rollbackOf != nil {
			out.Deploys.Rollbacks++
		}
		if d.status == StateSucceeded || d.status == StateRolledBack || d.status == StateStopped {
			out.Deploys.Succeeded++
		}
		if d.rollbackOf == nil {
			out.ChangeFailure.Total++
			if d.status == StateFailed || d.status == StateRolledBack || rolledBackBy[d.id] {
				out.ChangeFailure.Failed++
			}
		}
	}
	out.Deploys.PerDay = round(float64(out.Deploys.Total) / float64(days))
	out.Deploys.PerWeek = round(float64(out.Deploys.Total) * 7 / float64(days))
	if out.ChangeFailure.Total > 0 {
		out.ChangeFailure.Rate = round(float64(out.ChangeFailure.Failed) / float64(out.ChangeFailure.Total))
	}

	// recovery: recorded on the rollback, else time to the next success in the environment
	var recov []float64
	for i, d := range deps {
		if d.status != StateFailed && d.status != StateRolledBack {
			continue
		}
		if d.recovery != nil {
			recov = append(recov, *d.recovery)
			continue
		}
		failedAt := parseTime(d.finished)
		if failedAt.IsZero() {
			continue
		}
		for _, n := range deps[i+1:] {
			if (n.status == StateSucceeded || n.status == StateRolledBack || n.status == StateStopped) && n.finished != "" {
				if t := parseTime(n.finished); t.After(failedAt) {
					recov = append(recov, t.Sub(failedAt).Seconds())
					break
				}
			}
		}
	}
	out.Recovery.Samples = len(recov)
	out.Recovery.MedianSeconds, out.Recovery.P90Seconds = percentile(recov, 0.5), percentile(recov, 0.9)

	// lead time: change opened → the production success that shipped it
	crow, err := s.DB.Query(ctx, `SELECT created_at, merged_at FROM changes WHERE repo_id = ? AND state = 'merged' AND merged_at IS NOT NULL ORDER BY merged_at`, repo.ID)
	if err != nil {
		return out, err
	}
	type chg struct{ opened, merged time.Time }
	var changes []chg
	for crow.Next() {
		var o, m string
		if crow.Scan(&o, &m) == nil {
			changes = append(changes, chg{parseTime(o), parseTime(m)})
		}
	}
	crow.Close()
	var lead []float64
	var prevSuccess time.Time
	for _, d := range deps {
		if d.rollbackOf != nil || (d.status != StateSucceeded && d.status != StateRolledBack && d.status != StateStopped) || d.finished == "" {
			continue
		}
		shipped := parseTime(d.finished)
		for _, c := range changes {
			if !c.merged.After(prevSuccess) || c.merged.After(shipped) {
				continue
			}
			lead = append(lead, shipped.Sub(c.opened).Seconds())
		}
		prevSuccess = shipped
	}
	out.LeadTime.Samples = len(lead)
	out.LeadTime.MedianSeconds, out.LeadTime.P90Seconds = percentile(lead, 0.5), percentile(lead, 0.9)
	return out, nil
}

// percentile is the nearest-rank percentile of the values (0 for none).
func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64{}, v...)
	sort.Float64s(c)
	rank := int(math.Ceil(p*float64(len(c)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(c) {
		rank = len(c) - 1
	}
	return round(c[rank])
}

func round(f float64) float64 { return math.Round(f*1000) / 1000 }
