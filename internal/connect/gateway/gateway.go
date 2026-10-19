// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/review"
)

// Defaults for the per-session write rate limit.
const (
	DefaultRateLimit  = 20
	DefaultRateWindow = 10 * time.Minute
	tokenPrefix       = "gws_"
)

// Event types the gateway emits (io.ore.<entity>.<verb>).
const (
	EventSessionOpened     = "io.ore.agent.session.gateway.opened"
	EventSessionClosed     = "io.ore.agent.session.gateway.closed"
	EventWriteGated        = "io.ore.agent.session.write.gated"
	EventWriteExecuted     = "io.ore.agent.session.write.executed"
	EventCommentCreated    = "io.ore.comment.created"
	EventReviewRequested   = "io.ore.change.review.requested"
	EventDeployRequested   = "io.ore.deployment.request.created"
	refusalInvalid         = "invalid"
	refusalScope           = "outside_scope"
	refusalRateLimit       = "rate_limited"
	refusalProtectedBranch = "protected_branch"
	refusalUnsupported     = "unsupported"
	refusalTarget          = "bad_target"
	refusalSessionClosed   = "session_closed"
)

// ErrSessionClosed is returned when a closed session is used.
var ErrSessionClosed = errors.New("gateway session is closed")

// Gateway validates, gates, records and executes agent writes.
type Gateway struct {
	DB       *ledger.DB
	ReposDir string
	Review   *review.Service
	// RateLimit writes per RateWindow and session; zero means the defaults.
	RateLimit  int
	RateWindow time.Duration
	// Now is the clock (overridden by tests).
	Now func() time.Time

	mu sync.Mutex // serializes decisions so the rate limit is exact
}

// Session is an agent's write session on one repository.
type Session struct {
	ID           string  `json:"id"`
	OrgID        string  `json:"org_id"`
	AgentID      string  `json:"agent_id"`
	RepoID       string  `json:"repo_id"`
	Repo         string  `json:"repo"` // org/name
	Scope        string  `json:"scope"`
	State        string  `json:"state"` // open | closed
	Writes       int     `json:"writes"`
	Refused      int     `json:"refused"`
	WindowStart  string  `json:"window_start,omitempty"`
	WindowWrites int     `json:"window_writes"`
	CreatedAt    string  `json:"created_at"`
	ClosedAt     *string `json:"closed_at,omitempty"`
}

// Comment is a comment on an issue or a change, written through the gateway.
type Comment struct {
	ID           string       `json:"id"`
	OrgID        string       `json:"org_id"`
	RepoID       string       `json:"repo_id"`
	TargetKind   string       `json:"target_kind"` // issue | change
	TargetID     string       `json:"target_id"`
	TargetNumber int          `json:"target_number"`
	Body         string       `json:"body"`
	Actor        ledger.Actor `json:"actor"`
	SessionID    string       `json:"session_id,omitempty"`
	CreatedAt    string       `json:"created_at"`
}

// Result is the gateway's decision on one request.
type Result struct {
	Index   int    `json:"index"`
	Kind    string `json:"kind"`
	Status  string `json:"status"` // executed | refused
	Code    string `json:"code,omitempty"`
	Reason  string `json:"reason,omitempty"`
	EventID string `json:"event_id,omitempty"`

	Issue      *ledger.Issue  `json:"issue,omitempty"`
	Change     *ledger.Change `json:"change,omitempty"`
	Existing   bool           `json:"existing,omitempty"`
	Comment    *Comment       `json:"comment,omitempty"`
	Deployment map[string]any `json:"deployment_request,omitempty"`
	Review     map[string]any `json:"review_request,omitempty"`
}

// Response is the gateway's answer to an envelope.
type Response struct {
	Session  string   `json:"session"`
	Executed int      `json:"executed"`
	Refused  int      `json:"refused"`
	Results  []Result `json:"results"`
}

