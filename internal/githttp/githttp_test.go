// SPDX-License-Identifier: AGPL-3.0-only

package githttp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestPushRecordsRefUpdatesAndProtectsMain(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := ledger.Open(ctx, filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	backend, err := gitx.HTTPBackendPath(ctx)
	if err != nil {
		t.Skip(err)
	}
	org, _ := db.CreateOrg(ctx, "acme", ledger.System)
	repo, _ := db.CreateRepo(ctx, org, "app", "main", 2, 8080, ledger.System)
	reposDir := filepath.Join(dir, "repos")
	if err := gitx.InitBare(ctx, RepoPath(reposDir, "acme", "app"), "main"); err != nil {
		t.Fatal(err)
	}
	h := &Handler{DB: db, ReposDir: reposDir, Backend: backend, Auth: func(r *http.Request) *ledger.Actor {
		_, pw, ok := r.BasicAuth()
		if !ok || pw != "secret" {
			return nil
		}
		return &ledger.Actor{Kind: "human", ID: "tester"}
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	url := strings.Replace(srv.URL, "http://", "http://u:secret@", 1) + "/acme/app.git"

	work := filepath.Join(dir, "work")
	git(t, dir, "init", "-b", "main", work)
	os.WriteFile(filepath.Join(work, "README"), []byte("hi\n"), 0o644)
	git(t, work, "add", ".")
	git(t, work, "commit", "-m", "init")
	git(t, work, "push", url, "main") // first push to an empty ref is allowed

	os.WriteFile(filepath.Join(work, "README"), []byte("hi again\n"), 0o644)
	git(t, work, "commit", "-am", "second")
	cmd := exec.Command("git", "push", url, "main")
	cmd.Dir = work
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("expected protected push to fail, got success: %s", out)
	} else if !strings.Contains(string(out), "protected") {
		t.Fatalf("expected protected-ref message, got: %s", out)
	}
	git(t, work, "push", url, "main:feature") // branches are fine

	events, err := db.Events(ctx, ledger.EventsOptions{Type: "io.ore.ref.updated"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 ref.updated events, got %d", len(events))
	}
	if !strings.Contains(string(events[0].Data), `"ref":"refs/heads/main"`) || !strings.Contains(string(events[1].Data), `"ref":"refs/heads/feature"`) {
		t.Fatalf("unexpected events: %s / %s", events[0].Data, events[1].Data)
	}
	if events[0].OreActor.ID != "tester" || repo.ID == "" {
		t.Fatalf("actor not recorded: %+v", events[0].OreActor)
	}
	// unauthenticated clone is refused
	resp, _ := http.Get(srv.URL + "/acme/app.git/info/refs?service=git-upload-pack")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

// The organization's git transport switch (ADR 0016 §3): a push works, the
// switch goes to read-only (clone works, push refused), then off (both
// refused, over protocol v0 and v2, with the message naming the Ore client),
// then back on; refusals leave no ref.updated behind.
func TestGitTransportSwitchRefusesWithTheOreClientMessage(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := ledger.Open(ctx, filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := gitx.HTTPBackendPath(ctx); err != nil {
		t.Skip(err)
	}
	org, _ := db.CreateOrg(ctx, "acme", ledger.System)
	db.CreateRepo(ctx, org, "app", "main", 2, 8080, ledger.System)
	reposDir := filepath.Join(dir, "repos")
	if err := gitx.InitBare(ctx, RepoPath(reposDir, "acme", "app"), "main"); err != nil {
		t.Fatal(err)
	}
	h := &Handler{DB: db, ReposDir: reposDir, Auth: func(r *http.Request) *ledger.Actor {
		if _, pw, ok := r.BasicAuth(); !ok || pw != "secret" {
			return nil
		}
		return &ledger.Actor{Kind: "human", ID: "tester"}
	}}
	srv := httptest.NewServer(h)
	defer srv.Close()
	url := strings.Replace(srv.URL, "http://", "http://u:secret@", 1) + "/acme/app.git"
	set := func(mode, reason string) {
		t.Helper()
		if _, _, err := db.SetOrgSettings(ctx, org, ledger.OrgSettingsInput{GitTransport: &mode, Reason: &reason}, ledger.System); err != nil {
			t.Fatal(err)
		}
	}
	tryGit := func(dir string, args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	refEvents := func() int {
		evs, _ := db.Events(ctx, ledger.EventsOptions{Type: "io.ore.ref.updated"})
		return len(evs)
	}

	work := filepath.Join(dir, "work")
	git(t, dir, "init", "-b", "main", work)
	os.WriteFile(filepath.Join(work, "README"), []byte("hi\n"), 0o644)
	git(t, work, "add", ".")
	git(t, work, "commit", "-m", "init")
	git(t, work, "push", url, "main") // on: the push works
	if refEvents() != 1 {
		t.Fatalf("expected 1 ref.updated, got %d", refEvents())
	}

	// read-only: clone and fetch work, a push is refused and the message says what to do instead
	set(ledger.GitTransportReadOnly, "")
	clone1 := filepath.Join(dir, "clone1")
	git(t, dir, "clone", "-q", url, clone1)
	git(t, clone1, "fetch", "-q", url)
	os.WriteFile(filepath.Join(work, "f"), []byte("x\n"), 0o644)
	git(t, work, "add", ".")
	git(t, work, "commit", "-m", "second")
	out, err := tryGit(work, "push", url, "main:feature")
	if err == nil {
		t.Fatalf("push in read-only mode succeeded: %s", out)
	}
	want := "git pushes are off for acme; save your work with the Ore client: ore open acme/app, then ore save"
	if !strings.Contains(out, "remote error: "+want) {
		t.Fatalf("read-only push output:\n%s", out)
	}

	// off: everything is refused, the reason follows the message, protocol v2 clients read it too
	set(ledger.GitTransportOff, "we moved to Ore; ask #dev-tools")
	want = "the git transport is off for acme; use the Ore client: ore open acme/app (we moved to Ore; ask #dev-tools)"
	for _, args := range [][]string{
		{"clone", url, filepath.Join(dir, "clone2")},
		{"-c", "protocol.version=2", "clone", url, filepath.Join(dir, "clone3")},
		{"-c", "protocol.version=0", "clone", url, filepath.Join(dir, "clone4")},
		{"ls-remote", url},
	} {
		out, err := tryGit(dir, args...)
		if err == nil {
			t.Fatalf("git %v succeeded with the transport off: %s", args, out)
		}
		if !strings.Contains(out, "remote error: "+want) {
			t.Fatalf("git %v output:\n%s", args, out)
		}
	}
	out, err = tryGit(clone1, "fetch", url)
	if err == nil || !strings.Contains(out, "remote error: "+want) {
		t.Fatalf("fetch with the transport off: %v\n%s", err, out)
	}
	out, err = tryGit(work, "push", url, "main:feature")
	if err == nil || !strings.Contains(out, "remote error: "+want) {
		t.Fatalf("push with the transport off: %v\n%s", err, out)
	}
	if refEvents() != 1 {
		t.Fatalf("refused pushes recorded ref updates: %d", refEvents())
	}
	// the raw endpoints: the advertisement is a 200 ERR packet (what git needs to show the text),
	// the RPC endpoints are 403 with the message, every answer names the mode in a header
	req, _ := http.NewRequest("GET", url+"/info/refs?service=git-upload-pack", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("X-Ore-Git-Transport") != "off" || !strings.Contains(string(body), "ERR "+want) {
		t.Fatalf("advertisement: %d %q %q", resp.StatusCode, resp.Header.Get("X-Ore-Git-Transport"), body)
	}
	req, _ = http.NewRequest("POST", url+"/git-upload-pack", strings.NewReader(""))
	resp, _ = http.DefaultClient.Do(req)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "ore: "+want) {
		t.Fatalf("rpc: %d %q", resp.StatusCode, body)
	}
	// an anonymous probe still gets the 401, not the setting
	resp, _ = http.Get(srv.URL + "/acme/app.git/info/refs?service=git-upload-pack")
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("X-Ore-Git-Transport") != "" {
		t.Fatalf("anonymous: %d %q", resp.StatusCode, resp.Header.Get("X-Ore-Git-Transport"))
	}

	// back on: the push that was refused goes through and is recorded
	set(ledger.GitTransportOn, "")
	git(t, work, "push", url, "main:feature")
	if refEvents() != 2 {
		t.Fatalf("expected 2 ref.updated after switching back on, got %d", refEvents())
	}
}

func TestTransportRefusalMessages(t *testing.T) {
	st := ledger.OrgSettings{Org: "acme", GitTransport: ledger.GitTransportOn}
	if TransportRefusal(st, "app", true) != "" || TransportRefusal(st, "app", false) != "" {
		t.Fatal("on must allow everything")
	}
	st.GitTransport = ledger.GitTransportReadOnly
	if TransportRefusal(st, "app", false) != "" {
		t.Fatal("read-only must allow reads")
	}
	if got := TransportRefusal(st, "app", true); got != "git pushes are off for acme; save your work with the Ore client: ore open acme/app, then ore save" {
		t.Fatalf("read-only write: %q", got)
	}
	st.GitTransport, st.GitTransportReason = ledger.GitTransportOff, "why"
	if got := TransportRefusal(st, "app", false); got != "the git transport is off for acme; use the Ore client: ore open acme/app (why)" {
		t.Fatalf("off: %q", got)
	}
}
