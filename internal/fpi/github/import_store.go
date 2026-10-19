// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/review"
)

// Job statuses.
const (
	JobPlanned   = "planned"
	JobRunning   = "running"
	JobDeferred  = "deferred"
	JobFailed    = "failed"
	JobCompleted = "completed"
)

// Import phases, in order.
var importPhases = []string{"git", "labels", "milestones", "issues", "comments", "pulls", "releases", "webhooks", "workflows", "identities", "done"}

// ImportOptions tunes a run.
type ImportOptions struct {
	Issues    *bool `json:"issues,omitempty"`
	Pulls     *bool `json:"pulls,omitempty"`
	Releases  *bool `json:"releases,omitempty"`
	Webhooks  *bool `json:"webhooks,omitempty"`
	Workflows *bool `json:"workflows,omitempty"`
	Wiki      *bool `json:"wiki,omitempty"`
	// Wait runs the job synchronously (tests, CLI).
	Wait bool `json:"wait,omitempty"`
}

func on(b *bool) bool { return b == nil || *b }

// ImportError is one recorded, non-fatal problem.
type ImportError struct {
	Phase  string `json:"phase"`
	Object string `json:"object"`
	Error  string `json:"error"`
	At     string `json:"at"`
}

// ImportJob is a resumable import.
type ImportJob struct {
	ID            string            `json:"id"`
	OrgID         string            `json:"org_id"`
	RepoID        string            `json:"repo_id"`
	Source        string            `json:"source"`
	Owner         string            `json:"owner"`
	Name          string            `json:"name"`
	Options       ImportOptions     `json:"options"`
	Status        string            `json:"status"`
	Phase         string            `json:"phase"`
	Cursors       map[string]int    `json:"cursors"`
	Progress      map[string]int    `json:"progress"`
	Errors        []ImportError     `json:"errors"`
	Report        *FidelityReport   `json:"report,omitempty"`
	Conformance   []WorkflowReport  `json:"conformance"`
	Verification  *VerifyReport     `json:"verification,omitempty"`
	Error         string            `json:"error,omitempty"`
	NextAttemptAt *string           `json:"next_attempt_at,omitempty"`
	Actor         ledger.Actor      `json:"actor"`
	CreatedAt     string            `json:"created_at"`
	UpdatedAt     string            `json:"updated_at"`
	FinishedAt    *string           `json:"finished_at,omitempty"`
	Webhooks      []ImportedWebhook `json:"webhooks,omitempty"`
}

func (j *ImportJob) note(phase, object string, err error) {
	j.Errors = append(j.Errors, ImportError{Phase: phase, Object: object, Error: err.Error(), At: ledger.Now()})
}

func (j *ImportJob) add(key string, n int) {
	if j.Progress == nil {
		j.Progress = map[string]int{}
	}
	j.Progress[key] += n
}

const jobCols = `id, org_id, repo_id, source, owner, name, options, status, phase, cursors, progress, errors, report, conformance, verification, error, next_attempt_at, actor_kind, actor_id, created_at, updated_at, finished_at`

