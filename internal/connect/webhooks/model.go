// SPDX-License-Identifier: AGPL-3.0-only

package webhooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

// Delivery statuses.
const (
	StatusPending   = "pending"   // queued or waiting for its next attempt
	StatusSucceeded = "succeeded" // a 2xx was received
	StatusFailed    = "failed"    // given up without retry (410, refused address, cancelled)
	StatusExhausted = "exhausted" // retried for 72 h without a 2xx
)

// Delivery kinds.
const (
	KindEvent     = "event"
	KindPing      = "ping"
	KindReplay    = "replay"
	KindRedeliver = "redeliver"
)

// Event types the slice emits (io.ore.<entity>.<verb>).
const (
	EventEndpointCreated  = "io.ore.webhook.endpoint.created"
	EventEndpointUpdated  = "io.ore.webhook.endpoint.updated"
	EventEndpointDeleted  = "io.ore.webhook.endpoint.deleted"
	EventEndpointDisabled = "io.ore.webhook.endpoint.disabled"
	EventSecretRotated    = "io.ore.webhook.endpoint.secret.rotated"
	EventReplayRequested  = "io.ore.webhook.replay.requested"
	EventPing             = "io.ore.webhook.ping"
	EventAuditCreated     = "io.ore.audit.export.created"
	EventAuditDeleted     = "io.ore.audit.export.deleted"
)

// Secret is one signing key of an endpoint. During rotation an endpoint
// carries two: the new one and the old one until ExpiresAt.
type Secret struct {
	ID        string  `json:"id"`
	Value     string  `json:"value,omitempty"` // whsec_<base64>; omitted from listings
	CreatedAt string  `json:"created_at"`
	ExpiresAt *string `json:"expires_at,omitempty"`
}

// Filter selects the events an endpoint receives. Every set dimension must match.
type Filter struct {
	Types      []string `json:"types,omitempty"`       // type prefixes, e.g. io.ore.deployment.
	Subject    string   `json:"subject,omitempty"`     // glob over the subject, * per segment, ** across
	ActorKinds []string `json:"actor_kinds,omitempty"` // human, agent, app, system
	Expr       string   `json:"expr,omitempty"`        // expression (see expr.go)
}

// Endpoint is a webhook subscription.
type Endpoint struct {
	ID             string       `json:"id"`
	OrgID          string       `json:"org_id"`
	Org            string       `json:"org"`
	RepoID         *string      `json:"repo_id,omitempty"`
	Repo           string       `json:"repo,omitempty"` // org/name when repository-scoped
	URL            string       `json:"url"`
	Description    string       `json:"description"`
	Secrets        []Secret     `json:"secrets"`
	Filter         Filter       `json:"filter"`
	Payload        string       `json:"payload"` // thin | full
	Enabled        bool         `json:"enabled"`
	DisabledReason string       `json:"disabled_reason,omitempty"`
	Cursor         int64        `json:"cursor"`
	CreatedBy      ledger.Actor `json:"created_by"`
	CreatedAt      string       `json:"created_at"`
	UpdatedAt      string       `json:"updated_at"`
}

// Attempt is one try of a delivery, kept on the delivery row.
type Attempt struct {
	N       int    `json:"n"`
	At      string `json:"at"`
	Code    int    `json:"code"`
	Latency int64  `json:"latency_ms"`
	Error   string `json:"error,omitempty"`
}

// Delivery is one message to one endpoint: the event, its retry state and
// the attempts so far. webhook-id is the event id for every attempt.
type Delivery struct {
	ID             string    `json:"id"`
	OrgID          string    `json:"org_id"`
	EndpointID     string    `json:"endpoint_id"`
	EventSeq       int64     `json:"event_seq"`
	EventID        string    `json:"event_id"`
	Kind           string    `json:"kind"`
	Attempt        int       `json:"attempt"`
	Status         string    `json:"status"`
	ResponseCode   int       `json:"response_code"`
	LatencyMs      int64     `json:"latency_ms"`
	NextAttemptAt  *string   `json:"next_attempt_at,omitempty"`
	FirstAttemptAt *string   `json:"first_attempt_at,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	Attempts       []Attempt `json:"attempts"`
	CreatedAt      string    `json:"created_at"`
	UpdatedAt      string    `json:"updated_at"`
}

// AuditExport streams the ledger to a file directory or an S3 bucket.
type AuditExport struct {
	ID           string          `json:"id"`
	OrgID        string          `json:"org_id"` // "" exports every org
	Kind         string          `json:"kind"`   // file | s3
	Config       json.RawMessage `json:"config"`
	FilterTypes  []string        `json:"filter_types"`
	Cursor       int64           `json:"cursor"`
	Exported     int64           `json:"exported"`
	Enabled      bool            `json:"enabled"`
	LastError    string          `json:"last_error,omitempty"`
	LastExportAt *string         `json:"last_export_at,omitempty"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
}

