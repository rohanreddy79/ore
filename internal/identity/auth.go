// SPDX-License-Identifier: AGPL-3.0-only

package identity

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/orevcs/ore/internal/ledger"
)

// Principal is a resolved credential: who is acting and with what authority.
type Principal struct {
	Actor ledger.Actor
	User  *User  // set for humans (tokens, basic auth, SSH keys, the admin token)
	Agent *Agent // set for agent tokens
	Token *Token // nil for the admin token and for SSH keys
	// Admin is true for the admin token and for site owners: org policy does
	// not apply to them. Administrative routes additionally need CanAdmin.
	Admin bool
}

// HasScope reports whether the credential grants scope. Credentials that are
// not tokens (the admin token, SSH keys) carry every scope.
func (p *Principal) HasScope(scope string) bool {
	if p.Token == nil {
		return true
	}
	return p.Token.Scopes.Has(scope)
}

// CanAdmin reports whether the principal may administer the instance: a site
// owner whose credential carries the admin scope, or the admin token.
func (p *Principal) CanAdmin() bool { return p.Admin && p.HasScope(ScopeAdmin) }

// Resolution errors.
var (
	ErrNoCredential      = errors.New("no credential")
	ErrInvalidCredential = errors.New("invalid, expired or revoked credential")
)

// PolicyError explains a refusal; its text is safe to show to the caller.
type PolicyError struct{ Reason string }

func (e *PolicyError) Error() string { return e.Reason }

// Op is what a request wants to do.
type Op string

// Ops.
const (
	OpRead  Op = "read"
	OpWrite Op = "write"
)

// Resolve extracts the credential a request carries and resolves it. The
// first credential found wins, in this order: Authorization: Bearer, basic
// auth (the password is the token; the user name is informational), the
// ore_token cookie, the token query parameter. There is no fallback from a
// bad credential to the next one.
func (s *Service) Resolve(r *http.Request) (*Principal, error) {
	secret := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		secret = strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	} else if _, pw, ok := r.BasicAuth(); ok {
		secret = pw
	} else if c, err := r.Cookie("ore_token"); err == nil {
		secret = c.Value
	} else if q := r.URL.Query().Get("token"); q != "" {
		secret = q
	}
	if secret == "" {
		return nil, ErrNoCredential
	}
	return s.ResolveSecret(r.Context(), secret)
}

// ResolveSecret resolves a bearer secret: the admin token maps to the
// bootstrap owner; otherwise the SHA-256 of the secret is looked up among
// user and agent tokens, which must be unexpired and unrevoked and belong to
// an active principal.
func (s *Service) ResolveSecret(ctx context.Context, secret string) (*Principal, error) {
	if s.AdminToken != "" && subtle.ConstantTimeCompare([]byte(secret), []byte(s.AdminToken)) == 1 {
		p := &Principal{Actor: ledger.Actor{Kind: "human", ID: s.bootstrapName()}, Admin: true}
		if u, err := s.UserByName(ctx, s.bootstrapName()); err == nil {
			p.User = &u
			p.Actor.ID = u.Name
		}
		return p, nil
	}
	tok, err := s.TokenBySecret(ctx, secret)
	if err != nil {
		return nil, ErrInvalidCredential
	}
	if tok.RevokedAt != nil || tok.Expired(s.now()) {
		return nil, ErrInvalidCredential
	}
	p := &Principal{Token: &tok}
	switch {
	case tok.UserID != nil:
		u, err := s.UserByID(ctx, *tok.UserID)
		if err != nil || u.Status != StatusActive {
			return nil, ErrInvalidCredential
		}
		p.User, p.Actor, p.Admin = &u, ledger.Actor{Kind: "human", ID: u.Name}, u.Role == RoleOwner
	case tok.AgentID != nil:
		a, err := s.AgentByID(ctx, *tok.AgentID)
		if err != nil || a.Status != StatusActive {
			return nil, ErrInvalidCredential
		}
		p.Agent, p.Actor = &a, ledger.Actor{Kind: "agent", ID: a.Name}
	default:
		return nil, ErrInvalidCredential
	}
	s.touchToken(ctx, tok)
	return p, nil
}

// PrincipalForUser builds the principal of a user authenticated without a
// token (an SSH key): every scope, admin when the user is a site owner.
func (s *Service) PrincipalForUser(u User) *Principal {
	return &Principal{Actor: ledger.Actor{Kind: "human", ID: u.Name}, User: &u, Admin: u.Role == RoleOwner}
}

