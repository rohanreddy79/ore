// SPDX-License-Identifier: AGPL-3.0-only

package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/orevcs/ore/internal/ledger"
)

type fixture struct {
	s                   *Service
	org, other          ledger.Org
	alice, bob, carol   User
	bot                 Agent
	aliceTok, readTok   Token
	bobTok, carolTok    Token
	botTok, botWriteTok Token
	ownerTok            Token
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s, org := newService(t)
	ctx := context.Background()
	other, _ := s.DB.CreateOrg(ctx, "other", ledger.System)
	f := &fixture{s: s, org: org, other: other}
	mk := func(name, role, orgRole string) User {
		u, err := s.CreateUser(ctx, name, "", role, ledger.System)
		if err != nil {
			t.Fatal(err)
		}
		if orgRole != "" {
			if _, err := s.AddMembership(ctx, org, u, orgRole, ledger.System); err != nil {
				t.Fatal(err)
			}
		}
		return u
	}
	f.alice = mk("alice", RoleMember, RoleMember)
	f.bob = mk("bob", RoleMember, RoleGuest)
	f.carol = mk("carol", RoleMember, "")
	owner := mk("site-owner", RoleOwner, "")
	tok := func(spec TokenSpec) Token {
		tk, err := s.CreateToken(ctx, spec, ledger.System)
		if err != nil {
			t.Fatal(err)
		}
		return tk
	}
	f.aliceTok = tok(TokenSpec{User: &f.alice})
	f.readTok = tok(TokenSpec{User: &f.alice, Scopes: Scopes{ScopeRead}})
	f.bobTok = tok(TokenSpec{User: &f.bob})
	f.carolTok = tok(TokenSpec{User: &f.carol})
	f.ownerTok = tok(TokenSpec{User: &owner, Scopes: Scopes{ScopeAdmin}})
	bot, err := s.CreateAgent(ctx, org, "bot", f.alice, nil, ledger.System)
	if err != nil {
		t.Fatal(err)
	}
	f.bot = bot
	f.botTok = tok(TokenSpec{Agent: &bot})
	f.botWriteTok = tok(TokenSpec{Agent: &bot, Scopes: Scopes{ScopeWrite}})
	return f
}

func req(method, path string, mod func(r *http.Request)) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	if mod != nil {
		mod(r)
	}
	return r
}

func bearer(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

func TestResolveAdminTokenMapsToBootstrapOwner(t *testing.T) {
	f := newFixture(t)
	carriers := map[string]func(*http.Request){
		"bearer": bearer("admin-secret"),
		"basic":  func(r *http.Request) { r.SetBasicAuth("whoever", "admin-secret") },
		"cookie": func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "ore_token", Value: "admin-secret"}) },
		"query":  func(r *http.Request) { r.URL.RawQuery = "token=admin-secret" },
	}
	for name, mod := range carriers {
		p, err := f.s.Resolve(req("GET", "/api/v0/orgs", mod))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if p.Actor != (ledger.Actor{Kind: "human", ID: "admin"}) || !p.Admin || !p.CanAdmin() || p.Token != nil || p.User == nil || p.User.Role != RoleOwner {
			t.Fatalf("%s: principal %+v", name, p)
		}
	}
}

func TestResolveUserAndAgentTokens(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p, err := f.s.Resolve(req("GET", "/", bearer(f.aliceTok.Secret)))
	if err != nil || p.Actor != (ledger.Actor{Kind: "human", ID: "alice"}) || p.Admin || p.CanAdmin() || p.Token == nil || p.Token.ID != f.aliceTok.ID {
		t.Fatalf("alice: %+v %v", p, err)
	}
	// basic auth: the password is the token, the user name is informational
	p, err = f.s.Resolve(req("GET", "/", func(r *http.Request) { r.SetBasicAuth("git", f.aliceTok.Secret) }))
	if err != nil || p.Actor.ID != "alice" {
		t.Fatalf("basic: %+v %v", p, err)
	}
	stored, _ := f.s.TokenByID(ctx, f.aliceTok.ID)
	if stored.LastUsedAt == nil {
		t.Fatal("last_used_at not recorded")
	}
	p, err = f.s.Resolve(req("GET", "/", bearer(f.botTok.Secret)))
	if err != nil || p.Actor != (ledger.Actor{Kind: "agent", ID: "bot"}) || p.Agent == nil || p.User != nil || p.Admin {
		t.Fatalf("bot: %+v %v", p, err)
	}
	if !p.HasScope(ScopeRead) || p.HasScope(ScopeWrite) {
		t.Fatal("agent default token should be read-only")
	}
	p, err = f.s.Resolve(req("GET", "/", bearer(f.ownerTok.Secret)))
	if err != nil || !p.Admin || !p.CanAdmin() {
		t.Fatalf("site owner with admin scope: %+v %v", p, err)
	}
}

