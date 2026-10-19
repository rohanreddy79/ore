// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/workspace"
)

// ore runs a workstation command in dir and returns its output.
func (ts *testServer) ore(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runWorkspace(args[0], args[1:], dir, strings.NewReader(""), &out)
	t.Logf("$ ore %s\n%s", strings.Join(args, " "), out.String())
	return out.String(), err
}

// seedWorkspaceRepo creates acme/app at level 3 with 2,000 files, half of
// them under src/ (the declared active area) and half under vendor/, and a
// pipeline that checks the edit this test makes.
func (ts *testServer) seedWorkspaceRepo(t *testing.T) {
	t.Helper()
	if code, _ := ts.call("POST", "/api/v0/orgs", map[string]string{"name": "acme"}, nil); code != 201 {
		t.Fatalf("org %d", code)
	}
	if code, _ := ts.call("POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "app", "autonomy_level": 3}, nil); code != 201 {
		t.Fatalf("repo %d", code)
	}
	var files []map[string]any
	for i := 0; i < 1000; i++ {
		files = append(files, map[string]any{"path": fmt.Sprintf("src/pkg%02d/f%04d.txt", i%10, i), "content": fmt.Sprintf("file %d\n", i)})
		files = append(files, map[string]any{"path": fmt.Sprintf("vendor/lib%02d/v%04d.txt", i%10, i), "content": fmt.Sprintf("vendor %d\n", i)})
	}
	files = append(files, map[string]any{"path": "src/tool.sh", "content": "#!/bin/sh\necho hi\n", "mode": "100755"})
	files = append(files, map[string]any{"path": ".ore/pipeline.yaml", "content": "version: 0\npaths: [src]\ntasks:\n  - id: check\n    run: grep -q edited src/pkg00/f0000.txt && test ! -e src/pkg01/f0001.txt && test -x src/tool.sh\n"})
	if code, _ := ts.call("POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "main", "message": "init", "files": files}, nil); code != 201 {
		t.Fatalf("seed commit %d", code)
	}
}

