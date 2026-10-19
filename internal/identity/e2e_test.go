// SPDX-License-Identifier: AGPL-3.0-only

package identity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/orevcs/ore/internal/githttp"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/identity/sshd"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/review"
	"github.com/orevcs/ore/internal/web"
)

const adminToken = "admin-secret-token"

// env is a full server (HTTP API, git smart HTTP, git over SSH) with the
// identity extension applied, plus helpers that drive real git and ssh.
type env struct {
	t    *testing.T
	srv  *httptest.Server
	svc  *Service
	ssh  *sshd.Server
	dir  string
	home string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	backend, err := gitx.HTTPBackendPath(ctx)
	if err != nil {
		t.Skip(err)
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh client not found")
	}
	dir := t.TempDir()
	db, err := ledger.Open(ctx, filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repos, logs := filepath.Join(dir, "repos"), filepath.Join(dir, "logs")
	os.MkdirAll(repos, 0o755)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &web.Server{DB: db, ReposDir: repos, LogsDir: logs, Token: adminToken, BaseURL: "http://test", Log: quiet,
		Git:    &githttp.Handler{DB: db, ReposDir: repos, Backend: backend, Log: quiet},
		Review: &review.Service{DB: db, ReposDir: repos},
	}
	t.Setenv("ORE_SSH_ADDR", "127.0.0.1:0")
	extCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	if err := Extension(extCtx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, HostKeyFile)); err != nil {
		t.Fatalf("host key not generated in the data directory: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	home := filepath.Join(dir, "home")
	os.MkdirAll(home, 0o700)
	return &env{t: t, srv: srv, svc: s.Services["identity"].(*Service), ssh: s.Services["identity.sshd"].(*sshd.Server), dir: dir, home: home}
}

func (e *env) call(token, method, path string, in any) (int, string) {
	e.t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func (e *env) must(token, method, path string, in any, want int, out any) {
	e.t.Helper()
	code, body := e.call(token, method, path, in)
	if code != want {
		e.t.Fatalf("%s %s: got %d, want %d: %s", method, path, code, want, body)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(body), out); err != nil {
			e.t.Fatalf("%s %s: bad json: %v: %s", method, path, err, body)
		}
	}
}

// sshArgs isolates the ssh client from the user's config, agent and known hosts.
func sshArgs(key string) []string {
	return []string{"-F", "/dev/null", "-i", key, "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none",
		"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR", "-o", "BatchMode=yes"}
}

// sshCommand is the GIT_SSH_COMMAND for a private key.
func (e *env) sshCommand(key string) string {
	args := sshArgs(key)
	args[3] = "'" + key + "'"
	return "ssh " + strings.Join(args, " ")
}

func (e *env) git(key, dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-c", "credential.helper="}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+e.home, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if key != "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND="+e.sshCommand(key))
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (e *env) mustGit(key, dir string, args ...string) string {
	e.t.Helper()
	out, err := e.git(key, dir, args...)
	if err != nil {
		e.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

func (e *env) sshURL(org, name string) string {
	_, port, _ := net.SplitHostPort(e.ssh.Addr().String())
	return fmt.Sprintf("ssh://git@127.0.0.1:%s/%s/%s.git", port, org, name)
}

func (e *env) httpURL(user, token, org, name string) string {
	return strings.Replace(e.srv.URL, "http://", "http://"+user+":"+token+"@", 1) + "/" + org + "/" + name + ".git"
}

// keyPair writes a fresh ed25519 private key and returns its path and public line.
func (e *env) keyPair(name string) (string, string) {
	e.t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		e.t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		e.t.Fatal(err)
	}
	path := filepath.Join(e.dir, name+"_key")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		e.t.Fatal(err)
	}
	spub, _ := ssh.NewPublicKey(pub)
	return path, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(spub))) + " " + name + "@test"
}

// commitFile makes a commit in dir and returns nothing; the caller pushes.
func (e *env) commitFile(dir, name, content, msg string) {
	e.t.Helper()
	os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	e.mustGit("", dir, "add", name)
	e.mustGit("", dir, "commit", "-q", "-m", msg)
}

type refEvent struct {
	Ref, Old, New, Via string
}

func (e *env) refEvents() []struct {
	ledger.Event
	Data refEvent
} {
	e.t.Helper()
	var evs []ledger.Event
	e.must(adminToken, "GET", "/api/v0/events?type=io.ore.ref.updated&limit=1000", nil, 200, &evs)
	var out []struct {
		ledger.Event
		Data refEvent
	}
	for _, ev := range evs {
		var d refEvent
		json.Unmarshal(ev.Data, &d)
		out = append(out, struct {
			ledger.Event
			Data refEvent
		}{ev, d})
	}
	return out
}

func (e *env) findRefEvent(ref, via string) (ledger.Actor, bool) {
	for _, ev := range e.refEvents() {
		if ev.Data.Ref == ref && ev.Data.Via == via {
			return ev.OreActor, true
		}
	}
	return ledger.Actor{}, false
}

// seed creates acme/app with a commit on main and the user alice (member of
// acme) with a read-write token; it returns alice's token secret.
func (e *env) seed() string {
	e.t.Helper()
	e.must(adminToken, "POST", "/api/v0/orgs", map[string]string{"name": "acme"}, 201, nil)
	e.must(adminToken, "POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "app"}, 201, nil)
	e.must(adminToken, "POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "main", "message": "init",
		"files": []map[string]any{{"path": "README.md", "content": "# app\n"}}}, 201, nil)
	e.must(adminToken, "POST", "/api/v0/identity/users", map[string]any{"name": "alice", "email": "alice@example.com",
		"memberships": []map[string]string{{"org": "acme", "role": "member"}}}, 201, nil)
	var tok Token
	e.must(adminToken, "POST", "/api/v0/identity/tokens", map[string]any{"name": "laptop", "user": "alice"}, 201, &tok)
	if !strings.HasPrefix(tok.Secret, SecretPrefix) {
		e.t.Fatalf("no secret in the create response: %+v", tok)
	}
	return tok.Secret
}

// addKey registers a key for the token's user and returns the private key path.
func (e *env) addKey(token, name string) string {
	e.t.Helper()
	priv, pub := e.keyPair(name)
	var k SSHKey
	e.must(token, "POST", "/api/v0/identity/ssh-keys", map[string]string{"public_key": pub, "title": name}, 201, &k)
	if !strings.HasPrefix(k.Fingerprint, "SHA256:") {
		e.t.Fatalf("key: %+v", k)
	}
	return priv
}

func TestE2E_UserClonesOverHTTPAndPushesOverSSH(t *testing.T) {
	e := newEnv(t)
	alice := e.seed()

	// clone over HTTP with basic auth (user name: anything, password: the token)
	work := filepath.Join(e.dir, "work")
	e.mustGit("", e.dir, "clone", "-q", e.httpURL("alice", alice, "acme", "app"), work)
	if b, _ := os.ReadFile(filepath.Join(work, "README.md")); string(b) != "# app\n" {
		t.Fatalf("clone content: %q", b)
	}

	// add an SSH key through the API as alice, then push a branch over SSH
	key := e.addKey(alice, "alice")
	e.mustGit("", work, "remote", "set-url", "origin", e.sshURL("acme", "app"))
	e.mustGit("", work, "checkout", "-q", "-b", "feature")
	e.commitFile(work, "feature.txt", "hello\n", "add feature")
	out := e.mustGit(key, work, "push", "origin", "feature")
	if !strings.Contains(out, "feature -> feature") {
		t.Fatalf("push output: %s", out)
	}
	actor, ok := e.findRefEvent("refs/heads/feature", "ssh")
	if !ok || actor != (ledger.Actor{Kind: "human", ID: "alice"}) {
		t.Fatalf("ref.updated via ssh with alice as actor not found: %v %+v", ok, actor)
	}

	// a second commit on the branch over SSH, then clone over SSH (upload-pack)
	e.commitFile(work, "feature.txt", "hello again\n", "more")
	e.mustGit(key, work, "push", "-q", "origin", "feature")
	clone := filepath.Join(e.dir, "clone")
	e.mustGit(key, e.dir, "clone", "-q", "-b", "feature", e.sshURL("acme", "app"), clone)
	if b, _ := os.ReadFile(filepath.Join(clone, "feature.txt")); string(b) != "hello again\n" {
		t.Fatalf("ssh clone content: %q", b)
	}
	n := 0
	for _, ev := range e.refEvents() {
		if ev.Data.Ref == "refs/heads/feature" && ev.Data.Via == "ssh" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("expected 2 ssh ref.updated events for feature, got %d", n)
	}
	// the ledger also has the identity audit trail
	for _, typ := range []string{"io.ore.identity.user.created", "io.ore.identity.membership.added", "io.ore.identity.token.created", "io.ore.identity.ssh_key.added"} {
		var evs []ledger.Event
		e.must(adminToken, "GET", "/api/v0/events?type="+typ, nil, 200, &evs)
		if len(evs) == 0 {
			t.Errorf("no %s event", typ)
		}
	}
	// list routes work for the user
	var keys []SSHKey
	e.must(alice, "GET", "/api/v0/identity/ssh-keys", nil, 200, &keys)
	var ms []Membership
	e.must(alice, "GET", "/api/v0/identity/memberships", nil, 200, &ms)
	if len(keys) != 1 || len(ms) != 1 || ms[0].Org != "acme" {
		t.Fatalf("keys %+v memberships %+v", keys, ms)
	}
}

func TestE2E_ExpiredToken(t *testing.T) {
	e := newEnv(t)
	e.seed()
	var tok Token
	e.must(adminToken, "POST", "/api/v0/identity/tokens", map[string]any{"user": "alice", "expires_in": "1h"}, 201, &tok)
	e.must(tok.Secret, "GET", "/api/v0/repos/acme/app", nil, 200, nil)
	e.svc.Now = func() string { return "3000-01-01T00:00:00.000Z" } // the clock moves past the expiry
	e.must(tok.Secret, "GET", "/api/v0/repos/acme/app", nil, 401, nil)
	e.must(tok.Secret, "GET", "/api/v0/identity/me", nil, 401, nil)
	if out, err := e.git("", e.dir, "clone", "-q", e.httpURL("alice", tok.Secret, "acme", "app"), filepath.Join(e.dir, "x")); err == nil {
		t.Fatalf("clone with an expired token succeeded: %s", out)
	}
	e.svc.Now = nil
	e.must(tok.Secret, "GET", "/api/v0/repos/acme/app", nil, 200, nil)
	if code, _ := e.call(adminToken, "POST", "/api/v0/identity/tokens", map[string]any{"user": "alice", "expires_at": "2000-01-01T00:00:00Z"}); code != 400 {
		t.Fatalf("token already expired at creation accepted: %d", code)
	}
}

func TestE2E_RevokedToken(t *testing.T) {
	e := newEnv(t)
	alice := e.seed()
	var tok Token
	e.must(alice, "POST", "/api/v0/identity/tokens", map[string]any{"name": "ci"}, 201, &tok)
	e.must(tok.Secret, "GET", "/api/v0/identity/me", nil, 200, nil)
	// another user cannot revoke it
	e.must(adminToken, "POST", "/api/v0/identity/users", map[string]any{"name": "mallory"}, 201, nil)
	var mtok Token
	e.must(adminToken, "POST", "/api/v0/identity/tokens", map[string]any{"user": "mallory"}, 201, &mtok)
	e.must(mtok.Secret, "DELETE", "/api/v0/identity/tokens/"+tok.ID, nil, 403, nil)
	e.must(tok.Secret, "GET", "/api/v0/identity/me", nil, 200, nil)
	// the owner can
	e.must(alice, "DELETE", "/api/v0/identity/tokens/"+tok.ID, nil, 200, nil)
	e.must(tok.Secret, "GET", "/api/v0/identity/me", nil, 401, nil)
	e.must(tok.Secret, "GET", "/api/v0/repos/acme/app", nil, 401, nil)
	var evs []ledger.Event
	e.must(adminToken, "GET", "/api/v0/events?type=io.ore.identity.token.revoked", nil, 200, &evs)
	if len(evs) != 1 || evs[0].OreActor.ID != "alice" {
		t.Fatalf("revoked events: %+v", evs)
	}
}

func TestE2E_ReadOnlyTokenCannotPush(t *testing.T) {
	e := newEnv(t)
	alice := e.seed()
	var ro Token
	e.must(alice, "POST", "/api/v0/identity/tokens", map[string]any{"name": "ro", "scopes": []string{"read"}}, 201, &ro)
	// a read-only token cannot mint a stronger one
	if code, _ := e.call(ro.Secret, "POST", "/api/v0/identity/tokens", map[string]any{"scopes": []string{"write"}}); code != 403 {
		t.Fatalf("read-only token minted a write token: %d", code)
	}
	work := filepath.Join(e.dir, "work")
	e.mustGit("", e.dir, "clone", "-q", e.httpURL("alice", ro.Secret, "acme", "app"), work) // read is fine
	e.mustGit("", work, "checkout", "-q", "-b", "ro-branch")
	e.commitFile(work, "f", "x\n", "c")
	out, err := e.git("", work, "push", "origin", "ro-branch")
	if err == nil {
		t.Fatalf("push with a read-only token succeeded: %s", out)
	}
	if _, found := e.findRefEvent("refs/heads/ro-branch", "push"); found {
		t.Fatal("ref.updated recorded for a refused push")
	}
	e.must(ro.Secret, "GET", "/api/v0/repos/acme/app/issues", nil, 200, nil)
	e.must(ro.Secret, "POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": "x"}, 401, nil)
	// the read-write token pushes the same branch
	e.mustGit("", work, "remote", "set-url", "origin", e.httpURL("alice", alice, "acme", "app"))
	e.mustGit("", work, "push", "-q", "origin", "ro-branch")
	if actor, found := e.findRefEvent("refs/heads/ro-branch", "push"); !found || actor.ID != "alice" {
		t.Fatalf("push with the read-write token not recorded: %v %+v", found, actor)
	}
}

