// SPDX-License-Identifier: AGPL-3.0-only

package identity

import (
	"context"
	"crypto/rsa"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/orevcs/ore/internal/ledger"
)

// MaxPublicKeyLen bounds an uploaded public key line (the largest sane RSA
// key is well under 4 KiB; anything larger is refused before parsing).
const MaxPublicKeyLen = 8 * 1024

// MinRSABits is the smallest RSA modulus accepted.
const MinRSABits = 2048

// ParsePublicKey parses one authorized_keys-style line ("type base64 [comment]")
// and applies Ore's limits: size, no DSA, RSA of at least MinRSABits.
func ParsePublicKey(line string) (ssh.PublicKey, string, error) {
	if len(line) > MaxPublicKeyLen {
		return nil, "", fmt.Errorf("%w: public key is larger than %d bytes", ErrInvalid, MaxPublicKeyLen)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, "", fmt.Errorf("%w: public_key is required", ErrInvalid)
	}
	if strings.ContainsAny(line, "\r\n") {
		return nil, "", fmt.Errorf("%w: public key must be a single line", ErrInvalid)
	}
	pub, comment, _, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, "", fmt.Errorf("%w: not an OpenSSH public key (%v)", ErrInvalid, err)
	}
	if len(strings.TrimSpace(string(rest))) > 0 {
		return nil, "", fmt.Errorf("%w: public key must be a single key", ErrInvalid)
	}
	switch pub.Type() {
	case ssh.KeyAlgoDSA:
		return nil, "", fmt.Errorf("%w: ssh-dss keys are not accepted", ErrInvalid)
	case ssh.KeyAlgoRSA:
		if ck, ok := pub.(ssh.CryptoPublicKey); ok {
			if rk, ok := ck.CryptoPublicKey().(*rsa.PublicKey); ok && rk.N.BitLen() < MinRSABits {
				return nil, "", fmt.Errorf("%w: RSA keys must be at least %d bits", ErrInvalid, MinRSABits)
			}
		}
	}
	return pub, strings.TrimSpace(comment), nil
}

// Fingerprint returns the OpenSSH SHA-256 fingerprint ("SHA256:...") of a key.
func Fingerprint(pub ssh.PublicKey) string { return ssh.FingerprintSHA256(pub) }

// NormalizeKey renders a key as "type base64" without a comment, the stored form.
func NormalizeKey(pub ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
}

// AddSSHKey registers a public key for user and emits io.ore.identity.ssh_key.added.
func (s *Service) AddSSHKey(ctx context.Context, user User, publicKey, title string, actor ledger.Actor) (SSHKey, error) {
	pub, comment, err := ParsePublicKey(publicKey)
	if err != nil {
		return SSHKey{}, err
	}
	if title == "" {
		title = comment
	}
	if len(title) > 128 {
		return SSHKey{}, fmt.Errorf("%w: title is too long", ErrInvalid)
	}
	k := SSHKey{ID: ledger.NewID("key"), UserID: user.ID, PublicKey: NormalizeKey(pub), Fingerprint: Fingerprint(pub), Title: title, CreatedAt: s.now()}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return SSHKey{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO ssh_keys(id, org_id, user_id, public_key, fingerprint, title, created_at) VALUES(?,'',?,?,?,?,?)`,
		k.ID, k.UserID, k.PublicKey, k.Fingerprint, k.Title, k.CreatedAt); err != nil {
		if isUniqueViolation(err) {
			return SSHKey{}, fmt.Errorf("%w: key %s is already registered", ErrConflict, k.Fingerprint)
		}
		return SSHKey{}, err
	}
	if _, err := tx.Emit("", "io.ore.identity.ssh_key.added", "users/"+user.Name+"/ssh-keys/"+k.ID, actor,
		map[string]any{"key_id": k.ID, "user": user.Name, "fingerprint": k.Fingerprint, "type": pub.Type(), "title": k.Title}); err != nil {
		return SSHKey{}, err
	}
	return k, tx.Commit()
}

// RemoveSSHKey deletes a key and emits io.ore.identity.ssh_key.removed.
func (s *Service) RemoveSSHKey(ctx context.Context, k SSHKey, actor ledger.Actor) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM ssh_keys WHERE id = ?`, k.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ledger.ErrNotFound
	}
	var userName string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM users WHERE id = ?`, k.UserID).Scan(&userName); err != nil {
		return err
	}
	if _, err := tx.Emit("", "io.ore.identity.ssh_key.removed", "users/"+userName+"/ssh-keys/"+k.ID, actor,
		map[string]any{"key_id": k.ID, "user": userName, "fingerprint": k.Fingerprint}); err != nil {
		return err
	}
	return tx.Commit()
}

const keyCols = `id, user_id, public_key, fingerprint, title, created_at, last_used_at`

func scanKey(r interface{ Scan(...any) error }) (SSHKey, error) {
	var k SSHKey
	err := r.Scan(&k.ID, &k.UserID, &k.PublicKey, &k.Fingerprint, &k.Title, &k.CreatedAt, &k.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return k, ledger.ErrNotFound
	}
	return k, err
}

// SSHKeyByFingerprint finds a registered key.
func (s *Service) SSHKeyByFingerprint(ctx context.Context, fp string) (SSHKey, error) {
	return scanKey(s.DB.QueryRow(ctx, `SELECT `+keyCols+` FROM ssh_keys WHERE fingerprint = ?`, fp))
}

// SSHKeyByID finds a key by id.
func (s *Service) SSHKeyByID(ctx context.Context, id string) (SSHKey, error) {
	return scanKey(s.DB.QueryRow(ctx, `SELECT `+keyCols+` FROM ssh_keys WHERE id = ?`, id))
}

// SSHKeys lists a user's keys.
func (s *Service) SSHKeys(ctx context.Context, userID string) ([]SSHKey, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+keyCols+` FROM ssh_keys WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SSHKey{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// ActorForKey resolves an SSH public key to its user's actor; nil when the key
// is unknown or the user is disabled. It implements sshd.Auth.
func (s *Service) ActorForKey(ctx context.Context, key ssh.PublicKey) (*ledger.Actor, error) {
	k, err := s.SSHKeyByFingerprint(ctx, Fingerprint(key))
	if errors.Is(err, ledger.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if k.PublicKey != NormalizeKey(key) { // a fingerprint match must also be a key match
		return nil, nil
	}
	u, err := s.UserByID(ctx, k.UserID)
	if err != nil || u.Status != StatusActive {
		return nil, nil
	}
	threshold := time.Now().Add(-time.Minute).UTC().Format("2006-01-02T15:04:05.000Z07:00")
	if k.LastUsedAt == nil || *k.LastUsedAt <= threshold {
		if _, err := s.DB.Exec(ctx, `UPDATE ssh_keys SET last_used_at = ? WHERE id = ?`, s.now(), k.ID); err != nil && s.Log != nil {
			s.Log.Warn("identity: key last_used", "id", k.ID, "err", err)
		}
	}
	return &ledger.Actor{Kind: "human", ID: u.Name}, nil
}