// CheckPolicy decides whether p may perform op in org (org "" means a
// site-level route: only the scope is checked). The rules, in order: the
// credential must carry the scope; the admin token and site owners pass;
// users must be members of the org and guests may only read; agents must
// belong to the org.
func (s *Service) CheckPolicy(ctx context.Context, p *Principal, orgName string, op Op) error {
	need := ScopeRead
	if op == OpWrite {
		need = ScopeWrite
	}
	if !p.HasScope(need) {
		return &PolicyError{fmt.Sprintf("token %s lacks the %s scope", p.Token.Prefix, need)}
	}
	if p.Admin || orgName == "" {
		return nil
	}
	org, err := s.DB.OrgByName(ctx, orgName)
	if errors.Is(err, ledger.ErrNotFound) {
		return nil // let the route answer 404
	}
	if err != nil {
		return err
	}
	switch {
	case p.User != nil:
		role, member, err := s.MembershipRole(ctx, org.ID, p.User.ID)
		if err != nil {
			return err
		}
		if !member {
			return &PolicyError{fmt.Sprintf("%s is not a member of %s", p.User.Name, orgName)}
		}
		if op == OpWrite && role == RoleGuest {
			return &PolicyError{fmt.Sprintf("%s is a guest in %s and cannot write", p.User.Name, orgName)}
		}
	case p.Agent != nil:
		if p.Agent.OrgID != org.ID {
			return &PolicyError{fmt.Sprintf("agent %s does not belong to %s", p.Agent.Name, orgName)}
		}
	default:
		return &PolicyError{"unknown principal"}
	}
	return nil
}

// Classify derives what a request wants (read or write) and which org it
// targets: git smart-HTTP paths (/{org}/{repo}.git/...) are writes for
// receive-pack and reads otherwise; API paths under /api/v0/repos/{org}/ name
// the org; every non-GET request is a write.
func Classify(r *http.Request) (Op, string) {
	path := r.URL.Path
	if strings.Contains(path, ".git/") {
		parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 3)
		op := OpRead
		if strings.HasSuffix(path, "/git-receive-pack") || (strings.HasSuffix(path, "/info/refs") && r.URL.Query().Get("service") == "git-receive-pack") {
			op = OpWrite
		}
		return op, parts[0]
	}
	op := OpRead
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		op = OpWrite
	}
	org := ""
	if rest, ok := strings.CutPrefix(path, "/api/v0/repos/"); ok {
		org, _, _ = strings.Cut(rest, "/")
	}
	return op, org
}

// Authenticate is the web.Server.Authenticate hook: it resolves the request's
// credential and applies CheckPolicy to what the request wants; nil means the
// request gets a 401 from the server (the hook cannot distinguish 403).
func (s *Service) Authenticate(r *http.Request) *ledger.Actor {
	p, err := s.Resolve(r)
	if err != nil {
		return nil
	}
	op, org := Classify(r)
	if err := s.CheckPolicy(r.Context(), p, org, op); err != nil {
		if s.Log != nil {
			s.Log.Info("identity: refused", "actor", p.Actor.Kind+"/"+p.Actor.ID, "method", r.Method, "path", r.URL.Path, "reason", err.Error())
		}
		return nil
	}
	a := p.Actor
	return &a
}

// Authorize applies the org policy to an actor that authenticated over SSH
// (so without a token): the same rules as HTTP. It implements sshd.Auth.
func (s *Service) Authorize(ctx context.Context, actor ledger.Actor, repo ledger.Repo, write bool) error {
	var p *Principal
	switch actor.Kind {
	case "human":
		u, err := s.UserByName(ctx, actor.ID)
		if err != nil || u.Status != StatusActive {
			return &PolicyError{fmt.Sprintf("user %s is unknown or disabled", actor.ID)}
		}
		p = s.PrincipalForUser(u)
	case "agent":
		a, err := s.AgentByName(ctx, repo.OrgID, actor.ID)
		if err != nil || a.Status != StatusActive {
			return &PolicyError{fmt.Sprintf("agent %s is unknown in %s or disabled", actor.ID, repo.Org)}
		}
		p = &Principal{Actor: actor, Agent: &a}
	default:
		return &PolicyError{"unknown principal kind " + actor.Kind}
	}
	op := OpRead
	if write {
		op = OpWrite
	}
	return s.CheckPolicy(ctx, p, repo.Org, op)
}
