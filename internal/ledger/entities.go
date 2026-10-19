// SPDX-License-Identifier: AGPL-3.0-only

package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrNotFound is returned when an entity does not exist.
var ErrNotFound = errors.New("not found")

// Org is a tenant.
type Org struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

// Repo is a repository.
type Repo struct {
	ID            string `json:"id"`
	OrgID         string `json:"org_id"`
	Org           string `json:"org"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	AutonomyLevel int    `json:"autonomy_level"`
	DeployPort    int    `json:"deploy_port"`
	CreatedAt     string `json:"created_at"`
}

// Issue is a signal for work.
type Issue struct {
	ID        string  `json:"id"`
	OrgID     string  `json:"org_id"`
	RepoID    string  `json:"repo_id"`
	Number    int     `json:"number"`
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	State     string  `json:"state"`
	CreatedAt string  `json:"created_at"`
	ClosedAt  *string `json:"closed_at,omitempty"`
}

// Change is the unit of review (ADR 0008).
type Change struct {
	ID         string  `json:"id"`
	OrgID      string  `json:"org_id"`
	RepoID     string  `json:"repo_id"`
	Number     int     `json:"number"`
	ChangeID   string  `json:"change_id"`
	Title      string  `json:"title"`
	Body       string  `json:"body"`
	SourceRef  string  `json:"source_ref"`
	TargetRef  string  `json:"target_ref"`
	State      string  `json:"state"`
	BaseOID    string  `json:"base_oid"`
	HeadOID    string  `json:"head_oid"`
	MergeOID   *string `json:"merge_oid,omitempty"`
	Provenance string  `json:"provenance"`
	CreatedAt  string  `json:"created_at"`
	MergedAt   *string `json:"merged_at,omitempty"`
}

// PipelineRun is one execution of a repository's pipeline on a commit.
type PipelineRun struct {
	ID         string  `json:"id"`
	OrgID      string  `json:"org_id"`
	RepoID     string  `json:"repo_id"`
	ChangeID   *string `json:"change_id,omitempty"`
	CommitOID  string  `json:"commit_oid"`
	Status     string  `json:"status"`
	Tasks      string  `json:"tasks"`
	LogPath    string  `json:"-"`
	CreatedAt  string  `json:"created_at"`
	StartedAt  *string `json:"started_at,omitempty"`
	FinishedAt *string `json:"finished_at,omitempty"`
}

// Deployment is a release placed into an environment.
type Deployment struct {
	ID          string  `json:"id"`
	OrgID       string  `json:"org_id"`
	RepoID      string  `json:"repo_id"`
	Environment string  `json:"environment"`
	CommitOID   string  `json:"commit_oid"`
	Image       string  `json:"image"`
	ContainerID string  `json:"container_id"`
	URL         string  `json:"url"`
	Status      string  `json:"status"`
	PreviousID  *string `json:"previous_id,omitempty"`
	RollbackOf  *string `json:"rollback_of,omitempty"`
	Actor       Actor   `json:"actor"`
	CreatedAt   string  `json:"created_at"`
	FinishedAt  *string `json:"finished_at,omitempty"`
}

// AgentRun is one pass of the loop (docs/12).
type AgentRun struct {
	ID            string  `json:"id"`
	OrgID         string  `json:"org_id"`
	RepoID        string  `json:"repo_id"`
	IssueID       *string `json:"issue_id,omitempty"`
	Adapter       string  `json:"adapter"`
	Status        string  `json:"status"`
	Step          string  `json:"step"`
	Branch        string  `json:"branch"`
	ChangeID      *string `json:"change_id,omitempty"`
	PipelineRunID *string `json:"pipeline_run_id,omitempty"`
	DeploymentID  *string `json:"deployment_id,omitempty"`
	LogPath       string  `json:"-"`
	Error         string  `json:"error,omitempty"`
	CreatedAt     string  `json:"created_at"`
	FinishedAt    *string `json:"finished_at,omitempty"`
}

// CreateOrg creates a tenant and emits io.ore.org.created.
func (d *DB) CreateOrg(ctx context.Context, name string, actor Actor) (Org, error) {
	tx, err := d.Begin(ctx)
	if err != nil {
		return Org{}, err
	}
	defer tx.Rollback()
	o := Org{ID: NewID("org"), Name: name, CreatedAt: Now()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO orgs(id, name, created_at) VALUES(?,?,?)`, o.ID, o.Name, o.CreatedAt); err != nil {
		return Org{}, fmt.Errorf("create org: %w", err)
	}
	if _, err := tx.Emit(o.ID, "io.ore.org.created", "orgs/"+o.Name, actor, o); err != nil {
		return Org{}, err
	}
	return o, tx.Commit()
}