func TestResolveRefusals(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	expired, _ := f.s.CreateToken(ctx, TokenSpec{User: &f.alice, ExpiresAt: ptr("2999-01-01T00:00:00.000Z")}, ledger.System)
	revoked, _ := f.s.CreateToken(ctx, TokenSpec{User: &f.alice}, ledger.System)
	f.s.RevokeToken(ctx, revoked, ledger.System)
	disabled, _ := f.s.CreateUser(ctx, "dave", "", "", ledger.System)
	disabledTok, _ := f.s.CreateToken(ctx, TokenSpec{User: &disabled}, ledger.System)
	f.s.DB.Exec(ctx, `UPDATE users SET status = 'disabled' WHERE id = ?`, disabled.ID)
	off, _ := f.s.CreateAgent(ctx, f.org, "off", f.alice, nil, ledger.System)
	offTok, _ := f.s.CreateToken(ctx, TokenSpec{Agent: &off}, ledger.System)
	f.s.DB.Exec(ctx, `UPDATE agents SET status = 'disabled' WHERE id = ?`, off.ID)

	cases := map[string]struct {
		mod  func(*http.Request)
		want error
	}{
		"none":           {nil, ErrNoCredential},
		"empty bearer":   {bearer(""), ErrNoCredential},
		"unknown":        {bearer("ore_0000"), ErrInvalidCredential},
		"hash as secret": {bearer(HashSecret(f.aliceTok.Secret)), ErrInvalidCredential},
		"revoked":        {bearer(revoked.Secret), ErrInvalidCredential},
		"disabled user":  {bearer(disabledTok.Secret), ErrInvalidCredential},
		"disabled agent": {bearer(offTok.Secret), ErrInvalidCredential},
		"admin off":      {bearer("admin-secret"), ErrInvalidCredential},
	}
	for name, c := range cases {
		s := f.s
		if name == "admin off" {
			cp := *f.s
			cp.AdminToken = ""
			s = &cp
		}
		if _, err := s.Resolve(req("GET", "/", c.mod)); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", name, err, c.want)
		}
	}
	// expiry is evaluated against the service clock
	f.s.Now = func() string { return "3000-01-01T00:00:00.000Z" }
	if _, err := f.s.Resolve(req("GET", "/", bearer(expired.Secret))); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("expired: %v", err)
	}
	f.s.Now = nil
	if _, err := f.s.Resolve(req("GET", "/", bearer(expired.Secret))); err != nil {
		t.Fatalf("not yet expired: %v", err)
	}
}

func ptr(s string) *string { return &s }

func TestResolutionOrderHasNoFallback(t *testing.T) {
	f := newFixture(t)
	// Authorization beats the cookie: a bad bearer is not rescued by a good cookie.
	r := req("GET", "/", bearer("bogus"))
	r.AddCookie(&http.Cookie{Name: "ore_token", Value: f.aliceTok.Secret})
	if _, err := f.s.Resolve(r); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("bearer should win: %v", err)
	}
	// The cookie beats the query parameter.
	r = req("GET", "/?token="+f.aliceTok.Secret, nil)
	r.AddCookie(&http.Cookie{Name: "ore_token", Value: "bogus"})
	if _, err := f.s.Resolve(r); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("cookie should win: %v", err)
	}
	// Each carrier works alone.
	if p, err := f.s.Resolve(req("GET", "/?token="+f.aliceTok.Secret, nil)); err != nil || p.Actor.ID != "alice" {
		t.Fatalf("query: %v", err)
	}
	r = req("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "ore_token", Value: f.aliceTok.Secret})
	if p, err := f.s.Resolve(r); err != nil || p.Actor.ID != "alice" {
		t.Fatalf("cookie: %v", err)
	}
}

