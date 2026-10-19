// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/ledger/pg"
)

// syncBuffer collects server logs from several goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// testServer is an in-process `ore serve` on a free port with the host
// executor and no deployments, the way the e2e tests of other packages do.
type testServer struct {
	t    *testing.T
	s    *server
	cfg  config
	url  string
	logs *syncBuffer
}

// testConfig is a server configuration on a temporary data directory. On
// Postgres (ORE_TEST_DB_URL) it gets one dedicated schema for the whole test,
// so every Open of cfg.dbTarget() in the test sees the same database, as a
// real deployment would.
func testConfig(t *testing.T) config {
	cfg := config{Data: filepath.Join(t.TempDir(), "data"), Addr: "127.0.0.1:0", Executor: "host", AgentName: "test", PublicHost: "localhost",
		Token: "tok", LogFormat: "json", LogLevel: "info", DrainTimeout: 10 * time.Second, BackupKeep: 3, BackupInterval: time.Hour, Quiet: true}
	if base := os.Getenv("ORE_TEST_DB_URL"); base != "" {
		schema := pg.TestSchemaName()
		admin, err := sql.Open(pg.Driver, base)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec("CREATE SCHEMA " + pg.QuoteIdent(schema)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			admin.Exec("DROP SCHEMA " + pg.QuoteIdent(schema) + " CASCADE")
			admin.Close()
		})
		cfg.DB = pg.WithSearchPath(base, schema)
	}
	return cfg
}

func startTestServer(t *testing.T, extra func(*config)) *testServer {
	t.Helper()
	cfg := testConfig(t)
	if extra != nil {
		extra(&cfg)
	}
	return startWith(t, cfg)
}

func startWith(t *testing.T, cfg config) *testServer {
	t.Helper()
	logs := &syncBuffer{}
	s, err := newServer(context.Background(), cfg, logs)
	if err != nil {
		t.Fatal(err)
	}
	go s.serve()
	ts := &testServer{t: t, s: s, cfg: cfg, url: "http://" + s.addr(), logs: logs}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = s.shutdown(ctx)
	})
	return ts
}

func (ts *testServer) call(method, path string, in any, out any) (int, http.Header) {
	ts.t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, ts.url+path, body)
	req.Header.Set("Authorization", "Bearer "+ts.cfg.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		ts.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 400 {
		if s, ok := out.(*string); ok {
			*s = string(data)
		} else if err := json.Unmarshal(data, out); err != nil {
			ts.t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
	return resp.StatusCode, resp.Header
}

// seed creates acme/app with one commit whose pipeline runs `sleep N`.
func (ts *testServer) seed(sleep string) {
	ts.t.Helper()
	if code, _ := ts.call("POST", "/api/v0/orgs", map[string]string{"name": "acme"}, nil); code != 201 {
		ts.t.Fatalf("org %d", code)
	}
	if code, _ := ts.call("POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "app"}, nil); code != 201 {
		ts.t.Fatalf("repo %d", code)
	}
	code, _ := ts.call("POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "main", "message": "init", "files": []map[string]any{
		{"path": "hello.txt", "content": "hi\n"},
		{"path": ".ore/pipeline.yaml", "content": "version: 0\ntasks:\n  - id: wait\n    run: sleep " + sleep + "\n"},
	}}, nil)
	if code != 201 {
		ts.t.Fatalf("commit %d", code)
	}
}

func TestServeMetricsRequestIDsAndJSONLogs(t *testing.T) {
	ts := startTestServer(t, nil)
	ts.seed("0")
	req, _ := http.NewRequest("GET", ts.url+"/api/v0/orgs", nil)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("X-Request-ID", "trace-42")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-Request-ID") != "trace-42" {
		t.Fatalf("request id not echoed: %v", resp.Header)
	}
	resp, _ = http.Get(ts.url + "/healthz")
	if id := resp.Header.Get("X-Request-ID"); !strings.HasPrefix(id, "req_") {
		t.Fatalf("generated request id: %q", id)
	}
	resp.Body.Close()
	var run ledger.PipelineRun
	ts.call("POST", "/api/v0/repos/acme/app/runs", map[string]any{"wait": true}, &run)
	if run.Status != "succeeded" {
		t.Fatalf("run %+v", run)
	}
	resp, _ = http.Get(ts.url + "/metrics")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	text := string(body)
	for _, want := range []string{`ore_build_info{version=`, `ore_http_requests_total{method="POST",code="201"}`, `ore_http_requests_total{method="GET",code="200"}`, `ore_pipeline_runs_total{status="succeeded"} 1`, "ore_ledger_events_total ", "ore_http_request_duration_seconds_count", "ore_db_open_connections"} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics lack %q:\n%s", want, text)
		}
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
	}
	logs := ts.logs.String()
	if !strings.Contains(logs, `"request_id":"trace-42"`) || !strings.Contains(logs, `"msg":"request"`) || !strings.Contains(logs, `"path":"/api/v0/orgs"`) {
		t.Fatalf("no JSON request log with the request id:\n%s", logs)
	}
	var line map[string]any
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, "trace-42") {
			if err := json.Unmarshal([]byte(l), &line); err != nil {
				t.Fatalf("log line is not JSON: %v: %s", err, l)
			}
		}
	}
	if line["status"] != float64(200) || line["method"] != "GET" {
		t.Fatalf("log fields %v", line)
	}
}