// OrgByName looks up a tenant.
func (d *DB) OrgByName(ctx context.Context, name string) (Org, error) {
	var o Org
	err := d.QueryRow(ctx, `SELECT id, name, created_at FROM orgs WHERE name = ?`, name).Scan(&o.ID, &o.Name, &o.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	return o, err
}

// Orgs lists tenants.
func (d *DB) Orgs(ctx context.Context) ([]Org, error) {
	rows, err := d.Query(ctx, `SELECT id, name, created_at FROM orgs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Org
	for rows.Next() {
		var o Org
		if err := rows.Scan(&o.ID, &o.Name, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// CreateRepo records a repository (the caller initializes the git store) and emits io.ore.repository.created.
func (d *DB) CreateRepo(ctx context.Context, org Org, name, defaultBranch string, level, port int, actor Actor) (Repo, error) {
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	if port == 0 {
		port = 8080
	}
	tx, err := d.Begin(ctx)
	if err != nil {
		return Repo{}, err
	}
	defer tx.Rollback()
	r := Repo{ID: NewID("repo"), OrgID: org.ID, Org: org.Name, Name: name, DefaultBranch: defaultBranch, AutonomyLevel: level, DeployPort: port, CreatedAt: Now()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO repos(id, org_id, name, default_branch, autonomy_level, deploy_port, created_at) VALUES(?,?,?,?,?,?,?)`,
		r.ID, r.OrgID, r.Name, r.DefaultBranch, r.AutonomyLevel, r.DeployPort, r.CreatedAt); err != nil {
		return Repo{}, fmt.Errorf("create repo: %w", err)
	}
	if _, err := tx.Emit(org.ID, "io.ore.repository.created", r.Org+"/"+r.Name, actor, r); err != nil {
		return Repo{}, err
	}
	return r, tx.Commit()
}

const repoCols = `r.id, r.org_id, o.name, r.name, r.default_branch, r.autonomy_level, r.deploy_port, r.created_at`

func scanRepo(s interface{ Scan(...any) error }) (Repo, error) {
	var r Repo
	err := s.Scan(&r.ID, &r.OrgID, &r.Org, &r.Name, &r.DefaultBranch, &r.AutonomyLevel, &r.DeployPort, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// RepoByName finds org/name.
func (d *DB) RepoByName(ctx context.Context, org, name string) (Repo, error) {
	return scanRepo(d.QueryRow(ctx, `SELECT `+repoCols+` FROM repos r JOIN orgs o ON o.id = r.org_id WHERE o.name = ? AND r.name = ?`, org, name))
}

// RepoByID finds a repository by id.
func (d *DB) RepoByID(ctx context.Context, id string) (Repo, error) {
	return scanRepo(d.QueryRow(ctx, `SELECT `+repoCols+` FROM repos r JOIN orgs o ON o.id = r.org_id WHERE r.id = ?`, id))
}

// Repos lists repositories, optionally for one org.
func (d *DB) Repos(ctx context.Context, org string) ([]Repo, error) {
	q := `SELECT ` + repoCols + ` FROM repos r JOIN orgs o ON o.id = r.org_id`
	var args []any
	if org != "" {
		q += ` WHERE o.name = ?`
		args = append(args, org)
	}
	q += ` ORDER BY o.name, r.name`
	rows, err := d.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetAutonomyLevel changes a repository's level (ADR 0015) and records why.
func (d *DB) SetAutonomyLevel(ctx context.Context, r Repo, level int, reason string, actor Actor) error {
	if level < 0 || level > 5 {
		return fmt.Errorf("autonomy level must be 0..5")
	}
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE repos SET autonomy_level = ? WHERE id = ?`, level, r.ID); err != nil {
		return err
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.repository.autonomy.changed", r.Org+"/"+r.Name, actor, map[string]any{"from": r.AutonomyLevel, "to": level, "reason": reason}); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordRefUpdate writes a ref update and emits io.ore.ref.updated (or .deleted).
func (d *DB) RecordRefUpdate(ctx context.Context, r Repo, ref, oldOID, newOID, via string, actor Actor) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := recordRefUpdateTx(tx, r, ref, oldOID, newOID, via, actor); err != nil {
		return err
	}
	return tx.Commit()
}

func recordRefUpdateTx(tx *Tx, r Repo, ref, oldOID, newOID, via string, actor Actor) error {
	id := NewID("refu")
	if _, err := tx.ExecContext(tx.ctx, `INSERT INTO ref_updates(id, org_id, repo_id, ref, old_oid, new_oid, actor_kind, actor_id, via, created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		id, r.OrgID, r.ID, ref, oldOID, newOID, actor.Kind, actor.ID, via, Now()); err != nil {
		return err
	}
	typ := "io.ore.ref.updated"
	if newOID == ZeroOID {
		typ = "io.ore.ref.deleted"
	}
	_, err := tx.Emit(r.OrgID, typ, r.Org+"/"+r.Name+"/"+ref, actor, map[string]any{"repo_id": r.ID, "ref": ref, "old": oldOID, "new": newOID, "via": via})
	return err
}

// ZeroOID is git's null object id (SHA-1 width; SHA-256 repositories use 64 zeros).
const ZeroOID = "0000000000000000000000000000000000000000"

// NextNumber allocates the next per-repository number for a table (issues, changes).
func nextNumber(tx *Tx, table, repoID string) (int, error) {
	var n sql.NullInt64
	if err := tx.QueryRowContext(tx.ctx, `SELECT MAX(number) FROM `+table+` WHERE repo_id = ?`, repoID).Scan(&n); err != nil {
		return 0, err
	}
	return int(n.Int64) + 1, nil
}

// CreateIssue files a signal and emits io.ore.issue.opened.
func (d *DB) CreateIssue(ctx context.Context, r Repo, title, body string, actor Actor) (Issue, error) {
	tx, err := d.Begin(ctx)
	if err != nil {
		return Issue{}, err
	}
	defer tx.Rollback()
	n, err := nextNumber(tx, "issues", r.ID)
	if err != nil {
		return Issue{}, err
	}
	i := Issue{ID: NewID("iss"), OrgID: r.OrgID, RepoID: r.ID, Number: n, Title: title, Body: body, State: "open", CreatedAt: Now()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO issues(id, org_id, repo_id, number, title, body, state, created_at) VALUES(?,?,?,?,?,?,?,?)`,
		i.ID, i.OrgID, i.RepoID, i.Number, i.Title, i.Body, i.State, i.CreatedAt); err != nil {
		return Issue{}, err
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.issue.opened", fmt.Sprintf("%s/%s/issues/%d", r.Org, r.Name, n), actor, i); err != nil {
		return Issue{}, err
	}
	return i, tx.Commit()
}

// Issue fetches by number.
func (d *DB) Issue(ctx context.Context, r Repo, number int) (Issue, error) {
	var i Issue
	err := d.QueryRow(ctx, `SELECT id, org_id, repo_id, number, title, body, state, created_at, closed_at FROM issues WHERE repo_id = ? AND number = ?`, r.ID, number).
		Scan(&i.ID, &i.OrgID, &i.RepoID, &i.Number, &i.Title, &i.Body, &i.State, &i.CreatedAt, &i.ClosedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return i, ErrNotFound
	}
	return i, err
}

// Issues lists a repository's issues.
func (d *DB) Issues(ctx context.Context, r Repo) ([]Issue, error) {
	rows, err := d.Query(ctx, `SELECT id, org_id, repo_id, number, title, body, state, created_at, closed_at FROM issues WHERE repo_id = ? ORDER BY number DESC`, r.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Issue
	for rows.Next() {
		var i Issue
		if err := rows.Scan(&i.ID, &i.OrgID, &i.RepoID, &i.Number, &i.Title, &i.Body, &i.State, &i.CreatedAt, &i.ClosedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// CloseIssue closes with a reason and emits io.ore.issue.closed.
func (d *DB) CloseIssue(ctx context.Context, r Repo, i Issue, reason string, actor Actor) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := Now()
	if _, err := tx.ExecContext(ctx, `UPDATE issues SET state = 'closed', closed_at = ? WHERE id = ?`, now, i.ID); err != nil {
		return err
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.issue.closed", fmt.Sprintf("%s/%s/issues/%d", r.Org, r.Name, i.Number), actor, map[string]any{"issue_id": i.ID, "reason": reason}); err != nil {
		return err
	}
	return tx.Commit()
}

// InsertChange persists a change created by the review package and emits io.ore.change.opened.
func (d *DB) InsertChange(ctx context.Context, r Repo, c *Change, actor Actor) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	n, err := nextNumber(tx, "changes", r.ID)
	if err != nil {
		return err
	}
	c.ID, c.OrgID, c.RepoID, c.Number, c.State, c.CreatedAt = NewID("chg"), r.OrgID, r.ID, n, "open", Now()
	if c.Provenance == "" {
		c.Provenance = "{}"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO changes(id, org_id, repo_id, number, change_id, title, body, source_ref, target_ref, state, base_oid, head_oid, provenance, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, c.ID, c.OrgID, c.RepoID, c.Number, c.ChangeID, c.Title, c.Body, c.SourceRef, c.TargetRef, c.State, c.BaseOID, c.HeadOID, c.Provenance, c.CreatedAt); err != nil {
		return err
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.change.opened", fmt.Sprintf("%s/%s/changes/%d", r.Org, r.Name, n), actor, c); err != nil {
		return err
	}
	return tx.Commit()
}

const changeCols = `id, org_id, repo_id, number, change_id, title, body, source_ref, target_ref, state, base_oid, head_oid, merge_oid, provenance, created_at, merged_at`

func scanChange(s interface{ Scan(...any) error }) (Change, error) {
	var c Change
	err := s.Scan(&c.ID, &c.OrgID, &c.RepoID, &c.Number, &c.ChangeID, &c.Title, &c.Body, &c.SourceRef, &c.TargetRef, &c.State, &c.BaseOID, &c.HeadOID, &c.MergeOID, &c.Provenance, &c.CreatedAt, &c.MergedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// Change fetches by number.
func (d *DB) Change(ctx context.Context, r Repo, number int) (Change, error) {
	return scanChange(d.QueryRow(ctx, `SELECT `+changeCols+` FROM changes WHERE repo_id = ? AND number = ?`, r.ID, number))
}

// Changes lists a repository's changes.
func (d *DB) Changes(ctx context.Context, r Repo) ([]Change, error) {
	rows, err := d.Query(ctx, `SELECT `+changeCols+` FROM changes WHERE repo_id = ? ORDER BY number DESC`, r.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Change
	for rows.Next() {
		c, err := scanChange(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkMerged records a server-side merge: the change, the ref update and the event, atomically.
func (d *DB) MarkMerged(ctx context.Context, r Repo, c Change, mergeOID, oldTargetOID string, actor Actor) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := Now()
	if _, err := tx.ExecContext(ctx, `UPDATE changes SET state = 'merged', merge_oid = ?, merged_at = ? WHERE id = ?`, mergeOID, now, c.ID); err != nil {
		return err
	}
	if err := recordRefUpdateTx(tx, r, c.TargetRef, oldTargetOID, mergeOID, "merge", actor); err != nil {
		return err
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.change.merged", fmt.Sprintf("%s/%s/changes/%d", r.Org, r.Name, c.Number), actor,
		map[string]any{"change_id": c.ChangeID, "merge_oid": mergeOID, "target_ref": c.TargetRef, "head_oid": c.HeadOID, "provenance": c.Provenance}); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateChangeHead records a new patch set (head moved) and emits io.ore.change.patchset.pushed.
func (d *DB) UpdateChangeHead(ctx context.Context, r Repo, c Change, headOID string, actor Actor) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE changes SET head_oid = ? WHERE id = ?`, headOID, c.ID); err != nil {
		return err
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.change.patchset.pushed", fmt.Sprintf("%s/%s/changes/%d", r.Org, r.Name, c.Number), actor, map[string]any{"change_id": c.ChangeID, "old_head": c.HeadOID, "new_head": headOID}); err != nil {
		return err
	}
	return tx.Commit()
}

// CloseChange closes without merging.
func (d *DB) CloseChange(ctx context.Context, r Repo, c Change, reason string, actor Actor) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE changes SET state = 'closed' WHERE id = ?`, c.ID); err != nil {
		return err
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.change.closed", fmt.Sprintf("%s/%s/changes/%d", r.Org, r.Name, c.Number), actor, map[string]any{"change_id": c.ChangeID, "reason": reason}); err != nil {
		return err
	}
	return tx.Commit()
}

// OpenChangeBySourceRef finds an open change for a branch, if any.
func (d *DB) OpenChangeBySourceRef(ctx context.Context, r Repo, sourceRef string) (Change, error) {
	return scanChange(d.QueryRow(ctx, `SELECT `+changeCols+` FROM changes WHERE repo_id = ? AND source_ref = ? AND state = 'open'`, r.ID, sourceRef))
}

// InsertPipelineRun records a queued run and emits io.ore.pipeline.run.queued.
func (d *DB) InsertPipelineRun(ctx context.Context, r Repo, run *PipelineRun, actor Actor) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	run.ID, run.OrgID, run.RepoID, run.Status, run.CreatedAt = NewID("prun"), r.OrgID, r.ID, "queued", Now()
	if run.Tasks == "" {
		run.Tasks = "[]"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pipeline_runs(id, org_id, repo_id, change_id, commit_oid, status, tasks, log_path, created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		run.ID, run.OrgID, run.RepoID, run.ChangeID, run.CommitOID, run.Status, run.Tasks, run.LogPath, run.CreatedAt); err != nil {
		return err
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.pipeline.run.queued", r.Org+"/"+r.Name+"/runs/"+run.ID, actor, run); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdatePipelineRun sets status/tasks and emits the matching lifecycle event.
func (d *DB) UpdatePipelineRun(ctx context.Context, r Repo, run *PipelineRun, status, tasksJSON string, actor Actor) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := Now()
	run.Status = status
	if tasksJSON != "" {
		run.Tasks = tasksJSON
	}
	typ := "io.ore.pipeline.run.started"
	switch status {
	case "running":
		run.StartedAt = &now
		if _, err := tx.ExecContext(ctx, `UPDATE pipeline_runs SET status = ?, started_at = ?, log_path = ? WHERE id = ?`, status, now, run.LogPath, run.ID); err != nil {
			return err
		}
	default:
		run.FinishedAt = &now
		typ = "io.ore.pipeline.run.finished"
		if _, err := tx.ExecContext(ctx, `UPDATE pipeline_runs SET status = ?, tasks = ?, finished_at = ?, log_path = ? WHERE id = ?`, status, run.Tasks, now, run.LogPath, run.ID); err != nil {
			return err
		}
	}
	if _, err := tx.Emit(r.OrgID, typ, r.Org+"/"+r.Name+"/runs/"+run.ID, actor, run); err != nil {
		return err
	}
	return tx.Commit()
}

const runCols = `id, org_id, repo_id, change_id, commit_oid, status, tasks, log_path, created_at, started_at, finished_at`

func scanRun(s interface{ Scan(...any) error }) (PipelineRun, error) {
	var p PipelineRun
	err := s.Scan(&p.ID, &p.OrgID, &p.RepoID, &p.ChangeID, &p.CommitOID, &p.Status, &p.Tasks, &p.LogPath, &p.CreatedAt, &p.StartedAt, &p.FinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// PipelineRun fetches by id.
func (d *DB) PipelineRun(ctx context.Context, id string) (PipelineRun, error) {
	return scanRun(d.QueryRow(ctx, `SELECT `+runCols+` FROM pipeline_runs WHERE id = ?`, id))
}

// PipelineRuns lists a repository's runs, newest first.
func (d *DB) PipelineRuns(ctx context.Context, r Repo, limit int) ([]PipelineRun, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.Query(ctx, `SELECT `+runCols+` FROM pipeline_runs WHERE repo_id = ? ORDER BY created_at DESC LIMIT ?`, r.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PipelineRun
	for rows.Next() {
		p, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// InsertDeployment records a requested deployment and emits io.ore.deployment.requested.
func (d *DB) InsertDeployment(ctx context.Context, r Repo, dep *Deployment) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	dep.ID, dep.OrgID, dep.RepoID, dep.Status, dep.CreatedAt = NewID("dep"), r.OrgID, r.ID, "requested", Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployments(id, org_id, repo_id, environment, commit_oid, image, container_id, url, status, previous_id, rollback_of, actor_kind, actor_id, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, dep.ID, dep.OrgID, dep.RepoID, dep.Environment, dep.CommitOID, dep.Image, dep.ContainerID, dep.URL, dep.Status, dep.PreviousID, dep.RollbackOf, dep.Actor.Kind, dep.Actor.ID, dep.CreatedAt); err != nil {
		return err
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.deployment.requested", r.Org+"/"+r.Name+"/deployments/"+dep.ID, dep.Actor, dep); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateDeployment sets status and runtime details, emitting started/succeeded/failed/stopped/rolled_back.
func (d *DB) UpdateDeployment(ctx context.Context, r Repo, dep *Deployment, status string) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	dep.Status = status
	now := Now()
	if status != "started" {
		dep.FinishedAt = &now
	}
	if _, err := tx.ExecContext(ctx, `UPDATE deployments SET status = ?, image = ?, container_id = ?, url = ?, finished_at = ? WHERE id = ?`, dep.Status, dep.Image, dep.ContainerID, dep.URL, dep.FinishedAt, dep.ID); err != nil {
		return err
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.deployment."+status, r.Org+"/"+r.Name+"/deployments/"+dep.ID, dep.Actor, dep); err != nil {
		return err
	}
	return tx.Commit()
}

const depCols = `id, org_id, repo_id, environment, commit_oid, image, container_id, url, status, previous_id, rollback_of, actor_kind, actor_id, created_at, finished_at`

func scanDep(s interface{ Scan(...any) error }) (Deployment, error) {
	var p Deployment
	err := s.Scan(&p.ID, &p.OrgID, &p.RepoID, &p.Environment, &p.CommitOID, &p.Image, &p.ContainerID, &p.URL, &p.Status, &p.PreviousID, &p.RollbackOf, &p.Actor.Kind, &p.Actor.ID, &p.CreatedAt, &p.FinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// Deployment fetches by id.
func (d *DB) Deployment(ctx context.Context, id string) (Deployment, error) {
	return scanDep(d.QueryRow(ctx, `SELECT `+depCols+` FROM deployments WHERE id = ?`, id))
}

// CurrentDeployment returns the latest succeeded deployment for an environment.
func (d *DB) CurrentDeployment(ctx context.Context, r Repo, env string) (Deployment, error) {
	return scanDep(d.QueryRow(ctx, `SELECT `+depCols+` FROM deployments WHERE repo_id = ? AND environment = ? AND status = 'succeeded' ORDER BY created_at DESC LIMIT 1`, r.ID, env))
}

// Deployments lists a repository's deployments, newest first.
func (d *DB) Deployments(ctx context.Context, r Repo, limit int) ([]Deployment, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.Query(ctx, `SELECT `+depCols+` FROM deployments WHERE repo_id = ? ORDER BY created_at DESC LIMIT ?`, r.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Deployment
	for rows.Next() {
		p, err := scanDep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// InsertAgentRun records the start of a loop pass and emits io.ore.agent.session.started.
func (d *DB) InsertAgentRun(ctx context.Context, r Repo, run *AgentRun, actor Actor) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	run.ID, run.OrgID, run.RepoID, run.Status, run.Step, run.CreatedAt = NewID("arun"), r.OrgID, r.ID, "running", "signal", Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_runs(id, org_id, repo_id, issue_id, adapter, status, step, branch, log_path, created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		run.ID, run.OrgID, run.RepoID, run.IssueID, run.Adapter, run.Status, run.Step, run.Branch, run.LogPath, run.CreatedAt); err != nil {
		return err
	}
	if _, err := tx.Emit(r.OrgID, "io.ore.agent.session.started", r.Org+"/"+r.Name+"/agent/"+run.ID, actor, run); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateAgentRun persists progress and emits io.ore.agent.session.<status> or .step.
func (d *DB) UpdateAgentRun(ctx context.Context, r Repo, run *AgentRun, actor Actor) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if run.Status != "running" && run.FinishedAt == nil {
		now := Now()
		run.FinishedAt = &now
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, step = ?, branch = ?, change_id = ?, pipeline_run_id = ?, deployment_id = ?, log_path = ?, error = ?, finished_at = ? WHERE id = ?`,
		run.Status, run.Step, run.Branch, run.ChangeID, run.PipelineRunID, run.DeploymentID, run.LogPath, run.Error, run.FinishedAt, run.ID); err != nil {
		return err
	}
	typ := "io.ore.agent.session.step"
	if run.Status != "running" {
		typ = "io.ore.agent.session." + run.Status
	}
	if _, err := tx.Emit(r.OrgID, typ, r.Org+"/"+r.Name+"/agent/"+run.ID, actor, run); err != nil {
		return err
	}
	return tx.Commit()
}

const arunCols = `id, org_id, repo_id, issue_id, adapter, status, step, branch, change_id, pipeline_run_id, deployment_id, log_path, error, created_at, finished_at`

func scanARun(s interface{ Scan(...any) error }) (AgentRun, error) {
	var a AgentRun
	err := s.Scan(&a.ID, &a.OrgID, &a.RepoID, &a.IssueID, &a.Adapter, &a.Status, &a.Step, &a.Branch, &a.ChangeID, &a.PipelineRunID, &a.DeploymentID, &a.LogPath, &a.Error, &a.CreatedAt, &a.FinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// AgentRun fetches by id.
func (d *DB) AgentRun(ctx context.Context, id string) (AgentRun, error) {
	return scanARun(d.QueryRow(ctx, `SELECT `+arunCols+` FROM agent_runs WHERE id = ?`, id))
}

// AgentRuns lists a repository's loop passes, newest first.
func (d *DB) AgentRuns(ctx context.Context, r Repo, limit int) ([]AgentRun, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.Query(ctx, `SELECT `+arunCols+` FROM agent_runs WHERE repo_id = ? ORDER BY created_at DESC LIMIT ?`, r.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentRun
	for rows.Next() {
		a, err := scanARun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
