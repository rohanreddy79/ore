// SPDX-License-Identifier: AGPL-3.0-only

package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/web"
)

// Routes mounts the identity API under /api/v0/identity/.
func (s *Service) Routes(mux *http.ServeMux) {
	h := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, p *Principal) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			p, err := s.Resolve(r)
			if err != nil {
				web.JSONError(w, http.StatusUnauthorized, errors.New("missing or invalid token"))
				return
			}
			op, _ := Classify(r)
			if err := s.CheckPolicy(r.Context(), p, "", op); err != nil {
				web.JSONError(w, http.StatusForbidden, err)
				return
			}
			if err := fn(w, r, p); err != nil {
				web.JSONError(w, statusOf(err), err)
			}
		})
	}
	h("GET /api/v0/identity/me", s.me)
	h("GET /api/v0/identity/users", s.listUsers)
	h("POST /api/v0/identity/users", s.createUser)
	h("GET /api/v0/identity/users/{name}", s.getUser)
	h("GET /api/v0/identity/memberships", s.listMemberships)
	h("GET /api/v0/identity/orgs/{org}/members", s.listOrgMembers)
	h("POST /api/v0/identity/orgs/{org}/members", s.addMember)
	h("DELETE /api/v0/identity/orgs/{org}/members/{user}", s.removeMember)
	h("GET /api/v0/identity/tokens", s.listTokens)
	h("POST /api/v0/identity/tokens", s.createToken)
	h("DELETE /api/v0/identity/tokens/{id}", s.revokeToken)
	h("GET /api/v0/identity/ssh-keys", s.listKeys)
	h("POST /api/v0/identity/ssh-keys", s.addKey)
	h("DELETE /api/v0/identity/ssh-keys/{id}", s.removeKey)
	h("GET /api/v0/identity/agents", s.listAgents)
	h("POST /api/v0/identity/agents", s.createAgent)
	// organization settings (orgsettings.go): the git transport switch of ADR 0016 §3
	h("GET /api/v0/orgs/{org}/settings", s.getOrgSettings)
	h("PATCH /api/v0/orgs/{org}/settings", s.patchOrgSettings)
}

func statusOf(err error) int {
	var pe *PolicyError
	switch {
	case errors.Is(err, ledger.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, ErrForbidden), errors.As(err, &pe):
		return http.StatusForbidden
	case errors.Is(err, ErrConflict):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

func forbidden(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrForbidden, fmt.Sprintf(format, a...))
}

func badRequest(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// isOrgOwner reports whether p's user owns the org (site admins qualify).
func (s *Service) isOrgOwner(r *http.Request, p *Principal, org ledger.Org) (bool, error) {
	if p.CanAdmin() {
		return true, nil
	}
	if p.User == nil {
		return false, nil
	}
	role, member, err := s.MembershipRole(r.Context(), org.ID, p.User.ID)
	return member && role == RoleOwner, err
}

func (s *Service) me(w http.ResponseWriter, r *http.Request, p *Principal) error {
	return web.JSON(w, http.StatusOK, map[string]any{"actor": p.Actor, "admin": p.CanAdmin(), "user": p.User, "agent": p.Agent, "token": p.Token})
}

// ---- users ----

func (s *Service) listUsers(w http.ResponseWriter, r *http.Request, p *Principal) error {
	if !p.CanAdmin() {
		return forbidden("listing users requires an administrator")
	}
	users, err := s.Users(r.Context())
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, users)
}

func (s *Service) createUser(w http.ResponseWriter, r *http.Request, p *Principal) error {
	if !p.CanAdmin() {
		return forbidden("creating users requires an administrator")
	}
	var in struct {
		Name        string `json:"name"`
		Email       string `json:"email"`
		Role        string `json:"role"`
		Memberships []struct {
			Org  string `json:"org"`
			Role string `json:"role"`
		} `json:"memberships"`
	}
	if err := web.Decode(r, &in); err != nil {
		return badRequest("%v", err)
	}
	u, err := s.CreateUser(r.Context(), in.Name, in.Email, in.Role, p.Actor)
	if err != nil {
		return err
	}
	var ms []Membership
	for _, m := range in.Memberships {
		org, err := s.DB.OrgByName(r.Context(), m.Org)
		if err != nil {
			return fmt.Errorf("org %s: %w", m.Org, err)
		}
		mem, err := s.AddMembership(r.Context(), org, u, m.Role, p.Actor)
		if err != nil {
			return err
		}
		ms = append(ms, mem)
	}
	if ms == nil {
		ms = []Membership{}
	}
	return web.JSON(w, http.StatusCreated, map[string]any{"user": u, "memberships": ms})
}

func (s *Service) getUser(w http.ResponseWriter, r *http.Request, p *Principal) error {
	name := r.PathValue("name")
	if !p.CanAdmin() && (p.User == nil || p.User.Name != name) {
		return forbidden("only administrators may look at other users")
	}
	u, err := s.UserByName(r.Context(), name)
	if err != nil {
		return err
	}
	ms, err := s.Memberships(r.Context(), u.ID)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"user": u, "memberships": ms})
}