func TestE2E_UnknownSSHKeyIsRefused(t *testing.T) {
	e := newEnv(t)
	alice := e.seed()
	e.addKey(alice, "alice")
	stranger, _ := e.keyPair("stranger")
	out, err := e.git(stranger, e.dir, "clone", "-q", e.sshURL("acme", "app"), filepath.Join(e.dir, "x"))
	if err == nil {
		t.Fatalf("clone with an unregistered key succeeded: %s", out)
	}
	if !strings.Contains(out, "Permission denied") && !strings.Contains(out, "publickey") {
		t.Fatalf("expected an authentication failure, got: %s", out)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "x")); err == nil {
		t.Fatal("clone directory populated")
	}
}

func TestE2E_SSHPushToProtectedBranchIsRefused(t *testing.T) {
	e := newEnv(t)
	alice := e.seed()
	key := e.addKey(alice, "alice")
	work := filepath.Join(e.dir, "work")
	e.mustGit(key, e.dir, "clone", "-q", e.sshURL("acme", "app"), work)
	e.commitFile(work, "README.md", "# changed\n", "edit main")
	out, err := e.git(key, work, "push", "origin", "main")
	if err == nil {
		t.Fatalf("push to the protected default branch succeeded: %s", out)
	}
	if !strings.Contains(out, "refs/heads/main is protected") || !strings.Contains(out, "pre-receive hook declined") {
		t.Fatalf("expected the policy message, got: %s", out)
	}
	if _, found := e.findRefEvent("refs/heads/main", "ssh"); found {
		t.Fatal("ref.updated recorded for the refused push")
	}
	e.mustGit(key, work, "push", "-q", "origin", "main:refs/heads/fix") // a branch is fine
	if _, found := e.findRefEvent("refs/heads/fix", "ssh"); !found {
		t.Fatal("branch push not recorded")
	}
}

