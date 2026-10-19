// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

// Budget bounds an agent principal or a repository (docs/12 §3.4). Zero means
// unlimited. Budgets are read from the ledger, never counted in memory, so
// every server replica and the watcher see the same numbers.
type Budget struct {
	CIMinutesPerDay int `json:"ci_minutes_per_day"`
	RunsPerDay      int `json:"runs_per_day"`
	OpenChanges     int `json:"open_changes"`
	WritesPerHour   int `json:"writes_per_hour"`
}

// IsZero reports an unlimited budget.
func (b Budget) IsZero() bool {
	return b.CIMinutesPerDay == 0 && b.RunsPerDay == 0 && b.OpenChanges == 0 && b.WritesPerHour == 0
}

// Usage is what was spent in the budget's windows (24 h for CI minutes and
// runs, now for open changes, 1 h for writes).
type Usage struct {
	CIMinutes      float64 `json:"ci_minutes_24h"`
	Runs           int     `json:"runs_24h"`
	OpenChanges    int     `json:"open_changes"`
	WritesLastHour int     `json:"writes_1h"`
}

// BudgetStatus is a budget next to its usage.
type BudgetStatus struct {
	Scope     string   `json:"scope"` // repository | principal
	Principal string   `json:"principal,omitempty"`
	Source    string   `json:"source"` // fleet | identity | none
	Budget    Budget   `json:"budget"`
	Usage     Usage    `json:"usage"`
	Exceeded  []string `json:"exceeded,omitempty"`
}

// BudgetError reports which budget stopped a run.
type BudgetError struct {
	Scope, Principal, Budget string
	Used, Limit              float64
}

func (e *BudgetError) Error() string {
	who := "repository"
	if e.Scope == "principal" {
		who = "agent " + e.Principal
	}
	return fmt.Sprintf("budget exceeded: %s %s/%s for %s", e.Budget, trimFloat(e.Used), trimFloat(e.Limit), who)
}

func trimFloat(f float64) string {
	return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.2f", f), "0"), ".0")
}

// writeEventTypes are the ledger events that count as agent writes.
var writeEventTypes = []string{
	"io.ore.ref.updated", "io.ore.ref.deleted", "io.ore.change.opened", "io.ore.change.merged", "io.ore.change.patchset.pushed",
	"io.ore.comment.posted", "io.ore.review.submitted", "io.ore.issue.opened", "io.ore.issue.closed", "io.ore.landing.requested", "io.ore.deployment.requested",
}

