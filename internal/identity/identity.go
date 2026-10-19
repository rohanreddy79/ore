// SPDX-License-Identifier: AGPL-3.0-only

// Package identity is Ore's principal layer (docs/03 §2.2, ADR 0009): users
// with per-organization memberships, agents as first-class principals with a
// human owner and budgets, personal and agent tokens (SHA-256 hashed, scoped,
// expiring, revocable) and SSH public keys. It replaces the single admin-token
// authenticator through web.Server.Authenticate while keeping that token valid
// as the bootstrap owner, serves git over SSH from the sshd subpackage, and
// audits every change as an io.ore.identity.* event written in the same
// transaction as the change. It wires itself in from register.go.
package identity

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/orevcs/ore/internal/ledger"
)

// Roles. A user's site role governs the whole instance (owner = administrator);
// a membership's role governs one organization (guest = read-only).
const (
	RoleOwner  = "owner"
	RoleMember = "member"
	RoleGuest  = "guest"
)

// Principal statuses.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// DefaultBootstrapUser is the user the admin token maps to.
const DefaultBootstrapUser = "admin"

// Errors the REST layer maps to status codes.
var (
	ErrInvalid   = errors.New("invalid request")
	ErrForbidden = errors.New("forbidden")
	ErrConflict  = errors.New("already exists")
)

// User is a human principal.
type User struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Role      string `json:"role"`   // owner | member | guest (site-wide)
	Status    string `json:"status"` // active | disabled
	CreatedAt string `json:"created_at"`
}

// Membership places a user in an organization with a role.
type Membership struct {
	OrgID     string `json:"org_id"`
	Org       string `json:"org"`
	UserID    string `json:"user_id"`
	User      string `json:"user"`
	Role      string `json:"role"` // owner | member | guest
	CreatedAt string `json:"created_at"`
}

// Agent is a delegated principal with a human owner (ADR 0009).
type Agent struct {
	ID        string          `json:"id"`
	OrgID     string          `json:"org_id"`
	Org       string          `json:"org"`
	Name      string          `json:"name"`
	OwnerID   string          `json:"owner_user_id"`
	Owner     string          `json:"owner"`
	Budgets   json.RawMessage `json:"budgets"`
	Status    string          `json:"status"`
	CreatedAt string          `json:"created_at"`
}

// Token is a bearer credential for a user or an agent. Only the SHA-256 of
// the secret is stored; Secret is set once, on the response that creates it.
type Token struct {
	ID         string  `json:"id"`
	OrgID      string  `json:"org_id,omitempty"`
	UserID     *string `json:"user_id,omitempty"`
	AgentID    *string `json:"agent_id,omitempty"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"`
	Scopes     Scopes  `json:"scopes"`
	ExpiresAt  *string `json:"expires_at,omitempty"`
	RevokedAt  *string `json:"revoked_at,omitempty"`
	LastUsedAt *string `json:"last_used_at,omitempty"`
	CreatedAt  string  `json:"created_at"`
	Secret     string  `json:"token,omitempty"`
}

// SSHKey is a user's public key, identified by its SHA-256 fingerprint.
type SSHKey struct {
	ID          string  `json:"id"`
	UserID      string  `json:"user_id"`
	PublicKey   string  `json:"public_key"`
	Fingerprint string  `json:"fingerprint"`
	Title       string  `json:"title"`
	CreatedAt   string  `json:"created_at"`
	LastUsedAt  *string `json:"last_used_at,omitempty"`
}

// Service is the identity store and authenticator. It is attached to the
// server as s.Services["identity"].
type Service struct {
	DB         *ledger.DB
	AdminToken string // the token printed by `ore serve`; "" disables it
	Bootstrap  string // the user the admin token maps to (DefaultBootstrapUser)
	Log        *slog.Logger
	Now        func() string // ledger.Now unless a test overrides it
}

// New returns a service over db that honors adminToken as the bootstrap owner.
func New(db *ledger.DB, adminToken string) *Service {
	return &Service{DB: db, AdminToken: adminToken, Bootstrap: DefaultBootstrapUser, Log: slog.Default(), Now: ledger.Now}
}

func (s *Service) now() string {
	if s.Now != nil {
		return s.Now()
	}
	return ledger.Now()
}

func (s *Service) bootstrapName() string {
	if s.Bootstrap == "" {
		return DefaultBootstrapUser
	}
	return s.Bootstrap
}

// EnsureBootstrap creates the bootstrap owner user when it does not exist.
func (s *Service) EnsureBootstrap(ctx context.Context) (User, error) {
	u, err := s.UserByName(ctx, s.bootstrapName())
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, ledger.ErrNotFound) {
		return User{}, err
	}
	return s.CreateUser(ctx, s.bootstrapName(), "", RoleOwner, ledger.System)
}