func (g *Gateway) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *Gateway) limit() (int, time.Duration) {
	n, w := g.RateLimit, g.RateWindow
	if n <= 0 {
		n = DefaultRateLimit
	}
	if w <= 0 {
		w = DefaultRateWindow
	}
	return n, w
}

func (g *Gateway) repoPath(r ledger.Repo) string {
	return filepath.Join(g.ReposDir, r.Org, r.Name+".git")
}

// ---- sessions ----

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// IsSessionToken reports whether a bearer token is a gateway session token.
func IsSessionToken(tok string) bool { return strings.HasPrefix(tok, tokenPrefix) }

// CreateSession opens a session for agentID on repo and returns it with the
// one-time token (only the hash is stored). Emits io.ore.agent.session.gateway.opened.
func (g *Gateway) CreateSession(ctx context.Context, repo ledger.Repo, agentID string, actor ledger.Actor) (Session, string, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return Session{}, "", invalid("agent_id is required")
	}
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Session{}, "", err
	}
	token := tokenPrefix + hex.EncodeToString(b[:])
	s := Session{ID: ledger.NewID("gws"), OrgID: repo.OrgID, AgentID: agentID, RepoID: repo.ID, Repo: repo.Org + "/" + repo.Name,
		Scope: "repo:" + repo.Org + "/" + repo.Name, State: "open", CreatedAt: ledger.Now()}
	tx, err := g.DB.Begin(ctx)
	if err != nil {
		return Session{}, "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO gateway_sessions(id, org_id, agent_id, repo_id, scope, token_hash, state, created_at) VALUES(?,?,?,?,?,?,?,?)`,
		s.ID, s.OrgID, s.AgentID, s.RepoID, s.Scope, hashToken(token), s.State, s.CreatedAt); err != nil {
		return Session{}, "", err
	}
	if _, err := tx.Emit(repo.OrgID, EventSessionOpened, sessionSubject(repo, s.ID), actor, s); err != nil {
		return Session{}, "", err
	}
	return s, token, tx.Commit()
}

const sessionCols = `s.id, s.org_id, s.agent_id, s.repo_id, o.name || '/' || r.name, s.scope, s.state, s.writes, s.refused, s.window_start, s.window_writes, s.created_at, s.closed_at`

const sessionFrom = ` FROM gateway_sessions s JOIN repos r ON r.id = s.repo_id JOIN orgs o ON o.id = r.org_id`

func scanSession(row interface{ Scan(...any) error }) (Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.OrgID, &s.AgentID, &s.RepoID, &s.Repo, &s.Scope, &s.State, &s.Writes, &s.Refused, &s.WindowStart, &s.WindowWrites, &s.CreatedAt, &s.ClosedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ledger.ErrNotFound
	}
	return s, err
}

// Session fetches a session by id.
func (g *Gateway) Session(ctx context.Context, id string) (Session, error) {
	return scanSession(g.DB.QueryRow(ctx, `SELECT `+sessionCols+sessionFrom+` WHERE s.id = ?`, id))
}

// SessionByToken resolves a session token; closed sessions resolve too so
// callers can refuse (and record) their use.
func (g *Gateway) SessionByToken(ctx context.Context, token string) (Session, error) {
	if !IsSessionToken(token) {
		return Session{}, ledger.ErrNotFound
	}
	return scanSession(g.DB.QueryRow(ctx, `SELECT `+sessionCols+sessionFrom+` WHERE s.token_hash = ?`, hashToken(token)))
}