func TestE2E_RemovedMemberCannotPush(t *testing.T) {
	e := newEnv(t)
	alice := e.seed()
	key := e.addKey(alice, "alice")
	work := filepath.Join(e.dir, "work")
	e.mustGit(key, e.dir, "clone", "-q", e.sshURL("acme", "app"), work)
	e.commitFile(work, "f", "1\n", "one")
	e.mustGit(key, work, "push", "-q", "origin", "main:refs/heads/b1")

	e.must(adminToken, "DELETE", "/api/v0/identity/orgs/acme/members/alice", nil, 200, nil)
	e.commitFile(work, "f", "2\n", "two")
	out, err := e.git(key, work, "push", "origin", "main:refs/heads/b2")
	if err == nil || !strings.Contains(out, "alice is not a member of acme") {
		t.Fatalf("push after removal: err=%v out=%s", err, out)
	}
	if out, err := e.git(key, e.dir, "clone", "-q", e.sshURL("acme", "app"), filepath.Join(e.dir, "x")); err == nil {
		t.Fatalf("clone after removal succeeded: %s", out)
	}
	// over HTTP the token still authenticates but the org policy refuses (401)
	e.must(alice, "GET", "/api/v0/identity/me", nil, 200, nil)
	e.must(alice, "GET", "/api/v0/repos/acme/app", nil, 401, nil)
	if out, err := e.git("", work, "push", e.httpURL("alice", alice, "acme", "app"), "main:refs/heads/b3"); err == nil {
		t.Fatalf("http push after removal succeeded: %s", out)
	}
	if _, found := e.findRefEvent("refs/heads/b2", "ssh"); found {
		t.Fatal("refused push recorded")
	}
	// re-added as a guest: read only
	e.must(adminToken, "POST", "/api/v0/identity/orgs/acme/members", map[string]string{"user": "alice", "role": "guest"}, 201, nil)
	e.mustGit(key, e.dir, "clone", "-q", e.sshURL("acme", "app"), filepath.Join(e.dir, "y"))
	out, err = e.git(key, work, "push", "origin", "main:refs/heads/b2")
	if err == nil || !strings.Contains(out, "guest") {
		t.Fatalf("guest push: err=%v out=%s", err, out)
	}
}

