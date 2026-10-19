// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orevcs/ore/internal/fpi"
	"github.com/orevcs/ore/internal/ledger"
)

func TestLinkWithoutToken(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.addRepo("octo", "app", nil)
	h := newHarness(t, gh, Config{})
	h.createRepo("acme", "app")
	var out map[string]any
	code := h.api("POST", "/api/v0/repos/acme/app/github/link", LinkRequest{Owner: "octo", Name: "app"}, &out)
	if code != 503 || !strings.Contains(out["error"].(string), EnvToken) {
		t.Fatalf("expected a clear 503, got %d %v", code, out)
	}
	if len(h.events("io.ore.github.")) != 0 {
		t.Fatal("something was recorded")
	}
	// The FPI provider reports the same.
	p, _ := fpi.Lookup("github")
	if _, err := p.Repository(context.Background(), "octo", "app"); err != ErrNoToken {
		t.Fatalf("provider without token: %v", err)
	}
}

func TestLinkErrors(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.addRepo("octo", "app", nil)
	h := newHarness(t, gh, Config{Token: testToken})
	h.createRepo("acme", "app")
	var out map[string]any
	if code := h.api("POST", "/api/v0/repos/acme/app/github/link", LinkRequest{Owner: "octo", Name: "missing"}, &out); code != 404 || !strings.Contains(out["error"].(string), "octo/missing") {
		t.Fatalf("unknown GitHub repo: %d %v", code, out)
	}
	if code := h.api("POST", "/api/v0/repos/acme/app/github/link", LinkRequest{}, &out); code != 400 {
		t.Fatalf("empty request: %d %v", code, out)
	}
	if code := h.api("POST", "/api/v0/repos/acme/nope/github/link", LinkRequest{Owner: "octo", Name: "app"}, &out); code != 404 {
		t.Fatalf("unknown Ore repo: %d", code)
	}
	if code := h.api("POST", "/api/v0/repos/acme/app/github/link", map[string]string{"repository": "octo/app"}, &out); code != 201 {
		t.Fatalf("link by repository: %d %v", code, out)
	}
	if code := h.api("POST", "/api/v0/repos/acme/app/github/link", LinkRequest{Owner: "octo", Name: "app"}, &out); code != 409 {
		t.Fatalf("second link: %d %v", code, out)
	}
	// A bad token is a 502 with GitHub's message, not an Ore 401.
	h2 := newHarness(t, gh, Config{Token: "wrong"})
	h2.createRepo("acme", "app")
	if code := h2.api("POST", "/api/v0/repos/acme/app/github/link", LinkRequest{Owner: "octo", Name: "app"}, &out); code != 502 || !strings.Contains(out["error"].(string), "Bad credentials") {
		t.Fatalf("bad token: %d %v", code, out)
	}
	// Listing, getting and unlinking.
	var links []Link
	if code := h.api("GET", "/api/v0/github/links", nil, &links); code != 200 || len(links) != 1 || links[0].Secret != "" {
		t.Fatalf("list: %d %+v", code, links)
	}
	var got LinkResponse
	if code := h.api("GET", "/api/v0/repos/acme/app/github/link", nil, &got); code != 200 || got.Link.Secret == "" {
		t.Fatalf("get: %d %+v", code, got)
	}
	if code := h.api("DELETE", "/api/v0/repos/acme/app/github/link", nil, nil); code != 200 || len(h.events("io.ore.github.unlinked")) != 1 {
		t.Fatalf("unlink: %d", code)
	}
	if code := h.api("GET", "/api/v0/repos/acme/app/github/link", nil, &out); code != 404 {
		t.Fatalf("after unlink: %d", code)
	}
	if code, _ := h.deliver("ping", "d", got.Link.Secret, payload(t, "ping.json", nil, nil)); code != 404 {
		t.Fatalf("webhook after unlink: %d", code)
	}
}

func TestLinkAdoptsGitHubDefaultBranch(t *testing.T) {
	gh := newFakeGitHub(t)
	up := gh.addRepo("octo", "legacy", nil)
	up.setRef(t, "refs/heads/master", up.tip(t, "refs/heads/main"))
	up.deleteRef(t, "refs/heads/main")
	up.defaultBranch = "master"
	h := newHarness(t, gh, Config{Token: testToken})
	h.createRepo("acme", "legacy")
	res := h.link("acme", "legacy", "octo", "legacy")
	var repo ledger.Repo
	h.api("GET", "/api/v0/repos/acme/legacy", nil, &repo)
	if repo.DefaultBranch != "master" || res.Link.DefaultBranch != "master" {
		t.Fatalf("default branch: %+v %+v", repo, res.Link)
	}
	head, _ := os.ReadFile(filepath.Join(h.dir, "repos", "acme", "legacy.git", "HEAD"))
	if strings.TrimSpace(string(head)) != "ref: refs/heads/master" {
		t.Fatalf("HEAD: %s", head)
	}
	if l := h.events("io.ore.github.linked"); len(l) != 1 || data(t, l[0])["ore_default_branch_was"] != "main" {
		t.Fatalf("linked event: %+v", l)
	}
}

