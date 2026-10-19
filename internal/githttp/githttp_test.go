// SPDX-License-Identifier: AGPL-3.0-only

package githttp

import (
	"context"
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
