// SPDX-License-Identifier: AGPL-3.0-only

package workspace

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, content string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func kinds(edits []Edit) map[string]string {
	m := map[string]string{}
	for _, e := range edits {
		m[e.Path] = e.Kind
	}
	return m
}

// The manifest diff sees additions, deletions, content changes, mode
// changes and symlinks, skips what .oreignore and the metadata directory
// cover, and reuses recorded hashes for files whose size and mtime match.
func TestScanAndDiff(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "a\n", 0o644)
	write(t, root, "bin/run.sh", "#!/bin/sh\n", 0o755)
	write(t, root, "src/keep.go", "package x\n", 0o644)
	write(t, root, "src/gone.go", "package x\n", 0o644)
	write(t, root, ".ore/workspace.json", "{}", 0o644)
	write(t, root, ".ore/pipeline.yaml", "version: 0\n", 0o644)
	os.Symlink("a.txt", filepath.Join(root, "link"))
	manifest, err := Scan(root, nil, ParseIgnore(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest) != 6 || manifest[".ore/pipeline.yaml"].Mode != "100644" || manifest["bin/run.sh"].Mode != "100755" || manifest["a.txt"].Mode != "100644" || manifest["link"].Mode != "120000" || manifest["link"].SHA256 != hashOf([]byte("a.txt")) {
		t.Fatalf("manifest: %+v", manifest)
	}
	if _, ok := manifest[".ore/workspace.json"]; ok {
		t.Fatal("metadata must never be in the manifest")
	}
	// edits
	write(t, root, "a.txt", "a2\n", 0o644)                // changed
	write(t, root, "src/new.go", "package y\n", 0o644)    // added
	os.Remove(filepath.Join(root, "src/gone.go"))         // deleted
	os.Chmod(filepath.Join(root, "src/keep.go"), 0o755)   // mode change only
	write(t, root, "build/out.o", "x", 0o644)             // ignored directory
	write(t, root, "src/debug.log", "x", 0o644)           // ignored pattern
	write(t, root, "docs/notes.log", "x", 0o644)          // re-included
	os.Remove(filepath.Join(root, "link"))                // symlink retargeted
	os.Symlink("bin/run.sh", filepath.Join(root, "link")) // ...
	ig := ParseIgnore("# build output\nbuild/\n*.log\n!docs/*.log\n")
	cur, err := Scan(root, manifest, ig)
	if err != nil {
		t.Fatal(err)
	}
	got := kinds(Diff(manifest, cur))
	want := map[string]string{"a.txt": "changed", "src/new.go": "added", "src/gone.go": "deleted", "src/keep.go": "changed", "docs/notes.log": "added", "link": "changed"}
	if len(got) != len(want) {
		t.Fatalf("edits: %v, want %v", got, want)
	}
	for p, k := range want {
		if got[p] != k {
			t.Fatalf("edit %s = %q, want %q (all: %v)", p, got[p], k, got)
		}
	}
	if cur["bin/run.sh"].SHA256 != manifest["bin/run.sh"].SHA256 || cur["src/keep.go"].Mode != "100755" {
		t.Fatalf("unchanged or mode-only entries: %+v", cur)
	}
	// the mtime fast path: same size and mtime keep the recorded hash without reading
	fake := map[string]Entry{"a.txt": cur["a.txt"]}
	fake["a.txt"] = Entry{SHA256: "cached", Mode: "100644", Size: cur["a.txt"].Size, MTime: cur["a.txt"].MTime}
	again, _ := Scan(root, fake, ig)
	if again["a.txt"].SHA256 != "cached" {
		t.Fatalf("fast path not taken: %+v", again["a.txt"])
	}
	// content loading and the API shape, binary as base64
	write(t, root, "img.bin", "\x00\xff\xfe", 0o644)
	cur, _ = Scan(root, manifest, ig)
	edits := Diff(manifest, cur)
	if err := LoadContent(root, edits); err != nil {
		t.Fatal(err)
	}
	files := FileEdits(edits)
	byPath := map[string]FileEdit{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	if byPath["img.bin"].Encoding != "base64" || byPath["img.bin"].Content != "AP/+" || byPath["a.txt"].Content != "a2\n" || byPath["a.txt"].Encoding != "" ||
		!byPath["src/gone.go"].Delete || byPath["link"].Content != "bin/run.sh" || byPath["link"].Mode != "120000" || byPath["src/keep.go"].Mode != "100755" {
		t.Fatalf("file edits: %+v", byPath)
	}
}

func TestIgnorePatterns(t *testing.T) {
	ig := ParseIgnore("*.tmp\n/top.txt\nbuild/\nsrc/**/gen.go\n!keep.tmp\ndoc/*.md\n")
	cases := map[string]bool{
		"x.tmp": true, "a/b/x.tmp": true, "keep.tmp": false, "a/keep.tmp": false,
		"top.txt": true, "a/top.txt": false,
		"build": true, "build/x.o": true, "a/build/x.o": true,
		"src/gen.go": true, "src/a/b/gen.go": true, "src/a/gen.goo": false,
		"doc/a.md": true, "doc/sub/a.md": false,
		".ore/workspace.json": true, ".ore/pending.json": true, ".ore/pipeline.yaml": false, ".oreignore": false, "src/main.go": false,
	}
	for p, want := range cases {
		isDir := p == "build"
		if got := ig.Match(p, isDir); got != want {
			t.Errorf("Match(%q) = %v, want %v", p, got, want)
		}
	}
}

// Queued saves are sent in order; the first failure stops the flush and
// keeps the rest; a flushed queue opens the change.
func TestQueueFlushOrderAndPartialFailure(t *testing.T) {
	var sent []string
	fail := true
	var commits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/commits"):
			var req CommitRequest
			json.NewDecoder(r.Body).Decode(&req)
			if fail && len(sent) == 1 {
				w.WriteHeader(503)
				return
			}
			sent = append(sent, req.Message)
			commits++
			if len(sent) == 1 && req.Base == "" {
				t.Errorf("the first queued save must carry the base")
			}
			if len(sent) > 1 && req.Base != "" {
				t.Errorf("later queued saves must not carry a base: %+v", req)
			}
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]string{"commit": strings.Repeat("a", 39) + string(rune('0'+commits)), "ref": "refs/heads/" + req.Branch, "parent": "x"})
		case strings.HasSuffix(r.URL.Path, "/changes") && r.Method == "POST":
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"number": 7, "state": "open"})
		case strings.HasSuffix(r.URL.Path, "/changes/7"):
			json.NewEncoder(w).Encode(map[string]any{"number": 7, "state": "open"})
		default:
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		}
	}))
	defer srv.Close()
	root := t.TempDir()
	st := &State{Version: 1, Server: srv.URL, Org: "o", Repo: "r", DefaultBranch: "main", Base: "base0", User: "u", Manifest: map[string]Entry{}, root: root}
	st.Write()
	q := &Queue{Saves: []PendingSave{
		{Message: "one", Branch: "ore/u/one-1", Base: "base0", Files: []FileEdit{{Path: "a", Content: "1"}}},
		{Message: "two", Branch: "ore/u/one-1", Files: []FileEdit{{Path: "a", Content: "2"}}},
		{Message: "three", Branch: "ore/u/one-1", Files: []FileEdit{{Path: "b", Content: "3"}}},
	}}
	if err := q.Save(root); err != nil {
		t.Fatal(err)
	}
	c := NewClient(srv.URL, "t")
	var out strings.Builder
	n, err := st.Flush(t.Context(), c, &out)
	if n != 1 || !Unreachable(err) {
		t.Fatalf("partial flush: n=%d err=%v", n, err)
	}
	if st.Change == nil || st.Change.Number != 7 || !strings.HasSuffix(st.Base, "1") {
		t.Fatalf("state after the first send: %+v base %s", st.Change, st.Base)
	}
	left, _ := LoadQueue(root)
	if len(left.Saves) != 2 || left.Saves[0].Message != "two" {
		t.Fatalf("remaining queue: %+v", left.Saves)
	}
	fail = false
	n, err = st.Flush(t.Context(), c, &out)
	if n != 2 || err != nil {
		t.Fatalf("second flush: n=%d err=%v", n, err)
	}
	if strings.Join(sent, ",") != "one,two,three" {
		t.Fatalf("order: %v", sent)
	}
	if _, err := os.Stat(filepath.Join(root, PendingFile)); !os.IsNotExist(err) {
		t.Fatal("pending file should be removed")
	}
	if n, err := st.Flush(t.Context(), c, &out); n != 0 || err != nil {
		t.Fatalf("empty flush: %d %v", n, err)
	}
}