// usage computes a principal's (or, with principal "", all agents') usage.
func (s *Service) usage(ctx context.Context, repo ledger.Repo, principal string) (Usage, error) {
	var u Usage
	now := s.now()
	day, hour := stamp(now.Add(-24*time.Hour)), stamp(now.Add(-time.Hour))
	// runs in the last 24 h
	q, args := `SELECT COUNT(1) FROM agent_runs WHERE repo_id = ? AND created_at >= ?`, []any{repo.ID, day}
	if principal != "" {
		q += ` AND adapter = ?`
		args = append(args, principal)
	}
	if err := s.DB.QueryRow(ctx, q, args...).Scan(&u.Runs); err != nil {
		return u, err
	}
	// open agent changes
	rows, err := s.DB.Query(ctx, `SELECT provenance FROM changes WHERE repo_id = ? AND state = 'open'`, repo.ID)
	if err != nil {
		return u, err
	}
	for rows.Next() {
		var prov string
		if err := rows.Scan(&prov); err != nil {
			rows.Close()
			return u, err
		}
		var p struct {
			AuthorKind string `json:"author_kind"`
			AgentID    string `json:"agent_id"`
		}
		if json.Unmarshal([]byte(prov), &p) == nil && p.AuthorKind == "agent" && (principal == "" || p.AgentID == principal) {
			u.OpenChanges++
		}
	}
	rows.Close()
	// CI minutes: finished pipeline runs by agent actors in the last 24 h
	q, args = `SELECT data FROM events WHERE org_id = ? AND type = 'io.ore.pipeline.run.finished' AND actor_kind = 'agent' AND time >= ? AND subject LIKE ?`, []any{repo.OrgID, day, repoSubject(repo) + "/%"}
	if principal != "" {
		q += ` AND actor_id = ?`
		args = append(args, principal)
	}
	rows, err = s.DB.Query(ctx, q, args...)
	if err != nil {
		return u, err
	}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			rows.Close()
			return u, err
		}
		var run struct {
			StartedAt  *string `json:"started_at"`
			FinishedAt *string `json:"finished_at"`
			CreatedAt  string  `json:"created_at"`
		}
		if json.Unmarshal([]byte(data), &run) != nil || run.FinishedAt == nil {
			continue
		}
		start := run.CreatedAt
		if run.StartedAt != nil {
			start = *run.StartedAt
		}
		if d := parseStamp(*run.FinishedAt).Sub(parseStamp(start)); d > 0 {
			u.CIMinutes += d.Minutes()
		}
	}
	rows.Close()
	// writes in the last hour
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(writeEventTypes)), ",")
	q, args = `SELECT COUNT(1) FROM events WHERE org_id = ? AND actor_kind = 'agent' AND time >= ? AND subject LIKE ? AND type IN (`+placeholders+`)`, []any{repo.OrgID, hour, repoSubject(repo) + "/%"}
	for _, t := range writeEventTypes {
		args = append(args, t)
	}
	if principal != "" {
		q += ` AND actor_id = ?`
		args = append(args, principal)
	}
	if err := s.DB.QueryRow(ctx, q, args...).Scan(&u.WritesLastHour); err != nil {
		return u, err
	}
	return u, nil
}

