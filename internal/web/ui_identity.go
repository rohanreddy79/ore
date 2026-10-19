// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/orevcs/ore/internal/ledger"
)

// The identity pages talk to the identity slice through its API routes
// (the package imports web, so it cannot be called directly). Shapes mirror
// docs/changes/identity-ssh.md.

type idUser struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

type idMembership struct {
	Org  string `json:"org"`
	User string `json:"user"`
	Role string `json:"role"`
}

type idToken struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Prefix     string   `json:"prefix"`
	Scopes     []string `json:"scopes"`
	ExpiresAt  *string  `json:"expires_at"`
	RevokedAt  *string  `json:"revoked_at"`
	LastUsedAt *string  `json:"last_used_at"`
	CreatedAt  string   `json:"created_at"`
	Secret     string   `json:"token"`
}

type idKey struct {
	ID          string  `json:"id"`
	PublicKey   string  `json:"public_key"`
	Fingerprint string  `json:"fingerprint"`
	Title       string  `json:"title"`
	CreatedAt   string  `json:"created_at"`
	LastUsedAt  *string `json:"last_used_at"`
}

type idAgent struct {
	ID        string          `json:"id"`
	Org       string          `json:"org"`
	Name      string          `json:"name"`
	Owner     string          `json:"owner"`
	Budgets   json.RawMessage `json:"budgets"`
	Status    string          `json:"status"`
	CreatedAt string          `json:"created_at"`
}

type idMe struct {
	Actor ledger.Actor `json:"actor"`
	Admin bool         `json:"admin"`
	User  *idUser      `json:"user"`
	Agent *idAgent     `json:"agent"`
	Token *idToken     `json:"token"`
}

func (s *Server) mountIdentityUI(mux *http.ServeMux) {
	s.page(mux, "GET /identity", s.uiIdentity)
	s.page(mux, "GET /identity/tokens", s.uiTokens)
	s.page(mux, "POST /ui/identity/tokens", s.uiCreateToken)
	s.page(mux, "POST /ui/identity/tokens/{id}/revoke", s.uiRevokeToken)
	s.page(mux, "GET /identity/keys", s.uiKeys)
	s.page(mux, "POST /ui/identity/keys", s.uiAddKey)
	s.page(mux, "POST /ui/identity/keys/{id}/remove", s.uiRemoveKey)
	s.page(mux, "GET /identity/agents", s.uiAgents)
	s.page(mux, "POST /ui/identity/agents", s.uiCreateAgent)
	s.page(mux, "GET /identity/users", s.uiUsers)
	s.page(mux, "POST /ui/identity/users", s.uiCreateUser)
}

// identityBase returns the common data of identity pages, or Available=false
// when the extension is not mounted.
func (s *Server) identityBase(r *http.Request, title, tab string) (map[string]any, *idMe) {
	data := map[string]any{"Title": title, "IdTab": tab}
	res := s.api(r, http.MethodGet, "/api/v0/identity/me", nil)
	if !res.OK() {
		data["Available"] = res.Code != http.StatusNotFound || json.Valid(res.Body)
		if data["Available"] == true {
			data["Error"] = res.Err().Error()
		}
		return data, nil
	}
	var me idMe
	json.Unmarshal(res.Body, &me)
	data["Available"], data["Me"] = true, me
	return data, &me
}

func (s *Server) uiIdentity(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	data, me := s.identityBase(r, "Identity", "me")
	if me != nil && me.User != nil {
		var ms []idMembership
		s.api(r, http.MethodGet, "/api/v0/identity/memberships", nil).Decode(&ms)
		data["Memberships"] = ms
	}
	s.render(w, r, &a, http.StatusOK, "identity", data)
	return nil
}

func (s *Server) uiTokens(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	data, me := s.identityBase(r, "Tokens · Identity", "tokens")
	if me != nil {
		var toks []idToken
		s.api(r, http.MethodGet, "/api/v0/identity/tokens", nil).Decode(&toks)
		data["Tokens"] = toks
	}
	s.render(w, r, &a, http.StatusOK, "tokens", data)
	return nil
}

func (s *Server) uiCreateToken(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	body := map[string]any{"name": strings.TrimSpace(r.FormValue("name"))}
	if sc := r.Form["scopes"]; len(sc) > 0 {
		body["scopes"] = sc
	}
	if v := strings.TrimSpace(r.FormValue("expires_in")); v != "" {
		body["expires_in"] = v
	}
	if v := strings.TrimSpace(r.FormValue("user")); v != "" {
		body["user"] = v
	}
	if v := strings.TrimSpace(r.FormValue("agent")); v != "" {
		body["agent"] = v
	}
	res := s.api(r, http.MethodPost, "/api/v0/identity/tokens", body)
	if err := res.Err(); err != nil {
		return err
	}
	var tok idToken
	json.Unmarshal(res.Body, &tok)
	data, me := s.identityBase(r, "Tokens · Identity", "tokens")
	if me != nil {
		var toks []idToken
		s.api(r, http.MethodGet, "/api/v0/identity/tokens", nil).Decode(&toks)
		data["Tokens"] = toks
	}
	data["NewToken"] = tok // shown once, on this response only
	s.render(w, r, &a, http.StatusCreated, "tokens", data)
	return nil
}

