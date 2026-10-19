// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

func TestBackupAndRestoreRoundTrip(t *testing.T) {
	ts := startTestServer(t, nil)
	ts.seed("0")
	var run ledger.PipelineRun
	ts.call("POST", "/api/v0/repos/acme/app/runs", map[string]any{"wait": true}, &run)
	ts.call("POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": "keep me"}, nil)
	ts.call("POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "empty"}, nil) // an empty repository is backed up too
	os.WriteFile(filepath.Join(ts.cfg.Data, "secrets.key"), []byte("k3y"), 0o600)
	os.WriteFile(filepath.Join(ts.cfg.Data, "token"), []byte("tok\n"), 0o600) // as `ore serve` writes it when ORE_TOKEN is not given
	var before []ledger.Event
	ts.call("GET", "/api/v0/events?limit=1000", nil, &before)

	dest := filepath.Join(t.TempDir(), "backup")
	m, where, err := backup(context.Background(), ts.cfg, dest, newLogger(ts.cfg, io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	if where != dest || len(m.Repos) != 2 || m.Dialect == "" || len(m.Files) < 3 || m.Format != backupFormat {
		t.Fatalf("manifest %+v", m)
	}
	var emptyOK, appOK bool
	for _, r := range m.Repos {
		if r.Name == "empty" && r.Empty && r.Bundle == "" {
			emptyOK = true
		}
		if r.Name == "app" && r.Bundle == "repos/acme/app.bundle" && r.Head == "refs/heads/main" {
			appOK = true
		}
	}
	if !emptyOK || !appOK {
		t.Fatalf("repos %+v", m.Repos)
	}
	for _, f := range []string{"files/token", "files/secrets.key", "ledger.jsonl", "manifest.json", "repos/acme/app.bundle"} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Fatalf("%s must be in the backup", f)
		}
	}
	if m.Dialect == ledger.DialectSQLite {
		if _, err := os.Stat(filepath.Join(dest, "ore.db")); err != nil {
			t.Fatal("sqlite copy must be in the backup")
		}
	}
	// every listed checksum matches
	for _, f := range m.Files {
		b, _ := os.ReadFile(filepath.Join(dest, f.Path))
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != f.SHA256 || int64(len(b)) != f.Size {
			t.Fatalf("checksum of %s", f.Path)
		}
	}
	// backing up into the same directory again is refused
	if _, _, err := backup(context.Background(), ts.cfg, dest, newLogger(ts.cfg, io.Discard)); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("non-empty destination: %v", err)
	}

	// restore into a new data directory (and a fresh database) and serve from it
	rcfg := testConfig(t)
	rcfg.Data = filepath.Join(t.TempDir(), "restored")
	if _, err := restore(context.Background(), rcfg, dest, newLogger(rcfg, io.Discard)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(rcfg.Data, "secrets.key")); string(b) != "k3y" {
		t.Fatalf("secrets key %q", b)
	}
	if st, _ := os.Stat(filepath.Join(rcfg.Data, "token")); st == nil || st.Mode().Perm() != 0o600 {
		t.Fatal("token must be restored with mode 0600")
	}
	rs := startWith(t, rcfg)
	var repos []ledger.Repo
	if code, _ := rs.call("GET", "/api/v0/repos", nil, &repos); code != 200 || len(repos) != 2 {
		t.Fatalf("restored repos: %d %v", code, repos)
	}
	var file string
	if code, _ := rs.call("GET", "/api/v0/repos/acme/app/file?path=hello.txt", nil, &file); code != 200 || file != "hi\n" {
		t.Fatalf("restored file: %d %q", code, file)
	}
	var issues []ledger.Issue
	rs.call("GET", "/api/v0/repos/acme/app/issues", nil, &issues)
	if len(issues) != 1 || issues[0].Title != "keep me" {
		t.Fatalf("issues %+v", issues)
	}
	var after []ledger.Event
	rs.call("GET", "/api/v0/events?limit=1000", nil, &after)
	if len(after) != len(before) {
		t.Fatalf("ledger: %d events before, %d after restore", len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID || before[i].Seq != after[i].Seq {
			t.Fatalf("event %d differs", i)
		}
	}
	var refs map[string]string
	rs.call("GET", "/api/v0/repos/acme/empty/refs", nil, &refs)
	if len(refs) != 0 {
		t.Fatalf("empty repo must stay empty: %v", refs)
	}
	// the restored server keeps working: a new commit and run on the restored repository
	if code, _ := rs.call("POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "main", "message": "after", "files": []map[string]any{{"path": "x", "content": "y"}}}, nil); code != 201 {
		t.Fatalf("commit after restore: %d", code)
	}
}