func TestCheckPolicy(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	principal := func(tok Token) *Principal {
		p, err := f.s.ResolveSecret(ctx, tok.Secret)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	admin, _ := f.s.ResolveSecret(ctx, "admin-secret")
	cases := []struct {
		name string
		p    *Principal
		org  string
		op   Op
		ok   bool
	}{
		{"member writes", principal(f.aliceTok), "acme", OpWrite, true},
		{"member reads", principal(f.aliceTok), "acme", OpRead, true},
		{"member elsewhere", principal(f.aliceTok), "other", OpRead, false},
		{"read-only token reads", principal(f.readTok), "acme", OpRead, true},
		{"read-only token writes", principal(f.readTok), "acme", OpWrite, false},
		{"read-only token site write", principal(f.readTok), "", OpWrite, false},
		{"guest reads", principal(f.bobTok), "acme", OpRead, true},
		{"guest writes", principal(f.bobTok), "acme", OpWrite, false},
		{"non-member reads", principal(f.carolTok), "acme", OpRead, false},
		{"non-member site read", principal(f.carolTok), "", OpRead, true},
		{"agent reads own org", principal(f.botTok), "acme", OpRead, true},
		{"agent read token writes", principal(f.botTok), "acme", OpWrite, false},
		{"agent write token writes", principal(f.botWriteTok), "acme", OpWrite, true},
		{"agent other org", principal(f.botWriteTok), "other", OpRead, false},
		{"admin anywhere", admin, "other", OpWrite, true},
		{"site owner anywhere", principal(f.ownerTok), "other", OpWrite, true},
		{"unknown org passes to the route", principal(f.carolTok), "nope", OpWrite, true},
		{"ssh key principal", f.s.PrincipalForUser(f.alice), "acme", OpWrite, true},
		{"ssh key guest", f.s.PrincipalForUser(f.bob), "acme", OpWrite, false},
	}
	for _, c := range cases {
		err := f.s.CheckPolicy(ctx, c.p, c.org, c.op)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v want ok=%v", c.name, err, c.ok)
		}
		var pe *PolicyError
		if err != nil && !errors.As(err, &pe) {
			t.Errorf("%s: refusal is not a PolicyError: %v", c.name, err)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		method, path string
		op           Op
		org          string
	}{
		{"GET", "/acme/app.git/info/refs?service=git-upload-pack", OpRead, "acme"},
		{"GET", "/acme/app.git/info/refs?service=git-receive-pack", OpWrite, "acme"},
		{"POST", "/acme/app.git/git-upload-pack", OpRead, "acme"},
		{"POST", "/acme/app.git/git-receive-pack", OpWrite, "acme"},
		{"GET", "/api/v0/repos/acme/app", OpRead, "acme"},
		{"POST", "/api/v0/repos/acme/app/commits", OpWrite, "acme"},
		{"GET", "/api/v0/repos", OpRead, ""},
		{"POST", "/api/v0/orgs", OpWrite, ""},
		{"DELETE", "/api/v0/identity/tokens/x", OpWrite, ""},
		{"GET", "/", OpRead, ""},
	}
	for _, c := range cases {
		op, org := Classify(httptest.NewRequest(c.method, c.path, nil))
		if op != c.op || org != c.org {
			t.Errorf("%s %s: got %s %q, want %s %q", c.method, c.path, op, org, c.op, c.org)
		}
	}
}

func TestAuthenticateHook(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name         string
		tok          string
		method, path string
		want         *ledger.Actor
	}{
		{"admin api", "admin-secret", "POST", "/api/v0/orgs", &ledger.Actor{Kind: "human", ID: "admin"}},
		{"admin push", "admin-secret", "POST", "/acme/app.git/git-receive-pack", &ledger.Actor{Kind: "human", ID: "admin"}},
		{"member push", f.aliceTok.Secret, "POST", "/acme/app.git/git-receive-pack", &ledger.Actor{Kind: "human", ID: "alice"}},
		{"read-only token clone", f.readTok.Secret, "GET", "/acme/app.git/info/refs?service=git-upload-pack", &ledger.Actor{Kind: "human", ID: "alice"}},
		{"read-only token push advertisement", f.readTok.Secret, "GET", "/acme/app.git/info/refs?service=git-receive-pack", nil},
		{"read-only token push", f.readTok.Secret, "POST", "/acme/app.git/git-receive-pack", nil},
		{"read-only token api write", f.readTok.Secret, "POST", "/api/v0/repos/acme/app/issues", nil},
		{"guest clone", f.bobTok.Secret, "GET", "/acme/app.git/info/refs?service=git-upload-pack", &ledger.Actor{Kind: "human", ID: "bob"}},
		{"guest push", f.bobTok.Secret, "POST", "/acme/app.git/git-receive-pack", nil},
		{"non-member repo api", f.carolTok.Secret, "GET", "/api/v0/repos/acme/app", nil},
		{"non-member site api", f.carolTok.Secret, "GET", "/api/v0/orgs", &ledger.Actor{Kind: "human", ID: "carol"}},
		{"agent read", f.botTok.Secret, "GET", "/api/v0/repos/acme/app", &ledger.Actor{Kind: "agent", ID: "bot"}},
		{"agent write with write token", f.botWriteTok.Secret, "POST", "/api/v0/repos/acme/app/commits", &ledger.Actor{Kind: "agent", ID: "bot"}},
		{"agent other org", f.botWriteTok.Secret, "GET", "/api/v0/repos/other/app", nil},
		{"garbage", "nope", "GET", "/api/v0/orgs", nil},
		{"no credential", "", "GET", "/api/v0/orgs", nil},
	}
	for _, c := range cases {
		var mod func(*http.Request)
		if c.tok != "" {
			mod = bearer(c.tok)
		}
		got := f.s.Authenticate(req(c.method, c.path, mod))
		switch {
		case got == nil && c.want == nil:
		case got == nil || c.want == nil || *got != *c.want:
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestMembershipsAndRemoval(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ms, err := f.s.Memberships(ctx, f.alice.ID)
	if err != nil || len(ms) != 1 || ms[0].Org != "acme" || ms[0].Role != RoleMember {
		t.Fatalf("memberships: %+v %v", ms, err)
	}
	if _, err := f.s.AddMembership(ctx, f.org, f.alice, RoleOwner, ledger.System); err != nil {
		t.Fatal(err)
	}
	if role, ok, _ := f.s.MembershipRole(ctx, f.org.ID, f.alice.ID); !ok || role != RoleOwner {
		t.Fatalf("role after change: %s %v", role, ok)
	}
	if err := f.s.RemoveMembership(ctx, f.org, f.alice, ledger.System); err != nil {
		t.Fatal(err)
	}
	if err := f.s.RemoveMembership(ctx, f.org, f.alice, ledger.System); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("second removal: %v", err)
	}
	if err := f.s.CheckPolicy(ctx, f.s.PrincipalForUser(f.alice), "acme", OpRead); err == nil {
		t.Fatal("removed member still allowed")
	}
	members, _ := f.s.OrgMembers(ctx, f.org.ID)
	if len(members) != 1 || members[0].User != "bob" {
		t.Fatalf("org members: %+v", members)
	}
	for _, typ := range []string{"io.ore.identity.membership.added", "io.ore.identity.membership.changed", "io.ore.identity.membership.removed"} {
		evs, _ := f.s.DB.Events(ctx, ledger.EventsOptions{Type: typ, OrgID: f.org.ID})
		if len(evs) == 0 {
			t.Errorf("no %s event", typ)
		}
	}
}
