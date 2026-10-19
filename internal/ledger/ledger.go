// SPDX-License-Identifier: AGPL-3.0-only

// Package ledger is Ore's spine: typed entities with provenance, written in
// the same transaction as the CloudEvents that describe them (ADR 0005). It
// runs on exactly two SQL targets (ADR 0006): SQLite for the solo profile and
// Postgres for team and cloud. Statements are written once, in SQLite's
// dialect, and rewritten for Postgres by this package (internal/ledger/pg).
package ledger

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/orevcs/ore/internal/ledger/pg"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Dialects.
const (
	DialectSQLite   = "sqlite"
	DialectPostgres = "postgres"
)

// DB wraps the SQL connection. All methods are safe for concurrent use.
type DB struct {
	sql     *sql.DB
	dialect string
	target  string // the SQLite path or the Postgres URL Open was given

	// testSchema is the Postgres schema created for an isolated test database
	// (ORE_TEST_DB_URL); Close drops it through baseURL.
	testSchema string
	baseURL    string
}

// Options tunes Open.
type Options struct {
	// NoMigrate opens without applying pending migrations (`ore migrate status`).
	NoMigrate bool
}

// Open opens the ledger at target and applies migrations. target is a SQLite
// path (bare, `sqlite:path` or `sqlite:///abs/path`) or a Postgres URL
// (`postgres://user:pass@host:5432/db?sslmode=...`). When ORE_TEST_DB_URL is
// set, SQLite targets are redirected to that Postgres server in a fresh,
// isolated schema that Close drops: this is how the whole test suite runs on
// Postgres without changing a test (scripts/test-postgres.sh).
func Open(ctx context.Context, target string) (*DB, error) {
	return OpenWith(ctx, target, Options{})
}

// OpenWith opens with options.
func OpenWith(ctx context.Context, target string, opt Options) (*DB, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("ledger: empty database target")
	}
	if u := os.Getenv("ORE_TEST_DB_URL"); u != "" && !pg.IsURL(target) {
		return openTestPostgres(ctx, u, opt)
	}
	if pg.IsURL(target) {
		return openPostgres(ctx, target, opt)
	}
	return openSQLite(ctx, SQLitePath(target), opt)
}

// SQLitePath strips a sqlite: scheme: `sqlite:///abs/path` → `/abs/path`,
// `sqlite://rel/path` → `rel/path`, `sqlite:path` → `path`; bare paths pass through.
func SQLitePath(target string) string {
	switch {
	case strings.HasPrefix(target, "sqlite:///"):
		return strings.TrimPrefix(target, "sqlite://")
	case strings.HasPrefix(target, "sqlite://"):
		return strings.TrimPrefix(target, "sqlite://")
	case strings.HasPrefix(target, "sqlite:"):
		return strings.TrimPrefix(target, "sqlite:")
	}
	return target
}

