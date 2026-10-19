// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/orevcs/ore/internal/ledger"
)

// SQL behind the fleet. Rules: never call a DB method while a transaction is
// open (the v0 ledger serializes on one connection); emit the event in the
// transaction that writes the row.

func repoSubject(r ledger.Repo) string { return r.Org + "/" + r.Name }

func (s *Service) emit(ctx context.Context, orgID, typ, subject string, actor ledger.Actor, data any) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Emit(orgID, typ, subject, actor, data); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- settings ----

// Settings returns the repository's fleet settings (defaults when unset).
func (s *Service) Settings(ctx context.Context, repo ledger.Repo) (Settings, error) {
	st := DefaultSettings()
	var paused, autoPromote, autoDemote int
	var thresholds string
	err := s.DB.QueryRow(ctx, `SELECT paused, pause_reason, auto_promote, auto_demote, watcher_retries, watcher_window_minutes, evidence_window_days, thresholds, last_level_change_at, updated_at FROM fleet_settings WHERE repo_id = ?`, repo.ID).
		Scan(&paused, &st.PauseReason, &autoPromote, &autoDemote, &st.WatcherRetries, &st.WatcherWindowMinutes, &st.EvidenceWindowDays, &thresholds, &st.LastLevelChangeAt, &st.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return st, err
	}
	st.Paused, st.AutoPromote, st.AutoDemote = paused != 0, autoPromote != 0, autoDemote != 0
	var th Thresholds
	if thresholds != "" && thresholds != "{}" {
		_ = json.Unmarshal([]byte(thresholds), &th)
	}
	st.Thresholds = th.withDefaults()
	return st, nil
}