func TestGracefulShutdownDrainsARunningPipeline(t *testing.T) {
	ts := startTestServer(t, func(c *config) { c.DrainTimeout = 15 * time.Second })
	ts.seed("2")
	var run ledger.PipelineRun
	if code, _ := ts.call("POST", "/api/v0/repos/acme/app/runs", map[string]any{"wait": false}, &run); code != 202 {
		t.Fatalf("start run: %d", code)
	}
	time.Sleep(300 * time.Millisecond) // let it reach running
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := ts.s.shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < time.Second {
		t.Fatalf("shutdown did not wait for the running pipeline (%s)", time.Since(start))
	}
	db, err := ledger.Open(context.Background(), ts.cfg.dbTarget())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.PipelineRun(context.Background(), run.ID)
	if err != nil || got.Status != "succeeded" {
		t.Fatalf("drained run must finish: %v %+v", err, got)
	}
	if !strings.Contains(ts.logs.String(), "draining running work") {
		t.Fatalf("drain must be logged:\n%s", ts.logs.String())
	}
}

func TestShutdownPastTheDrainTimeoutMarksRunsInterrupted(t *testing.T) {
	ts := startTestServer(t, func(c *config) { c.DrainTimeout = 0 })
	ts.seed("3")
	var run ledger.PipelineRun
	ts.call("POST", "/api/v0/repos/acme/app/runs", map[string]any{"wait": false}, &run)
	time.Sleep(300 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := ts.s.shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := ledger.Open(context.Background(), ts.cfg.dbTarget())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := db.PipelineRun(context.Background(), run.ID)
	if got.Status != "interrupted" {
		t.Fatalf("run must be marked interrupted, is %s", got.Status)
	}
	evs, _ := db.Events(context.Background(), ledger.EventsOptions{Type: "io.ore.pipeline.run.finished"})
	if len(evs) == 0 || !strings.Contains(string(evs[len(evs)-1].Data), `"status":"interrupted"`) {
		t.Fatalf("interruption must be on the ledger: %v", evs)
	}
	// a crash leaves a run "running": the next start marks it interrupted
	repo, _ := db.RepoByName(context.Background(), "acme", "app")
	stale := ledger.PipelineRun{CommitOID: run.CommitOID}
	db.InsertPipelineRun(context.Background(), repo, &stale, ledger.System)
	db.UpdatePipelineRun(context.Background(), repo, &stale, "running", "", ledger.System)
	db.Close()
	ts2 := startWith(t, ts.cfg)
	db2, _ := ledger.Open(context.Background(), ts2.cfg.dbTarget())
	defer db2.Close()
	got2, _ := db2.PipelineRun(context.Background(), stale.ID)
	if got2.Status != "interrupted" {
		t.Fatalf("stale run must be recovered on start, is %s", got2.Status)
	}
	if !strings.Contains(ts2.logs.String(), "recovered interrupted runs") {
		t.Fatalf("recovery must be logged:\n%s", ts2.logs.String())
	}
}

func byName(r report, n string) check {
	for _, c := range r.Checks {
		if c.Name == n {
			return c
		}
	}
	return check{}
}

func TestDoctorHealthyAndUnhealthy(t *testing.T) {
	ctx := context.Background()
	cfg := config{Data: filepath.Join(t.TempDir(), "data"), Addr: "127.0.0.1:0", Executor: "docker", LogFormat: "text", LogLevel: "info"}
	r := doctor(ctx, cfg, doctorEnv{})
	if r.Failed() {
		t.Fatalf("healthy setup must pass: %+v", r.Checks)
	}
	if byName(r, "database").Status != "ok" || byName(r, "data").Status != "ok" || byName(r, "port").Status != "ok" || byName(r, "git").Status != "ok" {
		t.Fatalf("%+v", r.Checks)
	}
	// docker absent: reported as the host fallback, not a failure
	noDocker := doctorEnv{lookPath: func(n string) (string, error) {
		if n == "docker" {
			return "", errors.New("not found")
		}
		return exec.LookPath(n)
	}}
	r = doctor(ctx, cfg, noDocker)
	if d := byName(r, "docker"); d.Status != "warn" || !strings.Contains(d.Detail, "--executor host") {
		t.Fatalf("docker absent: %+v", d)
	}
	if r.Failed() {
		t.Fatal("a missing docker must not fail doctor")
	}
	hostCfg := cfg
	hostCfg.Executor = "host"
	if d := byName(doctor(ctx, hostCfg, noDocker), "docker"); d.Status != "ok" {
		t.Fatalf("docker not needed with --executor host: %+v", d)
	}
	// a data directory that is not writable fails
	if os.Getuid() != 0 {
		ro := filepath.Join(t.TempDir(), "ro")
		os.MkdirAll(ro, 0o555)
		r = doctor(ctx, config{Data: ro, Addr: "127.0.0.1:0", Executor: "host", LogFormat: "text", LogLevel: "info"}, doctorEnv{})
		if !r.Failed() || byName(r, "data").Status != "fail" {
			t.Fatalf("read-only data dir must fail: %+v", r.Checks)
		}
	}
	// an unreachable database fails, with the password redacted
	r = doctor(ctx, config{Data: t.TempDir(), Addr: "127.0.0.1:0", Executor: "host", DB: "postgres://ore:secret@127.0.0.1:1/ore?sslmode=disable", LogFormat: "text", LogLevel: "info"}, doctorEnv{})
	if d := byName(r, "database"); d.Status != "fail" || strings.Contains(d.Detail, "secret") {
		t.Fatalf("bad database: %+v", d)
	}
	// a busy port is a warning
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	r = doctor(ctx, config{Data: t.TempDir(), Addr: ln.Addr().String(), Executor: "host", LogFormat: "text", LogLevel: "info"}, doctorEnv{})
	if byName(r, "port").Status != "warn" {
		t.Fatalf("busy port: %+v", byName(r, "port"))
	}
	// pending migrations are a warning
	fresh := filepath.Join(t.TempDir(), "d")
	os.MkdirAll(fresh, 0o755)
	ndb, _ := ledger.OpenWith(ctx, filepath.Join(fresh, "ore.db"), ledger.Options{NoMigrate: true})
	if ndb != nil {
		ndb.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`)
		ndb.Exec(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(1, ?)`, ledger.Now())
		ndb.Close()
		if ndb.Dialect() == ledger.DialectSQLite {
			r = doctor(ctx, config{Data: fresh, Addr: "127.0.0.1:0", Executor: "host", LogFormat: "text", LogLevel: "info"}, doctorEnv{})
			if d := byName(r, "database"); d.Status != "warn" || !strings.Contains(d.Detail, "ore migrate apply") {
				t.Fatalf("pending migrations: %+v", d)
			}
		}
	}
	// the command prints a report and fails on a failed check
	var out bytes.Buffer
	if err := runDoctor([]string{"--data", t.TempDir(), "--executor", "host", "--addr", "127.0.0.1:0"}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "result: ok") || !strings.Contains(out.String(), "database") {
		t.Fatalf("report:\n%s", out.String())
	}
	out.Reset()
	if err := runDoctor([]string{"--data", t.TempDir(), "--executor", "host", "--db", "postgres://ore:secret@127.0.0.1:1/ore?sslmode=disable"}, &out); !errors.Is(err, errDoctorFailed) || !strings.Contains(out.String(), "result: FAIL") {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

func TestConfigPrintsEffectiveConfigurationRedacted(t *testing.T) {
	t.Setenv("ORE_DB_URL", "postgres://ore:hunter2@db/ore")
	t.Setenv("ORE_TOKEN", "supersecret")
	var out bytes.Buffer
	if err := runConfig([]string{"--log-format", "json", "--backup-interval", "6h"}, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if strings.Contains(s, "hunter2") || strings.Contains(s, "supersecret") {
		t.Fatalf("secrets leaked:\n%s", s)
	}
	for _, want := range []string{`"db": "postgres://ore:REDACTED@db/ore"`, `"db_dialect": "postgres"`, `"log_format": "json"`, `"backup_interval": "6h0m0s"`, `"token": "REDACTED"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("config lacks %s:\n%s", want, s)
		}
	}
	if _, err := loadConfig(parseFlags([]string{"--log-format", "xml"})); err == nil {
		t.Fatal("bad log format must be refused")
	}
	if _, err := loadConfig(parseFlags([]string{"--log-level", "loud"})); err == nil {
		t.Fatal("bad log level must be refused")
	}
	if _, err := loadConfig(parseFlags([]string{"--drain-timeout", "soon"})); err == nil {
		t.Fatal("bad duration must be refused")
	}
	if _, err := loadConfig(parseFlags([]string{"--executor", "k8s"})); err == nil {
		t.Fatal("bad executor must be refused")
	}
	t.Setenv("ORE_DB_URL", "")
	cfg, _ := loadConfig(parseFlags([]string{"--data", "/x", "--drain-timeout", "45"}))
	if cfg.dbTarget() != "/x/ore.db" || cfg.dbDialect() != "sqlite" || cfg.DrainTimeout != 45*time.Second {
		t.Fatalf("%+v", cfg)
	}
}

func TestMigrateCommandStatusAndApply(t *testing.T) {
	data := t.TempDir()
	var out bytes.Buffer
	if err := runMigrate([]string{"status", "--data", data}, &out); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("ORE_TEST_DB_URL") == "" && !strings.Contains(out.String(), "pending") {
		t.Fatalf("fresh status:\n%s", out.String())
	}
	out.Reset()
	if err := runMigrate([]string{"apply", "--data", data}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "0 pending") {
		t.Fatalf("apply:\n%s", out.String())
	}
	out.Reset()
	if err := runMigrate([]string{"apply", "--data", data}, &out); err != nil || !strings.Contains(out.String(), "0 pending") {
		t.Fatalf("second apply must be idempotent: %v\n%s", err, out.String())
	}
	if err := runMigrate([]string{"bogus", "--data", data}, &out); err == nil {
		t.Fatal("unknown subcommand")
	}
}

// TestConcurrentMigrateProcesses runs several `ore migrate apply` processes on
// one database at once: one wins each migration, the others wait or find it done.
func TestConcurrentMigrateProcesses(t *testing.T) {
	bin := buildOre(t)
	data := t.TempDir()
	dbURL := os.Getenv("ORE_TEST_DB_URL")
	if dbURL != "" {
		// the processes must share one database (a dedicated schema), not one isolated schema each
		admin, err := ledger.OpenWith(context.Background(), dbURL, ledger.Options{NoMigrate: true})
		if err != nil {
			t.Fatal(err)
		}
		schema := "ore_test_procs_" + fmt.Sprint(time.Now().UnixNano())
		admin.Exec(context.Background(), `CREATE SCHEMA "`+schema+`"`)
		t.Cleanup(func() { admin.Exec(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`); admin.Close() })
		if strings.Contains(dbURL, "?") {
			dbURL += "&search_path=" + schema
		} else {
			dbURL += "?search_path=" + schema
		}
	}
	var wg sync.WaitGroup
	outs := make([]string, 4)
	errs := make([]error, 4)
	for i := range outs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := []string{"migrate", "apply", "--data", data}
			if dbURL != "" {
				args = append(args, "--db", dbURL)
			}
			cmd := exec.Command(bin, args...)
			cmd.Env = append(os.Environ(), "ORE_TEST_DB_URL=")
			out, err := cmd.CombinedOutput()
			outs[i], errs[i] = string(out), err
		}(i)
	}
	wg.Wait()
	for i := range outs {
		if errs[i] != nil || !strings.Contains(outs[i], "0 pending") {
			t.Fatalf("migrator %d: %v\n%s", i, errs[i], outs[i])
		}
	}
}

var (
	buildOnce sync.Once
	builtBin  string
	buildErr  error
)

// buildOre builds the binary once per test run.
func buildOre(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ore-bin-*")
		if err != nil {
			buildErr = err
			return
		}
		builtBin = filepath.Join(dir, "ore")
		cmd := exec.Command("go", "build", "-o", builtBin, ".")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("build: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return builtBin
}