// Sessions lists sessions, newest first, optionally for one repository.
func (g *Gateway) Sessions(ctx context.Context, repoID string, limit int) ([]Session, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + sessionCols + sessionFrom
	var args []any
	if repoID != "" {
		q += ` WHERE s.repo_id = ?`
		args = append(args, repoID)
	}
	q += ` ORDER BY s.created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := g.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CloseSession ends a session; its token stops working at once.
func (g *Gateway) CloseSession(ctx context.Context, s Session, reason string, actor ledger.Actor) (Session, error) {
	if s.State != "open" {
		return s, ErrSessionClosed
	}
	repo, err := g.DB.RepoByID(ctx, s.RepoID)
	if err != nil {
		return s, err
	}
	tx, err := g.DB.Begin(ctx)
	if err != nil {
		return s, err
	}
	defer tx.Rollback()
	now := ledger.Now()
	if _, err := tx.ExecContext(ctx, `UPDATE gateway_sessions SET state = 'closed', closed_at = ? WHERE id = ? AND state = 'open'`, now, s.ID); err != nil {
		return s, err
	}
	s.State, s.ClosedAt = "closed", &now
	if _, err := tx.Emit(repo.OrgID, EventSessionClosed, sessionSubject(repo, s.ID), actor, map[string]any{"session_id": s.ID, "agent_id": s.AgentID, "reason": reason, "writes": s.Writes, "refused": s.Refused}); err != nil {
		return s, err
	}
	return s, tx.Commit()
}

func sessionSubject(repo ledger.Repo, sessionID string) string {
	return repo.Org + "/" + repo.Name + "/gateway/" + sessionID
}

// ---- decisions ----

// Submit gates every request in the envelope for session s and executes the
// accepted ones with the agent actor. Each request is decided on its own:
// one refusal does not stop the others. Only the envelope shape itself
// (ParseEnvelope) refuses the whole batch, and that too is recorded.
func (g *Gateway) Submit(ctx context.Context, s Session, body []byte) (Response, error) {
	repo, err := g.DB.RepoByID(ctx, s.RepoID)
	if err != nil {
		return Response{}, err
	}
	actor := ledger.Actor{Kind: "agent", ID: s.AgentID}
	resp := Response{Session: s.ID, Results: []Result{}}
	refuse := func(idx int, kind, code, reason string, req any) error {
		evID, err := g.recordRefusal(ctx, repo, s, actor, kind, code, reason, req)
		if err != nil {
			return err
		}
		resp.Refused++
		resp.Results = append(resp.Results, Result{Index: idx, Kind: kind, Status: "refused", Code: code, Reason: reason, EventID: evID})
		return nil
	}
	if s.State != "open" {
		return resp, refuse(0, "", refusalSessionClosed, ErrSessionClosed.Error(), nil)
	}
	env, err := ParseEnvelope(body)
	if err != nil {
		return resp, refuse(0, "", refusalInvalid, err.Error(), nil)
	}
	for i, raw := range env.Requests {
		req, err := ParseRequest(raw)
		if err != nil {
			if err := refuse(i, req.Kind, refusalInvalid, err.Error(), summarize(raw)); err != nil {
				return resp, err
			}
			continue
		}
		if env.Session.SessionID != s.ID || env.Session.AgentID != s.AgentID {
			if err := refuse(i, req.Kind, refusalScope, fmt.Sprintf("envelope claims session %s/%s, token is for %s/%s", env.Session.AgentID, env.Session.SessionID, s.AgentID, s.ID), summarize(raw)); err != nil {
				return resp, err
			}
			continue
		}
		if env.Session.RepositoryID != s.RepoID && env.Session.RepositoryID != s.Repo {
			if err := refuse(i, req.Kind, refusalScope, fmt.Sprintf("repository %q is outside the session scope (%s)", env.Session.RepositoryID, s.Scope), summarize(raw)); err != nil {
				return resp, err
			}
			continue
		}
		res := g.decide(ctx, repo, &s, actor, i, req, raw)
		if res.Status == "refused" {
			resp.Refused++
		} else {
			resp.Executed++
		}
		resp.Results = append(resp.Results, res)
	}
	return resp, nil
}

// decide applies policy (kind supported, protected branch, target exists),
// then the rate limit, then executes.
func (g *Gateway) decide(ctx context.Context, repo ledger.Repo, s *Session, actor ledger.Actor, idx int, req Request, raw json.RawMessage) Result {
	refuse := func(code, reason string) Result {
		evID, err := g.recordRefusal(ctx, repo, *s, actor, req.Kind, code, reason, summarize(raw))
		if err != nil {
			return Result{Index: idx, Kind: req.Kind, Status: "refused", Code: "error", Reason: err.Error()}
		}
		return Result{Index: idx, Kind: req.Kind, Status: "refused", Code: code, Reason: reason, EventID: evID}
	}
	defaultRef := "refs/heads/" + repo.DefaultBranch
	switch req.Kind {
	case "push", "label", "review":
		return refuse(refusalUnsupported, req.Kind+" is not supported by this gateway yet")
	case "open_change":
		if fullRef(req.Branch) == defaultRef {
			return refuse(refusalProtectedBranch, fmt.Sprintf("%s is the protected default branch of %s/%s", req.Branch, repo.Org, repo.Name))
		}
		if _, err := gitx.RevParse(ctx, g.repoPath(repo), fullRef(req.Branch)); err != nil {
			return refuse(refusalTarget, fmt.Sprintf("branch %s does not exist in %s/%s", req.Branch, repo.Org, repo.Name))
		}
		if req.Base != "" && fullRef(req.Base) != defaultRef {
			if _, err := gitx.RevParse(ctx, g.repoPath(repo), fullRef(req.Base)); err != nil {
				return refuse(refusalTarget, fmt.Sprintf("base branch %s does not exist in %s/%s", req.Base, repo.Org, repo.Name))
			}
		}
	case "comment", "request_review":
		if _, _, _, err := g.resolveTarget(ctx, repo, req.Target, req.Kind == "request_review"); err != nil {
			return refuse(refusalTarget, err.Error())
		}
	case "request_deployment":
		if _, err := gitx.RevParse(ctx, g.repoPath(repo), req.Release); err != nil {
			if _, err := gitx.RevParse(ctx, g.repoPath(repo), fullRef(req.Release)); err != nil {
				return refuse(refusalTarget, fmt.Sprintf("release %q is not a commit or branch of %s/%s", req.Release, repo.Org, repo.Name))
			}
		}
	}
	// rate limit: reserve a slot, exact under concurrency
	g.mu.Lock()
	ok, err := g.reserveWrite(ctx, s)
	g.mu.Unlock()
	if err != nil {
		return Result{Index: idx, Kind: req.Kind, Status: "refused", Code: "error", Reason: err.Error()}
	}
	if !ok {
		n, w := g.limit()
		return refuse(refusalRateLimit, fmt.Sprintf("rate limit exceeded: %d writes per %s for session %s", n, w, s.ID))
	}
	res, err := g.execute(ctx, repo, *s, actor, req)
	res.Index, res.Kind = idx, req.Kind
	if err != nil {
		// the slot stays consumed: a failed execution is still an attempted write
		res.Status, res.Code, res.Reason = "refused", "error", err.Error()
		if evID, rerr := g.recordRefusal(ctx, repo, *s, actor, req.Kind, "error", err.Error(), summarize(raw)); rerr == nil {
			res.EventID = evID
		}
		return res
	}
	res.Status = "executed"
	return res
}

// reserveWrite consumes one write from the session's window, resetting the
// window when it has elapsed. Returns false when the limit is reached.
func (g *Gateway) reserveWrite(ctx context.Context, s *Session) (bool, error) {
	limit, window := g.limit()
	now := g.now().UTC()
	cur, err := g.Session(ctx, s.ID)
	if err != nil {
		return false, err
	}
	start, _ := time.Parse(time.RFC3339Nano, cur.WindowStart)
	if cur.WindowStart == "" || now.Sub(start) >= window {
		start, cur.WindowWrites = now, 0
	}
	if cur.WindowWrites >= limit {
		return false, nil
	}
	cur.WindowWrites++
	cur.Writes++
	cur.WindowStart = start.Format("2006-01-02T15:04:05.000Z07:00")
	if _, err := g.DB.Exec(ctx, `UPDATE gateway_sessions SET writes = ?, window_start = ?, window_writes = ? WHERE id = ?`, cur.Writes, cur.WindowStart, cur.WindowWrites, s.ID); err != nil {
		return false, err
	}
	*s = cur
	return true, nil
}

// recordRefusal bumps the session's refused counter and emits the gated event in one transaction.
func (g *Gateway) recordRefusal(ctx context.Context, repo ledger.Repo, s Session, actor ledger.Actor, kind, code, reason string, req any) (string, error) {
	tx, err := g.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE gateway_sessions SET refused = refused + 1 WHERE id = ?`, s.ID); err != nil {
		return "", err
	}
	ev, err := tx.Emit(repo.OrgID, EventWriteGated, sessionSubject(repo, s.ID), actor, map[string]any{
		"session_id": s.ID, "agent_id": s.AgentID, "repo_id": repo.ID, "scope": s.Scope,
		"kind": kind, "decision": "refused", "code": code, "reason": reason, "request": req,
	})
	if err != nil {
		return "", err
	}
	return ev.ID, tx.Commit()
}

