// SPDX-License-Identifier: AGPL-3.0-only

package ledger

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/ledger/pg"
)

// The portable dump is JSON Lines: a header line per table followed by one
// line per row. Cells are tagged so integers, floats, text, bytes and NULL
// survive a round trip between dialects: {"i":1} {"f":1.5} {"s":"x"}
// {"b":"base64"} null. Tables are written in foreign-key order so a load
// never violates a constraint. `ore backup` writes it for every dialect (it is
// the pg_dump equivalent through the server's own connection) and `ore
// restore` loads it into an empty database of either dialect.

type dumpHeader struct {
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
}

type dumpRow struct {
	Row []json.RawMessage `json:"row"`
}

// DumpFormat identifies the dump layout in manifests.
const DumpFormat = "ore-ledger-jsonl/1"

// Tables lists the ledger's tables in foreign-key order (referenced first).
func (d *DB) Tables(ctx context.Context) ([]string, error) {
	var names []string
	var q string
	if d.dialect == DialectPostgres {
		q = `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_type = 'BASE TABLE' ORDER BY table_name`
	} else {
		q = `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`
	}
	rows, err := d.sql.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	rows.Close()
	deps := map[string][]string{}
	for _, t := range names {
		refs, err := d.references(ctx, t)
		if err != nil {
			return nil, err
		}
		deps[t] = refs
	}
	// topological order: referenced tables first
	var out []string
	state := map[string]int{}
	var visit func(t string)
	visit = func(t string) {
		if state[t] != 0 {
			return
		}
		state[t] = 1
		for _, r := range deps[t] {
			if r != t {
				visit(r)
			}
		}
		state[t] = 2
		out = append(out, t)
	}
	for _, t := range names {
		visit(t)
	}
	return out, nil
}

// references lists the tables t has foreign keys to.
func (d *DB) references(ctx context.Context, t string) ([]string, error) {
	var out []string
	if d.dialect == DialectPostgres {
		rows, err := d.sql.QueryContext(ctx, `
			SELECT DISTINCT ccu.table_name
			  FROM information_schema.table_constraints tc
			  JOIN information_schema.constraint_column_usage ccu ON ccu.constraint_name = tc.constraint_name AND ccu.constraint_schema = tc.constraint_schema
			 WHERE tc.constraint_type = 'FOREIGN KEY' AND tc.table_schema = current_schema() AND tc.table_name = $1`, t)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var r string
			if err := rows.Scan(&r); err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, rows.Err()
	}
	rows, err := d.sql.QueryContext(ctx, `PRAGMA foreign_key_list("`+strings.ReplaceAll(t, `"`, `""`)+`")`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		for i, c := range cols {
			if c == "table" {
				if s, ok := vals[i].(string); ok {
					out = append(out, s)
				}
			}
		}
	}
	return out, rows.Err()
}