func (s *Service) saveSettings(ctx context.Context, repo ledger.Repo, st *Settings, eventType string, actor ledger.Actor, data map[string]any) error {
	th, _ := json.Marshal(st.Thresholds)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st.UpdatedAt = stamp(s.now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO fleet_settings(repo_id, org_id, paused, pause_reason, auto_promote, auto_demote, watcher_retries, watcher_window_minutes, evidence_window_days, thresholds, last_level_change_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(repo_id) DO UPDATE SET paused = excluded.paused, pause_reason = excluded.pause_reason, auto_promote = excluded.auto_promote, auto_demote = excluded.auto_demote,
		watcher_retries = excluded.watcher_retries, watcher_window_minutes = excluded.watcher_window_minutes, evidence_window_days = excluded.evidence_window_days, thresholds = excluded.thresholds,
		last_level_change_at = excluded.last_level_change_at, updated_at = excluded.updated_at`,
		repo.ID, repo.OrgID, b2i(st.Paused), st.PauseReason, b2i(st.AutoPromote), b2i(st.AutoDemote), st.WatcherRetries, st.WatcherWindowMinutes, st.EvidenceWindowDays, string(th), st.LastLevelChangeAt, st.UpdatedAt); err != nil {
		return err
	}
	if eventType != "" {
		if data == nil {
			data = map[string]any{}
		}
		data["repo_id"] = repo.ID
		if _, err := tx.Emit(repo.OrgID, eventType, repoSubject(repo), actor, data); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// InstancePaused reports the instance-wide kill switch.
func (s *Service) InstancePaused(ctx context.Context) (bool, string, error) {
	var paused int
	var reason string
	err := s.DB.QueryRow(ctx, `SELECT paused, pause_reason FROM fleet_instance WHERE id = 1`).Scan(&paused, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	return paused != 0, reason, err
}

func (s *Service) setInstancePaused(ctx context.Context, paused bool, reason string, actor ledger.Actor) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO fleet_instance(id, paused, pause_reason, updated_at) VALUES(1,?,?,?)
		ON CONFLICT(id) DO UPDATE SET paused = excluded.paused, pause_reason = excluded.pause_reason, updated_at = excluded.updated_at`, b2i(paused), reason, stamp(s.now())); err != nil {
		return err
	}
	typ := "io.ore.fleet.paused"
	if !paused {
		typ = "io.ore.fleet.resumed"
	}
	if _, err := tx.Emit("", typ, "instance", actor, map[string]any{"scope": "instance", "reason": reason}); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- roles ----

const roleCols = `id, org_id, repo_id, name, adapter_cmd, adapter_name, prompt_profile, budgets, enabled, created_at, updated_at`

func scanRole(r interface{ Scan(...any) error }) (Role, error) {
	var ro Role
	var budgets string
	var enabled int
	err := r.Scan(&ro.ID, &ro.OrgID, &ro.RepoID, &ro.Name, &ro.AdapterCmd, &ro.AdapterName, &ro.PromptProfile, &budgets, &enabled, &ro.CreatedAt, &ro.UpdatedAt)
	if err != nil {
		return ro, err
	}
	ro.Enabled = enabled != 0
	_ = json.Unmarshal([]byte(budgets), &ro.Budgets)
	return ro, nil
}

func (s *Service) loadRoles(ctx context.Context, repo ledger.Repo) (map[string]Role, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+roleCols+` FROM fleet_roles WHERE repo_id = ?`, repo.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Role{}
	for rows.Next() {
		ro, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		out[ro.Name] = ro
	}
	return out, rows.Err()
}

func (s *Service) upsertRole(ctx context.Context, repo ledger.Repo, ro *Role, actor ledger.Actor) error {
	budgets, _ := json.Marshal(ro.Budgets)
	now := stamp(s.now())
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if ro.ID == "" {
		ro.ID, ro.OrgID, ro.RepoID, ro.CreatedAt = ledger.NewID("role"), repo.OrgID, repo.ID, now
	}
	ro.UpdatedAt = now
	if _, err := tx.ExecContext(ctx, `INSERT INTO fleet_roles(`+roleCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(repo_id, name) DO UPDATE SET adapter_cmd = excluded.adapter_cmd, adapter_name = excluded.adapter_name, prompt_profile = excluded.prompt_profile, budgets = excluded.budgets, enabled = excluded.enabled, updated_at = excluded.updated_at`,
		ro.ID, ro.OrgID, ro.RepoID, ro.Name, ro.AdapterCmd, ro.AdapterName, ro.PromptProfile, string(budgets), b2i(ro.Enabled), ro.CreatedAt, ro.UpdatedAt); err != nil {
		return err
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.fleet.role.updated", repoSubject(repo)+"/fleet/roles/"+ro.Name, actor, ro); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- budgets ----

func (s *Service) loadBudgetRows(ctx context.Context, repo ledger.Repo) (map[string]Budget, error) {
	rows, err := s.DB.Query(ctx, `SELECT principal, ci_minutes_per_day, runs_per_day, open_changes, writes_per_hour FROM fleet_budgets WHERE repo_id = ?`, repo.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Budget{}
	for rows.Next() {
		var p string
		var b Budget
		if err := rows.Scan(&p, &b.CIMinutesPerDay, &b.RunsPerDay, &b.OpenChanges, &b.WritesPerHour); err != nil {
			return nil, err
		}
		out[p] = b
	}
	return out, rows.Err()
}

// identityBudget reads a budget from the identity slice's agents table (the
// agent principal's budgets JSON) when the fleet has no row for it.
func (s *Service) identityBudget(ctx context.Context, repo ledger.Repo, principal string) (Budget, bool) {
	var raw string
	if err := s.DB.QueryRow(ctx, `SELECT budgets FROM agents WHERE org_id = ? AND name = ?`, repo.OrgID, principal).Scan(&raw); err != nil {
		return Budget{}, false
	}
	var b Budget
	if err := json.Unmarshal([]byte(raw), &b); err != nil || b.IsZero() {
		return Budget{}, false
	}
	return b, true
}

// SetBudget stores a budget for the repository (principal "") or one agent
// principal and emits io.ore.fleet.budget.changed.
func (s *Service) SetBudget(ctx context.Context, repo ledger.Repo, principal string, b Budget, actor ledger.Actor) (BudgetStatus, error) {
	if b.CIMinutesPerDay < 0 || b.RunsPerDay < 0 || b.OpenChanges < 0 || b.WritesPerHour < 0 {
		return BudgetStatus{}, fmt.Errorf("%w: budgets must be >= 0 (0 = unlimited)", ErrInvalid)
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return BudgetStatus{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO fleet_budgets(id, org_id, repo_id, principal, ci_minutes_per_day, runs_per_day, open_changes, writes_per_hour, updated_at) VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(repo_id, principal) DO UPDATE SET ci_minutes_per_day = excluded.ci_minutes_per_day, runs_per_day = excluded.runs_per_day, open_changes = excluded.open_changes, writes_per_hour = excluded.writes_per_hour, updated_at = excluded.updated_at`,
		ledger.NewID("bud"), repo.OrgID, repo.ID, principal, b.CIMinutesPerDay, b.RunsPerDay, b.OpenChanges, b.WritesPerHour, stamp(s.now())); err != nil {
		return BudgetStatus{}, err
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.fleet.budget.changed", repoSubject(repo), actor, map[string]any{"principal": principal, "budget": b}); err != nil {
		return BudgetStatus{}, err
	}
	if err := tx.Commit(); err != nil {
		return BudgetStatus{}, err
	}
	return s.budgetStatus(ctx, repo, principal, b)
}

// ---- signals (watcher decisions) ----

// Signal is one watcher decision about one ledger event.
type Signal struct {
	ID         string  `json:"id"`
	OrgID      string  `json:"org_id"`
	RepoID     string  `json:"repo_id"`
	WatcherID  string  `json:"watcher_id"`
	EventSeq   int64   `json:"event_seq"`
	EventType  string  `json:"event_type"`
	IssueID    *string `json:"issue_id,omitempty"`
	CauseRunID *string `json:"cause_run_id,omitempty"`
	RunID      *string `json:"run_id,omitempty"`
	Depth      int     `json:"depth"`
	Decision   string  `json:"decision"` // claimed | started | skipped
	Reason     string  `json:"reason,omitempty"`
	CreatedAt  string  `json:"created_at"`
}

const signalCols = `id, org_id, repo_id, watcher_id, event_seq, event_type, issue_id, cause_run_id, run_id, depth, decision, reason, created_at`

func scanSignal(r interface{ Scan(...any) error }) (Signal, error) {
	var sg Signal
	err := r.Scan(&sg.ID, &sg.OrgID, &sg.RepoID, &sg.WatcherID, &sg.EventSeq, &sg.EventType, &sg.IssueID, &sg.CauseRunID, &sg.RunID, &sg.Depth, &sg.Decision, &sg.Reason, &sg.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return sg, ledger.ErrNotFound
	}
	return sg, err
}

// claimSignal records that this watcher handles the event; false when another
// watcher already did (the UNIQUE(repo_id, event_seq) claim).
func (s *Service) claimSignal(ctx context.Context, repo ledger.Repo, ev ledger.Event, sg *Signal) (bool, error) {
	sg.ID, sg.OrgID, sg.RepoID, sg.WatcherID, sg.EventSeq, sg.EventType, sg.Decision, sg.CreatedAt = ledger.NewID("sig"), repo.OrgID, repo.ID, s.watcherID(), ev.Seq, ev.Type, "claimed", stamp(s.now())
	_, err := s.DB.Exec(ctx, `INSERT INTO fleet_signals(`+signalCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sg.ID, sg.OrgID, sg.RepoID, sg.WatcherID, sg.EventSeq, sg.EventType, sg.IssueID, sg.CauseRunID, sg.RunID, sg.Depth, sg.Decision, sg.Reason, sg.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "constraint") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *Service) decideSignal(ctx context.Context, repo ledger.Repo, sg *Signal, decision, reason string, data map[string]any) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	sg.Decision, sg.Reason = decision, reason
	if _, err := tx.ExecContext(ctx, `UPDATE fleet_signals SET issue_id = ?, cause_run_id = ?, run_id = ?, depth = ?, decision = ?, reason = ? WHERE id = ?`, sg.IssueID, sg.CauseRunID, sg.RunID, sg.Depth, sg.Decision, sg.Reason, sg.ID); err != nil {
		return err
	}
	if data == nil {
		data = map[string]any{}
	}
	data["signal"] = sg
	if _, err := tx.Emit(repo.OrgID, "io.ore.fleet.watcher."+decision, repoSubject(repo), FleetActor, data); err != nil {
		return err
	}
	return tx.Commit()
}

// signalForRun finds the watcher decision that started a run, if any.
func (s *Service) signalForRun(ctx context.Context, runID string) (Signal, error) {
	return scanSignal(s.DB.QueryRow(ctx, `SELECT `+signalCols+` FROM fleet_signals WHERE run_id = ? AND decision = 'started' ORDER BY created_at DESC LIMIT 1`, runID))
}

// startedForIssue reports whether the watcher already started a run for an issue.
func (s *Service) startedForIssue(ctx context.Context, issueID string) (bool, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT COUNT(1) FROM fleet_signals WHERE issue_id = ? AND decision = 'started'`, issueID).Scan(&n)
	return n > 0, err
}

// Signals lists a repository's watcher decisions, newest first.
func (s *Service) Signals(ctx context.Context, repo ledger.Repo, limit int) ([]Signal, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.DB.Query(ctx, `SELECT `+signalCols+` FROM fleet_signals WHERE repo_id = ? ORDER BY event_seq DESC LIMIT ?`, repo.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Signal{}
	for rows.Next() {
		sg, err := scanSignal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sg)
	}
	return out, rows.Err()
}

// ---- cursors ----

func (s *Service) cursor(ctx context.Context) (int64, error) {
	var seq int64
	err := s.DB.QueryRow(ctx, `SELECT after_seq FROM fleet_cursors WHERE watcher_id = ?`, s.watcherID()).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return seq, err
}

func (s *Service) saveCursor(ctx context.Context, seq int64) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO fleet_cursors(watcher_id, after_seq, updated_at) VALUES(?,?,?) ON CONFLICT(watcher_id) DO UPDATE SET after_seq = excluded.after_seq, updated_at = excluded.updated_at`, s.watcherID(), seq, stamp(s.now()))
	return err
}

