// SPDX-License-Identifier: AGPL-3.0-only

package ledger

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/orevcs/ore/internal/ledger/pg"
)

func openTemp(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ore.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func TestSQLitePathForms(t *testing.T) {
	for in, want := range map[string]string{
		"/tmp/ore.db":                "/tmp/ore.db",
		"ore-data/ore.db":            "ore-data/ore.db",
		"sqlite:///tmp/ore.db":       "/tmp/ore.db",
		"sqlite://ore-data/ore.db":   "ore-data/ore.db",
		"sqlite:ore-data/ore.db":     "ore-data/ore.db",
		"sqlite:/abs/ore.db":         "/abs/ore.db",
		"postgres://u:p@h/db":        "postgres://u:p@h/db", // not a sqlite target; passed through untouched
		"sqlite:///tmp/a b/ore.db":   "/tmp/a b/ore.db",
		"sqlite:////double/slash.db": "//double/slash.db",
	} {
		if got := SQLitePath(in); got != want {
			t.Errorf("SQLitePath(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := Open(context.Background(), "  "); err == nil {
		t.Fatal("empty target must fail")
	}
}

func TestMigrationsAreIdempotentAndListed(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	ms, err := db.Migrations(ctx)
	if err != nil || len(ms) < 7 {
		t.Fatalf("%v %d", err, len(ms))
	}
	for _, m := range ms {
		if m.AppliedAt == "" {
			t.Fatalf("migration %s not applied", m.Name)
		}
	}
	if ms[0].Version != 1 || !strings.HasPrefix(ms[0].Name, "0001_") {
		t.Fatalf("order %+v", ms[0])
	}
	// a second run applies nothing and fails nothing
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// opening without migrating lists pending migrations on a fresh database
	fresh, err := OpenWith(ctx, filepath.Join(t.TempDir(), "fresh.db"), Options{NoMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if fresh.Dialect() == DialectSQLite {
		pending, _ := fresh.Migrations(ctx)
		for _, m := range pending {
			if m.AppliedAt != "" {
				t.Fatalf("fresh database must have pending migrations: %+v", m)
			}
		}
	}
}

func TestConcurrentMigrateOneWinsOthersFindItDone(t *testing.T) {
	if os.Getenv("ORE_TEST_DB_URL") != "" {
		t.Skip("each test Open gets its own Postgres schema; the advisory lock is exercised by cmd/ore's process test")
	}
	path := filepath.Join(t.TempDir(), "ore.db")
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db, err := Open(context.Background(), path)
			if err != nil {
				errs[i] = err
				return
			}
			db.Close()
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("migrator %d: %v", i, err)
		}
	}
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(context.Background(), `SELECT COUNT(1) FROM schema_migrations`).Scan(&n); err != nil || n < 7 {
		t.Fatalf("schema_migrations rows: %d %v", n, err)
	}
}

func TestUniqueViolationIsRecognisedOnEveryDialect(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	if _, err := db.CreateOrg(ctx, "acme", System); err != nil {
		t.Fatal(err)
	}
	_, err := db.CreateOrg(ctx, "acme", System)
	if err == nil || !IsUniqueViolation(err) || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("duplicate org: %v", err)
	}
	if IsUniqueViolation(errors.New("other")) || IsUniqueViolation(nil) {
		t.Fatal("false positives")
	}
}

func TestTablesAreInForeignKeyOrder(t *testing.T) {
	db, _ := openTemp(t)
	tables, err := db.Tables(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, tb := range tables {
		pos[tb] = i
	}
	for _, pair := range [][2]string{{"orgs", "repos"}, {"repos", "issues"}, {"repos", "deployments"}, {"deployments", "deployment_approvals"}, {"changes", "patch_sets"}, {"users", "tokens"}} {
		a, okA := pos[pair[0]]
		b, okB := pos[pair[1]]
		if !okA || !okB {
			t.Fatalf("missing table in %v: %v", pair, tables)
		}
		if a > b {
			t.Fatalf("%s must come before %s: %v", pair[0], pair[1], tables)
		}
	}
}

func TestDumpAndLoadRoundTrip(t *testing.T) {
	ctx := context.Background()
	src, _ := openTemp(t)
	org, err := src.CreateOrg(ctx, "acme", System)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := src.CreateRepo(ctx, org, "app", "main", 3, 8080, System)
	if err != nil {
		t.Fatal(err)
	}
	issue, _ := src.CreateIssue(ctx, repo, "title with 'quotes' and ?", "body\nwith\nlines", Actor{Kind: "human", ID: "alice"})
	_ = src.CloseIssue(ctx, repo, issue, "done", System)
	// a blob column (secrets.ciphertext) and a nullable integer survive too
	if _, err := src.Exec(ctx, `INSERT INTO secrets(id, org_id, repo_id, environment, name, ciphertext, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?)`,
		"sec_1", org.ID, repo.ID, "", "k", []byte{0, 1, 2, 255}, Now(), Now()); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := src.Dump(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `{"table":"orgs"`) || !strings.Contains(buf.String(), `"b":"AAEC/w=="`) {
		t.Fatalf("dump:\n%s", buf.String())
	}
	dst, _ := openTemp(t)
	if err := dst.Load(ctx, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}
	got, err := dst.RepoByName(ctx, "acme", "app")
	if err != nil || got.ID != repo.ID || got.AutonomyLevel != 3 {
		t.Fatalf("%v %+v", err, got)
	}
	gi, err := dst.Issue(ctx, got, 1)
	if err != nil || gi.Title != issue.Title || gi.State != "closed" || gi.ClosedAt == nil {
		t.Fatalf("%v %+v", err, gi)
	}
	a, _ := src.Events(ctx, EventsOptions{Limit: 100})
	b, _ := dst.Events(ctx, EventsOptions{Limit: 100})
	if len(a) != len(b) || len(a) == 0 {
		t.Fatalf("events %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Seq != b[i].Seq || string(a[i].Data) != string(b[i].Data) {
			t.Fatalf("event %d differs: %+v vs %+v", i, a[i], b[i])
		}
	}
	var ct []byte
	if err := dst.QueryRow(ctx, `SELECT ciphertext FROM secrets WHERE id = ?`, "sec_1").Scan(&ct); err != nil || !bytes.Equal(ct, []byte{0, 1, 2, 255}) {
		t.Fatalf("blob %v %v", ct, err)
	}
	// new events continue after the restored sequence
	if _, err := dst.CreateOrg(ctx, "next", System); err != nil {
		t.Fatal(err)
	}
	evs, _ := dst.Events(ctx, EventsOptions{Type: "io.ore.org.created"})
	if len(evs) != 2 || evs[1].Seq <= evs[0].Seq {
		t.Fatalf("sequence after load: %+v", evs)
	}
	// loading into a non-empty database is refused
	if err := dst.Load(ctx, bytes.NewReader(buf.Bytes())); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("load into non-empty: %v", err)
	}
	counts, _ := dst.Counts(ctx)
	if counts["orgs"] != 2 || counts["repos"] != 1 {
		t.Fatalf("counts %v", counts)
	}
}

func TestVacuumIntoCopiesSQLite(t *testing.T) {
	db, _ := openTemp(t)
	if db.Dialect() != DialectSQLite {
		t.Skip("sqlite only")
	}
	ctx := context.Background()
	if _, err := db.CreateOrg(ctx, "acme", System); err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "copy.db")
	if err := db.VacuumInto(ctx, copyPath); err != nil {
		t.Fatal(err)
	}
	cp, err := Open(ctx, copyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	if _, err := cp.OrgByName(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresWrongCredentialsGiveAClearError(t *testing.T) {
	base := os.Getenv("ORE_TEST_DB_URL")
	if base == "" {
		t.Skip("needs ORE_TEST_DB_URL")
	}
	bad := strings.Replace(base, "@", "WRONGPASSWORD@", 1)
	_, err := Open(context.Background(), bad)
	if err == nil || !strings.Contains(err.Error(), "authentication failed") || strings.Contains(err.Error(), "WRONGPASSWORD") {
		t.Fatalf("expected a clear, redacted authentication error, got %v", err)
	}
	if !strings.Contains(err.Error(), pg.Redact(bad)) {
		t.Fatalf("error must name the redacted target: %v", err)
	}
}

func TestOpenPostgresDialectWhenConfigured(t *testing.T) {
	if os.Getenv("ORE_TEST_DB_URL") == "" {
		t.Skip("needs ORE_TEST_DB_URL")
	}
	db, _ := openTemp(t)
	if db.Dialect() != DialectPostgres || !strings.HasPrefix(db.Target(), "postgres://") || strings.Contains(db.Target(), ":ore@") {
		t.Fatalf("dialect %s target %s", db.Dialect(), db.Target())
	}
	ctx := context.Background()
	// the same statements run on both dialects: placeholders, ON CONFLICT, LIKE, COALESCE, LIMIT
	org, _ := db.CreateOrg(ctx, "acme", System)
	repo, _ := db.CreateRepo(ctx, org, "app", "main", 2, 8080, System)
	if _, err := db.Exec(ctx, `INSERT INTO preview_ttls(deployment_id, org_id, repo_id, expires_at, extensions) VALUES(?,?,?,?,0) ON CONFLICT(deployment_id) DO UPDATE SET expires_at = excluded.expires_at`, "dep_x", org.ID, repo.ID, Now()); err == nil {
		t.Fatal("foreign key to deployments must hold on postgres too")
	}
	evs, err := db.Events(ctx, EventsOptions{Type: "io.ore.", Limit: 10})
	if err != nil || len(evs) != 2 {
		t.Fatalf("%v %d", err, len(evs))
	}
}