// recordExecuted emits io.ore.agent.session.write.executed inside tx (or its own when tx is nil).
func (g *Gateway) recordExecuted(ctx context.Context, tx *ledger.Tx, repo ledger.Repo, s Session, actor ledger.Actor, kind string, result map[string]any) (string, error) {
	own := tx == nil
	if own {
		var err error
		if tx, err = g.DB.Begin(ctx); err != nil {
			return "", err
		}
		defer tx.Rollback()
	}
	data := map[string]any{"session_id": s.ID, "agent_id": s.AgentID, "repo_id": repo.ID, "scope": s.Scope, "kind": kind, "decision": "executed"}
	for k, v := range result {
		data[k] = v
	}
	ev, err := tx.Emit(repo.OrgID, EventWriteExecuted, sessionSubject(repo, s.ID), actor, data)
	if err != nil {
		return "", err
	}
	if own {
		return ev.ID, tx.Commit()
	}
	return ev.ID, nil
}

// ---- execution ----

func (g *Gateway) execute(ctx context.Context, repo ledger.Repo, s Session, actor ledger.Actor, req Request) (Result, error) {
	var res Result
	switch req.Kind {
	case "open_issue":
		issue, err := g.DB.CreateIssue(ctx, repo, req.Title, req.Body, actor)
		if err != nil {
			return res, err
		}
		res.Issue = &issue
		res.EventID, err = g.recordExecuted(ctx, nil, repo, s, actor, req.Kind, map[string]any{"issue_id": issue.ID, "issue": issue.Number, "labels": req.Labels})
		return res, err
	case "open_change":
		prov := *req.Provenance
		if prov.SessionID == "" {
			prov.SessionID = s.ID
		}
		pj, _ := json.Marshal(prov)
		existing, _ := g.DB.OpenChangeBySourceRef(ctx, repo, fullRef(req.Branch))
		ch, err := g.Review.Open(ctx, repo, review.OpenRequest{SourceRef: req.Branch, TargetRef: req.Base, Title: req.Title, Body: req.Body, Provenance: string(pj)}, actor)
		if err != nil {
			return res, err
		}
		res.Change, res.Existing = &ch, existing.ID != "" && existing.ID == ch.ID
		res.EventID, err = g.recordExecuted(ctx, nil, repo, s, actor, req.Kind, map[string]any{"change_id": ch.ID, "change": ch.Number, "existing": res.Existing, "draft": req.Draft == nil || *req.Draft})
		return res, err
	case "comment":
		kind, id, number, err := g.resolveTarget(ctx, repo, req.Target, false)
		if err != nil {
			return res, err
		}
		c := Comment{ID: ledger.NewID("cmt"), OrgID: repo.OrgID, RepoID: repo.ID, TargetKind: kind, TargetID: id, TargetNumber: number, Body: req.Body, Actor: actor, SessionID: s.ID, CreatedAt: ledger.Now()}
		tx, err := g.DB.Begin(ctx)
		if err != nil {
			return res, err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, org_id, repo_id, target_kind, target_id, target_number, body, actor_kind, actor_id, session_id, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			c.ID, c.OrgID, c.RepoID, c.TargetKind, c.TargetID, c.TargetNumber, c.Body, c.Actor.Kind, c.Actor.ID, c.SessionID, c.CreatedAt); err != nil {
			return res, err
		}
		subject := fmt.Sprintf("%s/%s/%ss/%d/comments/%s", repo.Org, repo.Name, kind, number, c.ID)
		if _, err := tx.Emit(repo.OrgID, EventCommentCreated, subject, actor, c); err != nil {
			return res, err
		}
		res.Comment = &c
		if res.EventID, err = g.recordExecuted(ctx, tx, repo, s, actor, req.Kind, map[string]any{"comment_id": c.ID, "target_kind": kind, "target": number}); err != nil {
			return res, err
		}
		return res, tx.Commit()
	case "request_review":
		_, id, number, err := g.resolveTarget(ctx, repo, req.Target, true)
		if err != nil {
			return res, err
		}
		data := map[string]any{"change_id": id, "change": number, "reviewers": req.Reviewers, "session_id": s.ID, "agent_id": s.AgentID}
		tx, err := g.DB.Begin(ctx)
		if err != nil {
			return res, err
		}
		defer tx.Rollback()
		if _, err := tx.Emit(repo.OrgID, EventReviewRequested, fmt.Sprintf("%s/%s/changes/%d", repo.Org, repo.Name, number), actor, data); err != nil {
			return res, err
		}
		res.Review = data
		if res.EventID, err = g.recordExecuted(ctx, tx, repo, s, actor, req.Kind, map[string]any{"change_id": id, "change": number, "reviewers": req.Reviewers}); err != nil {
			return res, err
		}
		return res, tx.Commit()
	case "request_deployment":
		oid, err := gitx.RevParse(ctx, g.repoPath(repo), req.Release)
		if err != nil {
			if oid, err = gitx.RevParse(ctx, g.repoPath(repo), fullRef(req.Release)); err != nil {
				return res, err
			}
		}
		reqID := ledger.NewID("dreq")
		data := map[string]any{"request_id": reqID, "release": req.Release, "commit_oid": oid, "environment": req.Environment, "session_id": s.ID, "agent_id": s.AgentID, "status": "requested"}
		tx, err := g.DB.Begin(ctx)
		if err != nil {
			return res, err
		}
		defer tx.Rollback()
		if _, err := tx.Emit(repo.OrgID, EventDeployRequested, fmt.Sprintf("%s/%s/deployments/requests/%s", repo.Org, repo.Name, reqID), actor, data); err != nil {
			return res, err
		}
		res.Deployment = data
		if res.EventID, err = g.recordExecuted(ctx, tx, repo, s, actor, req.Kind, map[string]any{"request_id": reqID, "commit_oid": oid, "environment": req.Environment}); err != nil {
			return res, err
		}
		return res, tx.Commit()
	}
	return res, fmt.Errorf("%s: not executable", req.Kind)
}