func validName(s string) bool {
	if s == "" || len(s) > 64 || strings.HasPrefix(s, ".") {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func validRole(r string) bool { return r == RoleOwner || r == RoleMember || r == RoleGuest }

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// ---- users ----

// CreateUser adds a user and emits io.ore.identity.user.created.
func (s *Service) CreateUser(ctx context.Context, name, email, role string, actor ledger.Actor) (User, error) {
	if !validName(name) {
		return User{}, fmt.Errorf("%w: user name must match [a-z0-9-_.] and be at most 64 characters", ErrInvalid)
	}
	if role == "" {
		role = RoleMember
	}
	if !validRole(role) {
		return User{}, fmt.Errorf("%w: role must be owner, member or guest", ErrInvalid)
	}
	if len(email) > 254 {
		return User{}, fmt.Errorf("%w: email is too long", ErrInvalid)
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	u := User{ID: ledger.NewID("usr"), Name: name, Email: email, Role: role, Status: StatusActive, CreatedAt: s.now()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users(id, name, email, role, status, created_at) VALUES(?,?,?,?,?,?)`,
		u.ID, u.Name, u.Email, u.Role, u.Status, u.CreatedAt); err != nil {
		if isUniqueViolation(err) {
			return User{}, fmt.Errorf("%w: user %s", ErrConflict, name)
		}
		return User{}, err
	}
	if _, err := tx.Emit("", "io.ore.identity.user.created", "users/"+u.Name, actor, u); err != nil {
		return User{}, err
	}
	return u, tx.Commit()
}

const userCols = `id, name, email, role, status, created_at`

func scanUser(r interface{ Scan(...any) error }) (User, error) {
	var u User
	err := r.Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.Status, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ledger.ErrNotFound
	}
	return u, err
}

// UserByName looks a user up by name.
func (s *Service) UserByName(ctx context.Context, name string) (User, error) {
	return scanUser(s.DB.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE name = ?`, name))
}

// UserByID looks a user up by id.
func (s *Service) UserByID(ctx context.Context, id string) (User, error) {
	return scanUser(s.DB.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// Users lists all users.
func (s *Service) Users(ctx context.Context) ([]User, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ---- memberships ----

// AddMembership puts user in org with role (or changes the role) and emits
// io.ore.identity.membership.added or .changed.
func (s *Service) AddMembership(ctx context.Context, org ledger.Org, user User, role string, actor ledger.Actor) (Membership, error) {
	if role == "" {
		role = RoleMember
	}
	if !validRole(role) {
		return Membership{}, fmt.Errorf("%w: role must be owner, member or guest", ErrInvalid)
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Membership{}, err
	}
	defer tx.Rollback()
	m := Membership{OrgID: org.ID, Org: org.Name, UserID: user.ID, User: user.Name, Role: role, CreatedAt: s.now()}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT role FROM memberships WHERE org_id = ? AND user_id = ?`, org.ID, user.ID).Scan(&existing)
	typ := "io.ore.identity.membership.added"
	switch {
	case err == nil:
		typ = "io.ore.identity.membership.changed"
		if _, err := tx.ExecContext(ctx, `UPDATE memberships SET role = ? WHERE org_id = ? AND user_id = ?`, role, org.ID, user.ID); err != nil {
			return Membership{}, err
		}
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `INSERT INTO memberships(org_id, user_id, role, created_at) VALUES(?,?,?,?)`, m.OrgID, m.UserID, m.Role, m.CreatedAt); err != nil {
			return Membership{}, err
		}
	default:
		return Membership{}, err
	}
	data := map[string]any{"org": org.Name, "user": user.Name, "role": role}
	if existing != "" {
		data["previous_role"] = existing
	}
	if _, err := tx.Emit(org.ID, typ, "orgs/"+org.Name+"/members/"+user.Name, actor, data); err != nil {
		return Membership{}, err
	}
	return m, tx.Commit()
}

// RemoveMembership takes user out of org and emits io.ore.identity.membership.removed.
func (s *Service) RemoveMembership(ctx context.Context, org ledger.Org, user User, actor ledger.Actor) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM memberships WHERE org_id = ? AND user_id = ?`, org.ID, user.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s is not a member of %s", ledger.ErrNotFound, user.Name, org.Name)
	}
	if _, err := tx.Emit(org.ID, "io.ore.identity.membership.removed", "orgs/"+org.Name+"/members/"+user.Name, actor, map[string]any{"org": org.Name, "user": user.Name}); err != nil {
		return err
	}
	return tx.Commit()
}

// MembershipRole returns the user's role in the org and whether they are a member.
func (s *Service) MembershipRole(ctx context.Context, orgID, userID string) (string, bool, error) {
	var role string
	err := s.DB.QueryRow(ctx, `SELECT role FROM memberships WHERE org_id = ? AND user_id = ?`, orgID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return role, err == nil, err
}

const membershipCols = `m.org_id, o.name, m.user_id, u.name, m.role, m.created_at`

func (s *Service) memberships(ctx context.Context, where string, args ...any) ([]Membership, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+membershipCols+` FROM memberships m JOIN orgs o ON o.id = m.org_id JOIN users u ON u.id = m.user_id WHERE `+where+` ORDER BY o.name, u.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Membership{}
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.OrgID, &m.Org, &m.UserID, &m.User, &m.Role, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Memberships lists the organizations a user belongs to.
func (s *Service) Memberships(ctx context.Context, userID string) ([]Membership, error) {
	return s.memberships(ctx, `m.user_id = ?`, userID)
}

// OrgMembers lists an organization's members.
func (s *Service) OrgMembers(ctx context.Context, orgID string) ([]Membership, error) {
	return s.memberships(ctx, `m.org_id = ?`, orgID)
}

// ---- agents ----

// CreateAgent registers an agent principal owned by a user and emits io.ore.identity.agent.created.
func (s *Service) CreateAgent(ctx context.Context, org ledger.Org, name string, owner User, budgets json.RawMessage, actor ledger.Actor) (Agent, error) {
	if !validName(name) {
		return Agent{}, fmt.Errorf("%w: agent name must match [a-z0-9-_.] and be at most 64 characters", ErrInvalid)
	}
	if len(budgets) == 0 {
		budgets = json.RawMessage("{}")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, budgets); err != nil || !strings.HasPrefix(compact.String(), "{") {
		return Agent{}, fmt.Errorf("%w: budgets must be a JSON object", ErrInvalid)
	}
	budgets = json.RawMessage(compact.Bytes())
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Agent{}, err
	}
	defer tx.Rollback()
	a := Agent{ID: ledger.NewID("agt"), OrgID: org.ID, Org: org.Name, Name: name, OwnerID: owner.ID, Owner: owner.Name, Budgets: budgets, Status: StatusActive, CreatedAt: s.now()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents(id, org_id, name, owner_user_id, budgets, status, created_at) VALUES(?,?,?,?,?,?,?)`,
		a.ID, a.OrgID, a.Name, a.OwnerID, string(a.Budgets), a.Status, a.CreatedAt); err != nil {
		if isUniqueViolation(err) {
			return Agent{}, fmt.Errorf("%w: agent %s/%s", ErrConflict, org.Name, name)
		}
		return Agent{}, err
	}
	if _, err := tx.Emit(org.ID, "io.ore.identity.agent.created", "orgs/"+org.Name+"/agents/"+name, actor, a); err != nil {
		return Agent{}, err
	}
	return a, tx.Commit()
}

const agentCols = `a.id, a.org_id, o.name, a.name, a.owner_user_id, u.name, a.budgets, a.status, a.created_at`

func scanAgent(r interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	var budgets string
	err := r.Scan(&a.ID, &a.OrgID, &a.Org, &a.Name, &a.OwnerID, &a.Owner, &budgets, &a.Status, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ledger.ErrNotFound
	}
	a.Budgets = json.RawMessage(budgets)
	return a, err
}

const agentFrom = ` FROM agents a JOIN orgs o ON o.id = a.org_id JOIN users u ON u.id = a.owner_user_id`

// AgentByName finds an agent in an org.
func (s *Service) AgentByName(ctx context.Context, orgID, name string) (Agent, error) {
	return scanAgent(s.DB.QueryRow(ctx, `SELECT `+agentCols+agentFrom+` WHERE a.org_id = ? AND a.name = ?`, orgID, name))
}

// AgentByID finds an agent by id.
func (s *Service) AgentByID(ctx context.Context, id string) (Agent, error) {
	return scanAgent(s.DB.QueryRow(ctx, `SELECT `+agentCols+agentFrom+` WHERE a.id = ?`, id))
}

// Agents lists agents, optionally for one org.
func (s *Service) Agents(ctx context.Context, orgID string) ([]Agent, error) {
	q := `SELECT ` + agentCols + agentFrom
	var args []any
	if orgID != "" {
		q += ` WHERE a.org_id = ?`
		args = append(args, orgID)
	}
	rows, err := s.DB.Query(ctx, q+` ORDER BY o.name, a.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