func TestE2E_ConcurrentSSHPushesAreBothRecorded(t *testing.T) {
	e := newEnv(t)
	alice := e.seed()
	key := e.addKey(alice, "alice")
	var wg sync.WaitGroup
	errs := make([]string, 2)
	for i := range errs {
		work := filepath.Join(e.dir, fmt.Sprintf("work%d", i))
		e.mustGit(key, e.dir, "clone", "-q", e.sshURL("acme", "app"), work)
		e.commitFile(work, fmt.Sprintf("f%d", i), "x\n", "c")
		wg.Add(1)
		go func(i int, work string) {
			defer wg.Done()
			if out, err := e.git(key, work, "push", "-q", "origin", fmt.Sprintf("main:refs/heads/branch-%d", i)); err != nil {
				errs[i] = out
			}
		}(i, work)
	}
	wg.Wait()
	for i, msg := range errs {
		if msg != "" {
			t.Fatalf("push %d failed: %s", i, msg)
		}
	}
	for i := range errs {
		ref := fmt.Sprintf("refs/heads/branch-%d", i)
		actor, found := e.findRefEvent(ref, "ssh")
		if !found || actor.ID != "alice" {
			t.Fatalf("%s not recorded via ssh: %v %+v", ref, found, actor)
		}
	}
	var refs map[string]string
	e.must(adminToken, "GET", "/api/v0/repos/acme/app/refs", nil, 200, &refs)
	if refs["refs/heads/branch-0"] == "" || refs["refs/heads/branch-1"] == "" {
		t.Fatalf("refs: %v", refs)
	}
}

