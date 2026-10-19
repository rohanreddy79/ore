// SPDX-License-Identifier: AGPL-3.0-only

package pipelines

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/ledger"
)

func testRepo(t *testing.T) (*ledger.DB, ledger.Repo, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := ledger.Open(ctx, filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	org, err := db.CreateOrg(ctx, "acme", ledger.System)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := db.CreateRepo(ctx, org, "app", "main", 2, 8080, ledger.System)
	if err != nil {
		t.Fatal(err)
	}
	return db, repo, dir
}

func saveCache(t *testing.T, c *CacheStore, repoID, scope, key, content string) {
	t.Helper()
	w, err := c.Begin(repoID, scope, key, []string{"~/.cache/x"}, "run", "task")
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.Blob("~/.cache/x")
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(b, content)
	b.Close()
	if _, err := w.Commit(); err != nil {
		t.Fatal(err)
	}
}

func readBlob(t *testing.T, e *CacheEntry) string {
	t.Helper()
	r, err := e.OpenBlob(0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

func TestCacheTrustScopes(t *testing.T) {
	c := &CacheStore{Dir: t.TempDir()}
	repo := "repo_1"
	// a fork writes the key the default branch uses: the default branch must not see it
	saveCache(t, c, repo, TrustFork, "go-mod-abc", "poisoned")
	if e, _, _ := c.Lookup(repo, TrustDefault, "go-mod-abc", nil); e != nil {
		t.Fatalf("default branch restored a fork entry: %+v", e)
	}
	if e, _, _ := c.Lookup(repo, TrustInternal, "go-mod-abc", nil); e != nil {
		t.Fatalf("internal branch restored a fork entry: %+v", e)
	}
	if e, exact, _ := c.Lookup(repo, TrustFork, "go-mod-abc", nil); e == nil || !exact || readBlob(t, e) != "poisoned" {
		t.Fatal("fork cannot read its own entry")
	}
	// the default branch writes; everyone below can read it, and the own scope wins
	saveCache(t, c, repo, TrustDefault, "go-mod-abc", "trusted")
	for _, s := range []string{TrustDefault, TrustProtected, TrustInternal} {
		e, exact, err := c.Lookup(repo, s, "go-mod-abc", nil)
		if err != nil || e == nil || !exact || readBlob(t, e) != "trusted" || e.Scope != TrustDefault {
			t.Fatalf("%s: %+v %v %v", s, e, exact, err)
		}
	}
	if e, _, _ := c.Lookup(repo, TrustFork, "go-mod-abc", nil); e == nil || readBlob(t, e) != "poisoned" {
		t.Fatal("fork must prefer its own scope")
	}
	// restore-keys: prefix match, newest first, scope rules apply
	saveCache(t, c, repo, TrustInternal, "go-mod-old", "old")
	saveCache(t, c, repo, TrustInternal, "go-mod-new", "new")
	e, exact, _ := c.Lookup(repo, TrustInternal, "go-mod-missing", []string{"go-mod-"})
	if e == nil || exact || readBlob(t, e) != "new" {
		t.Fatalf("restore-keys: %+v %v", e, exact)
	}
	if e, _, _ := c.Lookup(repo, TrustDefault, "nope", []string{"go-mod-n"}); e != nil {
		t.Fatal("default must not restore an internal entry by prefix")
	}
	if !c.Exists(repo, TrustInternal, "go-mod-new") || c.Exists(repo, TrustDefault, "go-mod-new") {
		t.Fatal("Exists is per scope")
	}
	if _, err := c.Begin(repo, "nonsense", "k", nil, "", ""); err == nil {
		t.Fatal("unknown scope accepted")
	}
	if _, err := c.Begin("../escape", TrustDefault, "k", nil, "", ""); err == nil {
		t.Fatal("bad repo id accepted")
	}
	if _, rel, err := CachePathBase("~/.npm"); err != nil || rel != ".npm" {
		t.Fatal(rel, err)
	}
	if base, rel, err := CachePathBase("/usr/local/lib"); err != nil || base != "/" || rel != "usr/local/lib" {
		t.Fatal(base, rel, err)
	}
	if _, _, err := CachePathBase("../x"); err == nil {
		t.Fatal("escape accepted")
	}
	if _, _, err := CachePathBase("**/node_modules"); err == nil {
		t.Fatal("glob accepted")
	}
}

func TestSecretsRoundTripAndMasking(t *testing.T) {
	db, repo, dir := testRepo(t)
	ctx := context.Background()
	s := &SecretStore{DB: db, KeyPath: filepath.Join(dir, "secrets.key")}
	if _, err := s.Set(ctx, repo, "", "bad-name", "x", ledger.System); err == nil {
		t.Fatal("bad name accepted")
	}
	if _, err := s.Set(ctx, repo, "", "GITHUB_X", "x", ledger.System); err == nil {
		t.Fatal("reserved prefix accepted")
	}
	info, err := s.Set(ctx, repo, "", "TOKEN", "repo-wide", ledger.System)
	if err != nil || info.Name != "TOKEN" {
		t.Fatal(info, err)
	}
	if _, err := s.Set(ctx, repo, "production", "TOKEN", "prod-only", ledger.System); err != nil {
		t.Fatal(err)
	}
	v, ok, err := s.Get(ctx, repo, "", "TOKEN")
	if err != nil || !ok || v != "repo-wide" {
		t.Fatal(v, ok, err)
	}
	v, ok, _ = s.Get(ctx, repo, "production", "TOKEN")
	if !ok || v != "prod-only" {
		t.Fatal(v)
	}
	v, ok, _ = s.Get(ctx, repo, "staging", "TOKEN")
	if !ok || v != "repo-wide" {
		t.Fatal("environment without its own value must fall back to the repository secret")
	}
	if _, ok, _ = s.Get(ctx, repo, "", "MISSING"); ok {
		t.Fatal("missing secret found")
	}
	// at rest: the value is not in the database or the key file
	var ct []byte
	db.QueryRow(ctx, `SELECT ciphertext FROM secrets WHERE name = 'TOKEN' AND environment = ''`).Scan(&ct)
	if strings.Contains(string(ct), "repo-wide") || len(ct) < 12+len("repo-wide")+16 {
		t.Fatal("ciphertext looks wrong")
	}
	key, _ := os.ReadFile(filepath.Join(dir, "secrets.key"))
	if len(key) != 32 {
		t.Fatal("key file")
	}
	// a second store with the same key file decrypts; a different key does not
	s2 := &SecretStore{DB: db, KeyPath: filepath.Join(dir, "secrets.key")}
	if v, _, _ := s2.Get(ctx, repo, "", "TOKEN"); v != "repo-wide" {
		t.Fatal("same key must decrypt")
	}
	os.WriteFile(filepath.Join(dir, "other.key"), make([]byte, 32), 0o600)
	s3 := &SecretStore{DB: db, KeyPath: filepath.Join(dir, "other.key")}
	if _, _, err := s3.Get(ctx, repo, "", "TOKEN"); err == nil {
		t.Fatal("other key must fail to decrypt")
	}
	// update and list and events never carry the value
	if _, err := s.Set(ctx, repo, "", "TOKEN", "rotated", ledger.System); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List(ctx, repo)
	if len(list) != 2 {
		t.Fatalf("list: %+v", list)
	}
	evs, _ := db.Events(ctx, ledger.EventsOptions{Type: "io.ore.secret."})
	if len(evs) != 3 {
		t.Fatalf("events: %d", len(evs))
	}
	for _, e := range evs {
		if strings.Contains(string(e.Data), "repo-wide") || strings.Contains(string(e.Data), "rotated") || strings.Contains(string(e.Data), "prod-only") {
			t.Fatalf("event leaks a value: %s", e.Data)
		}
	}
	if err := s.Delete(ctx, repo, "production", "TOKEN", ledger.System); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, repo, "production", "TOKEN", ledger.System); err == nil {
		t.Fatal("double delete")
	}
	m := &Masker{}
	m.Add("rotated")
	m.Add("multi\nline\n")
	m.Add("")
	if got := m.Mask("token=rotated and line and multi"); got != "token=*** and *** and ***" {
		t.Fatal(got)
	}
	if !m.Contains("xx rotated yy") || m.Contains("nothing") {
		t.Fatal("Contains")
	}
}

func TestArtifactsStore(t *testing.T) {
	db, repo, dir := testRepo(t)
	ctx := context.Background()
	a := &ArtifactStore{Dir: filepath.Join(dir, "artifacts"), DB: db}
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, "dist", "sub"), 0o755)
	os.WriteFile(filepath.Join(ws, "dist", "a.txt"), []byte("A"), 0o644)
	os.WriteFile(filepath.Join(ws, "dist", "sub", "b.txt"), []byte("B"), 0o644)
	os.WriteFile(filepath.Join(ws, "top.txt"), []byte("T"), 0o644)
	for _, bad := range []string{"../etc", "a/b", `a\b`, ".hidden", "", "x..y", "a:b", strings.Repeat("n", 129)} {
		if err := ValidArtifactName(bad); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
	if _, err := a.Put(ctx, repo, "prun_1", "build", "../escape", ws, []string{"top.txt"}, false, ledger.System); err == nil {
		t.Fatal("traversal name accepted")
	}
	if _, err := a.Put(ctx, repo, "prun_1", "build", "ok", ws, []string{"../outside"}, false, ledger.System); err == nil {
		t.Fatal("traversal path accepted")
	}
	files, _ := GlobFiles(ws, []string{"dist"})
	art, err := a.Put(ctx, repo, "prun_1", "build", "dist", ws, files, false, ledger.System)
	if err != nil || art.Files != 2 || art.Size == 0 || !strings.HasPrefix(art.Digest, "sha256:") {
		t.Fatalf("%+v %v", art, err)
	}
	if _, err := a.Put(ctx, repo, "prun_1", "build", "dist", ws, files, false, ledger.System); err == nil {
		t.Fatal("duplicate accepted without overwrite")
	}
	if _, err := a.Put(ctx, repo, "prun_1", "build", "dist", ws, files, true, ledger.System); err != nil {
		t.Fatal(err)
	}
	got, err := a.Get(ctx, "prun_1", "dist")
	if err != nil || got.Name != "dist" {
		t.Fatal(got, err)
	}
	dest := t.TempDir()
	n, err := a.Extract(got, dest)
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	// the directory upload round-trips to its contents (least common ancestor)
	if b, _ := os.ReadFile(filepath.Join(dest, "sub", "b.txt")); string(b) != "B" {
		t.Fatal("layout: expected sub/b.txt at the root of the artifact")
	}
	list, _ := a.List(ctx, "prun_1")
	if len(list) != 1 {
		t.Fatal(list)
	}
	if _, err := a.Get(ctx, "prun_1", "missing"); err == nil {
		t.Fatal("missing artifact found")
	}
	evs, _ := db.Events(ctx, ledger.EventsOptions{Type: "io.ore.artifact.published"})
	if len(evs) != 2 {
		t.Fatal(len(evs))
	}
	if commonDir([]string{"dist/a", "dist/sub/b"}) != "dist" || commonDir([]string{"a", "b/c"}) != "" || commonDir([]string{"x/y/z"}) != "x/y" {
		t.Fatal("commonDir")
	}
}

func TestContentHash(t *testing.T) {
	a := IRTask{ID: "t", Inputs: Inputs{Source: &SourceInput{Commit: "abc"}, Params: map[string]any{"b": 1, "a": 2}, Secrets: []SecretRef{{Path: "secrets/TOKEN", Name: "TOKEN"}}}, Steps: []Step{{Run: "go test"}}, Executor: Executor{Kind: "docker"}}
	b := IRTask{ID: "other-id", Name: "other name", Inputs: Inputs{Source: &SourceInput{Commit: "abc"}, Params: map[string]any{"a": 2, "b": 1}, Secrets: []SecretRef{{Path: "secrets/TOKEN", Name: "TOKEN"}}}, Steps: []Step{{Run: "go test"}}, Executor: Executor{Kind: "docker"}}
	if ContentHash(&a) != ContentHash(&b) {
		t.Fatal("hash must not depend on id, name or map order")
	}
	b.Inputs.Source.Commit = "def"
	if ContentHash(&a) == ContentHash(&b) {
		t.Fatal("hash must depend on the commit")
	}
	if !strings.HasPrefix(ContentHash(&a), "sha256:") || len(ContentHash(&a)) != 7+64 {
		t.Fatal(ContentHash(&a))
	}
}

func TestParseCommandsAndKeyValues(t *testing.T) {
	cmd, props, msg, ok := parseCommand("::error file=main.go,line=3::boom%0Anext")
	if !ok || cmd != "error" || props["file"] != "main.go" || props["line"] != "3" || msg != "boom\nnext" {
		t.Fatal(cmd, props, msg, ok)
	}
	if _, _, _, ok := parseCommand("not a command"); ok {
		t.Fatal("parsed")
	}
	kv := parseKeyValues("a=1\nb<<EOF\nline1\nline2\nEOF\nc=x=y\n")
	if kv["a"] != "1" || kv["b"] != "line1\nline2" || kv["c"] != "x=y" {
		t.Fatal(kv)
	}
}