func readFileAt(t *testing.T, ts *testServer, ref, path string) (string, bool) {
	t.Helper()
	rp := filepath.Join(ts.cfg.Data, "repos", "acme", "app.git")
	b, err := gitx.ReadFile(context.Background(), rp, ref, path)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// The whole workstation flow with no git on the client: open (sparse),
// edit, save (opens a change), sync after upstream moved (one conflict,
// written side by side), resolve and save again, land at L3 (verified by
// the pipeline), and the branch tip holds exactly what was saved.
func TestWorkstationClientEndToEnd(t *testing.T) {
	ts := startTestServer(t, nil)
	ts.seedWorkspaceRepo(t)
	t.Setenv("ORE_URL", ts.url)
	t.Setenv("ORE_TOKEN", ts.cfg.Token)
	t.Setenv("ORE_CONFIG_DIR", t.TempDir())
	home := t.TempDir()

	// open: only the declared paths and the pipeline file are materialized
	out, err := ts.ore(t, home, "open", "acme/app", "work")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "work")
	if !strings.Contains(out, "1002 files") || !strings.Contains(out, "active area: .ore/pipeline.yaml, src") {
		t.Fatalf("open output: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor")); !os.IsNotExist(err) {
		t.Fatal("vendor/ should not be materialized")
	}
	if st, err := os.Stat(filepath.Join(dir, "src/tool.sh")); err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("tool.sh should be executable: %v", err)
	}
	ws, err := workspace.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws.Manifest) != 1002 || ws.Org != "acme" || ws.Base == "" || len(ws.Paths) != 2 {
		t.Fatalf("state: %+v", ws)
	}
	if _, err := ts.ore(t, dir, "status"); err != nil {
		t.Fatal(err)
	}

	// edit: change, add, delete, chmod, and an ignored scratch file
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/pkg00/f0000.txt", "file 0 edited\n")
	write("src/pkg00/new.txt", "new\n")
	os.Remove(filepath.Join(dir, "src/pkg01/f0001.txt"))
	os.Chmod(filepath.Join(dir, "src/pkg02/f0002.txt"), 0o755)
	write(".oreignore", "*.tmp\nbuild/\n")
	write("src/scratch.tmp", "ignored\n")
	write("build/out.txt", "ignored\n")
	out, _ = ts.ore(t, dir, "status")
	if !strings.Contains(out, "2 changed, 2 added, 1 deleted (unsaved)") {
		t.Fatalf("status before save: %s", out)
	}

	// save: a change opens on a hidden branch; the user sees the number
	out, err = ts.ore(t, filepath.Join(dir, "src/pkg00"), "save", "-m", "Edit the greeting\n\nWhy: the test wants it.")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "saved change #1, patch set 1") || strings.Contains(out, "ore/") {
		t.Fatalf("save output: %s", out)
	}
	var ch ledger.Change
	ts.call("GET", "/api/v0/repos/acme/app/changes/1", nil, &ch)
	if ch.State != "open" || ch.Title != "Edit the greeting" || !strings.HasPrefix(ch.SourceRef, "refs/heads/ore/") || !strings.Contains(ch.SourceRef, "/edit-the-greeting-") || ch.Body != "Why: the test wants it." {
		t.Fatalf("change: %+v", ch)
	}
	if c, _ := readFileAt(t, ts, ch.SourceRef, "src/pkg00/f0000.txt"); c != "file 0 edited\n" {
		t.Fatalf("edit on the branch: %q", c)
	}
	if _, ok := readFileAt(t, ts, ch.SourceRef, "src/scratch.tmp"); ok {
		t.Fatal("ignored file was saved")
	}
	if _, ok := readFileAt(t, ts, ch.SourceRef, "build/out.txt"); ok {
		t.Fatal("ignored directory was saved")
	}
	out, err = ts.ore(t, dir, "save", "-m", "again")
	if err != nil || !strings.Contains(out, "nothing to save") {
		t.Fatalf("second save: %v %s", err, out)
	}

	// upstream moves on main: a new file, a file we did not touch, a file
	// we are editing locally, and a vendor file outside the area
	write("src/pkg04/f0004.txt", "file 4 local edit\n")
	code, _ := ts.call("POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "main", "message": "upstream", "files": []map[string]any{
		{"path": "src/upstream.txt", "content": "from upstream\n"},
		{"path": "src/pkg03/f0003.txt", "content": "file 3 upstream\n"},
		{"path": "src/pkg04/f0004.txt", "content": "file 4 upstream\n"},
		{"path": "src/pkg05/f0005.txt", "delete": true},
		{"path": "vendor/lib00/v0000.txt", "content": "vendor 0 upstream\n"},
	}}, nil)
	if code != 201 {
		t.Fatalf("upstream commit: %d", code)
	}
	out, _ = ts.ore(t, dir, "status")
	if !strings.Contains(out, "has moved") || !strings.Contains(out, "run `ore sync`") {
		t.Fatalf("status should see upstream: %s", out)
	}

	// sync: upstream's changes arrive, the merge lands on the change, the
	// locally edited file is a conflict written side by side
	out, err = ts.ore(t, dir, "sync")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2 updated, 1 deleted") || !strings.Contains(out, "1 conflict(s)") || !strings.Contains(out, "src/pkg04/f0004.txt.upstream") {
		t.Fatalf("sync output: %s", out)
	}
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return "<missing>"
		}
		return string(b)
	}
	if read("src/upstream.txt") != "from upstream\n" || read("src/pkg03/f0003.txt") != "file 3 upstream\n" || read("src/pkg05/f0005.txt") != "<missing>" {
		t.Fatalf("upstream files after sync: %q %q %q", read("src/upstream.txt"), read("src/pkg03/f0003.txt"), read("src/pkg05/f0005.txt"))
	}
	if read("src/pkg04/f0004.txt") != "file 4 local edit\n" || read("src/pkg04/f0004.txt.upstream") != "file 4 upstream\n" {
		t.Fatalf("conflict files: %q %q", read("src/pkg04/f0004.txt"), read("src/pkg04/f0004.txt.upstream"))
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor")); !os.IsNotExist(err) {
		t.Fatal("sync must stay inside the active area")
	}
	if _, err := ts.ore(t, dir, "save", "-m", "too early"); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("save with an unresolved conflict must refuse: %v", err)
	}
	// resolve: keep both lines, drop the side file, save
	write("src/pkg04/f0004.txt", "file 4 local edit\nfile 4 upstream\n")
	os.Remove(filepath.Join(dir, "src/pkg04/f0004.txt.upstream"))
	out, err = ts.ore(t, dir, "save", "-m", "Resolve f0004")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "saved change #1, patch set 3") {
		t.Fatalf("save after sync: %s", out)
	}
	out, _ = ts.ore(t, dir, "sync")
	if !strings.Contains(out, "already up to date") {
		t.Fatalf("sync when current: %s", out)
	}

	// land at L3: the pipeline verifies the head (host executor), then merges
	out, err = ts.ore(t, dir, "land")
	if err != nil {
		t.Fatalf("land: %v\n%s", err, out)
	}
	if !strings.Contains(out, "L3 land (land_when_green)") || !strings.Contains(out, "landed: change #1 merged as") || !strings.Contains(out, "verified by run") {
		t.Fatalf("land output: %s", out)
	}
	for path, want := range map[string]string{
		"src/pkg00/f0000.txt":    "file 0 edited\n",
		"src/pkg00/new.txt":      "new\n",
		"src/pkg03/f0003.txt":    "file 3 upstream\n",
		"src/pkg04/f0004.txt":    "file 4 local edit\nfile 4 upstream\n",
		"src/upstream.txt":       "from upstream\n",
		"vendor/lib00/v0000.txt": "vendor 0 upstream\n",
		"vendor/lib01/v0001.txt": "vendor 1\n",
	} {
		if got, ok := readFileAt(t, ts, "refs/heads/main", path); !ok || got != want {
			t.Errorf("main %s = %q, want %q", path, got, want)
		}
	}
	for _, gone := range []string{"src/pkg01/f0001.txt", "src/pkg05/f0005.txt", "src/scratch.tmp", "src/pkg04/f0004.txt.upstream"} {
		if _, ok := readFileAt(t, ts, "refs/heads/main", gone); ok {
			t.Errorf("main still has %s", gone)
		}
	}
	rp := filepath.Join(ts.cfg.Data, "repos", "acme", "app.git")
	if ls, _ := gitx.Run(context.Background(), rp, nil, "ls-tree", "refs/heads/main", "src/pkg02/f0002.txt"); !strings.HasPrefix(ls, "100755") {
		t.Errorf("mode change did not land: %s", ls)
	}
	out, _ = ts.ore(t, dir, "status")
	if !strings.Contains(out, "change #1 merged") || !strings.Contains(out, "patch sets: 3") || !strings.Contains(out, "succeeded") {
		t.Fatalf("status after landing: %s", out)
	}
	// the next save starts a new change from the landed state
	out, _ = ts.ore(t, dir, "sync")
	if !strings.Contains(out, "has landed") {
		t.Fatalf("sync after landing: %s", out)
	}
	write("src/pkg00/f0000.txt", "file 0 edited twice\n")
	out, err = ts.ore(t, dir, "save", "-m", "Second change")
	if err != nil || !strings.Contains(out, "saved change #2, patch set 1") {
		t.Fatalf("save after landing: %v %s", err, out)
	}
}