// ---- flags ----

// Flag is a human's mark that a change should not have landed (docs/12 §5).
type Flag struct {
	ID        string       `json:"id"`
	OrgID     string       `json:"org_id"`
	RepoID    string       `json:"repo_id"`
	ChangeID  string       `json:"change_id"`
	Number    int          `json:"change_number"`
	Reason    string       `json:"reason,omitempty"`
	Actor     ledger.Actor `json:"actor"`
	CreatedAt string       `json:"created_at"`
}

func (s *Service) insertFlag(ctx context.Context, repo ledger.Repo, ch ledger.Change, f *Flag) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	f.ID, f.OrgID, f.RepoID, f.ChangeID, f.Number, f.CreatedAt = ledger.NewID("flag"), repo.OrgID, repo.ID, ch.ID, ch.Number, stamp(s.now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO fleet_change_flags(id, org_id, repo_id, change_id, reason, actor_kind, actor_id, created_at) VALUES(?,?,?,?,?,?,?,?)`,
		f.ID, f.OrgID, f.RepoID, f.ChangeID, f.Reason, f.Actor.Kind, f.Actor.ID, f.CreatedAt); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "constraint") {
			return fmt.Errorf("%w: change #%d is already flagged", ErrConflict, ch.Number)
		}
		return err
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.change.flagged", fmt.Sprintf("%s/%s/changes/%d", repo.Org, repo.Name, ch.Number), f.Actor, map[string]any{
		"change_id": ch.ChangeID, "number": ch.Number, "state": ch.State, "merge_oid": ch.MergeOID, "reason": f.Reason, "flag_id": f.ID,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) flagsSince(ctx context.Context, repo ledger.Repo, since string) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT COUNT(1) FROM fleet_change_flags WHERE repo_id = ? AND created_at >= ?`, repo.ID, since).Scan(&n)
	return n, err
}