func TestLinkWithProvidedSecretAndInstallation(t *testing.T) {
	gh := newFakeGitHub(t)
	up := gh.addRepo("octo", "app", nil)
	h := newHarness(t, gh, Config{Token: testToken})
	h.createRepo("acme", "app")
	var res LinkResponse
	if code := h.api("POST", "/api/v0/repos/acme/app/github/link", LinkRequest{Owner: "Octo", Name: "App.git", Secret: "my-shared-secret", InstallationID: 99}, &res); code != 201 {
		t.Fatalf("link: %d", code)
	}
	if res.Link.Secret != "my-shared-secret" || res.Link.InstallationID != 99 || res.Link.Owner != "octo" || res.Link.Name != "app" {
		t.Fatalf("link: %+v", res.Link)
	}
	if code, _ := h.deliver("ping", "d", "my-shared-secret", payload(t, "ping.json", up, nil)); code != 200 {
		t.Fatalf("ping with provided secret: %d", code)
	}
}

func TestGitHubProviderReads(t *testing.T) {
	gh := newFakeGitHub(t)
	up := gh.addRepo("octo", "app", nil)
	up.issues = []map[string]any{
		{"number": 1, "title": "open one", "body": "b", "state": "open", "html_url": "https://github.com/octo/app/issues/1", "user": map[string]any{"login": "octocat"}},
		{"number": 2, "title": "a PR", "state": "open", "pull_request": map[string]any{"url": "x"}, "user": map[string]any{"login": "octocat"}},
		{"number": 3, "title": "closed one", "state": "closed", "user": map[string]any{"login": "hubot"}},
	}
	newHarness(t, gh, Config{Token: testToken})
	p, ok := fpi.Lookup("github")
	if !ok || p.Name() != "github" || !p.Capabilities().InboundHooks || p.Capabilities().ServerSideMerge {
		t.Fatalf("provider: %v %+v", ok, p)
	}
	ctx := context.Background()
	repo, err := p.Repository(ctx, "octo", "app")
	if err != nil || repo.DefaultBranch != "main" || repo.CloneURL == "" || repo.Provider != "github" {
		t.Fatalf("repository: %+v %v", repo, err)
	}
	refs, err := p.ListRefs(ctx, repo)
	if err != nil || len(refs) != 1 || refs[0].Name != "refs/heads/main" || refs[0].OID != up.tip(t, "refs/heads/main") {
		t.Fatalf("refs: %+v %v", refs, err)
	}
	issues, err := p.ListIssues(ctx, repo, fpi.IssueQuery{State: "all"})
	byNumber := map[int]fpi.Issue{}
	for _, i := range issues {
		byNumber[i.Number] = i
	}
	if err != nil || len(issues) != 2 || byNumber[1].Author != "octocat" || byNumber[3].Author != "hubot" {
		t.Fatalf("issues: %+v %v", issues, err)
	}
	if err := p.ReportStatus(ctx, repo, up.tip(t, "refs/heads/main"), fpi.Status{State: fpi.StatusSuccess, Context: "ore/manual"}); err != nil || len(gh.statusCalls()) != 1 {
		t.Fatalf("report status: %v", err)
	}
	if names := fpi.Names(); len(names) != 2 || names[0] != "github" || names[1] != "native" {
		t.Fatalf("providers: %v", names)
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv(EnvToken, " tok ")
	t.Setenv(EnvAPI, "http://fake/")
	t.Setenv(EnvSyncInterval, "90s")
	t.Setenv(EnvStatusInterval, "off")
	cfg, err := ConfigFromEnv()
	if err != nil || cfg.Token != "tok" || cfg.API != "http://fake" || cfg.SyncInterval != 90*time.Second || cfg.StatusInterval != -1 || !cfg.Configured() {
		t.Fatalf("%+v %v", cfg, err)
	}
	t.Setenv(EnvSyncInterval, "soon")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("expected an error for a bad interval")
	}
	t.Setenv(EnvSyncInterval, "")
	t.Setenv(EnvToken, "")
	cfg, _ = ConfigFromEnv()
	if cfg.Configured() || cfg.SyncInterval != DefaultSyncInterval || NewClient(cfg) != nil {
		t.Fatalf("%+v", cfg)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "token"), []byte("from-file\n"), 0o600)
	t.Setenv(EnvTokenFile, filepath.Join(dir, "token"))
	cfg, _ = ConfigFromEnv()
	c := NewClient(cfg)
	if tok, err := c.Token(); err != nil || tok != "from-file" {
		t.Fatalf("file token: %q %v", tok, err)
	}
	os.WriteFile(filepath.Join(dir, "token"), []byte("rotated\n"), 0o600)
	if tok, _ := c.Token(); tok != "rotated" {
		t.Fatalf("rotated token not picked up: %q", tok)
	}
}

func TestGitEnvScopesCredentials(t *testing.T) {
	c := NewClient(Config{Token: "abc"})
	env, err := c.gitEnv("https://github.com/octo/app.git")
	if err != nil || len(env) != 5 || env[1] != "GIT_CONFIG_KEY_0=http.https://github.com/.extraHeader" || !strings.HasPrefix(env[2], "GIT_CONFIG_VALUE_0=Authorization: Basic ") || strings.Contains(strings.Join(env, " "), "abc") {
		t.Fatalf("env: %v %v", env, err)
	}
	if env, err := c.gitEnv("file:///tmp/x.git"); err != nil || env != nil {
		t.Fatalf("file url: %v %v", env, err)
	}
}