// Saves made while the server is unreachable queue in .ore/pending.json in
// order and are sent, opening the change, by the next sync.
func TestSaveQueuesOfflineAndFlushes(t *testing.T) {
	ts := startTestServer(t, nil)
	ts.seedWorkspaceRepo(t)
	t.Setenv("ORE_URL", ts.url)
	t.Setenv("ORE_TOKEN", ts.cfg.Token)
	t.Setenv("ORE_CONFIG_DIR", t.TempDir())
	home := t.TempDir()
	if _, err := ts.ore(t, home, "open", "acme/app"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "app")
	ws, _ := workspace.Load(dir)
	setServer := func(url string) {
		ws, _ = workspace.Load(dir)
		ws.Server = url
		if err := ws.Write(); err != nil {
			t.Fatal(err)
		}
	}
	setServer("http://127.0.0.1:1") // nothing listens there
	os.WriteFile(filepath.Join(dir, "src/pkg00/f0000.txt"), []byte("offline one\n"), 0o644)
	out, err := ts.ore(t, dir, "save", "-m", "Offline edit")
	if err != nil || !strings.Contains(out, "save queued") {
		t.Fatalf("offline save: %v %s", err, out)
	}
	os.WriteFile(filepath.Join(dir, "src/pkg00/f0000.txt"), []byte("offline two\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "src/added.txt"), []byte("added offline\n"), 0o644)
	out, err = ts.ore(t, dir, "save", "-m", "Offline again")
	if err != nil || !strings.Contains(out, "2 queued in all") {
		t.Fatalf("second offline save: %v %s", err, out)
	}
	var q workspace.Queue
	b, _ := os.ReadFile(filepath.Join(dir, workspace.PendingFile))
	json.Unmarshal(b, &q)
	if len(q.Saves) != 2 || q.Saves[0].Base == "" || q.Saves[1].Base != "" || q.Saves[0].Branch != q.Saves[1].Branch || len(q.Saves[1].Files) != 2 {
		t.Fatalf("queue: %s", b)
	}
	out, _ = ts.ore(t, dir, "status")
	if !strings.Contains(out, "server unreachable") || !strings.Contains(out, "queued offline: 2 save(s), 3 file(s)") || !strings.Contains(out, "not opened yet") {
		t.Fatalf("offline status: %s", out)
	}
	if _, err := ts.ore(t, dir, "land"); err == nil {
		t.Fatal("land offline must fail")
	}
	setServer(ts.url)
	out, err = ts.ore(t, dir, "sync")
	if err != nil || strings.Count(out, "sent queued save") != 2 {
		t.Fatalf("flush on sync: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, workspace.PendingFile)); !os.IsNotExist(err) {
		t.Fatal("pending.json should be gone")
	}
	var ch ledger.Change
	ts.call("GET", "/api/v0/repos/acme/app/changes/1", nil, &ch)
	if ch.State != "open" || ch.Title != "Offline edit" {
		t.Fatalf("change from the queue: %+v", ch)
	}
	if c, _ := readFileAt(t, ts, ch.SourceRef, "src/pkg00/f0000.txt"); c != "offline two\n" {
		t.Fatalf("branch content: %q", c)
	}
	var sets []map[string]any
	ts.call("GET", "/api/v0/repos/acme/app/changes/1/patchsets", nil, &sets)
	if len(sets) != 2 {
		t.Fatalf("patch sets: %d", len(sets))
	}
	var refs map[string]string
	ts.call("GET", "/api/v0/repos/acme/app/refs", nil, &refs)
	ws, _ = workspace.Load(dir)
	if ws.Change == nil || ws.Change.Number != 1 || ws.Base != refs[ch.SourceRef] {
		t.Fatalf("state after flush: %+v base %s, branch at %s", ws.Change, ws.Base, refs[ch.SourceRef])
	}
	// an edit after the flush is a plain online save
	os.WriteFile(filepath.Join(dir, "src/added.txt"), []byte("edited online\n"), 0o644)
	if out, err := ts.ore(t, dir, "save", "-m", "Online"); err != nil || !strings.Contains(out, "patch set 3") {
		t.Fatalf("online save: %v %s", err, out)
	}
}

// --inline merges a conflicting file in place: regions only one side
// changed merge cleanly, overlapping ones get markers, and a save is refused
// until the markers are gone.
func TestSyncInlineMerge(t *testing.T) {
	ts := startTestServer(t, nil)
	ts.call("POST", "/api/v0/orgs", map[string]string{"name": "acme"}, nil)
	ts.call("POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "app"}, nil)
	ts.call("POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "main", "message": "init", "files": []map[string]any{
		{"path": "a.txt", "content": "one\ntwo\nthree\nfour\nfive\n"},
		{"path": "b.txt", "content": "x\ny\nz\n"},
	}}, nil)
	t.Setenv("ORE_URL", ts.url)
	t.Setenv("ORE_TOKEN", ts.cfg.Token)
	t.Setenv("ORE_CONFIG_DIR", t.TempDir())
	home := t.TempDir()
	if _, err := ts.ore(t, home, "open", "acme/app"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "app")
	// local: first line of a.txt and last line of b.txt; upstream: last line of a.txt and first line of b.txt
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("ONE\ntwo\nthree\nfour\nfive\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("x\ny\nZ local\n"), 0o644)
	ts.call("POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "main", "message": "up", "files": []map[string]any{
		{"path": "a.txt", "content": "one\ntwo\nthree\nfour\nFIVE\n"},
		{"path": "b.txt", "content": "x\ny\nZ upstream\n"},
	}}, nil)
	out, err := ts.ore(t, dir, "sync", "--inline")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1 merged") || !strings.Contains(out, "1 conflict(s)") || !strings.Contains(out, "b.txt") {
		t.Fatalf("inline sync: %s", out)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(a) != "ONE\ntwo\nthree\nfour\nFIVE\n" {
		t.Fatalf("a.txt merged: %q", a)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "b.txt"))
	if string(b) != "x\ny\n<<<<<<< local\nZ local\n=======\nZ upstream\n>>>>>>> main\n" {
		t.Fatalf("b.txt markers: %q", b)
	}
	if _, err := ts.ore(t, dir, "save", "-m", "with markers"); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("save with markers must refuse: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("x\ny\nZ both\n"), 0o644)
	out, err = ts.ore(t, dir, "save", "-m", "Merged by hand")
	if err != nil || !strings.Contains(out, "saved change #1") || !strings.Contains(out, "2 changed") {
		t.Fatalf("save after inline resolution: %v %s", err, out)
	}
	var ch ledger.Change
	ts.call("GET", "/api/v0/repos/acme/app/changes/1", nil, &ch)
	rp := filepath.Join(ts.cfg.Data, "repos", "acme", "app.git")
	if c, _ := gitx.ReadFile(context.Background(), rp, ch.SourceRef, "a.txt"); string(c) != "ONE\ntwo\nthree\nfour\nFIVE\n" {
		t.Fatalf("a.txt on the branch: %q", c)
	}
	if diff, _ := gitx.Diff(context.Background(), rp, ch.BaseOID, ch.HeadOID); !strings.Contains(diff, "+ONE") || strings.Contains(diff, "+FIVE") {
		t.Fatalf("the change should carry only the local edits:\n%s", diff)
	}
}
