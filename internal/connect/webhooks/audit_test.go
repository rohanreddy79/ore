// SPDX-License-Identifier: AGPL-3.0-only

package webhooks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

type auditFixture struct {
	store *Store
	org   ledger.Org
	now   time.Time
	ex    *Exporter
}

func newAuditFixture(t *testing.T) *auditFixture {
	t.Helper()
	db, err := ledger.Open(context.Background(), filepath.Join(t.TempDir(), "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	org, err := db.CreateOrg(context.Background(), "acme", ledger.Actor{Kind: "human", ID: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	f := &auditFixture{store: &Store{DB: db}, org: org, now: time.Date(2026, 1, 1, 23, 59, 0, 0, time.UTC)}
	f.ex = &Exporter{Store: f.store, Batch: 100, Now: func() time.Time { return f.now }}
	return f
}

func (f *auditFixture) emit(t *testing.T, n int, typ string) {
	t.Helper()
	tx, err := f.store.DB.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := tx.Emit(f.org.ID, typ, "acme/app/x", ledger.System, map[string]any{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func (f *auditFixture) export(t *testing.T, kind string, cfg any, types []string) AuditExport {
	t.Helper()
	raw, _ := json.Marshal(cfg)
	a := AuditExport{ID: ledger.NewID("aexp"), Kind: kind, Config: raw, FilterTypes: types, Enabled: true, CreatedAt: stamp(f.now), UpdatedAt: stamp(f.now)}
	if err := f.store.InsertAuditExport(context.Background(), &a, ledger.System); err != nil {
		t.Fatal(err)
	}
	return a
}

func lines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestAuditFileRotationAcrossDayBoundary(t *testing.T) {
	f := newAuditFixture(t)
	dir := t.TempDir()
	a := f.export(t, "file", FileConfig{Dir: dir}, []string{"io.ore.test."})
	f.emit(t, 3, "io.ore.test.one")
	f.emit(t, 2, "io.ore.other.ignored")
	f.ex.Tick(context.Background())
	day1 := FileName(dir, f.now)
	if got := lines(t, day1); len(got) != 3 || !strings.Contains(got[0], `"type":"io.ore.test.one"`) {
		t.Fatalf("day 1: %v", got)
	}
	cur, _ := f.store.AuditExport(context.Background(), a.ID)
	head, _ := f.store.HeadSeq(context.Background())
	if cur.Cursor != head || cur.Exported != 3 || cur.LastExportAt == nil {
		t.Fatalf("progress %+v head %d", cur, head)
	}
	// the clock crosses midnight: new events go to the next day's file, the old file is untouched
	f.now = f.now.Add(2 * time.Minute)
	f.emit(t, 2, "io.ore.test.two")
	f.ex.Tick(context.Background())
	day2 := FileName(dir, f.now)
	if day2 == day1 || !strings.HasSuffix(day2, "events-2026-01-02.ndjson") {
		t.Fatalf("rotation: %s", day2)
	}
	if got := lines(t, day2); len(got) != 2 || !strings.Contains(got[1], `"type":"io.ore.test.two"`) {
		t.Fatalf("day 2: %v", got)
	}
	if got := lines(t, day1); len(got) != 3 {
		t.Fatalf("day 1 changed: %v", got)
	}
	// a restarted exporter resumes from the persisted cursor without rewriting
	restarted := &Exporter{Store: f.store, Now: func() time.Time { return f.now }}
	restarted.Tick(context.Background())
	if got := lines(t, day2); len(got) != 2 {
		t.Fatalf("restart duplicated: %v", got)
	}
	f.emit(t, 1, "io.ore.test.three")
	restarted.Tick(context.Background())
	if got := lines(t, day2); len(got) != 3 {
		t.Fatalf("after restart: %v", got)
	}
	// every line is a complete CloudEvent
	for _, l := range lines(t, day2) {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil || m["specversion"] != "1.0" || m["oreorg"] != f.org.ID || m["oreseq"] == nil {
			t.Fatalf("line %s: %v", l, err)
		}
	}
	st, _ := f.ex.Status(context.Background())
	if len(st.Exports) != 1 || st.Exports[0].Lag != 0 || st.Exports[0].Exported != 6 {
		t.Fatalf("status %+v", st)
	}
}

func TestAuditFileExportPagesAndErrors(t *testing.T) {
	f := newAuditFixture(t)
	f.ex.Batch = 4
	dir := t.TempDir()
	f.export(t, "file", FileConfig{Dir: dir}, nil)
	f.emit(t, 10, "io.ore.test.page")
	f.ex.Tick(context.Background())
	// org.created + audit.export.created + 10 = 12 lines, written over several pages in one tick
	if got := lines(t, FileName(dir, f.now)); len(got) != 12 {
		t.Fatalf("lines %d", len(got))
	}
	// an unwritable directory is reported on the export, not fatal
	bad := f.export(t, "file", FileConfig{Dir: filepath.Join(dir, "events-2026-01-01.ndjson", "sub")}, nil)
	f.ex.Tick(context.Background())
	cur, _ := f.store.AuditExport(context.Background(), bad.ID)
	if cur.LastError == "" || cur.Cursor != 0 {
		t.Fatalf("bad export %+v", cur)
	}
}

func TestAuditS3PartialBatchFlushesAfterMaxDelay(t *testing.T) {
	f := newAuditFixture(t)
	f.ex.Batch, f.ex.MaxDelay = 10, 30*time.Second
	f.ex.Credentials = func() (string, string) { return "AK", "SK" }
	var puts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want, signed, scope := SigV4Signature(r, "SK", "us-east-1", "s3", r.Header.Get("x-amz-content-sha256"), r.Header.Get("x-amz-date"))
		if r.Header.Get("Authorization") != "AWS4-HMAC-SHA256 Credential=AK/"+scope+", SignedHeaders="+signed+", Signature="+want {
			w.WriteHeader(403)
			return
		}
		puts = append(puts, r.URL.Path)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	f.ex.Client = srv.Client()
	f.export(t, "s3", S3Config{Bucket: "b", Endpoint: srv.URL, Prefix: "p"}, []string{"io.ore.test."})
	f.emit(t, 3, "io.ore.test.partial")
	f.ex.Tick(context.Background())
	if len(puts) != 0 {
		t.Fatalf("partial batch flushed at once: %v", puts)
	}
	f.now = f.now.Add(31 * time.Second)
	f.ex.Tick(context.Background())
	if len(puts) != 1 || !strings.HasPrefix(puts[0], "/b/p/2026/01/01/events-") {
		t.Fatalf("after max delay: %v", puts)
	}
	// a full batch flushes immediately
	f.emit(t, 10, "io.ore.test.full")
	f.ex.Tick(context.Background())
	if len(puts) != 2 {
		t.Fatalf("full batch: %v", puts)
	}
	// an unknown kind is an error on the export, not a crash
	f.export(t, "ftp", map[string]any{}, nil)
	f.ex.Tick(context.Background())
	st, _ := f.ex.Status(context.Background())
	if len(st.Exports) != 2 || !strings.Contains(st.Exports[1].LastError, "unknown export kind") {
		t.Fatalf("status %+v", st.Exports)
	}
}