// Dump writes every table as JSON Lines from one consistent snapshot.
func (d *DB) Dump(ctx context.Context, w io.Writer) error {
	tables, err := d.Tables(ctx)
	if err != nil {
		return err
	}
	opts := &sql.TxOptions{ReadOnly: d.dialect == DialectPostgres}
	if d.dialect == DialectPostgres {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := d.sql.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	bw := bufio.NewWriterSize(w, 1<<20)
	enc := json.NewEncoder(bw)
	for _, t := range tables {
		rows, err := tx.QueryContext(ctx, `SELECT * FROM `+d.quote(t))
		if err != nil {
			return fmt.Errorf("dump %s: %w", t, err)
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return err
		}
		if err := enc.Encode(dumpHeader{Table: t, Columns: cols}); err != nil {
			rows.Close()
			return err
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return fmt.Errorf("dump %s: %w", t, err)
			}
			cells := make([]json.RawMessage, len(cols))
			for i, v := range vals {
				cells[i] = encodeCell(v)
			}
			if err := enc.Encode(dumpRow{Row: cells}); err != nil {
				rows.Close()
				return err
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	return bw.Flush()
}

func encodeCell(v any) json.RawMessage {
	var m map[string]any
	switch x := v.(type) {
	case nil:
		return json.RawMessage("null")
	case int64:
		m = map[string]any{"i": x}
	case int:
		m = map[string]any{"i": int64(x)}
	case int32:
		m = map[string]any{"i": int64(x)}
	case float64:
		m = map[string]any{"f": x}
	case float32:
		m = map[string]any{"f": float64(x)}
	case bool:
		n := 0
		if x {
			n = 1
		}
		m = map[string]any{"i": n}
	case []byte:
		m = map[string]any{"b": base64.StdEncoding.EncodeToString(x)}
	case string:
		m = map[string]any{"s": x}
	case time.Time:
		m = map[string]any{"s": x.UTC().Format("2006-01-02T15:04:05.000Z07:00")}
	default:
		m = map[string]any{"s": fmt.Sprint(x)}
	}
	b, _ := json.Marshal(m)
	return b
}

func decodeCell(raw json.RawMessage) (any, error) {
	s := strings.TrimSpace(string(raw))
	if s == "null" || s == "" {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for k, v := range m {
		switch k {
		case "i":
			var n int64
			return n, json.Unmarshal(v, &n)
		case "f":
			var f float64
			return f, json.Unmarshal(v, &f)
		case "s":
			var str string
			return str, json.Unmarshal(v, &str)
		case "b":
			var str string
			if err := json.Unmarshal(v, &str); err != nil {
				return nil, err
			}
			return base64.StdEncoding.DecodeString(str)
		}
	}
	return nil, fmt.Errorf("bad cell %s", s)
}

// ErrNotEmpty is returned by Load when the target database already holds rows.
var ErrNotEmpty = errors.New("database is not empty")

// Load reads a Dump into this database, which must be migrated and empty
// (every table but schema_migrations has no rows). It runs in one
// transaction; on Postgres identity sequences are advanced afterwards.
func (d *DB) Load(ctx context.Context, r io.Reader) error {
	tables, err := d.Tables(ctx)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, t := range tables {
		known[t] = true
		if t == "schema_migrations" {
			continue
		}
		var n int
		if err := d.sql.QueryRowContext(ctx, `SELECT COUNT(1) FROM `+d.quote(t)).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: table %s has %d rows", ErrNotEmpty, t, n)
		}
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if d.dialect == DialectSQLite {
		if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
			return err
		}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var cur dumpHeader
	var stmt *sql.Stmt
	var skip bool
	loaded := map[string]int{}
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		if strings.HasPrefix(string(line), `{"table"`) {
			if stmt != nil {
				stmt.Close()
				stmt = nil
			}
			if err := json.Unmarshal(line, &cur); err != nil {
				return err
			}
			skip = cur.Table == "schema_migrations" || !known[cur.Table]
			if skip {
				continue
			}
			cols := make([]string, len(cur.Columns))
			marks := make([]string, len(cur.Columns))
			for i, c := range cur.Columns {
				cols[i] = d.quote(c)
				marks[i] = "?"
			}
			q := fmt.Sprintf(`INSERT INTO %s(%s) VALUES(%s)`, d.quote(cur.Table), strings.Join(cols, ", "), strings.Join(marks, ","))
			stmt, err = tx.PrepareContext(ctx, d.rewrite(q))
			if err != nil {
				return fmt.Errorf("load %s: %w", cur.Table, err)
			}
			continue
		}
		if skip {
			continue
		}
		var row dumpRow
		if err := json.Unmarshal(line, &row); err != nil {
			return err
		}
		if len(row.Row) != len(cur.Columns) {
			return fmt.Errorf("load %s: row has %d cells for %d columns", cur.Table, len(row.Row), len(cur.Columns))
		}
		args := make([]any, len(row.Row))
		for i, c := range row.Row {
			v, err := decodeCell(c)
			if err != nil {
				return fmt.Errorf("load %s: %w", cur.Table, err)
			}
			args[i] = v
		}
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			return fmt.Errorf("load %s: %w", cur.Table, err)
		}
		loaded[cur.Table]++
	}
	if stmt != nil {
		stmt.Close()
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if d.dialect == DialectPostgres {
		if err := d.resetSequences(ctx, tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// resetSequences advances identity sequences past the loaded rows.
func (d *DB) resetSequences(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT table_name, column_name FROM information_schema.columns WHERE table_schema = current_schema() AND is_identity = 'YES'`)
	if err != nil {
		return err
	}
	type tc struct{ t, c string }
	var ids []tc
	for rows.Next() {
		var x tc
		if err := rows.Scan(&x.t, &x.c); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, x)
	}
	rows.Close()
	for _, x := range ids {
		q := fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%s', '%s'), COALESCE((SELECT MAX(%s) FROM %s), 0) + 1, false)`,
			pg.QuoteIdent(x.t), x.c, pg.QuoteIdent(x.c), pg.QuoteIdent(x.t))
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("reset sequence %s.%s: %w", x.t, x.c, err)
		}
	}
	return nil
}

func (d *DB) quote(ident string) string { return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"` }

// VacuumInto writes a consistent copy of a SQLite ledger to path.
func (d *DB) VacuumInto(ctx context.Context, path string) error {
	if d.dialect != DialectSQLite {
		return errors.New("VACUUM INTO is SQLite only")
	}
	_, err := d.sql.ExecContext(ctx, `VACUUM INTO '`+strings.ReplaceAll(path, "'", "''")+`'`)
	return err
}

// Counts returns row counts per table (for `ore doctor`, backups and metrics).
func (d *DB) Counts(ctx context.Context) (map[string]int, error) {
	tables, err := d.Tables(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, t := range tables {
		var n int
		if err := d.sql.QueryRowContext(ctx, `SELECT COUNT(1) FROM `+d.quote(t)).Scan(&n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, nil
}