func TestRestoreIntoNonEmptyDirectoryIsRefused(t *testing.T) {
	ts := startTestServer(t, nil)
	ts.seed("0")
	dest := filepath.Join(t.TempDir(), "backup")
	if _, _, err := backup(context.Background(), ts.cfg, dest, newLogger(ts.cfg, io.Discard)); err != nil {
		t.Fatal(err)
	}
	rcfg := testConfig(t)
	os.MkdirAll(rcfg.Data, 0o755)
	os.WriteFile(filepath.Join(rcfg.Data, "something"), []byte("x"), 0o644)
	_, err := restore(context.Background(), rcfg, dest, newLogger(rcfg, io.Discard))
	if err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("expected refusal, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(rcfg.Data, "ore.db")); err == nil {
		t.Fatal("nothing must have been written")
	}
	// a tampered backup is refused before anything is written
	rcfg2 := testConfig(t)
	os.WriteFile(filepath.Join(dest, "ledger.jsonl"), []byte("tampered\n"), 0o644)
	if _, err := restore(context.Background(), rcfg2, dest, newLogger(rcfg2, io.Discard)); err == nil || !strings.Contains(err.Error(), "checksum mismatch for ledger.jsonl") {
		t.Fatalf("tampered backup: %v", err)
	}
	if _, err := os.Stat(rcfg2.Data); err == nil {
		t.Fatal("tampered restore must not create the data directory")
	}
	// the command needs --from
	if err := runRestore([]string{"--data", t.TempDir()}, io.Discard); err == nil {
		t.Fatal("usage error expected")
	}
	if err := runBackup([]string{"--data", t.TempDir()}, io.Discard); err == nil {
		t.Fatal("usage error expected")
	}
}

