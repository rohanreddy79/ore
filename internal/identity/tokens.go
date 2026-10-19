// SPDX-License-Identifier: AGPL-3.0-only

package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

// SecretPrefix starts every token secret so leaked tokens are recognizable.
const SecretPrefix = "ore_"

// NewSecret returns a fresh token secret: SecretPrefix plus 160 random bits in hex.
func NewSecret() (string, error) {
	var b [20]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return SecretPrefix + hex.EncodeToString(b[:]), nil
}

// HashSecret returns the hex SHA-256 of a secret; only this is stored.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// prefixOf returns the displayable start of a secret ("ore_1a2b3c").
func prefixOf(secret string) string {
	if len(secret) > 10 {
		return secret[:10]
	}
	return secret
}

// Scopes a token may carry. write implies read; admin implies write.
const (
	ScopeRead  = "read"
	ScopeWrite = "write"
	ScopeAdmin = "admin"
)

// Scopes is a token's sorted, deduplicated scope set.
type Scopes []string

// DefaultUserScopes are given to user tokens created without scopes.
var DefaultUserScopes = Scopes{ScopeRead, ScopeWrite}

// DefaultAgentScopes are given to agent tokens created without scopes: agents
// read by default and get write only when their owner says so (ADR 0009).
var DefaultAgentScopes = Scopes{ScopeRead}

var scopeRank = map[string]int{ScopeRead: 1, ScopeWrite: 2, ScopeAdmin: 3}