// Store persists endpoints, deliveries and exports.
type Store struct {
	DB *ledger.DB
}

// Now is the ledger's timestamp format for a given time.
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }

func parseStamp(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ---- endpoints ----

const endpointCols = `e.id, e.org_id, o.name, e.repo_id, COALESCE(o.name || '/' || r.name, ''), e.url, e.description, e.secrets, e.filter_types, e.filter_subject, e.filter_actor_kinds, e.filter_expr, e.payload, e.enabled, e.disabled_reason, e.cursor, e.created_by_kind, e.created_by_id, e.created_at, e.updated_at`
const endpointFrom = ` FROM webhook_endpoints e JOIN orgs o ON o.id = e.org_id LEFT JOIN repos r ON r.id = e.repo_id`

func scanEndpoint(row interface{ Scan(...any) error }) (Endpoint, error) {
	var e Endpoint
	var secrets, types, kinds string
	var enabled int
	err := row.Scan(&e.ID, &e.OrgID, &e.Org, &e.RepoID, &e.Repo, &e.URL, &e.Description, &secrets, &types, &e.Filter.Subject, &kinds, &e.Filter.Expr, &e.Payload, &enabled, &e.DisabledReason, &e.Cursor, &e.CreatedBy.Kind, &e.CreatedBy.ID, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ledger.ErrNotFound
	}
	if err != nil {
		return e, err
	}
	e.Enabled = enabled == 1
	json.Unmarshal([]byte(secrets), &e.Secrets)
	json.Unmarshal([]byte(types), &e.Filter.Types)
	json.Unmarshal([]byte(kinds), &e.Filter.ActorKinds)
	if e.Secrets == nil {
		e.Secrets = []Secret{}
	}
	return e, nil
}

// InsertEndpoint stores a new endpoint and emits io.ore.webhook.endpoint.created.
func (s *Store) InsertEndpoint(ctx context.Context, e *Endpoint) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO webhook_endpoints(id, org_id, repo_id, url, description, secrets, filter_types, filter_subject, filter_actor_kinds, filter_expr, payload, enabled, disabled_reason, cursor, created_by_kind, created_by_id, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.OrgID, e.RepoID, e.URL, e.Description, mustJSON(e.Secrets), mustJSON(nonNil(e.Filter.Types)), e.Filter.Subject, mustJSON(nonNil(e.Filter.ActorKinds)), e.Filter.Expr, e.Payload, b2i(e.Enabled), e.DisabledReason, e.Cursor, e.CreatedBy.Kind, e.CreatedBy.ID, e.CreatedAt, e.UpdatedAt); err != nil {
		return err
	}
	if _, err := tx.Emit(e.OrgID, EventEndpointCreated, "webhooks/"+e.ID, e.CreatedBy, e.Public()); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateEndpoint persists url, description, filters, payload, enabled state
// and secrets, emitting typ (updated, disabled, secret.rotated) with data.
func (s *Store) UpdateEndpoint(ctx context.Context, e *Endpoint, typ string, actor ledger.Actor, data any) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e.UpdatedAt = ledger.Now()
	if _, err := tx.ExecContext(ctx, `UPDATE webhook_endpoints SET url = ?, description = ?, secrets = ?, filter_types = ?, filter_subject = ?, filter_actor_kinds = ?, filter_expr = ?, payload = ?, enabled = ?, disabled_reason = ?, updated_at = ? WHERE id = ?`,
		e.URL, e.Description, mustJSON(e.Secrets), mustJSON(nonNil(e.Filter.Types)), e.Filter.Subject, mustJSON(nonNil(e.Filter.ActorKinds)), e.Filter.Expr, e.Payload, b2i(e.Enabled), e.DisabledReason, e.UpdatedAt, e.ID); err != nil {
		return err
	}
	if typ != "" {
		if data == nil {
			data = e.Public()
		}
		if _, err := tx.Emit(e.OrgID, typ, "webhooks/"+e.ID, actor, data); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetCursor advances an endpoint's ledger cursor (no event: it moves constantly).
func (s *Store) SetCursor(ctx context.Context, id string, cursor int64) error {
	_, err := s.DB.Exec(ctx, `UPDATE webhook_endpoints SET cursor = ? WHERE id = ? AND cursor < ?`, cursor, id, cursor)
	return err
}

// DeleteEndpoint removes an endpoint and its deliveries, emitting io.ore.webhook.endpoint.deleted.
func (s *Store) DeleteEndpoint(ctx context.Context, e Endpoint, actor ledger.Actor) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM webhook_deliveries WHERE endpoint_id = ?`, e.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM webhook_endpoints WHERE id = ?`, e.ID); err != nil {
		return err
	}
	if _, err := tx.Emit(e.OrgID, EventEndpointDeleted, "webhooks/"+e.ID, actor, map[string]any{"endpoint_id": e.ID, "url": e.URL}); err != nil {
		return err
	}
	return tx.Commit()
}