func parseStamp(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// stepBudgets says which budgets a step spends: runs at admission and the
// first step, writes before a push, a landing or a deployment, open changes
// and CI minutes before a change is opened and verified or landed. Unknown
// steps check everything.
var stepBudgets = map[string][]string{
	"admit":   {"runs_per_day"},
	"plan":    {"runs_per_day"},
	"change":  {"writes_per_hour"},
	"verify":  {"open_changes", "ci_minutes_per_day", "writes_per_hour"},
	"review":  {"ci_minutes_per_day"},
	"land":    {"ci_minutes_per_day", "writes_per_hour"},
	"deploy":  {"writes_per_hour"},
	"observe": {"writes_per_hour"},
}

// exceeded lists the budgets usage breaks. running is how many runs count
// as already spent beyond the usage (the run being admitted).
func exceeded(b Budget, u Usage, running int) []*BudgetError {
	var out []*BudgetError
	if b.RunsPerDay > 0 && u.Runs+running > b.RunsPerDay {
		out = append(out, &BudgetError{Budget: "runs_per_day", Used: float64(u.Runs + running), Limit: float64(b.RunsPerDay)})
	}
	if b.CIMinutesPerDay > 0 && u.CIMinutes >= float64(b.CIMinutesPerDay) {
		out = append(out, &BudgetError{Budget: "ci_minutes_per_day", Used: u.CIMinutes, Limit: float64(b.CIMinutesPerDay)})
	}
	if b.OpenChanges > 0 && u.OpenChanges >= b.OpenChanges {
		out = append(out, &BudgetError{Budget: "open_changes", Used: float64(u.OpenChanges), Limit: float64(b.OpenChanges)})
	}
	if b.WritesPerHour > 0 && u.WritesLastHour >= b.WritesPerHour {
		out = append(out, &BudgetError{Budget: "writes_per_hour", Used: float64(u.WritesLastHour), Limit: float64(b.WritesPerHour)})
	}
	return out
}

// budgetFor resolves the effective budget of a scope: the fleet's row, else
// (for a principal) the identity slice's agent budgets, else unlimited.
func (s *Service) budgetFor(ctx context.Context, repo ledger.Repo, rows map[string]Budget, principal string) (Budget, string) {
	if b, ok := rows[principal]; ok {
		return b, "fleet"
	}
	if principal != "" {
		if b, ok := s.identityBudget(ctx, repo, principal); ok {
			return b, "identity"
		}
	}
	return Budget{}, "none"
}

func (s *Service) budgetStatus(ctx context.Context, repo ledger.Repo, principal string, b Budget) (BudgetStatus, error) {
	u, err := s.usage(ctx, repo, principal)
	if err != nil {
		return BudgetStatus{}, err
	}
	st := BudgetStatus{Scope: "repository", Principal: principal, Source: "fleet", Budget: b, Usage: u}
	if principal != "" {
		st.Scope = "principal"
	}
	for _, e := range exceeded(b, u, 0) {
		st.Exceeded = append(st.Exceeded, e.Budget)
	}
	return st, nil
}

// Budgets reports the repository budget and every principal with a budget or
// recent runs, each with its usage.
func (s *Service) Budgets(ctx context.Context, repo ledger.Repo) ([]BudgetStatus, error) {
	rows, err := s.loadBudgetRows(ctx, repo)
	if err != nil {
		return nil, err
	}
	principals := map[string]bool{}
	for p := range rows {
		if p != "" {
			principals[p] = true
		}
	}
	rs, err := s.DB.Query(ctx, `SELECT DISTINCT adapter FROM agent_runs WHERE repo_id = ? AND created_at >= ?`, repo.ID, stamp(s.now().Add(-24*time.Hour)))
	if err != nil {
		return nil, err
	}
	for rs.Next() {
		var a string
		if err := rs.Scan(&a); err == nil && a != "" {
			principals[a] = true
		}
	}
	rs.Close()
	out := []BudgetStatus{}
	repoBudget, src := s.budgetFor(ctx, repo, rows, "")
	st, err := s.budgetStatus(ctx, repo, "", repoBudget)
	if err != nil {
		return nil, err
	}
	st.Source = src
	out = append(out, st)
	for p := range principals {
		b, src := s.budgetFor(ctx, repo, rows, p)
		st, err := s.budgetStatus(ctx, repo, p, b)
		if err != nil {
			return nil, err
		}
		st.Source = src
		out = append(out, st)
	}
	return out, nil
}

// checkBudgets enforces the repository's and the principal's budgets before a
// step. At "admit" the run about to start counts; at later steps the run is
// already recorded. A breach is recorded as io.ore.agent.budget.exceeded when
// a run is known.
func (s *Service) checkBudgets(ctx context.Context, repo ledger.Repo, principal ledger.Actor, step string, run *ledger.AgentRun) error {
	rows, err := s.loadBudgetRows(ctx, repo)
	if err != nil {
		return err
	}
	running := 0
	if step == "admit" {
		running = 1
	}
	for _, scope := range []string{"", principal.ID} {
		b, _ := s.budgetFor(ctx, repo, rows, scope)
		if b.IsZero() {
			continue
		}
		u, err := s.usage(ctx, repo, scope)
		if err != nil {
			return err
		}
		over := exceeded(b, u, running)
		if relevant, ok := stepBudgets[step]; ok {
			var kept []*BudgetError
			for _, e := range over {
				for _, name := range relevant {
					if e.Budget == name {
						kept = append(kept, e)
					}
				}
			}
			over = kept
		}
		if len(over) > 0 {
			e := over[0]
			e.Scope, e.Principal = "repository", ""
			if scope != "" {
				e.Scope, e.Principal = "principal", scope
			}
			data := map[string]any{"step": step, "scope": e.Scope, "principal": e.Principal, "budget": e.Budget, "used": e.Used, "limit": e.Limit, "usage": u, "budgets": b}
			if run != nil {
				data["run_id"] = run.ID
			}
			if err := s.emit(ctx, repo.OrgID, "io.ore.agent.budget.exceeded", repoSubject(repo), principal, data); err != nil {
				return err
			}
			return e
		}
	}
	return nil
}
