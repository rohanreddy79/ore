// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"archive/tar"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/gitx"
)

// post sends a JSON body and returns the status and the body whatever the status.
func post(t *testing.T, srv string, tok, path string, in any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(in)
	req, _ := http.NewRequest("POST", srv+path, strings.NewReader(string(b)))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// readArchive fetches an archive and returns its entries (path → content)
// with the headers and the trailer.
func readArchive(t *testing.T, srv string, tok, q string) (map[string]string, http.Header, string, int) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv+"/api/v0/repos/acme/ws/archive"+q, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, resp.Body)
		return nil, resp.Header, "", resp.StatusCode
	}
	files := map[string]string{}
	tr := tar.NewReader(resp.Body)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		b, _ := io.ReadAll(tr)
		files[h.Name] = string(b)
	}
	io.Copy(io.Discard, resp.Body)
	return files, resp.Header, resp.Trailer.Get("X-Ore-Archive-Error"), 200
}

// The archive is sparse by default (the pipeline's paths), whole with all=1,
// explicit with path=; it names the commit and the paths it carries, and a
// bad ref or path is an error status, not a broken stream.
func TestArchiveSparseExplicitAndWhole(t *testing.T) {
	srv, tok := newTestServer(t)
	call(t, srv, tok, "POST", "/api/v0/orgs", map[string]string{"name": "acme"}, nil)
	call(t, srv, tok, "POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "ws"}, nil)
	var commit struct{ Commit string }
	code := call(t, srv, tok, "POST", "/api/v0/repos/acme/ws/commits", map[string]any{"branch": "main", "message": "init", "files": []map[string]any{
		{"path": "src/a.txt", "content": "a\n"},
		{"path": "src/b.txt", "content": "b\n", "mode": "100755"},
		{"path": "vendor/big.txt", "content": "v\n"},
		{"path": ".ore/pipeline.yaml", "content": "version: 0\npaths: [src]\ntasks:\n  - id: t\n    run: true\n"},
	}}, &commit)
	if code != 201 {
		t.Fatalf("commit: %d", code)
	}
	files, h, trailer, _ := readArchive(t, srv.URL, tok, "")
	if len(files) != 3 || files["src/a.txt"] != "a\n" || files[".ore/pipeline.yaml"] == "" || files["vendor/big.txt"] != "" {
		t.Fatalf("sparse archive: %v", files)
	}
	if h.Get("X-Ore-Commit") != commit.Commit || h.Get("X-Ore-Archive-Paths") != `[".ore/pipeline.yaml","src"]` || trailer != "" {
		t.Fatalf("headers: %v trailer %q", h, trailer)
	}
	files, h, _, _ = readArchive(t, srv.URL, tok, "?all=1")
	if len(files) != 4 || h.Get("X-Ore-Archive-Paths") != "[]" {
		t.Fatalf("whole archive: %v %v", files, h)
	}
	files, _, _, _ = readArchive(t, srv.URL, tok, "?path=vendor&path=src/b.txt")
	if len(files) != 2 || files["vendor/big.txt"] != "v\n" || files["src/b.txt"] != "b\n" {
		t.Fatalf("explicit archive: %v", files)
	}
	if _, _, _, code := readArchive(t, srv.URL, tok, "?ref=refs/heads/nope"); code != 404 {
		t.Fatalf("bad ref: %d", code)
	}
	if _, _, _, code := readArchive(t, srv.URL, tok, "?path=../etc"); code != 400 {
		t.Fatalf("bad path: %d", code)
	}
	if _, _, _, code := readArchive(t, srv.URL, tok, "?path=missing"); code != 404 {
		t.Fatalf("missing path: %d", code)
	}
}

// The diff lists added, modified (including mode-only) and deleted paths
// with modes and blob ids, filtered by path, with the merge base; commits
// honour base (new branch start, compare-and-swap) and merge (three-way
// with resolutions, 409 with the conflicting paths otherwise).
func TestDiffAndCommitBaseAndMerge(t *testing.T) {
	srv, tok := newTestServer(t)
	call(t, srv, tok, "POST", "/api/v0/orgs", map[string]string{"name": "acme"}, nil)
	call(t, srv, tok, "POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "ws"}, nil)
	var c1, c2, c3 struct{ Commit, Parent string }
	call(t, srv, tok, "POST", "/api/v0/repos/acme/ws/commits", map[string]any{"branch": "main", "message": "one", "files": []map[string]any{
		{"path": "a.txt", "content": "a\n"}, {"path": "b.txt", "content": "b\n"}, {"path": "c.txt", "content": "c\n"}, {"path": "d/e.txt", "content": "e\n"},
	}}, &c1)
	// a branch started at c1 through base, while main moves on
	if code := call(t, srv, tok, "POST", "/api/v0/repos/acme/ws/commits", map[string]any{"branch": "ore/u/x", "base": c1.Commit, "message": "mine", "files": []map[string]any{
		{"path": "a.txt", "content": "a mine\n"}, {"path": "bin", "content": "\x00\x01", "encoding": "base64"},
	}}, &c2); code != 400 { // "\x00\x01" is not valid base64
		t.Fatalf("bad base64: %d", code)
	}
	call(t, srv, tok, "POST", "/api/v0/repos/acme/ws/commits", map[string]any{"branch": "ore/u/x", "base": c1.Commit, "message": "mine", "files": []map[string]any{
		{"path": "a.txt", "content": "a mine\n"}, {"path": "bin", "content": "AAE=", "encoding": "base64"},
	}}, &c2)
	if c2.Parent != c1.Commit {
		t.Fatalf("branch should start at base: %+v", c2)
	}
	call(t, srv, tok, "POST", "/api/v0/repos/acme/ws/commits", map[string]any{"branch": "main", "message": "upstream", "files": []map[string]any{
		{"path": "b.txt", "content": "b2\n"}, {"path": "c.txt", "delete": true}, {"path": "d/e.txt", "content": "e\n", "mode": "100755"}, {"path": "new.txt", "content": "n\n"},
	}}, &c3)
	var diff struct {
		From      string           `json:"from"`
		To        string           `json:"to"`
		MergeBase string           `json:"merge_base"`
		Files     []gitx.DiffEntry `json:"files"`
	}
	var raw string
	call(t, srv, tok, "GET", "/api/v0/repos/acme/ws/diff?from="+c1.Commit+"&to=refs/heads/main", nil, &raw)
	json.Unmarshal([]byte(raw), &diff)
	if !strings.Contains(raw, `"merge_base"`) || diff.MergeBase != c1.Commit || len(diff.Files) != 4 {
		t.Fatalf("diff: %s", raw)
	}
	byPath := map[string]gitx.DiffEntry{}
	for _, e := range diff.Files {
		byPath[e.Path] = e
	}
	if byPath["b.txt"].Status != "M" || byPath["c.txt"].Status != "D" || byPath["c.txt"].Mode != "" || byPath["new.txt"].Status != "A" || byPath["new.txt"].OldMode != "" ||
		byPath["d/e.txt"].Status != "M" || byPath["d/e.txt"].OldMode != "100644" || byPath["d/e.txt"].Mode != "100755" || len(byPath["b.txt"].OID) != 40 {
		t.Fatalf("entries: %+v", byPath)
	}
	call(t, srv, tok, "GET", "/api/v0/repos/acme/ws/diff?from="+c1.Commit+"&to=refs/heads/main&path=d", nil, &raw)
	json.Unmarshal([]byte(raw), &diff)
	if len(diff.Files) != 1 || diff.Files[0].Path != "d/e.txt" {
		t.Fatalf("filtered diff: %s", raw)
	}
	// the merge base of the branch and main is c1
	call(t, srv, tok, "GET", "/api/v0/repos/acme/ws/diff?from=refs/heads/ore/u/x&to=refs/heads/main", nil, &raw)
	json.Unmarshal([]byte(raw), &diff)
	if diff.MergeBase != c1.Commit {
		t.Fatalf("merge base: %s", raw)
	}
	if code := call(t, srv, tok, "GET", "/api/v0/repos/acme/ws/diff?from=nope&to=main", nil, nil); code != 404 {
		t.Fatalf("bad from: %d", code)
	}
	// compare-and-swap: a stale base is refused with the tip
	var moved struct{ Error, Tip string }
	code, raw := post(t, srv.URL, tok, "/api/v0/repos/acme/ws/commits", map[string]any{"branch": "ore/u/x", "base": c1.Commit, "message": "stale", "files": []map[string]any{{"path": "z.txt", "content": "z\n"}}})
	if code != 409 {
		t.Fatalf("stale base: %d %s", code, raw)
	}
	json.Unmarshal([]byte(raw), &moved)
	if moved.Tip != c2.Commit || !strings.Contains(moved.Error, "branch moved") {
		t.Fatalf("stale base body: %s", raw)
	}
	// a clean merge of main into the branch: one commit with two parents
	var m struct{ Commit, Parent string }
	if code := call(t, srv, tok, "POST", "/api/v0/repos/acme/ws/commits", map[string]any{"branch": "ore/u/x", "base": c2.Commit, "merge": "refs/heads/main", "message": "sync"}, &m); code != 201 {
		t.Fatalf("merge: %d", code)
	}
	call(t, srv, tok, "GET", "/api/v0/repos/acme/ws/file?ref=refs/heads/ore/u/x&path=b.txt", nil, &raw)
	if raw != "b2\n" {
		t.Fatalf("merged content: %q", raw)
	}
	call(t, srv, tok, "GET", "/api/v0/repos/acme/ws/file?ref=refs/heads/ore/u/x&path=a.txt", nil, &raw)
	if raw != "a mine\n" {
		t.Fatalf("ours kept: %q", raw)
	}
	call(t, srv, tok, "GET", "/api/v0/repos/acme/ws/diff?from=refs/heads/ore/u/x&to=refs/heads/main", nil, &raw)
	json.Unmarshal([]byte(raw), &diff)
	if diff.MergeBase != c3.Commit {
		t.Fatalf("after the merge main should be the merge base: %s", raw)
	}
	// merging again is nothing to merge
	if code, raw := post(t, srv.URL, tok, "/api/v0/repos/acme/ws/commits", map[string]any{"branch": "ore/u/x", "merge": "refs/heads/main", "message": "sync"}); code != 409 || !strings.Contains(raw, "nothing to merge") {
		t.Fatalf("re-merge: %d %s", code, raw)
	}
	// a conflicting merge: both sides edit a.txt; unresolved is a 409 listing the path, a resolution lands
	call(t, srv, tok, "POST", "/api/v0/repos/acme/ws/commits", map[string]any{"branch": "main", "message": "upstream a", "files": []map[string]any{{"path": "a.txt", "content": "a theirs\n"}}}, nil)
	code, raw = post(t, srv.URL, tok, "/api/v0/repos/acme/ws/commits", map[string]any{"branch": "ore/u/x", "merge": "refs/heads/main", "message": "sync"})
	if code != 409 || !strings.Contains(raw, `"unresolved": [
    "a.txt"
  ]`) {
		t.Fatalf("conflict: %d %s", code, raw)
	}
	if code := call(t, srv, tok, "POST", "/api/v0/repos/acme/ws/commits", map[string]any{"branch": "ore/u/x", "merge": "refs/heads/main", "message": "sync", "files": []map[string]any{{"path": "a.txt", "content": "a resolved\n"}}}, &m); code != 201 {
		t.Fatalf("resolved merge: %d %s", code, raw)
	}
	call(t, srv, tok, "GET", "/api/v0/repos/acme/ws/file?ref=refs/heads/ore/u/x&path=a.txt", nil, &raw)
	if raw != "a resolved\n" {
		t.Fatalf("resolution: %q", raw)
	}
}