// Endpoint fetches by id.
func (s *Store) Endpoint(ctx context.Context, id string) (Endpoint, error) {
	return scanEndpoint(s.DB.QueryRow(ctx, `SELECT `+endpointCols+endpointFrom+` WHERE e.id = ?`, id))
}

// Endpoints lists endpoints, optionally for one org and/or repository; enabledOnly for the dispatcher.
func (s *Store) Endpoints(ctx context.Context, orgID, repoID string, enabledOnly bool) ([]Endpoint, error) {
	q := `SELECT ` + endpointCols + endpointFrom + ` WHERE 1=1`
	var args []any
	if orgID != "" {
		q += ` AND e.org_id = ?`
		args = append(args, orgID)
	}
	if repoID != "" {
		q += ` AND e.repo_id = ?`
		args = append(args, repoID)
	}
	if enabledOnly {
		q += ` AND e.enabled = 1`
	}
	q += ` ORDER BY e.created_at, e.id`
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Endpoint{}
	for rows.Next() {
		e, err := scanEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Public is the endpoint without secret values (for events and listings).
func (e Endpoint) Public() Endpoint {
	c := e
	c.Secrets = make([]Secret, len(e.Secrets))
	for i, s := range e.Secrets {
		s.Value = ""
		c.Secrets[i] = s
	}
	return c
}

// ActiveSecrets returns the secrets valid at t: the current one and any
// retiring one that has not expired yet, newest first.
func (e Endpoint) ActiveSecrets(t time.Time) []Secret {
	var out []Secret
	for i := len(e.Secrets) - 1; i >= 0; i-- {
		sec := e.Secrets[i]
		if sec.ExpiresAt != nil && !parseStamp(*sec.ExpiresAt).After(t) {
			continue
		}
		out = append(out, sec)
	}
	return out
}

// ---- deliveries ----

const deliveryCols = `id, org_id, endpoint_id, event_seq, event_id, kind, attempt, status, response_code, latency_ms, next_attempt_at, first_attempt_at, last_error, attempts, created_at, updated_at`

func scanDelivery(row interface{ Scan(...any) error }) (Delivery, error) {
	var d Delivery
	var attempts string
	err := row.Scan(&d.ID, &d.OrgID, &d.EndpointID, &d.EventSeq, &d.EventID, &d.Kind, &d.Attempt, &d.Status, &d.ResponseCode, &d.LatencyMs, &d.NextAttemptAt, &d.FirstAttemptAt, &d.LastError, &attempts, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ledger.ErrNotFound
	}
	if err != nil {
		return d, err
	}
	json.Unmarshal([]byte(attempts), &d.Attempts)
	if d.Attempts == nil {
		d.Attempts = []Attempt{}
	}
	return d, nil
}

// InsertDeliveries queues deliveries (status pending, due now) and advances
// the endpoint cursor to cursor in one transaction. Events that already have
// a delivery for the endpoint are skipped (the event id is the idempotency key).
func (s *Store) InsertDeliveries(ctx context.Context, e Endpoint, events []ledger.Event, kind string, cursor int64, now time.Time) (int, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := stamp(now)
	n := 0
	for _, ev := range events {
		if kind == KindEvent {
			var dup int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM webhook_deliveries WHERE endpoint_id = ? AND event_id = ? AND kind IN ('event','replay')`, e.ID, ev.ID).Scan(&dup); err != nil {
				return 0, err
			}
			if dup > 0 {
				continue
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO webhook_deliveries(id, org_id, endpoint_id, event_seq, event_id, kind, attempt, status, next_attempt_at, attempts, created_at, updated_at) VALUES(?,?,?,?,?,?,0,?,?,'[]',?,?)`,
			ledger.NewID("whd"), e.OrgID, e.ID, ev.Seq, ev.ID, kind, StatusPending, ts, ts, ts); err != nil {
			return 0, err
		}
		n++
	}
	if cursor > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE webhook_endpoints SET cursor = ? WHERE id = ? AND cursor < ?`, cursor, e.ID, cursor); err != nil {
			return 0, err
		}
	}
	return n, tx.Commit()
}

// InsertDelivery queues one delivery (ping, redeliver).
func (s *Store) InsertDelivery(ctx context.Context, d *Delivery) error {
	if d.Attempts == nil {
		d.Attempts = []Attempt{}
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO webhook_deliveries(`+deliveryCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.OrgID, d.EndpointID, d.EventSeq, d.EventID, d.Kind, d.Attempt, d.Status, d.ResponseCode, d.LatencyMs, d.NextAttemptAt, d.FirstAttemptAt, d.LastError, mustJSON(d.Attempts), d.CreatedAt, d.UpdatedAt)
	return err
}

// UpdateDelivery persists the state after an attempt.
func (s *Store) UpdateDelivery(ctx context.Context, d *Delivery) error {
	_, err := s.DB.Exec(ctx, `UPDATE webhook_deliveries SET attempt = ?, status = ?, response_code = ?, latency_ms = ?, next_attempt_at = ?, first_attempt_at = ?, last_error = ?, attempts = ?, updated_at = ? WHERE id = ?`,
		d.Attempt, d.Status, d.ResponseCode, d.LatencyMs, d.NextAttemptAt, d.FirstAttemptAt, d.LastError, mustJSON(d.Attempts), d.UpdatedAt, d.ID)
	return err
}

// Delivery fetches by id.
func (s *Store) Delivery(ctx context.Context, id string) (Delivery, error) {
	return scanDelivery(s.DB.QueryRow(ctx, `SELECT `+deliveryCols+` FROM webhook_deliveries WHERE id = ?`, id))
}

// DueDeliveries returns an endpoint's pending deliveries due at or before now, oldest event first.
func (s *Store) DueDeliveries(ctx context.Context, endpointID string, now time.Time, limit int) ([]Delivery, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+deliveryCols+` FROM webhook_deliveries WHERE endpoint_id = ? AND status = ? AND next_attempt_at <= ? ORDER BY event_seq, created_at LIMIT ?`, endpointID, StatusPending, stamp(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// NextDue returns when the endpoint's earliest pending delivery is due.
func (s *Store) NextDue(ctx context.Context, endpointID string) (time.Time, bool) {
	var at sql.NullString
	if err := s.DB.QueryRow(ctx, `SELECT MIN(next_attempt_at) FROM webhook_deliveries WHERE endpoint_id = ? AND status = ?`, endpointID, StatusPending).Scan(&at); err != nil || !at.Valid {
		return time.Time{}, false
	}
	return parseStamp(at.String), true
}

// DeliveriesOptions filters a delivery listing.
type DeliveriesOptions struct {
	Status   string
	AfterSeq int64
	Limit    int
}

// Deliveries lists an endpoint's deliveries, oldest event first, after a sequence number.
func (s *Store) Deliveries(ctx context.Context, endpointID string, opt DeliveriesOptions) ([]Delivery, error) {
	if opt.Limit <= 0 || opt.Limit > 1000 {
		opt.Limit = 100
	}
	q := `SELECT ` + deliveryCols + ` FROM webhook_deliveries WHERE endpoint_id = ? AND event_seq > ?`
	args := []any{endpointID, opt.AfterSeq}
	if opt.Status != "" {
		q += ` AND status = ?`
		args = append(args, opt.Status)
	}
	q += ` ORDER BY event_seq, created_at LIMIT ?`
	args = append(args, opt.Limit)
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RecentDeliveries returns the endpoint's most recently updated deliveries (for health).
func (s *Store) RecentDeliveries(ctx context.Context, endpointID string, limit int) ([]Delivery, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+deliveryCols+` FROM webhook_deliveries WHERE endpoint_id = ? AND attempt > 0 ORDER BY updated_at DESC, id DESC LIMIT ?`, endpointID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// CountDeliveries returns counts by status for an endpoint.
func (s *Store) CountDeliveries(ctx context.Context, endpointID string) (map[string]int, error) {
	rows, err := s.DB.Query(ctx, `SELECT status, COUNT(1) FROM webhook_deliveries WHERE endpoint_id = ? GROUP BY status`, endpointID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// ---- events ----

// EventByID fetches one ledger event.
func (s *Store) EventByID(ctx context.Context, id string) (ledger.Event, error) {
	var ev ledger.Event
	var data string
	err := s.DB.QueryRow(ctx, `SELECT seq, id, org_id, type, subject, time, actor_kind, actor_id, data FROM events WHERE id = ?`, id).
		Scan(&ev.Seq, &ev.ID, &ev.OreOrg, &ev.Type, &ev.Subject, &ev.Time, &ev.OreActor.Kind, &ev.OreActor.ID, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return ev, ledger.ErrNotFound
	}
	if err != nil {
		return ev, err
	}
	ev.SpecVersion, ev.DataContentType, ev.Source, ev.Data = "1.0", "application/json", "ore://"+ev.OreOrg, json.RawMessage(data)
	return ev, nil
}

// HeadSeq returns the ledger's latest sequence number.
func (s *Store) HeadSeq(ctx context.Context) (int64, error) {
	var n sql.NullInt64
	if err := s.DB.QueryRow(ctx, `SELECT MAX(seq) FROM events`).Scan(&n); err != nil {
		return 0, err
	}
	return n.Int64, nil
}

// SeqAt returns the first sequence number at or after time t (RFC 3339).
func (s *Store) SeqAt(ctx context.Context, t string) (int64, error) {
	var n sql.NullInt64
	if err := s.DB.QueryRow(ctx, `SELECT MIN(seq) FROM events WHERE time >= ?`, t).Scan(&n); err != nil {
		return 0, err
	}
	if !n.Valid {
		return 0, ledger.ErrNotFound
	}
	return n.Int64, nil
}

// ---- audit exports ----

const auditCols = `id, org_id, kind, config, filter_types, cursor, exported, enabled, last_error, last_export_at, created_at, updated_at`

func scanAudit(row interface{ Scan(...any) error }) (AuditExport, error) {
	var a AuditExport
	var cfg, types string
	var enabled int
	err := row.Scan(&a.ID, &a.OrgID, &a.Kind, &cfg, &types, &a.Cursor, &a.Exported, &enabled, &a.LastError, &a.LastExportAt, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ledger.ErrNotFound
	}
	if err != nil {
		return a, err
	}
	a.Config, a.Enabled = json.RawMessage(cfg), enabled == 1
	json.Unmarshal([]byte(types), &a.FilterTypes)
	if a.FilterTypes == nil {
		a.FilterTypes = []string{}
	}
	return a, nil
}

// InsertAuditExport stores an export and emits io.ore.audit.export.created.
func (s *Store) InsertAuditExport(ctx context.Context, a *AuditExport, actor ledger.Actor) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_exports(`+auditCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.OrgID, a.Kind, string(a.Config), mustJSON(nonNil(a.FilterTypes)), a.Cursor, a.Exported, b2i(a.Enabled), a.LastError, a.LastExportAt, a.CreatedAt, a.UpdatedAt); err != nil {
		return err
	}
	org := a.OrgID
	if org == "" {
		org = "ore"
	}
	if _, err := tx.Emit(org, EventAuditCreated, "audit/"+a.ID, actor, a); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteAuditExport removes an export.
func (s *Store) DeleteAuditExport(ctx context.Context, a AuditExport, actor ledger.Actor) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM audit_exports WHERE id = ?`, a.ID); err != nil {
		return err
	}
	org := a.OrgID
	if org == "" {
		org = "ore"
	}
	if _, err := tx.Emit(org, EventAuditDeleted, "audit/"+a.ID, actor, map[string]any{"export_id": a.ID, "kind": a.Kind}); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateAuditProgress persists the cursor, counters and last error (no event).
func (s *Store) UpdateAuditProgress(ctx context.Context, a *AuditExport) error {
	a.UpdatedAt = ledger.Now()
	_, err := s.DB.Exec(ctx, `UPDATE audit_exports SET cursor = ?, exported = ?, last_error = ?, last_export_at = ?, updated_at = ? WHERE id = ?`, a.Cursor, a.Exported, a.LastError, a.LastExportAt, a.UpdatedAt, a.ID)
	return err
}

// AuditExport fetches by id.
func (s *Store) AuditExport(ctx context.Context, id string) (AuditExport, error) {
	return scanAudit(s.DB.QueryRow(ctx, `SELECT `+auditCols+` FROM audit_exports WHERE id = ?`, id))
}

// AuditExports lists exports.
func (s *Store) AuditExports(ctx context.Context, enabledOnly bool) ([]AuditExport, error) {
	q := `SELECT ` + auditCols + ` FROM audit_exports`
	if enabledOnly {
		q += ` WHERE enabled = 1`
	}
	rows, err := s.DB.Query(ctx, q+` ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditExport{}
	for rows.Next() {
		a, err := scanAudit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// envelope renders a ledger event as the CloudEvents JSON body that is sent
// (full) or, with thin set, the envelope with data replaced by a signed URL.
func envelope(ev ledger.Event, thin bool, url string) map[string]any {
	m := map[string]any{
		"specversion":     "1.0",
		"id":              ev.ID,
		"source":          ev.Source,
		"type":            ev.Type,
		"subject":         ev.Subject,
		"time":            ev.Time,
		"datacontenttype": "application/json",
		"oreactor":        ev.OreActor,
		"oreorg":          ev.OreOrg,
	}
	if ev.Seq > 0 {
		m["oreseq"] = ev.Seq
	}
	if thin {
		m["data"] = map[string]any{"url": url, "thin": true}
	} else {
		m["data"] = json.RawMessage(ev.Data)
	}
	return m
}

func validKind(kind string) bool {
	switch kind {
	case KindEvent, KindPing, KindReplay, KindRedeliver:
		return true
	}
	return false
}

func describeStatus(code int, err error) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("HTTP %d", code)
}

var _ = strings.TrimSpace

// EventsRange returns events with afterSeq < seq <= uptoSeq (uptoSeq 0: no
// bound), optionally for one org, in sequence order. Reading the head first
// and bounding the scan by it lets a cursor advance past non-matching events
// without missing one committed meanwhile.
func (s *Store) EventsRange(ctx context.Context, orgID string, afterSeq, uptoSeq int64, limit int) ([]ledger.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q := `SELECT seq, id, org_id, type, subject, time, actor_kind, actor_id, data FROM events WHERE seq > ?`
	args := []any{afterSeq}
	if uptoSeq > 0 {
		q += ` AND seq <= ?`
		args = append(args, uptoSeq)
	}
	if orgID != "" {
		q += ` AND org_id = ?`
		args = append(args, orgID)
	}
	q += ` ORDER BY seq ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ledger.Event
	for rows.Next() {
		var ev ledger.Event
		var data string
		if err := rows.Scan(&ev.Seq, &ev.ID, &ev.OreOrg, &ev.Type, &ev.Subject, &ev.Time, &ev.OreActor.Kind, &ev.OreActor.ID, &data); err != nil {
			return nil, err
		}
		ev.SpecVersion, ev.DataContentType, ev.Source, ev.Data = "1.0", "application/json", "ore://"+ev.OreOrg, json.RawMessage(data)
		out = append(out, ev)
	}
	return out, rows.Err()
}
