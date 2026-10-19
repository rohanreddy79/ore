// SPDX-License-Identifier: AGPL-3.0-only

package pipelines

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/orevcs/ore/internal/ledger"
)

// SecretStore keeps repository secrets encrypted at rest (AES-256-GCM, key
// in <data>/secrets.key) and scoped by environment: a secret set for
// environment "production" overrides the repository-wide one of the same
// name for tasks bound to that environment. Pipelines reference secrets by
// name; values are injected only into the step process that references
// them and are masked in logs (ADR 0012 in its v0 form).
type SecretStore struct {
	DB      *ledger.DB
	KeyPath string

	mu  sync.Mutex
	key []byte
}

// SecretInfo describes a secret without its value.
type SecretInfo struct {
	ID          string `json:"id"`
	OrgID       string `json:"org_id"`
	RepoID      string `json:"repo_id"`
	Environment string `json:"environment"`
	Name        string `json:"name"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// ValidSecretName checks an Actions-style secret name: letters, digits and
// underscores, not starting with a digit or GITHUB_.
func ValidSecretName(name string) error {
	if name == "" || len(name) > 200 {
		return errors.New("secret name must be 1 to 200 characters")
	}
	for i, c := range name {
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || (i > 0 && c >= '0' && c <= '9') {
			continue
		}
		return fmt.Errorf("secret name %q: only letters, digits and underscores, not starting with a digit", name)
	}
	if strings.HasPrefix(strings.ToUpper(name), "GITHUB_") {
		return fmt.Errorf("secret name %q: the GITHUB_ prefix is reserved", name)
	}
	return nil
}

func (s *SecretStore) loadKey() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != nil {
		return s.key, nil
	}
	if s.KeyPath == "" {
		return nil, errors.New("secrets: no key path configured")
	}
	b, err := os.ReadFile(s.KeyPath)
	if err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("secrets: %s must hold 32 bytes", s.KeyPath)
		}
		s.key = b
		return b, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	b = make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(s.KeyPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(s.KeyPath, b, 0o600); err != nil {
		return nil, err
	}
	s.key = b
	return b, nil
}

func (s *SecretStore) aead() (cipher.AEAD, error) {
	key, err := s.loadKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func aad(repoID, env, name string) []byte { return []byte(repoID + "\x00" + env + "\x00" + name) }

func (s *SecretStore) encrypt(repoID, env, name, value string) ([]byte, error) {
	g, err := s.aead()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, g.Seal(nil, nonce, []byte(value), aad(repoID, env, name))...), nil
}

func (s *SecretStore) decrypt(repoID, env, name string, ct []byte) (string, error) {
	g, err := s.aead()
	if err != nil {
		return "", err
	}
	if len(ct) < g.NonceSize() {
		return "", errors.New("secrets: ciphertext too short")
	}
	pt, err := g.Open(nil, ct[:g.NonceSize()], ct[g.NonceSize():], aad(repoID, env, name))
	if err != nil {
		return "", fmt.Errorf("secrets: decrypt %s: %w", name, err)
	}
	return string(pt), nil
}

// Set creates or replaces a secret and emits io.ore.secret.created|updated
// (the event never carries the value).
func (s *SecretStore) Set(ctx context.Context, repo ledger.Repo, env, name, value string, actor ledger.Actor) (SecretInfo, error) {
	if err := ValidSecretName(name); err != nil {
		return SecretInfo{}, err
	}
	if env != "" && !safeSegment(env) {
		return SecretInfo{}, fmt.Errorf("bad environment name %q", env)
	}
	ct, err := s.encrypt(repo.ID, env, name, value)
	if err != nil {
		return SecretInfo{}, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return SecretInfo{}, err
	}
	defer tx.Rollback()
	now := ledger.Now()
	info := SecretInfo{OrgID: repo.OrgID, RepoID: repo.ID, Environment: env, Name: name, UpdatedAt: now}
	typ := "io.ore.secret.updated"
	err = tx.QueryRowContext(ctx, `SELECT id, created_at FROM secrets WHERE repo_id = ? AND environment = ? AND name = ?`, repo.ID, env, name).Scan(&info.ID, &info.CreatedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		info.ID, info.CreatedAt, typ = ledger.NewID("sec"), now, "io.ore.secret.created"
		if _, err := tx.ExecContext(ctx, `INSERT INTO secrets(id, org_id, repo_id, environment, name, ciphertext, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?)`,
			info.ID, info.OrgID, info.RepoID, env, name, ct, now, now); err != nil {
			return SecretInfo{}, err
		}
	case err != nil:
		return SecretInfo{}, err
	default:
		if _, err := tx.ExecContext(ctx, `UPDATE secrets SET ciphertext = ?, updated_at = ? WHERE id = ?`, ct, now, info.ID); err != nil {
			return SecretInfo{}, err
		}
	}
	if _, err := tx.Emit(repo.OrgID, typ, repo.Org+"/"+repo.Name+"/secrets/"+name, actor, info); err != nil {
		return SecretInfo{}, err
	}
	return info, tx.Commit()
}

// Get resolves name for env: the environment-scoped value first, then the
// repository-wide one. ok is false when neither exists.
func (s *SecretStore) Get(ctx context.Context, repo ledger.Repo, env, name string) (value string, ok bool, err error) {
	for _, e := range []string{env, ""} {
		var ct []byte
		err := s.DB.QueryRow(ctx, `SELECT ciphertext FROM secrets WHERE repo_id = ? AND environment = ? AND name = ?`, repo.ID, e, name).Scan(&ct)
		if errors.Is(err, sql.ErrNoRows) {
			if e == "" {
				break
			}
			continue
		}
		if err != nil {
			return "", false, err
		}
		v, err := s.decrypt(repo.ID, e, name, ct)
		if err != nil {
			return "", false, err
		}
		return v, true, nil
	}
	return "", false, nil
}

// List returns the repository's secrets (names only), environment then name.
func (s *SecretStore) List(ctx context.Context, repo ledger.Repo) ([]SecretInfo, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, org_id, repo_id, environment, name, created_at, updated_at FROM secrets WHERE repo_id = ? ORDER BY environment, name`, repo.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SecretInfo{}
	for rows.Next() {
		var i SecretInfo
		if err := rows.Scan(&i.ID, &i.OrgID, &i.RepoID, &i.Environment, &i.Name, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// Delete removes a secret and emits io.ore.secret.deleted.
func (s *SecretStore) Delete(ctx context.Context, repo ledger.Repo, env, name string, actor ledger.Actor) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE repo_id = ? AND environment = ? AND name = ?`, repo.ID, env, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ledger.ErrNotFound
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.secret.deleted", repo.Org+"/"+repo.Name+"/secrets/"+name, actor, map[string]string{"name": name, "environment": env}); err != nil {
		return err
	}
	return tx.Commit()
}

// Masker replaces secret values in log lines with ***. Values are matched
// longest first; multi-line values are masked line by line.
type Masker struct {
	mu     sync.RWMutex
	values []string
}

// Add registers a value to mask. Empty and whitespace-only values are ignored.
func (m *Masker) Add(v string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, line := range strings.Split(v, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		dup := false
		for _, x := range m.values {
			if x == line {
				dup = true
				break
			}
		}
		if !dup {
			m.values = append(m.values, line)
		}
	}
	sort.Slice(m.values, func(i, j int) bool { return len(m.values[i]) > len(m.values[j]) })
}

// Mask returns s with every registered value replaced by ***.
func (m *Masker) Mask(s string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, v := range m.values {
		s = strings.ReplaceAll(s, v, "***")
	}
	return s
}

// Contains reports whether s contains any registered value (tests).
func (m *Masker) Contains(s string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, v := range m.values {
		if strings.Contains(s, v) {
			return true
		}
	}
	return false
}