func TestE2E_SSHNonGitCommandIsRefused(t *testing.T) {
	e := newEnv(t)
	alice := e.seed()
	key := e.addKey(alice, "alice")
	host, port, _ := net.SplitHostPort(e.ssh.Addr().String())
	run := func(args ...string) (string, int) {
		cmd := exec.Command("ssh", append(append(sshArgs(key), "-p", port, "git@"+host), args...)...)
		cmd.Env = append(os.Environ(), "HOME="+e.home)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("ssh: %v %s", err, out)
		}
		return string(out), code
	}
	cases := map[string]string{
		"ls -la":                               "only git-upload-pack and git-receive-pack",
		"git-upload-pack acme/app.git; id":     "only git-upload-pack and git-receive-pack",
		"git-upload-pack 'acme/app.git' extra": "only git-upload-pack and git-receive-pack",
		"git-upload-pack '../x.git'":           "expected a repository path",
		"git-upload-pack 'acme/nope.git'":      "not found",
	}
	for cmdline, want := range cases {
		out, code := run(cmdline)
		if code != 1 || !strings.Contains(out, want) {
			t.Errorf("%q: exit %d, out %q, want %q", cmdline, code, out, want)
		}
	}
	out, code := run() // no command: a shell request
	if code != 1 || !strings.Contains(out, "does not provide shell access") {
		t.Fatalf("shell: exit %d, out %q", code, out)
	}
}

