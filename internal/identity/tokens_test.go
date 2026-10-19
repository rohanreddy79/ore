// SPDX-License-Identifier: AGPL-3.0-only

package identity

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

func newService(t *testing.T) (*Service, ledger.Org) {
	t.Helper()
	ctx := context.Background()
	db, err := ledger.Open(ctx, filepath.Join(t.TempDir(), "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := New(db, "admin-secret")
	s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := s.EnsureBootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	org, err := db.CreateOrg(ctx, "acme", ledger.System)
	if err != nil {
		t.Fatal(err)
	}
	return s, org
}

func TestSecretAndHash(t *testing.T) {
	a, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewSecret()
	if !strings.HasPrefix(a, SecretPrefix) || len(a) != len(SecretPrefix)+40 {
		t.Fatalf("unexpected secret shape %q", a)
	}
	if a == b {
		t.Fatal("two secrets are equal")
	}
	if HashSecret(a) != HashSecret(a) || len(HashSecret(a)) != 64 {
		t.Fatal("hash is not a stable 64-hex sha256")
	}
	if HashSecret(a) == HashSecret(b) || HashSecret(a) == a {
		t.Fatal("hash collides or is the plaintext")
	}
	if prefixOf(a) != a[:10] || prefixOf("short") != "short" {
		t.Fatalf("prefix: %q", prefixOf(a))
	}
}

func TestParseScopes(t *testing.T) {
	got, err := ParseScopes([]string{"write", " Read ", "read"})
	if err != nil || got.String() != "read,write" {
		t.Fatalf("parse: %v %v", got, err)
	}
	if _, err := ParseScopes([]string{"repo:push"}); err == nil {
		t.Fatal("unknown scope accepted")
	}
	if _, err := ParseScopes(nil); err == nil {
		t.Fatal("empty scopes accepted")
	}
}

func TestScopesImplication(t *testing.T) {
	cases := []struct {
		have  Scopes
		scope string
		want  bool
	}{
		{Scopes{"read"}, "read", true},
		{Scopes{"read"}, "write", false},
		{Scopes{"read"}, "admin", false},
		{Scopes{"write"}, "read", true},
		{Scopes{"write"}, "write", true},
		{Scopes{"write"}, "admin", false},
		{Scopes{"admin"}, "read", true},
		{Scopes{"admin"}, "write", true},
		{Scopes{"admin"}, "admin", true},
		{Scopes{}, "read", false},
		{Scopes{"read"}, "bogus", false},
	}
	for _, c := range cases {
		if got := c.have.Has(c.scope); got != c.want {
			t.Errorf("%v.Has(%s) = %v, want %v", c.have, c.scope, got, c.want)
		}
	}
}

func TestCreateTokenDefaultsAndExpiry(t *testing.T) {
	s, org := newService(t)
	ctx := context.Background()
	alice, err := s.CreateUser(ctx, "alice", "alice@example.com", "", ledger.System)
	if err != nil || alice.Role != RoleMember {
		t.Fatalf("create user: %+v %v", alice, err)
	}
	tok, err := s.CreateToken(ctx, TokenSpec{User: &alice, Name: "laptop"}, ledger.System)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Secret == "" || tok.Scopes.String() != "read,write" || tok.UserID == nil || *tok.UserID != alice.ID || tok.Prefix != tok.Secret[:10] {
		t.Fatalf("token: %+v", tok)
	}
	stored, err := s.TokenBySecret(ctx, tok.Secret)
	if err != nil || stored.Secret != "" || stored.ID != tok.ID {
		t.Fatalf("stored token: %+v %v", stored, err)
	}
	bot, err := s.CreateAgent(ctx, org, "bot", alice, nil, ledger.System)
	if err != nil || string(bot.Budgets) != "{}" {
		t.Fatalf("agent: %+v %v", bot, err)
	}
	atok, err := s.CreateToken(ctx, TokenSpec{Agent: &bot}, ledger.System)
	if err != nil || atok.Scopes.String() != "read" || atok.OrgID != org.ID || atok.AgentID == nil {
		t.Fatalf("agent token defaults: %+v %v", atok, err)
	}
	if _, err := s.CreateToken(ctx, TokenSpec{User: &alice, Agent: &bot}, ledger.System); err == nil {
		t.Fatal("token with both a user and an agent accepted")
	}
	past := "2000-01-01T00:00:00.000Z"
	if _, err := s.CreateToken(ctx, TokenSpec{User: &alice, ExpiresAt: &past}, ledger.System); err == nil {
		t.Fatal("token expiring in the past accepted")
	}
	future := time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000Z07:00")
	ft, err := s.CreateToken(ctx, TokenSpec{User: &alice, ExpiresAt: &future}, ledger.System)
	if err != nil || ft.Expired(ledger.Now()) || !ft.Expired(time.Now().Add(2*time.Hour).UTC().Format("2006-01-02T15:04:05.000Z07:00")) {
		t.Fatalf("expiry: %+v %v", ft, err)
	}
	evs, _ := s.DB.Events(ctx, ledger.EventsOptions{Type: "io.ore.identity.token.created"})
	if len(evs) != 3 {
		t.Fatalf("expected 3 token.created events, got %d", len(evs))
	}
	for _, e := range evs {
		if strings.Contains(string(e.Data), tok.Secret) || strings.Contains(string(e.Data), HashSecret(tok.Secret)) {
			t.Fatal("event leaks the secret or its hash")
		}
	}
}

func TestRevokeTokenIsIdempotent(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	alice, _ := s.CreateUser(ctx, "alice", "", "", ledger.System)
	tok, _ := s.CreateToken(ctx, TokenSpec{User: &alice}, ledger.System)
	if err := s.RevokeToken(ctx, tok, ledger.System); err != nil {
		t.Fatal(err)
	}
	again, _ := s.TokenByID(ctx, tok.ID)
	if again.RevokedAt == nil {
		t.Fatal("not revoked")
	}
	if err := s.RevokeToken(ctx, again, ledger.System); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeToken(ctx, tok, ledger.System); err != nil { // stale copy without RevokedAt
		t.Fatal(err)
	}
	evs, _ := s.DB.Events(ctx, ledger.EventsOptions{Type: "io.ore.identity.token.revoked"})
	if len(evs) != 1 {
		t.Fatalf("expected exactly one revoked event, got %d", len(evs))
	}
}