func openSQLite(ctx context.Context, path string, opt Options) (*DB, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_txlock=immediate"
	s, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	s.SetMaxOpenConns(1) // SQLite: serialize writers; readers share the single connection in v0.
	db := &DB{sql: s, dialect: DialectSQLite, target: path}
	// the first connection switches the journal to WAL, which needs the write
	// lock: wait for a concurrent opener (another ore process) to finish
	if err := db.withRetry(ctx, func() error { return db.sql.PingContext(ctx) }); err != nil {
		s.Close()
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if !opt.NoMigrate {
		if err := db.Migrate(ctx); err != nil {
			s.Close()
			return nil, err
		}
	}
	return db, nil
}

func openPostgres(ctx context.Context, url string, opt Options) (*DB, error) {
	s, err := sql.Open(pg.Driver, url)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	s.SetMaxOpenConns(16)
	s.SetMaxIdleConns(4)
	s.SetConnMaxIdleTime(5 * time.Minute)
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.PingContext(pctx); err != nil {
		s.Close()
		switch {
		case pg.IsAuthError(err):
			return nil, fmt.Errorf("postgres %s: authentication failed, check the user and password in ORE_DB_URL: %w", pg.Redact(url), err)
		case errors.Is(err, context.DeadlineExceeded):
			return nil, fmt.Errorf("postgres %s: no answer within 10s, check the host and port in ORE_DB_URL: %w", pg.Redact(url), err)
		}
		return nil, fmt.Errorf("postgres %s: %w", pg.Redact(url), err)
	}
	db := &DB{sql: s, dialect: DialectPostgres, target: url}
	if !opt.NoMigrate {
		if err := db.Migrate(ctx); err != nil {
			s.Close()
			return nil, err
		}
	}
	return db, nil
}

// openTestPostgres creates a fresh schema on the test server and opens it.
func openTestPostgres(ctx context.Context, base string, opt Options) (*DB, error) {
	schema := pg.TestSchemaName()
	admin, err := sql.Open(pg.Driver, base)
	if err != nil {
		return nil, err
	}
	_, err = admin.ExecContext(ctx, "CREATE SCHEMA "+pg.QuoteIdent(schema))
	admin.Close()
	if err != nil {
		return nil, fmt.Errorf("ORE_TEST_DB_URL: create schema: %w", err)
	}
	db, err := openPostgres(ctx, pg.WithSearchPath(base, schema), opt)
	if err != nil {
		return nil, err
	}
	db.testSchema, db.baseURL = schema, base
	return db, nil
}

// Close closes the database (and drops an isolated test schema).
func (d *DB) Close() error {
	err := d.sql.Close()
	if d.testSchema != "" {
		if admin, aerr := sql.Open(pg.Driver, d.baseURL); aerr == nil {
			_, _ = admin.Exec("DROP SCHEMA " + pg.QuoteIdent(d.testSchema) + " CASCADE")
			admin.Close()
		}
	}
	return err
}

// Dialect is "sqlite" or "postgres".
func (d *DB) Dialect() string { return d.dialect }

// TableExists reports whether a table exists on either dialect; feature
// packages use it to degrade gracefully when another slice's migration is
// absent. On Postgres it is scoped to the current schema.
func (d *DB) TableExists(ctx context.Context, name string) bool {
	var n int
	q := `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`
	if d.dialect == "postgres" {
		q = `SELECT COUNT(1) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?`
	}
	if err := d.QueryRow(ctx, q, name).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// Target describes the database for humans, with any password redacted.
func (d *DB) Target() string {
	if d.dialect == DialectPostgres {
		return pg.Redact(d.target)
	}
	return d.target
}

// Ping checks connectivity.
func (d *DB) Ping(ctx context.Context) error { return d.sql.PingContext(ctx) }

// Stats returns the connection pool statistics.
func (d *DB) Stats() sql.DBStats { return d.sql.Stats() }

// rewrite adapts a statement written for SQLite to the open dialect.
func (d *DB) rewrite(q string) string {
	if d.dialect == DialectPostgres {
		return pg.Placeholders(q)
	}
	return q
}

// uniqueError gives Postgres unique violations the message SQLite callers
// test for, so one check works on both dialects.
type uniqueError struct{ err error }

func (e *uniqueError) Error() string { return "UNIQUE constraint failed: " + e.err.Error() }
func (e *uniqueError) Unwrap() error { return e.err }

func (d *DB) wrapErr(err error) error {
	if err == nil {
		return nil
	}
	if d.dialect == DialectPostgres && pg.IsUniqueViolation(err) {
		return &uniqueError{err: err}
	}
	return err
}

// IsUniqueViolation reports whether err is a unique-constraint violation on either dialect.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var ue *uniqueError
	return errors.As(err, &ue) || pg.IsUniqueViolation(err) || strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// Migration describes one embedded migration file and whether it is applied.
type Migration struct {
	Version   int    `json:"version"`
	Name      string `json:"name"`
	AppliedAt string `json:"applied_at,omitempty"`
}

func migrationFiles() ([]Migration, error) {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, e := range entries {
		version, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			return nil, fmt.Errorf("migration %s: bad version prefix", e.Name())
		}
		out = append(out, Migration{Version: version, Name: e.Name()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Migrations lists the embedded migrations with their applied time ("" when pending).
func (d *DB) Migrations(ctx context.Context) ([]Migration, error) {
	files, err := migrationFiles()
	if err != nil {
		return nil, err
	}
	applied := map[int]string{}
	rows, err := d.sql.QueryContext(ctx, `SELECT version, applied_at FROM schema_migrations`)
	if err == nil {
		for rows.Next() {
			var v int
			var at string
			if rows.Scan(&v, &at) == nil {
				applied[v] = at
			}
		}
		rows.Close()
	}
	for i := range files {
		files[i].AppliedAt = applied[files[i].Version]
	}
	return files, nil
}

// Migrate applies embedded migrations in filename order, once each, under a
// lock so concurrent processes serialize: a Postgres advisory lock, and on
// SQLite immediate transactions with a re-check inside each one, so a second
// `ore migrate` waits or finds the work done.
func (d *DB) Migrate(ctx context.Context) error {
	files, err := migrationFiles()
	if err != nil {
		return err
	}
	unlock, err := d.lockMigrations(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := d.withRetry(ctx, func() error {
		_, err := d.sql.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`)
		return err
	}); err != nil {
		return fmt.Errorf("schema_migrations: %w", err)
	}
	for _, m := range files {
		var n int
		if err := d.sql.QueryRowContext(ctx, d.rewrite(`SELECT COUNT(1) FROM schema_migrations WHERE version = ?`), m.Version).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + m.Name)
		if err != nil {
			return err
		}
		ddl := string(body)
		if d.dialect == DialectPostgres {
			ddl = pg.Schema(ddl)
		}
		if err := d.withRetry(ctx, func() error { return d.applyMigration(ctx, m, ddl) }); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) applyMigration(ctx context.Context, m Migration, ddl string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, d.rewrite(`SELECT COUNT(1) FROM schema_migrations WHERE version = ?`), m.Version).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil // another process applied it while we waited
	}
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("apply %s: %w", m.Name, err)
	}
	if _, err := tx.ExecContext(ctx, d.rewrite(`INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`), m.Version, Now()); err != nil {
		return fmt.Errorf("record %s: %w", m.Name, err)
	}
	return tx.Commit()
}

// lockMigrations serializes migrators across processes.
func (d *DB) lockMigrations(ctx context.Context) (func(), error) {
	if d.dialect != DialectPostgres {
		return func() {}, nil
	}
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(7265)`); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migration lock: %w", err)
	}
	return func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(7265)`)
		conn.Close()
	}, nil
}

func isBusy(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "SQLITE_BUSY") || strings.Contains(s, "database is locked") || strings.Contains(s, "database table is locked")
}

// withRetry retries SQLite busy errors (another process holds the write lock)
// for up to a minute.
func (d *DB) withRetry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < 240; attempt++ {
		err = fn()
		if err == nil || d.dialect != DialectSQLite || !isBusy(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return err
}

// Tx is a transaction that can emit events atomically with entity writes. Its
// statement methods rewrite the SQL for the open dialect, so callers write
// SQLite syntax with `?` placeholders on both targets.
type Tx struct {
	*sql.Tx
	ctx context.Context
	db  *DB
}

// Begin starts a transaction.
func (d *DB) Begin(ctx context.Context) (*Tx, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &Tx{Tx: tx, ctx: ctx, db: d}, nil
}

// ExecContext executes a statement in the transaction.
func (t *Tx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	r, err := t.Tx.ExecContext(ctx, t.db.rewrite(q), args...)
	return r, t.db.wrapErr(err)
}

// Exec executes a statement with the transaction's context.
func (t *Tx) Exec(q string, args ...any) (sql.Result, error) { return t.ExecContext(t.ctx, q, args...) }

// QueryContext runs a query in the transaction.
func (t *Tx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	rows, err := t.Tx.QueryContext(ctx, t.db.rewrite(q), args...)
	return rows, t.db.wrapErr(err)
}

// Query runs a query with the transaction's context.
func (t *Tx) Query(q string, args ...any) (*sql.Rows, error) {
	return t.QueryContext(t.ctx, q, args...)
}

// QueryRowContext runs a single-row query in the transaction.
func (t *Tx) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(ctx, t.db.rewrite(q), args...)
}

// QueryRow runs a single-row query with the transaction's context.
func (t *Tx) QueryRow(q string, args ...any) *sql.Row { return t.QueryRowContext(t.ctx, q, args...) }

// Query proxies read queries.
func (d *DB) Query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	rows, err := d.sql.QueryContext(ctx, d.rewrite(q), args...)
	return rows, d.wrapErr(err)
}

// QueryRow proxies single-row reads.
func (d *DB) QueryRow(ctx context.Context, q string, args ...any) *sql.Row {
	return d.sql.QueryRowContext(ctx, d.rewrite(q), args...)
}

// Exec proxies writes that do not need events.
func (d *DB) Exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	r, err := d.sql.ExecContext(ctx, d.rewrite(q), args...)
	return r, d.wrapErr(err)
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
	rows, err := d.Query(ctx, q, args...)
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
