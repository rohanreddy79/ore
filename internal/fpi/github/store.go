// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

// Link ties an Ore repository to a GitHub repository.
type Link struct {
	ID             string       `json:"id"`
	OrgID          string       `json:"org_id"`
	RepoID         string       `json:"repo_id"`
	Owner          string       `json:"owner"`
	Name           string       `json:"name"`
	GitHubID       int64        `json:"github_id"`
	Secret         string       `json:"secret"`
	InstallationID int64        `json:"installation_id"`
	CloneURL       string       `json:"clone_url"`
	HTMLURL        string       `json:"html_url"`
	DefaultBranch  string       `json:"default_branch"`
	LastSyncAt     *string      `json:"last_sync_at,omitempty"`
	LastSyncStatus string       `json:"last_sync_status"`
	LastSyncError  string       `json:"last_sync_error,omitempty"`
	NextSyncAt     *string      `json:"next_sync_at,omitempty"`
	Actor          ledger.Actor `json:"actor"`
	CreatedAt      string       `json:"created_at"`
}

// FullName returns owner/name.
func (l Link) FullName() string { return l.Owner + "/" + l.Name }

// Redacted returns the link without its secret (for listings).
func (l Link) Redacted() Link { l.Secret = ""; return l }

type store struct{ db *ledger.DB }

const linkCols = `id, org_id, repo_id, owner, name, github_id, secret, installation_id, clone_url, html_url, default_branch, last_sync_at, last_sync_status, last_sync_error, next_sync_at, actor_kind, actor_id, created_at`

func scanLink(s interface{ Scan(...any) error }) (Link, error) {
	var l Link
	err := s.Scan(&l.ID, &l.OrgID, &l.RepoID, &l.Owner, &l.Name, &l.GitHubID, &l.Secret, &l.InstallationID, &l.CloneURL, &l.HTMLURL, &l.DefaultBranch,
		&l.LastSyncAt, &l.LastSyncStatus, &l.LastSyncError, &l.NextSyncAt, &l.Actor.Kind, &l.Actor.ID, &l.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ledger.ErrNotFound
	}
	return l, err
}

// insertLink records a link (and the repository's new default branch when GitHub's differs) with io.ore.github.linked.
func (st store) insertLink(ctx context.Context, repo ledger.Repo, l *Link, newDefaultBranch string, actor ledger.Actor) error {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	l.ID, l.OrgID, l.RepoID, l.Actor, l.CreatedAt = ledger.NewID("ghl"), repo.OrgID, repo.ID, actor, ledger.Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO github_links(`+linkCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		l.ID, l.OrgID, l.RepoID, l.Owner, l.Name, l.GitHubID, l.Secret, l.InstallationID, l.CloneURL, l.HTMLURL, l.DefaultBranch,
		l.LastSyncAt, l.LastSyncStatus, l.LastSyncError, l.NextSyncAt, l.Actor.Kind, l.Actor.ID, l.CreatedAt); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return errAlreadyLinked
		}
		return fmt.Errorf("github: insert link: %w", err)
	}
	data := map[string]any{"link_id": l.ID, "repo_id": repo.ID, "github": l.FullName(), "github_id": l.GitHubID, "clone_url": l.CloneURL, "default_branch": l.DefaultBranch}
	if newDefaultBranch != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE repos SET default_branch = ? WHERE id = ?`, newDefaultBranch, repo.ID); err != nil {
			return err
		}
		data["ore_default_branch_was"] = repo.DefaultBranch
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.github.linked", repo.Org+"/"+repo.Name+"/github", actor, data); err != nil {
		return err
	}
	return tx.Commit()
}

var errAlreadyLinked = errors.New("repository is already linked to GitHub")