// resolveTarget parses "issues/N", "changes/N", "#N" (an issue), an issue id
// (iss_…) or a change id (chg_…) into (kind, id, number). changesOnly
// restricts it to changes (request_review).
func (g *Gateway) resolveTarget(ctx context.Context, repo ledger.Repo, target string, changesOnly bool) (kind, id string, number int, err error) {
	t := strings.TrimSpace(target)
	var n int
	switch {
	case strings.HasPrefix(t, "issues/"), strings.HasPrefix(t, "issue/"), strings.HasPrefix(t, "#"):
		kind = "issue"
		n, _ = strconv.Atoi(t[strings.LastIndex(t, "/")+1:])
		if strings.HasPrefix(t, "#") {
			n, _ = strconv.Atoi(t[1:])
		}
	case strings.HasPrefix(t, "changes/"), strings.HasPrefix(t, "change/"):
		kind = "change"
		n, _ = strconv.Atoi(t[strings.LastIndex(t, "/")+1:])
	case strings.HasPrefix(t, "iss_"):
		kind = "issue"
	case strings.HasPrefix(t, "chg_"):
		kind = "change"
	default:
		return "", "", 0, fmt.Errorf("target %q must be issues/N, changes/N, an issue id or a change id", target)
	}
	if changesOnly && kind != "change" {
		return "", "", 0, fmt.Errorf("target %q must be a change", target)
	}
	switch kind {
	case "issue":
		var i ledger.Issue
		if n > 0 {
			i, err = g.DB.Issue(ctx, repo, n)
		} else {
			err = g.DB.QueryRow(ctx, `SELECT id, number FROM issues WHERE repo_id = ? AND id = ?`, repo.ID, t).Scan(&i.ID, &i.Number)
			if errors.Is(err, sql.ErrNoRows) {
				err = ledger.ErrNotFound
			}
		}
		if err != nil {
			return "", "", 0, fmt.Errorf("issue %q not found in %s/%s", target, repo.Org, repo.Name)
		}
		return kind, i.ID, i.Number, nil
	default:
		var c ledger.Change
		if n > 0 {
			c, err = g.DB.Change(ctx, repo, n)
		} else {
			err = g.DB.QueryRow(ctx, `SELECT id, number FROM changes WHERE repo_id = ? AND id = ?`, repo.ID, t).Scan(&c.ID, &c.Number)
			if errors.Is(err, sql.ErrNoRows) {
				err = ledger.ErrNotFound
			}
		}
		if err != nil {
			return "", "", 0, fmt.Errorf("change %q not found in %s/%s", target, repo.Org, repo.Name)
		}
		return kind, c.ID, c.Number, nil
	}
}