func TestE2E_BadKeyUploads(t *testing.T) {
	e := newEnv(t)
	alice := e.seed()
	_, good := e.keyPair("good")
	cases := map[string]any{
		"oversized":  map[string]string{"public_key": good + strings.Repeat("A", MaxPublicKeyLen)},
		"malformed":  map[string]string{"public_key": "ssh-ed25519 not-base64 x"},
		"prose":      map[string]string{"public_key": "my key"},
		"empty":      map[string]string{"public_key": ""},
		"wrong type": map[string]string{"public_key": "ssh-rsa " + strings.Fields(good)[1]},
		"bad field":  map[string]string{"pubkey": good},
	}
	for name, body := range cases {
		if code, out := e.call(alice, "POST", "/api/v0/identity/ssh-keys", body); code != 400 {
			t.Errorf("%s: got %d %s", name, code, out)
		}
	}
	// a body far over the limit is cut off by the reader, still a 400
	huge := map[string]string{"public_key": strings.Repeat("A", 200*1024)}
	if code, _ := e.call(alice, "POST", "/api/v0/identity/ssh-keys", huge); code != 400 {
		t.Errorf("huge body: %d", code)
	}
	e.must(alice, "POST", "/api/v0/identity/ssh-keys", map[string]string{"public_key": good}, 201, nil)
	e.must(alice, "POST", "/api/v0/identity/ssh-keys", map[string]string{"public_key": good}, 409, nil)
	var keys []SSHKey
	e.must(alice, "GET", "/api/v0/identity/ssh-keys", nil, 200, &keys)
	if len(keys) != 1 {
		t.Fatalf("keys: %+v", keys)
	}
	e.must(alice, "DELETE", "/api/v0/identity/ssh-keys/"+keys[0].ID, nil, 200, nil)
	e.must(alice, "GET", "/api/v0/identity/ssh-keys", nil, 200, &keys)
	if len(keys) != 0 {
		t.Fatalf("keys after removal: %+v", keys)
	}
}

func TestE2E_AdminTokenStillWorksForEverything(t *testing.T) {
	e := newEnv(t)
	alice := e.seed()
	var me struct {
		Actor ledger.Actor
		Admin bool
		User  *User
	}
	e.must(adminToken, "GET", "/api/v0/identity/me", nil, 200, &me)
	if me.Actor != (ledger.Actor{Kind: "human", ID: "admin"}) || !me.Admin || me.User == nil || me.User.Role != RoleOwner {
		t.Fatalf("admin principal: %+v", me)
	}
	// every pre-existing API path with the bearer token
	e.must(adminToken, "GET", "/api/v0/orgs", nil, 200, nil)
	e.must(adminToken, "POST", "/api/v0/orgs", map[string]string{"name": "second"}, 201, nil)
	e.must(adminToken, "POST", "/api/v0/repos", map[string]any{"org": "second", "name": "r"}, 201, nil)
	e.must(adminToken, "POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": "t"}, 201, nil)
	e.must(adminToken, "GET", "/api/v0/events", nil, 200, nil)
	// git over HTTP with the token as the password and any user name
	work := filepath.Join(e.dir, "work")
	e.mustGit("", e.dir, "clone", "-q", e.httpURL("u", adminToken, "acme", "app"), work)
	e.commitFile(work, "f", "x\n", "c")
	e.mustGit("", work, "push", "-q", "origin", "main:refs/heads/admin-branch")
	if actor, found := e.findRefEvent("refs/heads/admin-branch", "push"); !found || actor != (ledger.Actor{Kind: "human", ID: "admin"}) {
		t.Fatalf("admin push: %v %+v", found, actor)
	}
	// the protected branch stays protected for the admin token too
	e.commitFile(work, "f", "y\n", "d")
	if out, err := e.git("", work, "push", "origin", "main"); err == nil || !strings.Contains(out, "protected") {
		t.Fatalf("admin push to main: %v %s", err, out)
	}
	// identity administration
	e.must(adminToken, "GET", "/api/v0/identity/users", nil, 200, nil)
	e.must(alice, "GET", "/api/v0/identity/users", nil, 403, nil)
	e.must(alice, "POST", "/api/v0/identity/users", map[string]any{"name": "eve"}, 403, nil)
	e.must(adminToken, "POST", "/api/v0/identity/orgs/second/members", map[string]string{"user": "alice", "role": "owner"}, 201, nil)
	e.must(adminToken, "POST", "/api/v0/identity/ssh-keys", map[string]string{"public_key": func() string { _, p := e.keyPair("x"); return p }(), "user": "alice"}, 201, nil)
	// the admin token works over git SSH policy too: the admin user can push to any org once it has a key
	key := e.addKey(adminToken, "admin")
	e.mustGit(key, work, "push", "-q", e.sshURL("acme", "app"), "main:refs/heads/admin-ssh")
	if actor, found := e.findRefEvent("refs/heads/admin-ssh", "ssh"); !found || actor.ID != "admin" {
		t.Fatalf("admin ssh push: %v %+v", found, actor)
	}
	// the UI login flow still accepts the token as a cookie
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(e.srv.URL + "/login?token=" + adminToken)
	if err != nil || resp.StatusCode != 302 || len(resp.Cookies()) == 0 {
		t.Fatalf("login: %v %v", err, resp)
	}
	resp.Body.Close()
	resp, err = noRedirect.Get(e.srv.URL + "/login?token=nope")
	if err != nil || resp.StatusCode != 401 {
		t.Fatalf("login with a bad token: %v %v", err, resp)
	}
	resp.Body.Close()
}

