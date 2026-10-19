// SPDX-License-Identifier: AGPL-3.0-only

package review

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/orevcs/ore/internal/ledger"
)

// This file is the SQL behind review v1. Rules: never call a DB method while a
// transaction is open (the v0 ledger serializes on one connection), and emit
// the event in the same transaction as the row it describes.

func subject(r ledger.Repo, c ledger.Change) string {
	return fmt.Sprintf("%s/%s/changes/%d", r.Org, r.Name, c.Number)
}

// ---- patch sets ----

const patchSetCols = `id, org_id, repo_id, change_id, number, base_oid, head_oid, pushed_by_kind, pushed_by_id, created_at`

func scanPatchSet(s interface{ Scan(...any) error }) (PatchSet, error) {
	var p PatchSet
	err := s.Scan(&p.ID, &p.OrgID, &p.RepoID, &p.ChangeID, &p.Number, &p.BaseOID, &p.HeadOID, &p.PushedBy.Kind, &p.PushedBy.ID, &p.CreatedAt)
	return p, err
}

func (s *Service) loadPatchSets(ctx context.Context, c ledger.Change) ([]PatchSet, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+patchSetCols+` FROM patch_sets WHERE change_id = ? ORDER BY number ASC`, c.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PatchSet
	for rows.Next() {
		p, err := scanPatchSet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func insertPatchSetTx(ctx context.Context, tx *ledger.Tx, r ledger.Repo, c ledger.Change, p *PatchSet) error {
	p.ID, p.OrgID, p.RepoID, p.ChangeID, p.CreatedAt = ledger.NewID("ps"), r.OrgID, r.ID, c.ID, ledger.Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO patch_sets(`+patchSetCols+`) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.OrgID, p.RepoID, p.ChangeID, p.Number, p.BaseOID, p.HeadOID, p.PushedBy.Kind, p.PushedBy.ID, p.CreatedAt); err != nil {
		return fmt.Errorf("insert patch set: %w", err)
	}
	_, err := tx.Emit(r.OrgID, "io.ore.patchset.recorded", subject(r, c), p.PushedBy, map[string]any{
		"change_id": c.ChangeID, "number": c.Number, "patch_set": p.Number, "base_oid": p.BaseOID, "head_oid": p.HeadOID, "pushed_by": p.PushedBy,
	})
	return err
}

// pushedBy finds who moved ref to head (the ledger's ref_updates), falling
// back to the given actor.
func (s *Service) pushedBy(ctx context.Context, r ledger.Repo, ref, head string, fallback ledger.Actor) ledger.Actor {
	var a ledger.Actor
	err := s.DB.QueryRow(ctx, `SELECT actor_kind, actor_id FROM ref_updates WHERE repo_id = ? AND ref = ? AND new_oid = ? ORDER BY created_at DESC LIMIT 1`, r.ID, ref, head).Scan(&a.Kind, &a.ID)
	if err != nil {
		return fallback
	}
	return a
}

// ---- change state changes owned by review v1 ----

func (s *Service) setChangeState(ctx context.Context, r ledger.Repo, c ledger.Change, state, eventType string, actor ledger.Actor, data map[string]any) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if state == "open" {
		if _, err := tx.ExecContext(ctx, `UPDATE changes SET state = 'open', merge_oid = NULL, merged_at = NULL WHERE id = ?`, c.ID); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE changes SET state = ? WHERE id = ?`, state, c.ID); err != nil {
		return err
	}
	if data == nil {
		data = map[string]any{}
	}
	data["change_id"] = c.ChangeID
	data["number"] = c.Number
	data["state"] = state
	if _, err := tx.Emit(r.OrgID, eventType, subject(r, c), actor, data); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) retargetChange(ctx context.Context, r ledger.Repo, c ledger.Change, targetRef, baseOID string, actor ledger.Actor) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE changes SET target_ref = ?, base_oid = ? WHERE id = ?`, targetRef, baseOID, c.ID); err != nil {
		return err
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.change.retargeted", subject(r, c), actor, map[string]any{
		"change_id": c.ChangeID, "number": c.Number, "old_target": c.TargetRef, "new_target": targetRef, "base_oid": baseOID,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- comments ----

const commentCols = `id, org_id, repo_id, change_id, patch_set, path, start_line, end_line, body, author_kind, author_id, lane, resolved, created_at`

func scanComment(s interface{ Scan(...any) error }) (Comment, error) {
	var c Comment
	var resolved int
	err := s.Scan(&c.ID, &c.OrgID, &c.RepoID, &c.ChangeID, &c.PatchSet, &c.Path, &c.StartLine, &c.EndLine, &c.Body, &c.Author.Kind, &c.Author.ID, &c.Lane, &resolved, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ledger.ErrNotFound
	}
	c.Resolved = resolved != 0
	return c, err
}

func (s *Service) insertComment(ctx context.Context, r ledger.Repo, ch ledger.Change, c *Comment) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	c.ID, c.OrgID, c.RepoID, c.ChangeID, c.CreatedAt = ledger.NewID("cmt"), r.OrgID, r.ID, ch.ID, ledger.Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO change_comments(`+commentCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.OrgID, c.RepoID, c.ChangeID, c.PatchSet, c.Path, c.StartLine, c.EndLine, c.Body, c.Author.Kind, c.Author.ID, c.Lane, 0, c.CreatedAt); err != nil {
		return fmt.Errorf("insert comment: %w", err)
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.comment.posted", subject(r, ch), c.Author, map[string]any{
		"change_id": ch.ChangeID, "number": ch.Number, "comment_id": c.ID, "patch_set": c.PatchSet, "path": c.Path, "lane": c.Lane, "change_state": ch.State,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) loadComments(ctx context.Context, ch ledger.Change) ([]Comment, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+commentCols+` FROM change_comments WHERE change_id = ? ORDER BY created_at ASC, id ASC`, ch.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Comment
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) loadComment(ctx context.Context, ch ledger.Change, id string) (Comment, error) {
	return scanComment(s.DB.QueryRow(ctx, `SELECT `+commentCols+` FROM change_comments WHERE change_id = ? AND id = ?`, ch.ID, id))
}

func (s *Service) markResolved(ctx context.Context, r ledger.Repo, ch ledger.Change, c Comment, resolved bool, actor ledger.Actor) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v := 0
	if resolved {
		v = 1
	}
	if _, err := tx.ExecContext(ctx, `UPDATE change_comments SET resolved = ? WHERE id = ?`, v, c.ID); err != nil {
		return err
	}
	typ := "io.ore.comment.resolved"
	if !resolved {
		typ = "io.ore.comment.reopened"
	}
	if _, err := tx.Emit(r.OrgID, typ, subject(r, ch), actor, map[string]any{"change_id": ch.ChangeID, "number": ch.Number, "comment_id": c.ID}); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- reviews ----

const reviewCols = `id, org_id, repo_id, change_id, patch_set, reviewer_kind, reviewer_id, lane, verdict, findings, body, created_at`

func scanReview(s interface{ Scan(...any) error }) (Review, error) {
	var rv Review
	var findings string
	err := s.Scan(&rv.ID, &rv.OrgID, &rv.RepoID, &rv.ChangeID, &rv.PatchSet, &rv.Reviewer.Kind, &rv.Reviewer.ID, &rv.Lane, &rv.Verdict, &findings, &rv.Body, &rv.CreatedAt)
	if err != nil {
		return rv, err
	}
	rv.Findings = []Finding{}
	if findings != "" {
		_ = json.Unmarshal([]byte(findings), &rv.Findings)
	}
	return rv, nil
}

func (s *Service) insertReview(ctx context.Context, r ledger.Repo, ch ledger.Change, rv *Review) error {
	findings, err := json.Marshal(rv.Findings)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rv.ID, rv.OrgID, rv.RepoID, rv.ChangeID, rv.CreatedAt = ledger.NewID("rev"), r.OrgID, r.ID, ch.ID, ledger.Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO reviews(`+reviewCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		rv.ID, rv.OrgID, rv.RepoID, rv.ChangeID, rv.PatchSet, rv.Reviewer.Kind, rv.Reviewer.ID, rv.Lane, rv.Verdict, string(findings), rv.Body, rv.CreatedAt); err != nil {
		return fmt.Errorf("insert review: %w", err)
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.review.submitted", subject(r, ch), rv.Reviewer, map[string]any{
		"change_id": ch.ChangeID, "number": ch.Number, "review_id": rv.ID, "patch_set": rv.PatchSet, "verdict": rv.Verdict, "lane": rv.Lane, "findings": len(rv.Findings),
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) loadReviews(ctx context.Context, ch ledger.Change) ([]Review, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+reviewCols+` FROM reviews WHERE change_id = ? ORDER BY created_at ASC, id ASC`, ch.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Review
	for rows.Next() {
		rv, err := scanReview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rv)
	}
	return out, rows.Err()
}

// ---- policy ----

// Policy returns the repository's review policy (defaults when unset).
func (s *Service) Policy(ctx context.Context, r ledger.Repo) (Policy, error) {
	p := Policy{DefaultStrategy: StrategyQueue}
	var allow int
	err := s.DB.QueryRow(ctx, `SELECT required_approvals, allow_agent_approvals, default_strategy FROM review_policies WHERE repo_id = ?`, r.ID).Scan(&p.RequiredApprovals, &allow, &p.DefaultStrategy)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	p.AllowAgentApprovals = allow != 0
	if p.DefaultStrategy == "" {
		p.DefaultStrategy = StrategyQueue
	}
	return p, nil
}

// SetPolicy stores the repository's review policy and emits
// io.ore.repository.review_policy.changed.
func (s *Service) SetPolicy(ctx context.Context, r ledger.Repo, p Policy, actor ledger.Actor) (Policy, error) {
	if p.RequiredApprovals < 0 {
		return p, fmt.Errorf("%w: required_approvals must be >= 0", ErrBadRequest)
	}
	if p.DefaultStrategy == "" {
		p.DefaultStrategy = StrategyQueue
	}
	if !validStrategy(p.DefaultStrategy) {
		return p, fmt.Errorf("%w: default_strategy must be queue|land_when_green|optimistic", ErrBadRequest)
	}
	old, err := s.Policy(ctx, r)
	if err != nil {
		return p, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	allow := 0
	if p.AllowAgentApprovals {
		allow = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO review_policies(repo_id, org_id, required_approvals, allow_agent_approvals, default_strategy, updated_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(repo_id) DO UPDATE SET required_approvals = excluded.required_approvals, allow_agent_approvals = excluded.allow_agent_approvals, default_strategy = excluded.default_strategy, updated_at = excluded.updated_at`,
		r.ID, r.OrgID, p.RequiredApprovals, allow, p.DefaultStrategy, ledger.Now()); err != nil {
		return p, err
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.repository.review_policy.changed", r.Org+"/"+r.Name, actor, map[string]any{"from": old, "to": p}); err != nil {
		return p, err
	}
	return p, tx.Commit()
}

func validStrategy(s string) bool {
	return s == StrategyQueue || s == StrategyLandWhenGreen || s == StrategyOptimistic
}

// ---- stacks ----

const stackCols = `id, org_id, repo_id, target_ref, change_ids, state, failed_change_id, error, created_by_kind, created_by_id, created_at, updated_at`

func scanStack(s interface{ Scan(...any) error }) (Stack, error) {
	var st Stack
	var ids string
	err := s.Scan(&st.ID, &st.OrgID, &st.RepoID, &st.TargetRef, &ids, &st.State, &st.FailedChangeID, &st.Error, &st.CreatedBy.Kind, &st.CreatedBy.ID, &st.CreatedAt, &st.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return st, ledger.ErrNotFound
	}
	if err != nil {
		return st, err
	}
	st.ChangeIDs = []string{}
	_ = json.Unmarshal([]byte(ids), &st.ChangeIDs)
	return st, nil
}

func (s *Service) insertStack(ctx context.Context, r ledger.Repo, st *Stack, numbers []int) error {
	ids, _ := json.Marshal(st.ChangeIDs)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st.ID, st.OrgID, st.RepoID, st.State, st.CreatedAt = ledger.NewID("stk"), r.OrgID, r.ID, "open", ledger.Now()
	st.UpdatedAt = st.CreatedAt
	if _, err := tx.ExecContext(ctx, `INSERT INTO stacks(`+stackCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		st.ID, st.OrgID, st.RepoID, st.TargetRef, string(ids), st.State, nil, "", st.CreatedBy.Kind, st.CreatedBy.ID, st.CreatedAt, st.UpdatedAt); err != nil {
		return fmt.Errorf("insert stack: %w", err)
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.stack.opened", fmt.Sprintf("%s/%s/stacks/%s", r.Org, r.Name, st.ID), st.CreatedBy, map[string]any{
		"stack_id": st.ID, "target_ref": st.TargetRef, "change_ids": st.ChangeIDs, "change_numbers": numbers,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) updateStack(ctx context.Context, r ledger.Repo, st *Stack, state, eventType string, actor ledger.Actor, data map[string]any) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st.State, st.UpdatedAt = state, ledger.Now()
	if _, err := tx.ExecContext(ctx, `UPDATE stacks SET state = ?, failed_change_id = ?, error = ?, updated_at = ? WHERE id = ?`, st.State, st.FailedChangeID, st.Error, st.UpdatedAt, st.ID); err != nil {
		return err
	}
	if eventType != "" {
		if data == nil {
			data = map[string]any{}
		}
		data["stack_id"] = st.ID
		data["state"] = st.State
		data["error"] = st.Error
		if _, err := tx.Emit(r.OrgID, eventType, fmt.Sprintf("%s/%s/stacks/%s", r.Org, r.Name, st.ID), actor, data); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) loadStack(ctx context.Context, r ledger.Repo, id string) (Stack, error) {
	return scanStack(s.DB.QueryRow(ctx, `SELECT `+stackCols+` FROM stacks WHERE repo_id = ? AND id = ?`, r.ID, id))
}

// Stacks lists a repository's stacks, newest first (without their changes).
func (s *Service) Stacks(ctx context.Context, r ledger.Repo) ([]Stack, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+stackCols+` FROM stacks WHERE repo_id = ? ORDER BY created_at DESC`, r.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Stack{}
	for rows.Next() {
		st, err := scanStack(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Service) changeByID(ctx context.Context, r ledger.Repo, id string) (ledger.Change, error) {
	var number int
	if err := s.DB.QueryRow(ctx, `SELECT number FROM changes WHERE repo_id = ? AND id = ?`, r.ID, id).Scan(&number); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ledger.Change{}, ledger.ErrNotFound
		}
		return ledger.Change{}, err
	}
	return s.DB.Change(ctx, r, number)
}

// ---- landings ----

const landingCols = `l.id, l.org_id, l.repo_id, l.change_id, c.number, l.patch_set, l.strategy, l.status, l.merge_oid, l.revert_oid, l.verification_run_id, l.issue_number, l.error, l.actor_kind, l.actor_id, l.created_at, l.finished_at`

func scanLanding(s interface{ Scan(...any) error }) (Landing, error) {
	var l Landing
	err := s.Scan(&l.ID, &l.OrgID, &l.RepoID, &l.ChangeID, &l.ChangeNumber, &l.PatchSet, &l.Strategy, &l.Status, &l.MergeOID, &l.RevertOID, &l.VerificationRunID, &l.IssueNumber, &l.Error, &l.Actor.Kind, &l.Actor.ID, &l.CreatedAt, &l.FinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ledger.ErrNotFound
	}
	return l, err
}

func (s *Service) insertLanding(ctx context.Context, r ledger.Repo, c ledger.Change, l *Landing) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	l.ID, l.OrgID, l.RepoID, l.ChangeID, l.ChangeNumber, l.Status, l.CreatedAt = ledger.NewID("land"), r.OrgID, r.ID, c.ID, c.Number, LandingRequested, ledger.Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO landings(id, org_id, repo_id, change_id, patch_set, strategy, status, error, actor_kind, actor_id, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		l.ID, l.OrgID, l.RepoID, l.ChangeID, l.PatchSet, l.Strategy, l.Status, "", l.Actor.Kind, l.Actor.ID, l.CreatedAt); err != nil {
		return fmt.Errorf("insert landing: %w", err)
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.landing.requested", subject(r, c), l.Actor, l); err != nil {
		return err
	}
	return tx.Commit()
}

// updateLanding persists the landing's progress and emits io.ore.landing.<status>;
// extra events (e.g. io.ore.change.reverted) ride in the same transaction.
func (s *Service) updateLanding(ctx context.Context, r ledger.Repo, c ledger.Change, l *Landing, status string, extra func(tx *ledger.Tx) error) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	l.Status = status
	if status != LandingVerifying && status != LandingRequested && l.FinishedAt == nil {
		now := ledger.Now()
		l.FinishedAt = &now
	}
	if _, err := tx.ExecContext(ctx, `UPDATE landings SET status = ?, merge_oid = ?, revert_oid = ?, verification_run_id = ?, issue_number = ?, error = ?, finished_at = ? WHERE id = ?`,
		l.Status, l.MergeOID, l.RevertOID, l.VerificationRunID, l.IssueNumber, l.Error, l.FinishedAt, l.ID); err != nil {
		return err
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.landing."+status, subject(r, c), l.Actor, l); err != nil {
		return err
	}
	if extra != nil {
		if err := extra(tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Landings lists a change's landing attempts, oldest first.
func (s *Service) Landings(ctx context.Context, r ledger.Repo, c ledger.Change) ([]Landing, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+landingCols+` FROM landings l JOIN changes c ON c.id = l.change_id WHERE l.change_id = ? ORDER BY l.created_at ASC, l.id ASC`, c.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Landing{}
	for rows.Next() {
		l, err := scanLanding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