// ---- memberships ----

// targetUser resolves the user a request acts on: the caller's own user, or
// another one named in `user` when the caller is an administrator.
func (s *Service) targetUser(r *http.Request, p *Principal, name string) (User, error) {
	if name == "" || (p.User != nil && p.User.Name == name) {
		if p.User == nil {
			return User{}, badRequest("this credential is not a user; name one with \"user\"")
		}
		return *p.User, nil
	}
	if !p.CanAdmin() {
		return User{}, forbidden("acting on another user requires an administrator")
	}
	return s.UserByName(r.Context(), name)
}

func (s *Service) listMemberships(w http.ResponseWriter, r *http.Request, p *Principal) error {
	u, err := s.targetUser(r, p, r.URL.Query().Get("user"))
	if err != nil {
		return err
	}
	ms, err := s.Memberships(r.Context(), u.ID)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, ms)
}

func (s *Service) listOrgMembers(w http.ResponseWriter, r *http.Request, p *Principal) error {
	org, err := s.DB.OrgByName(r.Context(), r.PathValue("org"))
	if err != nil {
		return err
	}
	if err := s.CheckPolicy(r.Context(), p, org.Name, OpRead); err != nil {
		return err
	}
	ms, err := s.OrgMembers(r.Context(), org.ID)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, ms)
}

func (s *Service) addMember(w http.ResponseWriter, r *http.Request, p *Principal) error {
	org, err := s.DB.OrgByName(r.Context(), r.PathValue("org"))
	if err != nil {
		return err
	}
	if ok, err := s.isOrgOwner(r, p, org); err != nil || !ok {
		if err != nil {
			return err
		}
		return forbidden("adding members requires an owner of %s", org.Name)
	}
	var in struct {
		User string `json:"user"`
		Role string `json:"role"`
	}
	if err := web.Decode(r, &in); err != nil || in.User == "" {
		return badRequest("user is required")
	}
	u, err := s.UserByName(r.Context(), in.User)
	if err != nil {
		return err
	}
	m, err := s.AddMembership(r.Context(), org, u, in.Role, p.Actor)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusCreated, m)
}

func (s *Service) removeMember(w http.ResponseWriter, r *http.Request, p *Principal) error {
	org, err := s.DB.OrgByName(r.Context(), r.PathValue("org"))
	if err != nil {
		return err
	}
	if ok, err := s.isOrgOwner(r, p, org); err != nil || !ok {
		if err != nil {
			return err
		}
		return forbidden("removing members requires an owner of %s", org.Name)
	}
	u, err := s.UserByName(r.Context(), r.PathValue("user"))
	if err != nil {
		return err
	}
	if err := s.RemoveMembership(r.Context(), org, u, p.Actor); err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"removed": true, "org": org.Name, "user": u.Name})
}

// ---- tokens ----