func scanJob(s interface{ Scan(...any) error }) (ImportJob, error) {
	var j ImportJob
	var options, cursors, progress, errs, report, conformance, verification string
	err := s.Scan(&j.ID, &j.OrgID, &j.RepoID, &j.Source, &j.Owner, &j.Name, &options, &j.Status, &j.Phase, &cursors, &progress, &errs, &report, &conformance, &verification,
		&j.Error, &j.NextAttemptAt, &j.Actor.Kind, &j.Actor.ID, &j.CreatedAt, &j.UpdatedAt, &j.FinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return j, ledger.ErrNotFound
	}
	if err != nil {
		return j, err
	}
	_ = json.Unmarshal([]byte(options), &j.Options)
	_ = json.Unmarshal([]byte(cursors), &j.Cursors)
	_ = json.Unmarshal([]byte(progress), &j.Progress)
	_ = json.Unmarshal([]byte(errs), &j.Errors)
	_ = json.Unmarshal([]byte(conformance), &j.Conformance)
	if report != "" {
		j.Report = &FidelityReport{}
		_ = json.Unmarshal([]byte(report), j.Report)
	}
	if verification != "" {
		j.Verification = &VerifyReport{}
		_ = json.Unmarshal([]byte(verification), j.Verification)
	}
	if j.Cursors == nil {
		j.Cursors = map[string]int{}
	}
	if j.Progress == nil {
		j.Progress = map[string]int{}
	}
	if j.Errors == nil {
		j.Errors = []ImportError{}
	}
	if j.Conformance == nil {
		j.Conformance = []WorkflowReport{}
	}
	return j, nil
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// insertJob records a new job with io.ore.import.<status>.
func (st store) insertJob(ctx context.Context, repo ledger.Repo, j *ImportJob, event string) error {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := ledger.Now()
	j.ID, j.OrgID, j.RepoID, j.CreatedAt, j.UpdatedAt = ledger.NewID("imp"), repo.OrgID, repo.ID, now, now
	if j.Cursors == nil {
		j.Cursors = map[string]int{}
	}
	if j.Progress == nil {
		j.Progress = map[string]int{}
	}
	if j.Errors == nil {
		j.Errors = []ImportError{}
	}
	if j.Conformance == nil {
		j.Conformance = []WorkflowReport{}
	}
	report := ""
	if j.Report != nil {
		report = jsonString(j.Report)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO import_jobs(`+jobCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, j.OrgID, j.RepoID, j.Source, j.Owner, j.Name, jsonString(j.Options), j.Status, j.Phase, jsonString(j.Cursors), jsonString(j.Progress), jsonString(j.Errors),
		report, jsonString(j.Conformance), "", j.Error, j.NextAttemptAt, j.Actor.Kind, j.Actor.ID, j.CreatedAt, j.UpdatedAt, j.FinishedAt); err != nil {
		return fmt.Errorf("insert import job: %w", err)
	}
	data := map[string]any{"job_id": j.ID, "source": j.Source, "status": j.Status}
	if j.Report != nil {
		data["score"] = j.Report.Score
		data["lines"] = j.Report.Lines
	}
	if _, err := tx.Emit(repo.OrgID, event, repo.Org+"/"+repo.Name+"/imports/"+j.ID, j.Actor, data); err != nil {
		return err
	}
	return tx.Commit()
}

// saveJob persists progress; event "" saves silently.
func (st store) saveJob(ctx context.Context, repo ledger.Repo, j *ImportJob, event string, data map[string]any) error {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	j.UpdatedAt = ledger.Now()
	report, verification := "", ""
	if j.Report != nil {
		report = jsonString(j.Report)
	}
	if j.Verification != nil {
		verification = jsonString(j.Verification)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE import_jobs SET status = ?, phase = ?, cursors = ?, progress = ?, errors = ?, report = ?, conformance = ?, verification = ?, error = ?, next_attempt_at = ?, updated_at = ?, finished_at = ? WHERE id = ?`,
		j.Status, j.Phase, jsonString(j.Cursors), jsonString(j.Progress), jsonString(j.Errors), report, jsonString(j.Conformance), verification, j.Error, j.NextAttemptAt, j.UpdatedAt, j.FinishedAt, j.ID); err != nil {
		return err
	}
	if event != "" {
		if data == nil {
			data = map[string]any{}
		}
		data["job_id"], data["source"], data["status"], data["phase"] = j.ID, j.Source, j.Status, j.Phase
		if _, err := tx.Emit(repo.OrgID, event, repo.Org+"/"+repo.Name+"/imports/"+j.ID, j.Actor, data); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (st store) job(ctx context.Context, id string) (ImportJob, error) {
	return scanJob(st.db.QueryRow(ctx, `SELECT `+jobCols+` FROM import_jobs WHERE id = ?`, id))
}

func (st store) jobs(ctx context.Context, where string, args ...any) ([]ImportJob, error) {
	rows, err := st.db.Query(ctx, `SELECT `+jobCols+` FROM import_jobs `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImportJob
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ---- mappings ----

type mapping struct {
	Kind         string
	SourceID     string
	TargetID     string
	TargetNumber int
	Extra        map[string]any
}

func (st store) mapping(ctx context.Context, repoID, kind, sourceID string) (mapping, bool, error) {
	var m mapping
	var extra string
	err := st.db.QueryRow(ctx, `SELECT kind, source_id, target_id, target_number, extra FROM import_mappings WHERE repo_id = ? AND kind = ? AND source_id = ?`, repoID, kind, sourceID).
		Scan(&m.Kind, &m.SourceID, &m.TargetID, &m.TargetNumber, &extra)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	_ = json.Unmarshal([]byte(extra), &m.Extra)
	return m, true, nil
}

func insertMappingTx(ctx context.Context, tx *ledger.Tx, repo ledger.Repo, jobID string, m mapping) error {
	extra := m.Extra
	if extra == nil {
		extra = map[string]any{}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO import_mappings(org_id, repo_id, job_id, kind, source_id, target_id, target_number, extra, created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		repo.OrgID, repo.ID, jobID, m.Kind, m.SourceID, m.TargetID, m.TargetNumber, jsonString(extra), ledger.Now())
	return err
}

func (st store) insertMapping(ctx context.Context, repo ledger.Repo, jobID string, m mapping) error {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertMappingTx(ctx, tx, repo, jobID, m); err != nil {
		return err
	}
	return tx.Commit()
}

func (st store) mappings(ctx context.Context, repoID, kind string) ([]mapping, error) {
	rows, err := st.db.Query(ctx, `SELECT kind, source_id, target_id, target_number, extra FROM import_mappings WHERE repo_id = ? AND kind = ? ORDER BY target_number, source_id`, repoID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mapping
	for rows.Next() {
		var m mapping
		var extra string
		if err := rows.Scan(&m.Kind, &m.SourceID, &m.TargetID, &m.TargetNumber, &extra); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(extra), &m.Extra)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (st store) countMappings(ctx context.Context, repoID, kind string) (int, error) {
	var n int
	err := st.db.QueryRow(ctx, `SELECT COUNT(1) FROM import_mappings WHERE repo_id = ? AND kind = ?`, repoID, kind).Scan(&n)
	return n, err
}

// ---- entities with original authors and timestamps ----

func importActor(u ghUser) ledger.Actor {
	if u.Login == "" {
		return ledger.Actor{Kind: "app", ID: "github"}
	}
	if u.Type == "Bot" {
		return ledger.Actor{Kind: "app", ID: "github:" + u.Login}
	}
	return ledger.Actor{Kind: "human", ID: "github:" + u.Login}
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// reserveIssueNumbers inserts a closed marker issue at the highest GitHub
// number so native issues continue after it while the import fills the
// numbers below (docs/10 §2: #N stays #N). Returns whether it was created.
func (st store) reserveIssueNumbers(ctx context.Context, repo ledger.Repo, jobID string, max int, actor ledger.Actor) (bool, error) {
	if max <= 0 {
		return false, nil
	}
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var cur sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(number) FROM issues WHERE repo_id = ?`, repo.ID).Scan(&cur); err != nil {
		return false, err
	}
	if int(cur.Int64) >= max {
		return false, nil
	}
	now := ledger.Now()
	i := ledger.Issue{ID: ledger.NewID("iss"), OrgID: repo.OrgID, RepoID: repo.ID, Number: max, State: "closed", CreatedAt: now, ClosedAt: &now,
		Title: fmt.Sprintf("GitHub import: numbers up to #%d are reserved", max),
		Body:  fmt.Sprintf("Ore reserved issue numbers 1..%d so imported GitHub issues keep their numbers and new issues continue after #%d. If GitHub #%d is an issue this marker is replaced by it; if it was a pull request the marker stays closed.", max, max, max)}
	if _, err := tx.ExecContext(ctx, `INSERT INTO issues(id, org_id, repo_id, number, title, body, state, created_at, closed_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		i.ID, i.OrgID, i.RepoID, i.Number, i.Title, i.Body, i.State, i.CreatedAt, i.ClosedAt); err != nil {
		return false, err
	}
	if err := insertMappingTx(ctx, tx, repo, jobID, mapping{Kind: "reservation", SourceID: fmt.Sprint(max), TargetID: i.ID, TargetNumber: max}); err != nil {
		return false, err
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.import.numbers.reserved", fmt.Sprintf("%s/%s/issues/%d", repo.Org, repo.Name, max), actor, map[string]any{"job_id": jobID, "up_to": max, "issue_id": i.ID}); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// importIssue writes a GitHub issue as an Ore issue with its number (when
// free), the original author and timestamps, the adapter's github_issue_map
// row (so webhooks keep it current afterwards) and the import mapping, in
// one transaction. A reservation marker at that number is replaced in place.
func (st store) importIssue(ctx context.Context, repo ledger.Repo, jobID string, gi ghIssueFull, renumberAbove int, actor ledger.Actor) (ledger.Issue, bool, error) {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return ledger.Issue{}, false, err
	}
	defer tx.Rollback()
	created := gi.CreatedAt
	if created == "" {
		created = ledger.Now()
	}
	i := ledger.Issue{ID: ledger.NewID("iss"), OrgID: repo.OrgID, RepoID: repo.ID, Number: gi.Number, Title: gi.Title, Body: gi.Body, State: gi.State, CreatedAt: created, ClosedAt: nullable(gi.ClosedAt)}
	if i.State != "closed" {
		i.State = "open"
		i.ClosedAt = nil
	}
	preserved := true
	var existingID string
	var reserved int
	if err := tx.QueryRowContext(ctx, `SELECT i.id, (SELECT COUNT(1) FROM import_mappings m WHERE m.repo_id = i.repo_id AND m.kind = 'reservation' AND m.target_id = i.id) FROM issues i WHERE i.repo_id = ? AND i.number = ?`, repo.ID, gi.Number).Scan(&existingID, &reserved); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ledger.Issue{}, false, err
	}
	switch {
	case existingID != "" && reserved > 0:
		i.ID = existingID
		if _, err := tx.ExecContext(ctx, `UPDATE issues SET title = ?, body = ?, state = ?, created_at = ?, closed_at = ? WHERE id = ?`, i.Title, i.Body, i.State, i.CreatedAt, i.ClosedAt, i.ID); err != nil {
			return ledger.Issue{}, false, err
		}
	case existingID != "":
		n, err := freeNumberAbove(ctx, tx, "issues", repo.ID, 0, renumberAbove)
		if err != nil {
			return ledger.Issue{}, false, err
		}
		i.Number, preserved = n, false
		fallthrough
	default:
		if _, err := tx.ExecContext(ctx, `INSERT INTO issues(id, org_id, repo_id, number, title, body, state, created_at, closed_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			i.ID, i.OrgID, i.RepoID, i.Number, i.Title, i.Body, i.State, i.CreatedAt, i.ClosedAt); err != nil {
			return ledger.Issue{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO github_issue_map(org_id, repo_id, github_number, github_id, issue_id, issue_number, html_url, updated_at) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(repo_id, github_number) DO UPDATE SET issue_id = excluded.issue_id, issue_number = excluded.issue_number, github_id = excluded.github_id, html_url = excluded.html_url, updated_at = excluded.updated_at`,
		repo.OrgID, repo.ID, gi.Number, gi.ID, i.ID, i.Number, gi.HTMLURL, ledger.Now()); err != nil {
		return ledger.Issue{}, false, err
	}
	labels := make([]string, 0, len(gi.Labels))
	for _, l := range gi.Labels {
		labels = append(labels, l.Name)
	}
	extra := map[string]any{"author": gi.User.Login, "labels": labels, "comments": gi.Comments, "preserved": preserved, "github_id": gi.ID, "url": gi.HTMLURL}
	if gi.Milestone != nil {
		extra["milestone"] = gi.Milestone.Title
	}
	if err := insertMappingTx(ctx, tx, repo, jobID, mapping{Kind: "issue", SourceID: fmt.Sprint(gi.Number), TargetID: i.ID, TargetNumber: i.Number, Extra: extra}); err != nil {
		return ledger.Issue{}, false, err
	}
	data := map[string]any{"id": i.ID, "number": i.Number, "title": i.Title, "state": i.State, "created_at": i.CreatedAt, "closed_at": i.ClosedAt, "imported": true, "job_id": jobID,
		"github": map[string]any{"number": gi.Number, "id": gi.ID, "url": gi.HTMLURL, "author": gi.User.Login, "labels": labels, "number_preserved": preserved}}
	if _, err := tx.Emit(repo.OrgID, "io.ore.issue.imported", fmt.Sprintf("%s/%s/issues/%d", repo.Org, repo.Name, i.Number), actor, data); err != nil {
		return ledger.Issue{}, false, err
	}
	return i, preserved, tx.Commit()
}

// importIssueComment writes an issue comment into the gateway's comments table.
func (st store) importIssueComment(ctx context.Context, repo ledger.Repo, jobID string, issue ledger.Issue, c ghComment, actor ledger.Actor) error {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id := ledger.NewID("cmt")
	created := c.CreatedAt
	if created == "" {
		created = ledger.Now()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, org_id, repo_id, target_kind, target_id, target_number, body, actor_kind, actor_id, session_id, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		id, repo.OrgID, repo.ID, "issue", issue.ID, issue.Number, c.Body, actor.Kind, actor.ID, "", created); err != nil {
		return err
	}
	if err := insertMappingTx(ctx, tx, repo, jobID, mapping{Kind: "comment", SourceID: fmt.Sprint(c.ID), TargetID: id, TargetNumber: issue.Number, Extra: map[string]any{"author": c.User.Login, "target": "issue"}}); err != nil {
		return err
	}
	data := map[string]any{"comment_id": id, "target_kind": "issue", "target": issue.Number, "created_at": created, "imported": true, "job_id": jobID, "github": map[string]any{"id": c.ID, "author": c.User.Login, "url": c.HTMLURL}}
	if _, err := tx.Emit(repo.OrgID, "io.ore.comment.imported", fmt.Sprintf("%s/%s/issues/%d/comments/%s", repo.Org, repo.Name, issue.Number, id), actor, data); err != nil {
		return err
	}
	return tx.Commit()
}

// importChange writes a pull request as a change (number preserved when
// free) with its patch sets, the adapter's github_pr_map row and the
// mapping, in one transaction. heads are the observed heads, oldest first,
// each with the base to record; the last one is the current head.
func (st store) importChange(ctx context.Context, repo ledger.Repo, jobID string, pr ghPullFull, c *ledger.Change, heads []observedHead, renumberAbove int, actor ledger.Actor) (bool, error) {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	n, err := freeNumberAbove(ctx, tx, "changes", repo.ID, pr.Number, renumberAbove)
	if err != nil {
		return false, err
	}
	preserved := n == pr.Number
	created := pr.CreatedAt
	if created == "" {
		created = ledger.Now()
	}
	c.ID, c.OrgID, c.RepoID, c.Number, c.CreatedAt = ledger.NewID("chg"), repo.OrgID, repo.ID, n, created
	state, mergeOID := "open", ""
	var mergedAt *string
	switch {
	case pr.MergedAt != "":
		state = "merged"
		mergedAt = nullable(pr.MergedAt)
		if pr.MergeCommitSHA != nil {
			mergeOID = *pr.MergeCommitSHA
		}
	case pr.State == "closed":
		state = "closed"
	}
	c.State, c.MergedAt = state, mergedAt
	if mergeOID != "" {
		c.MergeOID = &mergeOID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO changes(id, org_id, repo_id, number, change_id, title, body, source_ref, target_ref, state, base_oid, head_oid, merge_oid, provenance, created_at, merged_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, c.ID, c.OrgID, c.RepoID, c.Number, c.ChangeID, c.Title, c.Body, c.SourceRef, c.TargetRef, c.State, c.BaseOID, c.HeadOID, c.MergeOID, c.Provenance, c.CreatedAt, c.MergedAt); err != nil {
		return false, err
	}
	prState := state
	if _, err := tx.ExecContext(ctx, `INSERT INTO github_pr_map(org_id, repo_id, github_number, github_id, change_id, change_number, head_sha, base_ref, state, html_url, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(repo_id, github_number) DO UPDATE SET change_id = excluded.change_id, change_number = excluded.change_number, head_sha = excluded.head_sha, state = excluded.state, updated_at = excluded.updated_at`,
		repo.OrgID, repo.ID, pr.Number, pr.ID, c.ID, c.Number, c.HeadOID, pr.Base.Ref, prState, pr.HTMLURL, ledger.Now()); err != nil {
		return false, err
	}
	for i, h := range heads {
		psID := ledger.NewID("ps")
		at := h.At
		if at == "" {
			at = created
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO patch_sets(id, org_id, repo_id, change_id, number, base_oid, head_oid, pushed_by_kind, pushed_by_id, created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			psID, repo.OrgID, repo.ID, c.ID, i+1, h.Base, h.Head, actor.Kind, actor.ID, at); err != nil {
			return false, err
		}
		if _, err := tx.Emit(repo.OrgID, "io.ore.patchset.recorded", fmt.Sprintf("%s/%s/changes/%d", repo.Org, repo.Name, c.Number), actor,
			map[string]any{"change_id": c.ChangeID, "number": c.Number, "patch_set": i + 1, "base_oid": h.Base, "head_oid": h.Head, "pushed_by": actor, "imported": true}); err != nil {
			return false, err
		}
	}
	extra := map[string]any{"author": pr.User.Login, "preserved": preserved, "github_id": pr.ID, "url": pr.HTMLURL, "state": state, "head": c.HeadOID, "patch_sets": len(heads), "base_ref": pr.Base.Ref}
	if err := insertMappingTx(ctx, tx, repo, jobID, mapping{Kind: "pull", SourceID: fmt.Sprint(pr.Number), TargetID: c.ID, TargetNumber: c.Number, Extra: extra}); err != nil {
		return false, err
	}
	data := map[string]any{"id": c.ID, "number": c.Number, "change_id": c.ChangeID, "title": c.Title, "state": c.State, "head_oid": c.HeadOID, "base_oid": c.BaseOID, "merge_oid": mergeOID,
		"provenance": c.Provenance, "created_at": c.CreatedAt, "patch_sets": len(heads), "imported": true, "job_id": jobID,
		"github": map[string]any{"pr": pr.Number, "id": pr.ID, "url": pr.HTMLURL, "author": pr.User.Login, "number_preserved": preserved}}
	if _, err := tx.Emit(repo.OrgID, "io.ore.change.imported", fmt.Sprintf("%s/%s/changes/%d", repo.Org, repo.Name, c.Number), actor, data); err != nil {
		return false, err
	}
	return preserved, tx.Commit()
}

// importChangeComment writes a conversation or review comment into the
// review store's change_comments: anchored to (patch set, path, line) when
// the diff still exists, otherwise at the latest patch set with a migrated
// marker in the body and in the mapping.
func (st store) importChangeComment(ctx context.Context, repo ledger.Repo, jobID string, c ledger.Change, kind, sourceID string, patchSet int, path string, start, end *int, body string, migrated string, created string, actor ledger.Actor) error {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id := ledger.NewID("cmt")
	if created == "" {
		created = ledger.Now()
	}
	lane := review.LaneFor(actor)
	if migrated != "" {
		body = strings.TrimRight(body, "\n") + "\n\n_(migrated anchor: " + migrated + ")_"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO change_comments(id, org_id, repo_id, change_id, patch_set, path, start_line, end_line, body, author_kind, author_id, lane, resolved, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, repo.OrgID, repo.ID, c.ID, patchSet, path, start, end, body, actor.Kind, actor.ID, lane, 0, created); err != nil {
		return err
	}
	extra := map[string]any{"target": "change", "anchor": "exact"}
	if migrated != "" {
		extra["anchor"] = "migrated"
		extra["original_anchor"] = migrated
	}
	if err := insertMappingTx(ctx, tx, repo, jobID, mapping{Kind: kind, SourceID: sourceID, TargetID: id, TargetNumber: c.Number, Extra: extra}); err != nil {
		return err
	}
	data := map[string]any{"comment_id": id, "target_kind": "change", "target": c.Number, "patch_set": patchSet, "path": path, "lane": lane, "created_at": created, "imported": true, "job_id": jobID, "anchor": extra["anchor"]}
	if _, err := tx.Emit(repo.OrgID, "io.ore.comment.imported", fmt.Sprintf("%s/%s/changes/%d/comments/%s", repo.Org, repo.Name, c.Number, id), actor, data); err != nil {
		return err
	}
	return tx.Commit()
}

// importReview writes a review verdict with its original timestamp.
func (st store) importReview(ctx context.Context, repo ledger.Repo, jobID string, c ledger.Change, rv ghReview, patchSet int, verdict, body string, actor ledger.Actor) error {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id := ledger.NewID("rev")
	created := rv.SubmittedAt
	if created == "" {
		created = ledger.Now()
	}
	lane := review.LaneFor(actor)
	if _, err := tx.ExecContext(ctx, `INSERT INTO reviews(id, org_id, repo_id, change_id, patch_set, reviewer_kind, reviewer_id, lane, verdict, findings, body, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, repo.OrgID, repo.ID, c.ID, patchSet, actor.Kind, actor.ID, lane, verdict, "[]", body, created); err != nil {
		return err
	}
	if err := insertMappingTx(ctx, tx, repo, jobID, mapping{Kind: "review", SourceID: fmt.Sprint(rv.ID), TargetID: id, TargetNumber: c.Number, Extra: map[string]any{"author": rv.User.Login, "state": rv.State, "verdict": verdict}}); err != nil {
		return err
	}
	data := map[string]any{"change_id": c.ChangeID, "number": c.Number, "review_id": id, "patch_set": patchSet, "verdict": verdict, "lane": lane, "findings": 0, "created_at": created, "imported": true, "job_id": jobID, "github": map[string]any{"id": rv.ID, "state": rv.State}}
	if _, err := tx.Emit(repo.OrgID, "io.ore.review.submitted", fmt.Sprintf("%s/%s/changes/%d", repo.Org, repo.Name, c.Number), actor, data); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- releases, assets, webhooks, identities ----

// ImportedRelease is a release recorded by the importer (Ore's release entity arrives later).
type ImportedRelease struct {
	ID          string          `json:"id"`
	GitHubID    int64           `json:"github_id"`
	Tag         string          `json:"tag"`
	Name        string          `json:"name"`
	Draft       bool            `json:"draft"`
	Prerelease  bool            `json:"prerelease"`
	Author      string          `json:"author"`
	PublishedAt *string         `json:"published_at,omitempty"`
	Assets      []ImportedAsset `json:"assets"`
}

// ImportedAsset is a downloaded release asset.
type ImportedAsset struct {
	GitHubID    int64  `json:"github_id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type,omitempty"`
	Path        string `json:"path,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Status      string `json:"status"` // ok | failed
	Error       string `json:"error,omitempty"`
}

func (st store) release(ctx context.Context, repoID string, githubID int64) (string, bool, error) {
	var id string
	err := st.db.QueryRow(ctx, `SELECT id FROM import_releases WHERE repo_id = ? AND github_id = ?`, repoID, githubID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

func (st store) insertRelease(ctx context.Context, repo ledger.Repo, jobID string, r ghRelease, actor ledger.Actor) (string, error) {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	id := ledger.NewID("rel")
	created := r.CreatedAt
	if created == "" {
		created = ledger.Now()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO import_releases(id, org_id, repo_id, job_id, github_id, tag, name, body, draft, prerelease, author, html_url, created_at, published_at, imported_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, repo.OrgID, repo.ID, jobID, r.ID, r.TagName, r.Name, r.Body, boolInt(r.Draft), boolInt(r.Prerelease), r.Author.Login, r.HTMLURL, created, nullable(r.PublishedAt), ledger.Now()); err != nil {
		return "", err
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.release.imported", repo.Org+"/"+repo.Name+"/releases/"+r.TagName, actor,
		map[string]any{"release_id": id, "tag": r.TagName, "name": r.Name, "draft": r.Draft, "prerelease": r.Prerelease, "assets": len(r.Assets), "job_id": jobID, "github": map[string]any{"id": r.ID, "url": r.HTMLURL, "author": r.Author.Login}}); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (st store) assetDone(ctx context.Context, repoID string, githubID int64) (bool, error) {
	var n int
	err := st.db.QueryRow(ctx, `SELECT COUNT(1) FROM import_assets WHERE repo_id = ? AND github_id = ? AND status = 'ok'`, repoID, githubID).Scan(&n)
	return n > 0, err
}

func (st store) upsertAsset(ctx context.Context, repo ledger.Repo, releaseID string, a ghAsset, path, sum string, size int64, status, errText string) error {
	_, err := st.db.Exec(ctx, `INSERT INTO import_assets(id, org_id, repo_id, release_id, github_id, name, size, content_type, path, sha256, status, error, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(repo_id, github_id) DO UPDATE SET path = excluded.path, sha256 = excluded.sha256, size = excluded.size, status = excluded.status, error = excluded.error`,
		ledger.NewID("ast"), repo.OrgID, repo.ID, releaseID, a.ID, a.Name, size, a.ContentType, path, sum, status, errText, ledger.Now())
	return err
}

func (st store) releases(ctx context.Context, repoID string) ([]ImportedRelease, error) {
	rows, err := st.db.Query(ctx, `SELECT id, github_id, tag, name, draft, prerelease, author, published_at FROM import_releases WHERE repo_id = ? ORDER BY created_at`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImportedRelease
	for rows.Next() {
		var r ImportedRelease
		var draft, pre int
		if err := rows.Scan(&r.ID, &r.GitHubID, &r.Tag, &r.Name, &draft, &pre, &r.Author, &r.PublishedAt); err != nil {
			return nil, err
		}
		r.Draft, r.Prerelease = draft == 1, pre == 1
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		arows, err := st.db.Query(ctx, `SELECT github_id, name, size, content_type, path, sha256, status, error FROM import_assets WHERE release_id = ? ORDER BY name`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for arows.Next() {
			var a ImportedAsset
			if err := arows.Scan(&a.GitHubID, &a.Name, &a.Size, &a.ContentType, &a.Path, &a.SHA256, &a.Status, &a.Error); err != nil {
				arows.Close()
				return nil, err
			}
			out[i].Assets = append(out[i].Assets, a)
		}
		arows.Close()
	}
	return out, nil
}

// ImportedWebhook is a GitHub webhook recreated for Ore with a new secret, listed for rotation.
type ImportedWebhook struct {
	ID          string   `json:"id"`
	GitHubID    int64    `json:"github_id"`
	URL         string   `json:"url"`
	Events      []string `json:"events"`
	ContentType string   `json:"content_type"`
	Active      bool     `json:"active"`
	Secret      string   `json:"secret"`
	CreatedAt   string   `json:"created_at"`
}

func (st store) insertWebhook(ctx context.Context, repo ledger.Repo, jobID string, h ghHook, actor ledger.Actor) (ImportedWebhook, bool, error) {
	var n int
	if err := st.db.QueryRow(ctx, `SELECT COUNT(1) FROM import_webhooks WHERE repo_id = ? AND github_id = ?`, repo.ID, h.ID).Scan(&n); err != nil {
		return ImportedWebhook{}, false, err
	}
	if n > 0 {
		return ImportedWebhook{}, false, nil
	}
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return ImportedWebhook{}, false, err
	}
	defer tx.Rollback()
	w := ImportedWebhook{ID: ledger.NewID("whk"), GitHubID: h.ID, URL: h.Config.URL, Events: h.Events, ContentType: h.Config.ContentType, Active: h.Active, Secret: newSecret(), CreatedAt: ledger.Now()}
	if w.Events == nil {
		w.Events = []string{}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO import_webhooks(id, org_id, repo_id, job_id, github_id, url, events, content_type, active, secret, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		w.ID, repo.OrgID, repo.ID, jobID, w.GitHubID, w.URL, jsonString(w.Events), w.ContentType, boolInt(w.Active), w.Secret, w.CreatedAt); err != nil {
		return w, false, err
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.import.webhook.recreated", repo.Org+"/"+repo.Name+"/webhooks/"+w.ID, actor,
		map[string]any{"webhook_id": w.ID, "url": w.URL, "events": w.Events, "active": w.Active, "job_id": jobID, "github_hook_id": h.ID, "secret_rotated": true}); err != nil {
		return w, false, err
	}
	return w, true, tx.Commit()
}

func (st store) webhooks(ctx context.Context, repoID string) ([]ImportedWebhook, error) {
	rows, err := st.db.Query(ctx, `SELECT id, github_id, url, events, content_type, active, secret, created_at FROM import_webhooks WHERE repo_id = ? ORDER BY created_at`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImportedWebhook
	for rows.Next() {
		var w ImportedWebhook
		var events string
		var active int
		if err := rows.Scan(&w.ID, &w.GitHubID, &w.URL, &events, &w.ContentType, &active, &w.Secret, &w.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(events), &w.Events)
		w.Active = active == 1
		out = append(out, w)
	}
	return out, rows.Err()
}

// ImportedIdentity is a mannequin: a GitHub login seen in the import, to be
// claimed by an Ore user later.
type ImportedIdentity struct {
	ID              string   `json:"id"`
	Login           string   `json:"login"`
	GitHubID        int64    `json:"github_id"`
	Kind            string   `json:"kind"`
	Roles           []string `json:"roles"`
	Mentions        int      `json:"mentions"`
	SuggestedUserID string   `json:"suggested_user_id,omitempty"`
	ClaimedUserID   string   `json:"claimed_user_id,omitempty"`
}

func (st store) upsertIdentity(ctx context.Context, repo ledger.Repo, id ImportedIdentity, actor ledger.Actor) error {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := ledger.Now()
	newID := ledger.NewID("man")
	if id.Roles == nil {
		id.Roles = []string{}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO import_identities(id, org_id, login, github_id, kind, roles, mentions, suggested_user_id, claimed_user_id, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(org_id, login) DO UPDATE SET github_id = CASE WHEN excluded.github_id > 0 THEN excluded.github_id ELSE import_identities.github_id END, kind = excluded.kind, roles = excluded.roles, mentions = excluded.mentions, suggested_user_id = excluded.suggested_user_id, updated_at = excluded.updated_at`,
		newID, repo.OrgID, id.Login, id.GitHubID, id.Kind, jsonString(id.Roles), id.Mentions, id.SuggestedUserID, "", now, now); err != nil {
		return err
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.import.identity.recorded", "orgs/"+repo.Org+"/identities/github:"+id.Login, actor,
		map[string]any{"login": id.Login, "github_id": id.GitHubID, "roles": id.Roles, "mentions": id.Mentions, "suggested_user_id": id.SuggestedUserID, "claimable": true}); err != nil {
		return err
	}
	return tx.Commit()
}

func (st store) identities(ctx context.Context, orgID string) ([]ImportedIdentity, error) {
	rows, err := st.db.Query(ctx, `SELECT id, login, github_id, kind, roles, mentions, suggested_user_id, claimed_user_id FROM import_identities WHERE org_id = ? ORDER BY login`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImportedIdentity
	for rows.Next() {
		var i ImportedIdentity
		var roles string
		if err := rows.Scan(&i.ID, &i.Login, &i.GitHubID, &i.Kind, &roles, &i.Mentions, &i.SuggestedUserID, &i.ClaimedUserID); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(roles), &i.Roles)
		out = append(out, i)
	}
	return out, rows.Err()
}

// parseTime reads GitHub's RFC 3339 timestamps.
func parseTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	return t, err == nil
}

// freeNumberAbove is freeNumber with a floor: a taken number is replaced by
// one above both the table's maximum and floor, so an import that must
// renumber one object never takes a number a later object still needs.
func freeNumberAbove(ctx context.Context, tx *ledger.Tx, table, repoID string, preferred, floor int) (int, error) {
	var n int
	if preferred > 0 {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM `+table+` WHERE repo_id = ? AND number = ?`, repoID, preferred).Scan(&n); err != nil {
			return 0, err
		}
		if n == 0 {
			return preferred, nil
		}
	}
	var m sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(number) FROM `+table+` WHERE repo_id = ?`, repoID).Scan(&m); err != nil {
		return 0, err
	}
	next := int(m.Int64) + 1
	if floor+1 > next {
		next = floor + 1
	}
	return next, nil
}