func TestBackupWithUnreadableRepositoryFailsLoudly(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	ts := startTestServer(t, nil)
	ts.seed("0")
	ts.call("POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "locked"}, nil)
	locked := filepath.Join(ts.cfg.Data, "repos", "acme", "locked.git")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	dest := filepath.Join(t.TempDir(), "backup")
	_, _, err := backup(context.Background(), ts.cfg, dest, newLogger(ts.cfg, io.Discard))
	if err == nil || !strings.Contains(err.Error(), "repository acme/locked") || !strings.Contains(err.Error(), "not readable") {
		t.Fatalf("expected a loud error naming the repository, got %v", err)
	}
}

func TestBackupDuringAnActivePipelineIsConsistent(t *testing.T) {
	ts := startTestServer(t, nil)
	ts.seed("3")
	var run ledger.PipelineRun
	ts.call("POST", "/api/v0/repos/acme/app/runs", map[string]any{"wait": false}, &run)
	time.Sleep(300 * time.Millisecond)
	dest := filepath.Join(t.TempDir(), "backup")
	m, _, err := backup(context.Background(), ts.cfg, dest, newLogger(ts.cfg, io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	// the snapshot is internally consistent: the dump and the manifest agree, and it restores
	for _, f := range m.Files {
		b, _ := os.ReadFile(filepath.Join(dest, f.Path))
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Fatalf("checksum of %s changed under the backup", f.Path)
		}
	}
	dump, _ := os.ReadFile(filepath.Join(dest, "ledger.jsonl"))
	if !bytes.Contains(dump, []byte(run.ID)) {
		t.Fatal("the running pipeline must be in the snapshot")
	}
	rcfg := testConfig(t)
	rcfg.Data = filepath.Join(t.TempDir(), "restored")
	if _, err := restore(context.Background(), rcfg, dest, newLogger(rcfg, io.Discard)); err != nil {
		t.Fatal(err)
	}
	// the restored copy saw the run as running/queued; a server starting on it marks it interrupted
	rs := startWith(t, rcfg)
	var runs []ledger.PipelineRun
	rs.call("GET", "/api/v0/repos/acme/app/runs", nil, &runs)
	if len(runs) != 1 || runs[0].Status != "interrupted" {
		t.Fatalf("restored run: %+v", runs)
	}
	// meanwhile the original finished normally
	deadline := time.Now().Add(10 * time.Second)
	for {
		var got ledger.PipelineRun
		ts.call("GET", "/api/v0/repos/acme/app/runs/"+run.ID, nil, &got)
		if got.Status == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("original run did not finish: %+v", got)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestScheduledBackupWritesTimestampedDirsAndPrunes(t *testing.T) {
	root := t.TempDir()
	ts := startTestServer(t, func(c *config) { c.BackupTo = root; c.BackupKeep = 2 })
	ts.seed("0")
	for i := 0; i < 3; i++ {
		ts.s.backupOnce(context.Background())
		time.Sleep(1100 * time.Millisecond) // the directory name has second resolution
	}
	entries, _ := os.ReadDir(root)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 {
		t.Fatalf("retention must keep 2 of 3: %v", names)
	}
	for _, n := range names {
		if !backupDirRE.MatchString(n) {
			t.Fatalf("name %q", n)
		}
		if _, err := os.Stat(filepath.Join(root, n, "manifest.json")); err != nil {
			t.Fatalf("%s has no manifest", n)
		}
	}
	if !strings.Contains(ts.logs.String(), "scheduled backup written") || !strings.Contains(ts.logs.String(), "backup pruned") {
		t.Fatalf("logs:\n%s", ts.logs.String())
	}
	// pruneBackups ignores unrelated directories
	os.MkdirAll(filepath.Join(root, "notes"), 0o755)
	pruned, err := pruneBackups(root, 1)
	if err != nil || len(pruned) != 1 {
		t.Fatalf("%v %v", pruned, err)
	}
	if _, err := os.Stat(filepath.Join(root, "notes")); err != nil {
		t.Fatal("unrelated directories must survive")
	}
}

// fakeS3 accepts PUTs, stores bodies and checks the SigV4 Authorization header
// by recomputing the signature with the shared secret.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	auths   []string
	bad     int
	secret  string
	region  string
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != http.MethodPut {
		w.WriteHeader(405)
		return
	}
	auth := r.Header.Get("Authorization")
	f.auths = append(f.auths, auth)
	sum := sha256.Sum256(body)
	if r.Header.Get("X-Amz-Content-Sha256") != hex.EncodeToString(sum[:]) {
		f.bad++
		http.Error(w, "payload hash mismatch", 400)
		return
	}
	// recompute the signature from the canonical request the client must have built
	c := &s3Client{region: f.region, secretKey: f.secret, accessKey: "AKIDEXAMPLE", now: func() time.Time {
		t, _ := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
		return t
	}}
	req := r.Clone(r.Context())
	req.URL.Scheme, req.URL.Host = "http", r.Host
	req.Header = http.Header{}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	c.sign(req, body)
	if req.Header.Get("Authorization") != auth {
		f.bad++
		http.Error(w, "signature mismatch: "+auth+" vs "+req.Header.Get("Authorization"), 403)
		return
	}
	f.objects[r.URL.Path] = body
	w.WriteHeader(200)
}

func TestBackupToS3UploadsEveryFileWithSigV4(t *testing.T) {
	fs := &fakeS3{objects: map[string][]byte{}, secret: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", region: "eu-west-1"}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", fs.secret)
	t.Setenv("AWS_REGION", fs.region)
	t.Setenv("AWS_ENDPOINT_URL", srv.URL)
	t.Setenv("AWS_SESSION_TOKEN", "")
	ts := startTestServer(t, nil)
	ts.seed("0")
	m, where, err := backup(context.Background(), ts.cfg, "s3://ore-backups/prod/nightly", newLogger(ts.cfg, io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	if where != "s3://ore-backups/prod/nightly" {
		t.Fatalf("where %q", where)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.bad != 0 {
		t.Fatalf("%d requests failed signature or payload checks: %v", fs.bad, fs.auths)
	}
	for _, f := range m.Files {
		if _, ok := fs.objects["/ore-backups/prod/nightly/"+f.Path]; !ok {
			t.Fatalf("object %s not uploaded; have %v", f.Path, keys(fs.objects))
		}
	}
	mf, ok := fs.objects["/ore-backups/prod/nightly/manifest.json"]
	if !ok {
		t.Fatal("manifest not uploaded")
	}
	var got manifest
	if err := json.Unmarshal(mf, &got); err != nil || got.Format != backupFormat {
		t.Fatalf("manifest %v %s", err, mf)
	}
	if !strings.HasPrefix(fs.auths[0], "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/") || !strings.Contains(fs.auths[0], "/eu-west-1/s3/aws4_request, SignedHeaders=") || !strings.Contains(fs.auths[0], "x-amz-content-sha256;x-amz-date") {
		t.Fatalf("authorization header %q", fs.auths[0])
	}
	// no credentials is a clear error, not a crash
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	if _, _, err := backup(context.Background(), ts.cfg, "s3://b/p", newLogger(ts.cfg, io.Discard)); err == nil || !strings.Contains(err.Error(), "AWS_ACCESS_KEY_ID") {
		t.Fatalf("missing credentials: %v", err)
	}
	if tgt, ok := parseS3("s3://bucket"); !ok || tgt.bucket != "bucket" || tgt.prefix != "" {
		t.Fatalf("%+v %v", tgt, ok)
	}
	if _, ok := parseS3("/local/path"); ok {
		t.Fatal("local path is not s3")
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestInstallScript builds a release for this host, serves it from a file://
// URL and installs it; then checks the permission and checksum failure paths.
func TestInstallScript(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}
	dist := filepath.Join(t.TempDir(), "dist")
	rel := exec.Command("sh", "../../scripts/release.sh", "--only", "host", "--out", dist)
	rel.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := rel.CombinedOutput(); err != nil {
		t.Fatalf("release.sh: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dist, "SHA256SUMS")); err != nil {
		t.Fatal("SHA256SUMS missing")
	}
	home := t.TempDir()
	run := func(env ...string) (string, error) {
		cmd := exec.Command("sh", "../../deploy/install.sh")
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "ORE_INSTALL_URL=file://" + dist}, env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run()
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if !strings.Contains(out, "checksum verified") || !strings.Contains(out, "installed ore 0.1.0") {
		t.Fatalf("install output:\n%s", out)
	}
	bin := filepath.Join(home, ".local", "bin", "ore")
	if v, err := exec.Command(bin, "version").Output(); err != nil || strings.TrimSpace(string(v)) != "ore 0.1.0" {
		t.Fatalf("installed binary: %v %q", err, v)
	}
	// no write permission: a clear message, no sudo
	if os.Getuid() != 0 {
		ro := filepath.Join(t.TempDir(), "ro")
		os.MkdirAll(ro, 0o555)
		out, err = run("ORE_INSTALL_DIR=" + ro)
		if err == nil || !strings.Contains(out, "not writable") || !strings.Contains(out, "never runs sudo") {
			t.Fatalf("read-only install dir: %v\n%s", err, out)
		}
		if strings.Contains(out, "downloading") {
			t.Fatal("permission must be checked before downloading")
		}
	}
	// a tampered archive is refused
	sums, _ := os.ReadFile(filepath.Join(dist, "SHA256SUMS"))
	os.WriteFile(filepath.Join(dist, "SHA256SUMS"), []byte(strings.Repeat("0", 64)+string(sums[64:])), 0o644)
	out, err = run("ORE_INSTALL_DIR=" + t.TempDir())
	if err == nil || !strings.Contains(out, "checksum mismatch") {
		t.Fatalf("tampered checksum: %v\n%s", err, out)
	}
}