func (st store) deleteLink(ctx context.Context, repo ledger.Repo, l Link, actor ledger.Actor) error {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM github_links WHERE id = ?`, l.ID); err != nil {
		return err
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.github.unlinked", repo.Org+"/"+repo.Name+"/github", actor, map[string]any{"link_id": l.ID, "repo_id": repo.ID, "github": l.FullName()}); err != nil {
		return err
	}
	return tx.Commit()
}

func (st store) linkByRepo(ctx context.Context, repoID string) (Link, error) {
	return scanLink(st.db.QueryRow(ctx, `SELECT `+linkCols+` FROM github_links WHERE repo_id = ?`, repoID))
}

func (st store) links(ctx context.Context, q string, args ...any) ([]Link, error) {
	rows, err := st.db.Query(ctx, `SELECT `+linkCols+` FROM github_links `+q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// linksByFullName matches owner/name case-insensitively (GitHub names are).
func (st store) linksByFullName(ctx context.Context, owner, name string) ([]Link, error) {
	return st.links(ctx, `WHERE lower(owner) = lower(?) AND lower(name) = lower(?) ORDER BY created_at`, owner, name)
}

func (st store) allLinks(ctx context.Context) ([]Link, error) {
	return st.links(ctx, `ORDER BY created_at`)
}

func (st store) setInstallation(ctx context.Context, l Link, installation int64) error {
	_, err := st.db.Exec(ctx, `UPDATE github_links SET installation_id = ? WHERE id = ?`, installation, l.ID)
	return err
}

// updateSync records a sync outcome: status ok | deferred | failed, with an optional next attempt.
func (st store) updateSync(ctx context.Context, l Link, status, errText string, at time.Time, next *time.Time) error {
	var nextS *string
	if next != nil {
		s := next.UTC().Format("2006-01-02T15:04:05.000Z07:00")
		nextS = &s
	}
	_, err := st.db.Exec(ctx, `UPDATE github_links SET last_sync_at = ?, last_sync_status = ?, last_sync_error = ?, next_sync_at = ? WHERE id = ?`,
		at.UTC().Format("2006-01-02T15:04:05.000Z07:00"), status, errText, nextS, l.ID)
	return err
}

// ---- issues ----

type issueMapping struct {
	GitHubNumber int
	GitHubID     int64
	IssueID      string
	IssueNumber  int
	HTMLURL      string
}

func (st store) issueMapping(ctx context.Context, repoID string, number int) (issueMapping, bool, error) {
	var m issueMapping
	err := st.db.QueryRow(ctx, `SELECT github_number, github_id, issue_id, issue_number, html_url FROM github_issue_map WHERE repo_id = ? AND github_number = ?`, repoID, number).
		Scan(&m.GitHubNumber, &m.GitHubID, &m.IssueID, &m.IssueNumber, &m.HTMLURL)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	return m, err == nil, err
}

// freeNumber returns preferred when no row in table has it for the repository, else MAX+1.
func freeNumber(ctx context.Context, tx *ledger.Tx, table, repoID string, preferred int) (int, error) {
	var n int
	if preferred > 0 {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM `+table+` WHERE repo_id = ? AND number = ?`, repoID, preferred).Scan(&n); err != nil {
			return 0, err
		}
		if n == 0 {
			return preferred, nil
		}
	}
	if err := tx.LockRow("repos", repoID); err != nil {
		return 0, err
	}
	var m sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(number) FROM `+table+` WHERE repo_id = ?`, repoID).Scan(&m); err != nil {
		return 0, err
	}
	return int(m.Int64) + 1, nil
}

// createIssue mirrors a GitHub issue: the Ore issue keeps the GitHub number
// when it is free (docs/10 §2), the mapping is written in the same
// transaction, and io.ore.issue.opened is emitted.
func (st store) createIssue(ctx context.Context, repo ledger.Repo, ev IssueEvent, actor ledger.Actor) (ledger.Issue, error) {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return ledger.Issue{}, err
	}
	defer tx.Rollback()
	n, err := freeNumber(ctx, tx, "issues", repo.ID, ev.Number)
	if err != nil {
		return ledger.Issue{}, err
	}
	now := ledger.Now()
	i := ledger.Issue{ID: ledger.NewID("iss"), OrgID: repo.OrgID, RepoID: repo.ID, Number: n, Title: ev.Title, Body: ev.Body, State: "open", CreatedAt: now}
	if ev.State == "closed" {
		i.State = "closed"
		i.ClosedAt = &now
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO issues(id, org_id, repo_id, number, title, body, state, created_at, closed_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		i.ID, i.OrgID, i.RepoID, i.Number, i.Title, i.Body, i.State, i.CreatedAt, i.ClosedAt); err != nil {
		return ledger.Issue{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO github_issue_map(org_id, repo_id, github_number, github_id, issue_id, issue_number, html_url, updated_at) VALUES(?,?,?,?,?,?,?,?)`,
		repo.OrgID, repo.ID, ev.Number, ev.ID, i.ID, i.Number, ev.URL, now); err != nil {
		return ledger.Issue{}, err
	}
	data := map[string]any{"id": i.ID, "org_id": i.OrgID, "repo_id": i.RepoID, "number": i.Number, "title": i.Title, "body": i.Body, "state": i.State, "created_at": i.CreatedAt,
		"github": map[string]any{"number": ev.Number, "id": ev.ID, "url": ev.URL, "author": ev.Author}}
	if _, err := tx.Emit(repo.OrgID, "io.ore.issue.opened", fmt.Sprintf("%s/%s/issues/%d", repo.Org, repo.Name, i.Number), actor, data); err != nil {
		return ledger.Issue{}, err
	}
	return i, tx.Commit()
}