func TestE2E_AgentPrincipal(t *testing.T) {
	e := newEnv(t)
	alice := e.seed()
	// alice is a member, not an owner: she cannot create agents; the admin can and names her the owner
	if code, _ := e.call(alice, "POST", "/api/v0/identity/agents", map[string]any{"org": "acme", "name": "fixer"}); code != 403 {
		t.Fatalf("member created an agent: %d", code)
	}
	var agent Agent
	e.must(adminToken, "POST", "/api/v0/identity/agents", map[string]any{"org": "acme", "name": "fixer", "owner": "alice", "budgets": map[string]any{"ci_minutes": 60}}, 201, &agent)
	var budgets map[string]int
	json.Unmarshal(agent.Budgets, &budgets)
	if agent.Owner != "alice" || agent.Org != "acme" || budgets["ci_minutes"] != 60 {
		t.Fatalf("agent: %+v", agent)
	}
	// the owner mints the agent's tokens: read-only by default
	var ro Token
	e.must(alice, "POST", "/api/v0/identity/tokens", map[string]any{"agent": "acme/fixer"}, 201, &ro)
	var me struct{ Actor ledger.Actor }
	e.must(ro.Secret, "GET", "/api/v0/identity/me", nil, 200, &me)
	if me.Actor != (ledger.Actor{Kind: "agent", ID: "fixer"}) {
		t.Fatalf("agent actor: %+v", me.Actor)
	}
	e.must(ro.Secret, "GET", "/api/v0/repos/acme/app", nil, 200, nil)
	e.must(ro.Secret, "POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "agent", "files": []map[string]any{{"path": "a", "content": "1"}}}, 401, nil)
	var rw Token
	e.must(alice, "POST", "/api/v0/identity/tokens", map[string]any{"agent": "acme/fixer", "scopes": []string{"read", "write"}}, 201, &rw)
	e.must(rw.Secret, "POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "agent", "files": []map[string]any{{"path": "a", "content": "1"}}}, 201, nil)
	if actor, found := e.findRefEvent("refs/heads/agent", "api"); !found || actor != (ledger.Actor{Kind: "agent", ID: "fixer"}) {
		t.Fatalf("agent commit actor: %v %+v", found, actor)
	}
	// agents have no SSH keys and cannot administer
	if code, _ := e.call(rw.Secret, "POST", "/api/v0/identity/ssh-keys", map[string]string{"public_key": "x"}); code != 400 {
		t.Fatalf("agent key upload: %d", code)
	}
	e.must(rw.Secret, "POST", "/api/v0/identity/users", map[string]any{"name": "x"}, 403, nil)
	var agents []Agent
	e.must(alice, "GET", "/api/v0/identity/agents?org=acme", nil, 200, &agents)
	if len(agents) != 1 || agents[0].ID != agent.ID {
		t.Fatalf("agents: %+v", agents)
	}
	var evs []ledger.Event
	e.must(adminToken, "GET", "/api/v0/events?type=io.ore.identity.agent.created", nil, 200, &evs)
	if len(evs) != 1 || evs[0].OreActor.ID != "admin" {
		t.Fatalf("agent.created: %+v", evs)
	}
}