func (s *Server) uiRevokeToken(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	if err := s.api(r, http.MethodDelete, "/api/v0/identity/tokens/"+url.PathEscape(r.PathValue("id")), nil).Err(); err != nil {
		return err
	}
	s.redirectFlash(w, r, "/identity/tokens", "Token revoked.", "")
	return nil
}

func (s *Server) uiKeys(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	data, me := s.identityBase(r, "SSH keys · Identity", "keys")
	if me != nil && me.User != nil {
		var keys []idKey
		s.api(r, http.MethodGet, "/api/v0/identity/ssh-keys", nil).Decode(&keys)
		data["Keys"] = keys
	}
	if sshd, ok := s.Services["identity.sshd"].(interface{ HostKeyFingerprint() string }); ok {
		data["HostKey"] = sshd.HostKeyFingerprint()
	}
	s.render(w, r, &a, http.StatusOK, "keys", data)
	return nil
}

func (s *Server) uiAddKey(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	body := map[string]string{"public_key": strings.TrimSpace(r.FormValue("public_key")), "title": strings.TrimSpace(r.FormValue("title"))}
	if u := strings.TrimSpace(r.FormValue("user")); u != "" {
		body["user"] = u
	}
	if err := s.api(r, http.MethodPost, "/api/v0/identity/ssh-keys", body).Err(); err != nil {
		return err
	}
	s.redirectFlash(w, r, "/identity/keys", "Key added.", "")
	return nil
}

func (s *Server) uiRemoveKey(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	if err := s.api(r, http.MethodDelete, "/api/v0/identity/ssh-keys/"+url.PathEscape(r.PathValue("id")), nil).Err(); err != nil {
		return err
	}
	s.redirectFlash(w, r, "/identity/keys", "Key removed.", "")
	return nil
}

func (s *Server) uiAgents(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	data, me := s.identityBase(r, "Agents · Identity", "agents")
	if me != nil {
		orgs, _ := s.DB.Orgs(r.Context())
		data["Orgs"] = orgs
		org := r.URL.Query().Get("org")
		path := "/api/v0/identity/agents"
		if org != "" {
			path += "?org=" + url.QueryEscape(org)
		} else if !me.Admin && len(orgs) > 0 {
			org = orgs[0].Name
			path += "?org=" + url.QueryEscape(org)
		}
		data["Org"] = org
		var agents []idAgent
		res := s.api(r, http.MethodGet, path, nil)
		if res.OK() {
			json.Unmarshal(res.Body, &agents)
		} else {
			data["Error"] = res.Err().Error()
		}
		data["Agents"] = agents
	}
	s.render(w, r, &a, http.StatusOK, "agents", data)
	return nil
}

func (s *Server) uiCreateAgent(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	body := map[string]any{"org": strings.TrimSpace(r.FormValue("org")), "name": strings.TrimSpace(r.FormValue("name"))}
	if o := strings.TrimSpace(r.FormValue("owner")); o != "" {
		body["owner"] = o
	}
	if b := strings.TrimSpace(r.FormValue("budgets")); b != "" {
		if !json.Valid([]byte(b)) {
			return fmt.Errorf("%w: budgets must be a JSON object", errBadRequest)
		}
		body["budgets"] = json.RawMessage(b)
	}
	if err := s.api(r, http.MethodPost, "/api/v0/identity/agents", body).Err(); err != nil {
		return err
	}
	s.redirectFlash(w, r, "/identity/agents?org="+url.QueryEscape(r.FormValue("org")), "Agent created.", "")
	return nil
}

func (s *Server) uiUsers(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	data, me := s.identityBase(r, "Users · Identity", "users")
	if me != nil {
		var users []idUser
		res := s.api(r, http.MethodGet, "/api/v0/identity/users", nil)
		if res.OK() {
			json.Unmarshal(res.Body, &users)
		} else {
			data["Error"] = res.Err().Error()
		}
		data["Users"] = users
		orgs, _ := s.DB.Orgs(r.Context())
		data["Orgs"] = orgs
	}
	s.render(w, r, &a, http.StatusOK, "users", data)
	return nil
}

func (s *Server) uiCreateUser(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	body := map[string]any{"name": strings.TrimSpace(r.FormValue("name")), "email": strings.TrimSpace(r.FormValue("email")), "role": r.FormValue("role")}
	if org := strings.TrimSpace(r.FormValue("org")); org != "" {
		role := r.FormValue("org_role")
		if role == "" {
			role = "member"
		}
		body["memberships"] = []map[string]string{{"org": org, "role": role}}
	}
	if err := s.api(r, http.MethodPost, "/api/v0/identity/users", body).Err(); err != nil {
		return err
	}
	s.redirectFlash(w, r, "/identity/users", "User "+r.FormValue("name")+" created.", "")
	return nil
}