// ParseScopes validates and normalizes a scope list.
func ParseScopes(in []string) (Scopes, error) {
	seen := map[string]bool{}
	var out Scopes
	for _, sc := range in {
		sc = strings.ToLower(strings.TrimSpace(sc))
		if _, ok := scopeRank[sc]; !ok {
			return nil, fmt.Errorf("%w: unknown scope %q (read, write, admin)", ErrInvalid, sc)
		}
		if !seen[sc] {
			seen[sc] = true
			out = append(out, sc)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: at least one scope is required", ErrInvalid)
	}
	sort.Slice(out, func(i, j int) bool { return scopeRank[out[i]] < scopeRank[out[j]] })
	return out, nil
}

// Has reports whether the set grants scope, honoring admin ⊇ write ⊇ read.
func (s Scopes) Has(scope string) bool {
	need, ok := scopeRank[scope]
	if !ok {
		return false
	}
	for _, have := range s {
		if scopeRank[have] >= need {
			return true
		}
	}
	return false
}

// String joins the scopes with commas.
func (s Scopes) String() string { return strings.Join(s, ",") }

// TokenSpec describes a token to create: exactly one of User or Agent.
type TokenSpec struct {
	User      *User
	Agent     *Agent
	Name      string
	Scopes    Scopes
	ExpiresAt *string // ledger timestamp; nil never expires
}

// CreateToken mints a token, stores its hash and emits io.ore.identity.token.created.
// The returned Token carries the secret in Secret; it is never stored or shown again.
func (s *Service) CreateToken(ctx context.Context, spec TokenSpec, actor ledger.Actor) (Token, error) {
	if (spec.User == nil) == (spec.Agent == nil) {
		return Token{}, fmt.Errorf("%w: a token belongs to exactly one user or agent", ErrInvalid)
	}
	if len(spec.Name) > 128 {
		return Token{}, fmt.Errorf("%w: token name is too long", ErrInvalid)
	}
	scopes := spec.Scopes
	if len(scopes) == 0 {
		scopes = DefaultUserScopes
		if spec.Agent != nil {
			scopes = DefaultAgentScopes
		}
	}
	if spec.ExpiresAt != nil && *spec.ExpiresAt <= s.now() {
		return Token{}, fmt.Errorf("%w: expires_at must be in the future", ErrInvalid)
	}
	secret, err := NewSecret()
	if err != nil {
		return Token{}, err
	}
	scopesJSON, _ := json.Marshal(scopes)
	t := Token{ID: ledger.NewID("tok"), Name: spec.Name, Prefix: prefixOf(secret), Scopes: scopes, ExpiresAt: spec.ExpiresAt, CreatedAt: s.now()}
	subjectPrefix := ""
	if spec.User != nil {
		id := spec.User.ID
		t.UserID = &id
		subjectPrefix = "users/" + spec.User.Name
	} else {
		id := spec.Agent.ID
		t.AgentID = &id
		t.OrgID = spec.Agent.OrgID
		subjectPrefix = "orgs/" + spec.Agent.Org + "/agents/" + spec.Agent.Name
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Token{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO tokens(id, org_id, user_id, agent_id, name, secret_hash, prefix, scopes, expires_at, created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.OrgID, t.UserID, t.AgentID, t.Name, HashSecret(secret), t.Prefix, string(scopesJSON), t.ExpiresAt, t.CreatedAt); err != nil {
		return Token{}, err
	}
	if _, err := tx.Emit(t.OrgID, "io.ore.identity.token.created", subjectPrefix+"/tokens/"+t.ID, actor, t); err != nil {
		return Token{}, err
	}
	if err := tx.Commit(); err != nil {
		return Token{}, err
	}
	t.Secret = secret
	return t, nil
}

// RevokeToken marks a token revoked and emits io.ore.identity.token.revoked.
// Revoking an already revoked token is a no-op.
func (s *Service) RevokeToken(ctx context.Context, t Token, actor ledger.Actor) error {
	if t.RevokedAt != nil {
		return nil
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now()
	res, err := tx.ExecContext(ctx, `UPDATE tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, now, t.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	if _, err := tx.Emit(t.OrgID, "io.ore.identity.token.revoked", "tokens/"+t.ID, actor, map[string]any{"token_id": t.ID, "name": t.Name, "prefix": t.Prefix, "user_id": t.UserID, "agent_id": t.AgentID}); err != nil {
		return err
	}
	return tx.Commit()
}

const tokenCols = `id, org_id, user_id, agent_id, name, prefix, scopes, expires_at, revoked_at, last_used_at, created_at`

func scanToken(r interface{ Scan(...any) error }) (Token, error) {
	var t Token
	var scopes string
	err := r.Scan(&t.ID, &t.OrgID, &t.UserID, &t.AgentID, &t.Name, &t.Prefix, &scopes, &t.ExpiresAt, &t.RevokedAt, &t.LastUsedAt, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ledger.ErrNotFound
	}
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal([]byte(scopes), &t.Scopes); err != nil {
		return t, fmt.Errorf("token %s: bad scopes: %w", t.ID, err)
	}
	return t, nil
}

// TokenBySecret finds the token whose hash matches secret, revoked or not.
func (s *Service) TokenBySecret(ctx context.Context, secret string) (Token, error) {
	return scanToken(s.DB.QueryRow(ctx, `SELECT `+tokenCols+` FROM tokens WHERE secret_hash = ?`, HashSecret(secret)))
}

// TokenByID finds a token by id.
func (s *Service) TokenByID(ctx context.Context, id string) (Token, error) {
	return scanToken(s.DB.QueryRow(ctx, `SELECT `+tokenCols+` FROM tokens WHERE id = ?`, id))
}

// Tokens lists a user's or an agent's tokens (one of the ids is set).
func (s *Service) Tokens(ctx context.Context, userID, agentID string) ([]Token, error) {
	q := `SELECT ` + tokenCols + ` FROM tokens WHERE user_id = ? ORDER BY created_at`
	arg := userID
	if agentID != "" {
		q = `SELECT ` + tokenCols + ` FROM tokens WHERE agent_id = ? ORDER BY created_at`
		arg = agentID
	}
	rows, err := s.DB.Query(ctx, q, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Expired reports whether the token has passed its expiry at now.
func (t Token) Expired(now string) bool { return t.ExpiresAt != nil && *t.ExpiresAt <= now }

// touchToken records last use at most once a minute (the ledger has one writer).
func (s *Service) touchToken(ctx context.Context, t Token) {
	threshold := time.Now().Add(-time.Minute).UTC().Format("2006-01-02T15:04:05.000Z07:00")
	if t.LastUsedAt != nil && *t.LastUsedAt > threshold {
		return
	}
	if _, err := s.DB.Exec(ctx, `UPDATE tokens SET last_used_at = ? WHERE id = ?`, s.now(), t.ID); err != nil && s.Log != nil {
		s.Log.Warn("identity: token last_used", "id", t.ID, "err", err)
	}
}
