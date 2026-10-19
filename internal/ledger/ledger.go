// SPDX-License-Identifier: AGPL-3.0-only

// Package ledger is Ore's spine: typed entities with provenance, written in
// the same transaction as the CloudEvents that describe them (ADR 0005). v0
// targets SQLite; the schema is written so the same migrations run on
// Postgres (ADR 0006).
package ledger

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// DB wraps the SQL connection. All methods are safe for concurrent use.
type DB struct {
	sql *sql.DB
}

// Open opens (and creates) the SQLite database at path and applies migrations.
func Open(ctx context.Context, path string) (*DB, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	s, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	s.SetMaxOpenConns(1) // SQLite: serialize writers; readers share the single connection in v0.
	db := &DB{sql: s}
	if err := db.Migrate(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return db, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.sql.Close() }

// Migrate applies embedded migrations in filename order, once each.
func (d *DB) Migrate(ctx context.Context) error {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if _, err := d.sql.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %s: bad version prefix", name)
		}
		var n int
		if err := d.sql.QueryRowContext(ctx, `SELECT COUNT(1) FROM schema_migrations WHERE version = ?`, version).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := d.sql.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, version, Now()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Tx is a transaction that can emit events atomically with entity writes.
type Tx struct {
	*sql.Tx
	ctx context.Context
}

// Begin starts a transaction.
func (d *DB) Begin(ctx context.Context) (*Tx, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &Tx{Tx: tx, ctx: ctx}, nil
}

// Query proxies read queries.
func (d *DB) Query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return d.sql.QueryContext(ctx, q, args...)
}

// QueryRow proxies single-row reads.
func (d *DB) QueryRow(ctx context.Context, q string, args ...any) *sql.Row {
	return d.sql.QueryRowContext(ctx, q, args...)
}

// Exec proxies writes that do not need events.
func (d *DB) Exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return d.sql.ExecContext(ctx, q, args...)
}

// Now returns the ledger's canonical timestamp format (RFC 3339, UTC, millisecond).
func Now() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

// NewID returns a time-ordered, random, URL-safe identifier with a type prefix,
// e.g. "repo_01J9X...". Lexically sortable by creation time.
func NewID(prefix string) string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixMilli()))
	if _, err := rand.Read(b[8:]); err != nil {
		panic(err)
	}
	enc := base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)
	return prefix + "_" + enc.EncodeToString(b[:])[:20]
}

// Actor identifies who caused a transition (ADR 0009: actor kind is first-class).
type Actor struct {
	Kind string `json:"kind"` // human | app | agent | system
	ID   string `json:"id"`
}

// System is the actor for operations the platform performs itself.
var System = Actor{Kind: "system", ID: "ore"}

// Event is a CloudEvents 1.0 envelope with Ore's extension attributes.
type Event struct {
	Seq             int64           `json:"seq,omitempty"`
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Subject         string          `json:"subject"`
	Time            string          `json:"time"`
	DataContentType string          `json:"datacontenttype"`
	OreActor        Actor           `json:"oreactor"`
	OreOrg          string          `json:"oreorg"`
	Data            json.RawMessage `json:"data"`
}

// Emit writes an event inside the transaction so it commits with the entity change.
func (t *Tx) Emit(orgID, typ, subject string, actor Actor, data any) (Event, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Event{}, err
	}
	ev := Event{
		SpecVersion:     "1.0",
		ID:              NewID("evt"),
		Source:          "ore://" + orgID,
		Type:            typ,
		Subject:         subject,
		Time:            Now(),
		DataContentType: "application/json",
		OreActor:        actor,
		OreOrg:          orgID,
		Data:            raw,
	}
	_, err = t.ExecContext(t.ctx, `INSERT INTO events(id, org_id, type, subject, time, actor_kind, actor_id, data) VALUES(?,?,?,?,?,?,?,?)`,
		ev.ID, ev.OreOrg, ev.Type, ev.Subject, ev.Time, ev.OreActor.Kind, ev.OreActor.ID, string(ev.Data))
	return ev, err
}

// EventsOptions filters the event stream.
type EventsOptions struct {
	AfterSeq int64
	Type     string // exact type or prefix ending in '.'
	OrgID    string
	Subject  string
	Limit    int
}

// Events returns events in sequence order (replay-friendly).
func (d *DB) Events(ctx context.Context, opt EventsOptions) ([]Event, error) {
	if opt.Limit <= 0 || opt.Limit > 1000 {
		opt.Limit = 200
	}
	q := `SELECT seq, id, org_id, type, subject, time, actor_kind, actor_id, data FROM events WHERE seq > ?`
	args := []any{opt.AfterSeq}
	if opt.Type != "" {
		if strings.HasSuffix(opt.Type, ".") {
			q += ` AND type LIKE ?`
			args = append(args, opt.Type+"%")
		} else {
			q += ` AND type = ?`
			args = append(args, opt.Type)
		}
	}
	if opt.OrgID != "" {
		q += ` AND org_id = ?`
		args = append(args, opt.OrgID)
	}
	if opt.Subject != "" {
		q += ` AND subject = ?`
		args = append(args, opt.Subject)
	}
	q += ` ORDER BY seq ASC LIMIT ?`
	args = append(args, opt.Limit)
	rows, err := d.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var ev Event
		var data string
		if err := rows.Scan(&ev.Seq, &ev.ID, &ev.OreOrg, &ev.Type, &ev.Subject, &ev.Time, &ev.OreActor.Kind, &ev.OreActor.ID, &data); err != nil {
			return nil, err
		}
		ev.SpecVersion, ev.DataContentType, ev.Source = "1.0", "application/json", "ore://"+ev.OreOrg
		ev.Data = json.RawMessage(data)
		out = append(out, ev)
	}
	return out, rows.Err()
}