// Comments lists the comments on an issue or change, oldest first.
func (g *Gateway) Comments(ctx context.Context, repo ledger.Repo, targetKind, targetID string) ([]Comment, error) {
	rows, err := g.DB.Query(ctx, `SELECT id, org_id, repo_id, target_kind, target_id, target_number, body, actor_kind, actor_id, session_id, created_at FROM comments WHERE repo_id = ? AND target_kind = ? AND target_id = ? ORDER BY created_at ASC`, repo.ID, targetKind, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Comment{}
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.OrgID, &c.RepoID, &c.TargetKind, &c.TargetID, &c.TargetNumber, &c.Body, &c.Actor.Kind, &c.Actor.ID, &c.SessionID, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func fullRef(name string) string {
	if strings.HasPrefix(name, "refs/") {
		return name
	}
	return "refs/heads/" + name
}

// summarize keeps the identifying fields of a request for the ledger and
// truncates bodies so a refusal never stores a 64 KiB payload.
func summarize(raw json.RawMessage) map[string]any {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return map[string]any{"raw": truncate(string(raw), 256)}
	}
	for k, v := range m {
		if s, ok := v.(string); ok && len(s) > 256 {
			m[k] = truncate(s, 256)
		}
	}
	return m
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("… (%d bytes)", len(s))
}

// RecordRefusal records a refused write decision that was taken outside
// Submit (for example by the MCP server when a closed session's token is
// presented). It bumps the session's refused counter and emits
// io.ore.agent.session.write.gated.
func (g *Gateway) RecordRefusal(ctx context.Context, s Session, kind, code, reason string, req any) (string, error) {
	repo, err := g.DB.RepoByID(ctx, s.RepoID)
	if err != nil {
		return "", err
	}
	return g.recordRefusal(ctx, repo, s, ledger.Actor{Kind: "agent", ID: s.AgentID}, kind, code, reason, req)
}

// Refusal codes, exported for callers that record their own decisions.
const (
	CodeSessionClosed = refusalSessionClosed
	CodeOutsideScope  = refusalScope
	CodeInvalid       = refusalInvalid
	CodeRateLimited   = refusalRateLimit
)