func (s *Service) agentRef(r *http.Request, ref string) (Agent, ledger.Org, error) {
	orgName, name, ok := strings.Cut(ref, "/")
	if !ok {
		return Agent{}, ledger.Org{}, badRequest("agent must be org/name")
	}
	org, err := s.DB.OrgByName(r.Context(), orgName)
	if err != nil {
		return Agent{}, ledger.Org{}, fmt.Errorf("org %s: %w", orgName, err)
	}
	a, err := s.AgentByName(r.Context(), org.ID, name)
	if err != nil {
		return Agent{}, ledger.Org{}, fmt.Errorf("agent %s: %w", ref, err)
	}
	return a, org, nil
}

// mayManageAgent: administrators, the agent's owner, and owners of its org.
func (s *Service) mayManageAgent(r *http.Request, p *Principal, a Agent, org ledger.Org) (bool, error) {
	if p.User != nil && p.User.ID == a.OwnerID {
		return true, nil
	}
	return s.isOrgOwner(r, p, org)
}

func (s *Service) listTokens(w http.ResponseWriter, r *http.Request, p *Principal) error {
	q := r.URL.Query()
	if ref := q.Get("agent"); ref != "" {
		a, org, err := s.agentRef(r, ref)
		if err != nil {
			return err
		}
		if ok, err := s.mayManageAgent(r, p, a, org); err != nil || !ok {
			if err != nil {
				return err
			}
			return forbidden("listing %s's tokens requires its owner", ref)
		}
		toks, err := s.Tokens(r.Context(), "", a.ID)
		if err != nil {
			return err
		}
		return web.JSON(w, http.StatusOK, toks)
	}
	if p.Agent != nil && q.Get("user") == "" {
		toks, err := s.Tokens(r.Context(), "", p.Agent.ID)
		if err != nil {
			return err
		}
		return web.JSON(w, http.StatusOK, toks)
	}
	u, err := s.targetUser(r, p, q.Get("user"))
	if err != nil {
		return err
	}
	toks, err := s.Tokens(r.Context(), u.ID, "")
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, toks)
}

func (s *Service) createToken(w http.ResponseWriter, r *http.Request, p *Principal) error {
	var in struct {
		Name      string   `json:"name"`
		Scopes    []string `json:"scopes"`
		ExpiresIn string   `json:"expires_in"`
		ExpiresAt string   `json:"expires_at"`
		User      string   `json:"user"`
		Agent     string   `json:"agent"`
	}
	if err := web.Decode(r, &in); err != nil {
		return badRequest("%v", err)
	}
	spec := TokenSpec{Name: in.Name}
	switch {
	case in.Agent != "":
		a, org, err := s.agentRef(r, in.Agent)
		if err != nil {
			return err
		}
		if ok, err := s.mayManageAgent(r, p, a, org); err != nil || !ok {
			if err != nil {
				return err
			}
			return forbidden("minting a token for %s requires its owner", in.Agent)
		}
		spec.Agent = &a
	default:
		u, err := s.targetUser(r, p, in.User)
		if err != nil {
			return err
		}
		spec.User = &u
	}
	if len(in.Scopes) > 0 {
		scopes, err := ParseScopes(in.Scopes)
		if err != nil {
			return err
		}
		for _, sc := range scopes { // a token never mints a stronger token
			if !p.HasScope(sc) {
				return forbidden("this credential cannot grant the %s scope", sc)
			}
		}
		spec.Scopes = scopes
	}
	switch {
	case in.ExpiresIn != "":
		d, err := time.ParseDuration(in.ExpiresIn)
		if err != nil || d <= 0 {
			return badRequest("expires_in must be a positive duration such as 720h")
		}
		at := time.Now().Add(d).UTC().Format("2006-01-02T15:04:05.000Z07:00")
		spec.ExpiresAt = &at
	case in.ExpiresAt != "":
		t, err := time.Parse(time.RFC3339, in.ExpiresAt)
		if err != nil {
			return badRequest("expires_at must be RFC 3339")
		}
		at := t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
		spec.ExpiresAt = &at
	}
	tok, err := s.CreateToken(r.Context(), spec, p.Actor)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusCreated, tok)
}

