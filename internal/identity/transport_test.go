// SPDX-License-Identifier: AGPL-3.0-only

package identity

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/workspace"
)

// The per-organization git transport switch (ADR 0016 §3), end to end on a
// real server with the identity extension, driven by real git and ssh: a
// push over HTTP before the switch; members can read but not flip the setting;
// an administrator switches it off with a reason and the event is on the
// ledger; pushes and clones are refused over HTTP and over SSH with the
// message naming the Ore client; `ore open` (the archive endpoint) and the
// commits API keep working for the same user; an owner of the organization
// switches it back on and the push goes through.
func TestE2E_GitTransportSwitch(t *testing.T) {
	e := newEnv(t)
	alice := e.seed()
	key := e.addKey(alice, "alice")
	// bob is a user outside acme
	e.must(adminToken, "POST", "/api/v0/identity/users", map[string]any{"name": "bob", "email": "bob@example.com"}, 201, nil)
	var bobTok Token
	e.must(adminToken, "POST", "/api/v0/identity/tokens", map[string]any{"name": "laptop", "user": "bob"}, 201, &bobTok)
	bob := bobTok.Secret

	// before the switch: clone over HTTP, push a branch over HTTP and another over SSH
	work := filepath.Join(e.dir, "work")
	e.mustGit("", e.dir, "clone", "-q", e.httpURL("alice", alice, "acme", "app"), work)
	e.mustGit("", work, "checkout", "-q", "-b", "before")
	e.commitFile(work, "before.txt", "x\n", "before the switch")
	e.mustGit("", work, "push", "-q", "origin", "before")
	if _, ok := e.findRefEvent("refs/heads/before", "push"); !ok {
		t.Fatal("push over HTTP before the switch not recorded")
	}
	e.mustGit(key, work, "push", "-q", e.sshURL("acme", "app"), "before:before-ssh")

	// reading the setting: members and administrators, not outsiders or unknown organizations
	var st ledger.OrgSettings
	e.must(alice, "GET", "/api/v0/orgs/acme/settings", nil, 200, &st)
	if st.Org != "acme" || st.GitTransport != ledger.GitTransportOn || st.UpdatedBy != nil {
		t.Fatalf("defaults: %+v", st)
	}
	e.must(bob, "GET", "/api/v0/orgs/acme/settings", nil, 403, nil)
	e.must(alice, "GET", "/api/v0/orgs/nope/settings", nil, 404, nil)
	e.must("", "GET", "/api/v0/orgs/acme/settings", nil, 401, nil)

	// flipping it: a member cannot, an outsider cannot, bad input is a 400, an administrator can
	e.must(alice, "PATCH", "/api/v0/orgs/acme/settings", map[string]any{"git_transport": "off"}, 403, nil)
	e.must(bob, "PATCH", "/api/v0/orgs/acme/settings", map[string]any{"git_transport": "off"}, 403, nil)
	e.must(adminToken, "PATCH", "/api/v0/orgs/acme/settings", map[string]any{"git_transport": "sometimes"}, 400, nil)
	e.must(adminToken, "PATCH", "/api/v0/orgs/acme/settings", map[string]any{"transport": "off"}, 400, nil)
	e.must(adminToken, "PATCH", "/api/v0/orgs/acme/settings", map[string]any{}, 400, nil)
	e.must(adminToken, "PATCH", "/api/v0/orgs/nope/settings", map[string]any{"git_transport": "off"}, 404, nil)
	if st, _ = e.svc.DB.OrgSettings(context.Background(), ledger.Org{Name: "acme", ID: st.OrgID}); st.GitTransport != ledger.GitTransportOn {
		t.Fatalf("a refused change was applied: %+v", st)
	}
	e.must(adminToken, "PATCH", "/api/v0/orgs/acme/settings", map[string]any{"git_transport": "off", "reason": "last day of git was Friday"}, 200, &st)
	if st.GitTransport != ledger.GitTransportOff || st.GitTransportReason != "last day of git was Friday" || st.UpdatedBy == nil || st.UpdatedBy.ID != "admin" {
		t.Fatalf("after the switch: %+v", st)
	}
	var evs []ledger.Event
	e.must(adminToken, "GET", "/api/v0/events?type=io.ore.organization.settings.changed", nil, 200, &evs)
	if len(evs) != 1 || evs[0].OreActor.ID != "admin" || evs[0].Subject != "orgs/acme/settings" {
		t.Fatalf("settings event: %+v", evs)
	}
	var data struct {
		Changes map[string]map[string]string `json:"changes"`
	}
	json.Unmarshal(evs[0].Data, &data)
	if data.Changes["git_transport"]["from"] != "on" || data.Changes["git_transport"]["to"] != "off" {
		t.Fatalf("event data: %s", evs[0].Data)
	}
	// members read the new state and the reason
	e.must(alice, "GET", "/api/v0/orgs/acme/settings", nil, 200, &st)
	if st.GitTransport != ledger.GitTransportOff || st.GitTransportReason != "last day of git was Friday" {
		t.Fatalf("member view: %+v", st)
	}

	// over HTTP: push, fetch and clone are refused with the message; nothing is recorded
	want := "the git transport is off for acme; use the Ore client: ore open acme/app (last day of git was Friday)"
	before := len(e.refEvents())
	e.commitFile(work, "after.txt", "y\n", "after the switch")
	out, err := e.git("", work, "push", "origin", "before:after-http")
	if err == nil || !strings.Contains(out, "remote error: "+want) {
		t.Fatalf("HTTP push with the transport off: %v\n%s", err, out)
	}
	if out, err := e.git("", work, "fetch", "origin"); err == nil || !strings.Contains(out, "remote error: "+want) {
		t.Fatalf("HTTP fetch with the transport off: %v\n%s", err, out)
	}
	if out, err := e.git("", e.dir, "clone", e.httpURL("alice", alice, "acme", "app"), filepath.Join(e.dir, "clone-off")); err == nil || !strings.Contains(out, "remote error: "+want) {
		t.Fatalf("HTTP clone with the transport off: %v\n%s", err, out)
	}
	// the administrator is refused too: the switch is about the transport, not the principal
	if out, err := e.git("", e.dir, "clone", e.httpURL("admin", adminToken, "acme", "app"), filepath.Join(e.dir, "clone-admin")); err == nil || !strings.Contains(out, "remote error: "+want) {
		t.Fatalf("admin HTTP clone with the transport off: %v\n%s", err, out)
	}
	// over SSH: the same message on stderr, exit status 1
	if out, err := e.git(key, work, "push", e.sshURL("acme", "app"), "before:after-ssh"); err == nil || !strings.Contains(out, "ore: "+want) {
		t.Fatalf("SSH push with the transport off: %v\n%s", err, out)
	}
	if out, err := e.git(key, e.dir, "clone", e.sshURL("acme", "app"), filepath.Join(e.dir, "clone-ssh-off")); err == nil || !strings.Contains(out, "ore: "+want) {
		t.Fatalf("SSH clone with the transport off: %v\n%s", err, out)
	}
	if n := len(e.refEvents()); n != before {
		t.Fatalf("refused pushes recorded %d ref updates", n-before)
	}

	// the Ore client and the API are untouched: `ore open` materializes the tree, the commits endpoint writes
	c := workspace.NewClient(e.srv.URL, alice)
	ws, err := workspace.Open(context.Background(), c, "acme", "app", filepath.Join(e.dir, "opened"), true, "alice", io.Discard)
	if err != nil {
		t.Fatalf("ore open with the transport off: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(ws.Root(), "README.md")); err != nil || string(b) != "# app\n" {
		t.Fatalf("opened tree: %q %v", b, err)
	}
	e.must(alice, "POST", "/api/v0/repos/acme/app/commits", map[string]any{"branch": "api-branch", "message": "through the API",
		"files": []map[string]any{{"path": "api.txt", "content": "api\n"}}}, 201, nil)
	e.must(alice, "GET", "/api/v0/repos/acme/app/refs", nil, 200, nil)
	e.must(alice, "GET", "/api/v0/repos/acme/app/log?ref=api-branch", nil, 200, nil)
	if _, ok := e.findRefEvent("refs/heads/api-branch", "api"); !ok {
		// the via of API commits is whatever the commits endpoint records; what matters is that the ref moved
		var refs map[string]string
		e.must(alice, "GET", "/api/v0/repos/acme/app/refs", nil, 200, &refs)
		if refs["refs/heads/api-branch"] == "" {
			t.Fatalf("API commit did not move the ref: %v", refs)
		}
	}

	// read-only: clone and fetch work again, pushes are still refused with the push message
	e.must(adminToken, "PATCH", "/api/v0/orgs/acme/settings", map[string]any{"git_transport": "read-only"}, 200, &st)
	if st.GitTransportReason != "" {
		t.Fatalf("the reason of the old mode survived: %+v", st)
	}
	e.mustGit("", work, "fetch", "-q", "origin")
	e.mustGit(key, e.dir, "clone", "-q", e.sshURL("acme", "app"), filepath.Join(e.dir, "clone-ssh-ro"))
	wantRO := "git pushes are off for acme; save your work with the Ore client: ore open acme/app, then ore save"
	if out, err := e.git("", work, "push", "origin", "before:after-http"); err == nil || !strings.Contains(out, "remote error: "+wantRO) {
		t.Fatalf("HTTP push in read-only mode: %v\n%s", err, out)
	}
	if out, err := e.git(key, work, "push", e.sshURL("acme", "app"), "before:after-ssh"); err == nil || !strings.Contains(out, "ore: "+wantRO) {
		t.Fatalf("SSH push in read-only mode: %v\n%s", err, out)
	}

	// an owner of the organization (not an administrator) switches it back on; the pushes go through
	e.must(adminToken, "POST", "/api/v0/identity/orgs/acme/members", map[string]string{"user": "alice", "role": "owner"}, 201, nil)
	e.must(alice, "PATCH", "/api/v0/orgs/acme/settings", map[string]any{"git_transport": "on"}, 200, &st)
	if st.GitTransport != ledger.GitTransportOn || st.UpdatedBy == nil || st.UpdatedBy.ID != "alice" {
		t.Fatalf("back on: %+v", st)
	}
	e.mustGit("", work, "push", "-q", "origin", "before:after-http")
	e.mustGit(key, work, "push", "-q", e.sshURL("acme", "app"), "before:after-ssh")
	if _, ok := e.findRefEvent("refs/heads/after-http", "push"); !ok {
		t.Fatal("HTTP push after switching back on not recorded")
	}
	if _, ok := e.findRefEvent("refs/heads/after-ssh", "ssh"); !ok {
		t.Fatal("SSH push after switching back on not recorded")
	}
	e.must(adminToken, "GET", "/api/v0/events?type=io.ore.organization.settings.changed", nil, 200, &evs)
	if len(evs) != 3 {
		t.Fatalf("expected 3 settings events, got %d", len(evs))
	}
	// an agent of the organization reads the setting but cannot change it
	e.must(adminToken, "POST", "/api/v0/identity/agents", map[string]any{"org": "acme", "name": "fixer", "owner": "alice"}, 201, nil)
	var agentTok Token
	e.must(alice, "POST", "/api/v0/identity/tokens", map[string]any{"agent": "acme/fixer", "scopes": []string{"read", "write"}}, 201, &agentTok)
	e.must(agentTok.Secret, "GET", "/api/v0/orgs/acme/settings", nil, 200, nil)
	e.must(agentTok.Secret, "PATCH", "/api/v0/orgs/acme/settings", map[string]any{"git_transport": "off"}, 403, nil)
}