// updateIssue applies an edit or a state change from GitHub and emits io.ore.issue.updated / .closed.
func (st store) updateIssue(ctx context.Context, repo ledger.Repo, issue ledger.Issue, ev IssueEvent, actor ledger.Actor) (changed bool, err error) {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := ledger.Now()
	var fields []string
	if issue.Title != ev.Title || issue.Body != ev.Body {
		if _, err := tx.ExecContext(ctx, `UPDATE issues SET title = ?, body = ? WHERE id = ?`, ev.Title, ev.Body, issue.ID); err != nil {
			return false, err
		}
		fields = append(fields, "title", "body")
	}
	subject := fmt.Sprintf("%s/%s/issues/%d", repo.Org, repo.Name, issue.Number)
	gh := map[string]any{"number": ev.Number, "action": ev.Action, "url": ev.URL}
	switch {
	case ev.State == "closed" && issue.State != "closed":
		if _, err := tx.ExecContext(ctx, `UPDATE issues SET state = 'closed', closed_at = ? WHERE id = ?`, now, issue.ID); err != nil {
			return false, err
		}
		reason := "closed on GitHub"
		if ev.StateReason != "" {
			reason += " (" + ev.StateReason + ")"
		}
		if _, err := tx.Emit(repo.OrgID, "io.ore.issue.closed", subject, actor, map[string]any{"issue_id": issue.ID, "reason": reason, "github": gh}); err != nil {
			return false, err
		}
		fields = append(fields, "state")
	case ev.State == "open" && issue.State != "open":
		if _, err := tx.ExecContext(ctx, `UPDATE issues SET state = 'open', closed_at = NULL WHERE id = ?`, issue.ID); err != nil {
			return false, err
		}
		fields = append(fields, "state")
	}
	if len(fields) == 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE github_issue_map SET updated_at = ? WHERE repo_id = ? AND github_number = ?`, now, repo.ID, ev.Number); err != nil {
		return false, err
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.issue.updated", subject, actor, map[string]any{"issue_id": issue.ID, "fields": fields, "title": ev.Title, "state": ev.State, "github": gh}); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// ---- pull requests ----

type prMapping struct {
	GitHubNumber int
	ChangeID     string
	ChangeNumber int
	HeadSHA      string
	BaseRef      string
	State        string
	HTMLURL      string
}

func (st store) prMapping(ctx context.Context, repoID string, number int) (prMapping, bool, error) {
	var m prMapping
	err := st.db.QueryRow(ctx, `SELECT github_number, change_id, change_number, head_sha, base_ref, state, html_url FROM github_pr_map WHERE repo_id = ? AND github_number = ?`, repoID, number).
		Scan(&m.GitHubNumber, &m.ChangeID, &m.ChangeNumber, &m.HeadSHA, &m.BaseRef, &m.State, &m.HTMLURL)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	return m, err == nil, err
}

func (st store) openPRs(ctx context.Context, repoID string) ([]prMapping, error) {
	rows, err := st.db.Query(ctx, `SELECT github_number, change_id, change_number, head_sha, base_ref, state, html_url FROM github_pr_map WHERE repo_id = ? AND state = 'open' ORDER BY github_number`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []prMapping
	for rows.Next() {
		var m prMapping
		if err := rows.Scan(&m.GitHubNumber, &m.ChangeID, &m.ChangeNumber, &m.HeadSHA, &m.BaseRef, &m.State, &m.HTMLURL); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// createChange mirrors a pull request as a change (number preserved when
// free) with provenance {"source":"github","pr":N}, writing the mapping in
// the same transaction and emitting io.ore.change.opened.
func (st store) createChange(ctx context.Context, repo ledger.Repo, c *ledger.Change, ev PullRequestEvent, actor ledger.Actor) error {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	n, err := freeNumber(ctx, tx, "changes", repo.ID, ev.Number)
	if err != nil {
		return err
	}
	now := ledger.Now()
	c.ID, c.OrgID, c.RepoID, c.Number, c.State, c.CreatedAt = ledger.NewID("chg"), repo.OrgID, repo.ID, n, "open", now
	if _, err := tx.ExecContext(ctx, `INSERT INTO changes(id, org_id, repo_id, number, change_id, title, body, source_ref, target_ref, state, base_oid, head_oid, provenance, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, c.ID, c.OrgID, c.RepoID, c.Number, c.ChangeID, c.Title, c.Body, c.SourceRef, c.TargetRef, c.State, c.BaseOID, c.HeadOID, c.Provenance, c.CreatedAt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO github_pr_map(org_id, repo_id, github_number, github_id, change_id, change_number, head_sha, base_ref, state, html_url, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		repo.OrgID, repo.ID, ev.Number, ev.ID, c.ID, c.Number, c.HeadOID, ev.BaseRef, "open", ev.URL, now); err != nil {
		return err
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.change.opened", fmt.Sprintf("%s/%s/changes/%d", repo.Org, repo.Name, n), actor, c); err != nil {
		return err
	}
	return tx.Commit()
}

func (st store) setPRHead(ctx context.Context, repoID string, number int, head string) error {
	_, err := st.db.Exec(ctx, `UPDATE github_pr_map SET head_sha = ?, updated_at = ? WHERE repo_id = ? AND github_number = ?`, head, ledger.Now(), repoID, number)
	return err
}

// updateChangeMeta applies a title/body edit or a reopen and emits io.ore.change.updated.
func (st store) updateChangeMeta(ctx context.Context, repo ledger.Repo, c ledger.Change, ev PullRequestEvent, actor ledger.Actor) (bool, error) {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var fields []string
	if c.Title != ev.Title || c.Body != ev.Body {
		if _, err := tx.ExecContext(ctx, `UPDATE changes SET title = ?, body = ? WHERE id = ?`, ev.Title, ev.Body, c.ID); err != nil {
			return false, err
		}
		fields = append(fields, "title", "body")
	}
	if ev.State == "open" && c.State == "closed" {
		if _, err := tx.ExecContext(ctx, `UPDATE changes SET state = 'open' WHERE id = ?`, c.ID); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE github_pr_map SET state = 'open', updated_at = ? WHERE repo_id = ? AND github_number = ?`, ledger.Now(), repo.ID, ev.Number); err != nil {
			return false, err
		}
		fields = append(fields, "state")
	}
	if len(fields) == 0 {
		return false, nil
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.change.updated", fmt.Sprintf("%s/%s/changes/%d", repo.Org, repo.Name, c.Number), actor,
		map[string]any{"change_id": c.ChangeID, "fields": fields, "title": ev.Title, "state": ev.State, "github": map[string]any{"pr": ev.Number, "action": ev.Action, "url": ev.URL}}); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// markMergedOnGitHub records a merge that happened on GitHub (the target
// ref update arrives with the push) and emits io.ore.change.merged.
func (st store) markMergedOnGitHub(ctx context.Context, repo ledger.Repo, c ledger.Change, mergeOID string, ev PullRequestEvent, actor ledger.Actor) error {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := ledger.Now()
	var m *string
	if mergeOID != "" {
		m = &mergeOID
	}
	if _, err := tx.ExecContext(ctx, `UPDATE changes SET state = 'merged', merge_oid = ?, merged_at = ? WHERE id = ?`, m, now, c.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE github_pr_map SET state = 'merged', updated_at = ? WHERE repo_id = ? AND github_number = ?`, now, repo.ID, ev.Number); err != nil {
		return err
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.change.merged", fmt.Sprintf("%s/%s/changes/%d", repo.Org, repo.Name, c.Number), actor,
		map[string]any{"change_id": c.ChangeID, "merge_oid": mergeOID, "target_ref": c.TargetRef, "head_oid": c.HeadOID, "provenance": c.Provenance, "via": "github", "github": map[string]any{"pr": ev.Number, "url": ev.URL}}); err != nil {
		return err
	}
	return tx.Commit()
}

func (st store) closePR(ctx context.Context, repo ledger.Repo, c ledger.Change, ev PullRequestEvent, actor ledger.Actor) error {
	if err := st.db.CloseChange(ctx, repo, c, "closed on GitHub without merging", actor); err != nil {
		return err
	}
	_, err := st.db.Exec(ctx, `UPDATE github_pr_map SET state = 'closed', updated_at = ? WHERE repo_id = ? AND github_number = ?`, ledger.Now(), repo.ID, ev.Number)
	return err
}

// ---- deliveries ----

func (st store) deliverySeen(ctx context.Context, repoID, delivery string) (bool, error) {
	var n int
	err := st.db.QueryRow(ctx, `SELECT COUNT(1) FROM github_deliveries WHERE delivery_id = ? AND repo_id = ?`, delivery, repoID).Scan(&n)
	return n > 0, err
}

// recordDelivery stores a processed delivery (idempotency key) with io.ore.github.delivery.received.
func (st store) recordDelivery(ctx context.Context, repo ledger.Repo, delivery, event, action, outcome string, actor ledger.Actor, extra map[string]any) error {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if delivery != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO github_deliveries(delivery_id, org_id, repo_id, event, action, outcome, received_at) VALUES(?,?,?,?,?,?,?)`,
			delivery, repo.OrgID, repo.ID, event, action, outcome, ledger.Now()); err != nil {
			return err
		}
	}
	data := map[string]any{"delivery": delivery, "event": event, "action": action, "outcome": outcome, "repo_id": repo.ID}
	for k, v := range extra {
		data[k] = v
	}
	typ := "io.ore.github.delivery.received"
	if outcome == "ignored" {
		typ = "io.ore.github.event.ignored"
	}
	if _, err := tx.Emit(repo.OrgID, typ, repo.Org+"/"+repo.Name+"/github/deliveries/"+delivery, actor, data); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- statuses and the ledger cursor ----

func (st store) statusPosted(ctx context.Context, runID, state string) (bool, error) {
	var n int
	err := st.db.QueryRow(ctx, `SELECT COUNT(1) FROM github_statuses WHERE run_id = ? AND state = ?`, runID, state).Scan(&n)
	return n > 0, err
}

func (st store) recordStatus(ctx context.Context, repo ledger.Repo, link Link, runID, sha, state, context_, target string) error {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO github_statuses(id, org_id, repo_id, run_id, sha, state, context, created_at) VALUES(?,?,?,?,?,?,?,?)`,
		ledger.NewID("ghs"), repo.OrgID, repo.ID, runID, sha, state, context_, ledger.Now()); err != nil {
		return err
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.github.status.posted", repo.Org+"/"+repo.Name+"/github/statuses/"+sha, ledger.Actor{Kind: "app", ID: "github"},
		map[string]any{"run_id": runID, "sha": sha, "state": state, "context": context_, "target_url": target, "github": link.FullName()}); err != nil {
		return err
	}
	return tx.Commit()
}

func (st store) cursor(ctx context.Context, name string) (int64, bool, error) {
	var seq int64
	err := st.db.QueryRow(ctx, `SELECT seq FROM github_cursors WHERE name = ?`, name).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return seq, err == nil, err
}

func (st store) setCursor(ctx context.Context, name string, seq int64) error {
	_, err := st.db.Exec(ctx, `INSERT INTO github_cursors(name, org_id, seq, updated_at) VALUES(?, '', ?, ?) ON CONFLICT(name) DO UPDATE SET seq = excluded.seq, updated_at = excluded.updated_at`, name, seq, ledger.Now())
	return err
}

func (st store) maxEventSeq(ctx context.Context) (int64, error) {
	var m sql.NullInt64
	if err := st.db.QueryRow(ctx, `SELECT MAX(seq) FROM events`).Scan(&m); err != nil {
		return 0, err
	}
	return m.Int64, nil
}

// emit writes a standalone event for the adapter (sync outcomes, pings).
func (st store) emit(ctx context.Context, repo ledger.Repo, typ, subject string, actor ledger.Actor, data any) error {
	tx, err := st.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Emit(repo.OrgID, typ, subject, actor, data); err != nil {
		return err
	}
	return tx.Commit()
}