func (s *Service) revokeToken(w http.ResponseWriter, r *http.Request, p *Principal) error {
	tok, err := s.TokenByID(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	allowed := p.CanAdmin()
	switch {
	case tok.UserID != nil && p.User != nil && *tok.UserID == p.User.ID:
		allowed = true
	case tok.AgentID != nil:
		if p.Agent != nil && p.Agent.ID == *tok.AgentID {
			allowed = true
		} else if a, err := s.AgentByID(r.Context(), *tok.AgentID); err == nil {
			org, err := s.DB.OrgByName(r.Context(), a.Org)
			if err != nil {
				return err
			}
			if ok, err := s.mayManageAgent(r, p, a, org); err == nil && ok {
				allowed = true
			}
		}
	}
	if !allowed {
		return forbidden("only the token's owner or an administrator may revoke it")
	}
	if err := s.RevokeToken(r.Context(), tok, p.Actor); err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"revoked": true, "id": tok.ID})
}

// ---- ssh keys ----

func (s *Service) listKeys(w http.ResponseWriter, r *http.Request, p *Principal) error {
	u, err := s.targetUser(r, p, r.URL.Query().Get("user"))
	if err != nil {
		return err
	}
	keys, err := s.SSHKeys(r.Context(), u.ID)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, keys)
}

func (s *Service) addKey(w http.ResponseWriter, r *http.Request, p *Principal) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var in struct {
		PublicKey string `json:"public_key"`
		Title     string `json:"title"`
		User      string `json:"user"`
	}
	if err := web.Decode(r, &in); err != nil {
		return badRequest("%v", err)
	}
	u, err := s.targetUser(r, p, in.User)
	if err != nil {
		return err
	}
	k, err := s.AddSSHKey(r.Context(), u, in.PublicKey, in.Title, p.Actor)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusCreated, k)
}

func (s *Service) removeKey(w http.ResponseWriter, r *http.Request, p *Principal) error {
	k, err := s.SSHKeyByID(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	if !p.CanAdmin() && (p.User == nil || p.User.ID != k.UserID) {
		return forbidden("only the key's owner or an administrator may remove it")
	}
	if err := s.RemoveSSHKey(r.Context(), k, p.Actor); err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"removed": true, "id": k.ID})
}

// ---- agents ----

func (s *Service) listAgents(w http.ResponseWriter, r *http.Request, p *Principal) error {
	orgName := r.URL.Query().Get("org")
	if orgName == "" {
		if !p.CanAdmin() {
			return badRequest("org is required")
		}
		agents, err := s.Agents(r.Context(), "")
		if err != nil {
			return err
		}
		return web.JSON(w, http.StatusOK, agents)
	}
	org, err := s.DB.OrgByName(r.Context(), orgName)
	if err != nil {
		return err
	}
	if err := s.CheckPolicy(r.Context(), p, org.Name, OpRead); err != nil {
		return err
	}
	agents, err := s.Agents(r.Context(), org.ID)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, agents)
}

func (s *Service) createAgent(w http.ResponseWriter, r *http.Request, p *Principal) error {
	var in struct {
		Org     string          `json:"org"`
		Name    string          `json:"name"`
		Owner   string          `json:"owner"`
		Budgets json.RawMessage `json:"budgets"`
	}
	if err := web.Decode(r, &in); err != nil || in.Org == "" {
		return badRequest("org and name are required")
	}
	org, err := s.DB.OrgByName(r.Context(), in.Org)
	if err != nil {
		return err
	}
	if ok, err := s.isOrgOwner(r, p, org); err != nil || !ok {
		if err != nil {
			return err
		}
		return forbidden("creating agents requires an owner of %s", org.Name)
	}
	var owner User
	switch {
	case in.Owner != "":
		owner, err = s.UserByName(r.Context(), in.Owner)
		if err != nil {
			return fmt.Errorf("owner %s: %w", in.Owner, err)
		}
	case p.User != nil:
		owner = *p.User
	default:
		return badRequest("owner is required when the caller is not a user")
	}
	a, err := s.CreateAgent(r.Context(), org, in.Name, owner, in.Budgets, p.Actor)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusCreated, a)
}