// A save against an unreachable server queues and moves the manifest, so
// the next save diffs against the queued content.
func TestSaveOfflineQueuesAndAdvancesManifest(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "one\n", 0o644)
	st := &State{Version: 1, Server: "http://127.0.0.1:1", Org: "o", Repo: "r", DefaultBranch: "main", Base: "base0", User: "u", Manifest: map[string]Entry{}, root: root}
	cur, _ := Scan(root, nil, ParseIgnore(""))
	st.Manifest = cur
	st.Write()
	c := NewClient(st.Server, "t")
	var out strings.Builder
	write(t, root, "a.txt", "two\n", 0o644)
	res, err := st.Save(t.Context(), c, "First", &out)
	if err != nil || !res.Queued || st.Change == nil || st.Change.Number != 0 || !strings.HasPrefix(st.Change.Branch, "ore/u/first-") {
		t.Fatalf("offline save: %v %+v %+v", err, res, st.Change)
	}
	if st.Manifest["a.txt"].SHA256 != hashOf([]byte("two\n")) {
		t.Fatal("manifest should reflect the queued save")
	}
	if _, err := st.Save(t.Context(), c, "Nothing", &out); err != nil || !strings.Contains(out.String(), "nothing to save") {
		t.Fatalf("no-op save: %v %s", err, out.String())
	}
	q, _ := LoadQueue(root)
	if len(q.Saves) != 1 || q.Saves[0].Base != "base0" || q.Saves[0].Files[0].Content != "two\n" {
		t.Fatalf("queue: %+v", q.Saves)
	}
	if _, err := st.Save(t.Context(), c, "", &out); err == nil {
		t.Fatal("a message is required")
	}
}

func TestMerge3(t *testing.T) {
	base := []byte("a\nb\nc\nd\ne\n")
	cases := []struct {
		name, ours, theirs, want string
		conflicts                int
	}{
		{"identical", "a\nb\nc\nd\ne\n", "a\nb\nc\nd\ne\n", "a\nb\nc\nd\ne\n", 0},
		{"ours only", "A\nb\nc\nd\ne\n", "a\nb\nc\nd\ne\n", "A\nb\nc\nd\ne\n", 0},
		{"theirs only", "a\nb\nc\nd\ne\n", "a\nb\nc\nd\nE\n", "a\nb\nc\nd\nE\n", 0},
		{"disjoint", "A\nb\nc\nd\ne\n", "a\nb\nc\nd\nE\n", "A\nb\nc\nd\nE\n", 0},
		{"same change", "a\nB\nc\nd\ne\n", "a\nB\nc\nd\ne\n", "a\nB\nc\nd\ne\n", 0},
		{"insert vs insert elsewhere", "a\nx\nb\nc\nd\ne\n", "a\nb\nc\nd\ny\ne\n", "a\nx\nb\nc\nd\ny\ne\n", 0},
		{"delete vs edit elsewhere", "b\nc\nd\ne\n", "a\nb\nc\nd\nE\n", "b\nc\nd\nE\n", 0},
		{"overlap", "a\nB1\nc\nd\ne\n", "a\nB2\nc\nd\ne\n", "a\n<<<<<<< ours\nB1\n=======\nB2\n>>>>>>> theirs\nc\nd\ne\n", 1},
		{"append both", "a\nb\nc\nd\ne\nf\n", "a\nb\nc\nd\ne\ng\n", "a\nb\nc\nd\ne\n<<<<<<< ours\nf\n=======\ng\n>>>>>>> theirs\n", 1},
		{"no final newline, ours only", "a\nb\nc\nd\ne", "a\nb\nc\nd\ne\n", "a\nb\nc\nd\ne", 0},
		{"no final newline, conflict", "a\nb\nc\nd\ne", "a\nb\nc\nd\nE\n", "a\nb\nc\nd\n<<<<<<< ours\ne\n=======\nE\n>>>>>>> theirs\n", 1},
	}
	for _, tc := range cases {
		got, n := Merge3(base, []byte(tc.ours), []byte(tc.theirs), "ours", "theirs")
		if string(got) != tc.want || n != tc.conflicts {
			t.Errorf("%s: got %q (%d conflicts), want %q (%d)", tc.name, got, n, tc.want, tc.conflicts)
		}
	}
	if _, n := Merge3([]byte("a\x00b"), []byte("a"), []byte("b"), "o", "t"); n != -1 {
		t.Fatal("binary content must not be merged")
	}
	// empty base: both sides added the file differently
	got, n := Merge3(nil, []byte("mine\n"), []byte("theirs\n"), "ours", "theirs")
	if n != 1 || !strings.Contains(string(got), "mine\n") {
		t.Fatalf("empty base: %q %d", got, n)
	}
}

func TestSlugAndBranchName(t *testing.T) {
	if Slug("Fix the Retry in Uploader!") != "fix-the-retry-in-uploader" || Slug("   ") != "change" {
		t.Fatal(Slug("Fix the Retry in Uploader!"))
	}
	b := BranchName("Alice Smith", "Fix it")
	if !strings.HasPrefix(b, "ore/alice-smith/fix-it-") || len(b) != len("ore/alice-smith/fix-it-")+6 {
		t.Fatal(b)
	}
}

func TestCredentialsRoundTrip(t *testing.T) {
	t.Setenv("ORE_CONFIG_DIR", t.TempDir())
	t.Setenv("ORE_TOKEN", "")
	c, _ := LoadCredentials()
	c.Set("localhost:7777/", "secret", "alice")
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(CredentialsPath()); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	tok, user := TokenFor("http://localhost:7777")
	if tok != "secret" || user != "alice" {
		t.Fatalf("%q %q", tok, user)
	}
	t.Setenv("ORE_TOKEN", "env")
	if tok, _ := TokenFor("http://localhost:7777"); tok != "env" {
		t.Fatal("ORE_TOKEN must win")
	}
}
