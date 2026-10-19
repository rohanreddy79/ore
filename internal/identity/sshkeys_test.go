// SPDX-License-Identifier: AGPL-3.0-only

package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/orevcs/ore/internal/ledger"
)

func ed25519Line(t *testing.T, comment string) (ssh.PublicKey, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(spub)))
	if comment != "" {
		line += " " + comment
	}
	return spub, line
}

func TestFingerprintMatchesOpenSSH(t *testing.T) {
	spub, line := ed25519Line(t, "alice@laptop")
	pub, comment, err := ParsePublicKey(line)
	if err != nil {
		t.Fatal(err)
	}
	if comment != "alice@laptop" {
		t.Fatalf("comment %q", comment)
	}
	fp := Fingerprint(pub)
	if !strings.HasPrefix(fp, "SHA256:") || fp != ssh.FingerprintSHA256(spub) {
		t.Fatalf("fingerprint %q", fp)
	}
	if NormalizeKey(pub) != strings.TrimSuffix(line, " alice@laptop") {
		t.Fatalf("normalized %q", NormalizeKey(pub))
	}
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen not found")
	}
	path := filepath.Join(t.TempDir(), "k.pub")
	os.WriteFile(path, []byte(line+"\n"), 0o644)
	out, err := exec.Command(keygen, "-lf", path).CombinedOutput()
	if err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	if !strings.Contains(string(out), fp) {
		t.Fatalf("ssh-keygen fingerprint %q does not contain %q", out, fp)
	}
}

func TestParsePublicKeyLimits(t *testing.T) {
	_, good := ed25519Line(t, "")
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 1024)
	small, _ := ssh.NewPublicKey(&rsaKey.PublicKey)
	cases := map[string]string{
		"empty":          "",
		"prose":          "not a key at all",
		"bad base64":     "ssh-ed25519 AAAAnotbase64!!! x",
		"truncated":      good[:len(good)-10],
		"oversized":      good + " " + strings.Repeat("x", MaxPublicKeyLen),
		"two keys":       good + "\n" + good,
		"small rsa":      strings.TrimSpace(string(ssh.MarshalAuthorizedKey(small))),
		"dss by type":    "ssh-dss " + strings.Fields(good)[1],
		"type mismatch":  "ssh-rsa " + strings.Fields(good)[1],
		"private key":    "-----BEGIN OPENSSH PRIVATE KEY-----",
		"multiline junk": "ssh-ed25519\nAAAA",
	}
	for name, in := range cases {
		if _, _, err := ParsePublicKey(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: expected ErrInvalid, got %v", name, err)
		}
	}
	if _, _, err := ParsePublicKey("  " + good + "  "); err != nil {
		t.Fatalf("whitespace-padded good key refused: %v", err)
	}
}

func TestSSHKeyStoreAndActorForKey(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	alice, _ := s.CreateUser(ctx, "alice", "", "", ledger.System)
	spub, line := ed25519Line(t, "alice@laptop")
	k, err := s.AddSSHKey(ctx, alice, line, "", ledger.System)
	if err != nil || k.Title != "alice@laptop" || k.Fingerprint != ssh.FingerprintSHA256(spub) {
		t.Fatalf("add key: %+v %v", k, err)
	}
	if _, err := s.AddSSHKey(ctx, alice, line, "again", ledger.System); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate key: %v", err)
	}
	actor, err := s.ActorForKey(ctx, spub)
	if err != nil || actor == nil || actor.Kind != "human" || actor.ID != "alice" {
		t.Fatalf("actor for key: %+v %v", actor, err)
	}
	got, _ := s.SSHKeyByID(ctx, k.ID)
	if got.LastUsedAt == nil {
		t.Fatal("last_used_at not recorded")
	}
	unknown, _ := ed25519Line(t, "")
	if a, err := s.ActorForKey(ctx, unknown); err != nil || a != nil {
		t.Fatalf("unknown key resolved: %+v %v", a, err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE users SET status = 'disabled' WHERE id = ?`, alice.ID); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.ActorForKey(ctx, spub); a != nil {
		t.Fatal("disabled user's key resolved")
	}
	s.DB.Exec(ctx, `UPDATE users SET status = 'active' WHERE id = ?`, alice.ID)
	if err := s.RemoveSSHKey(ctx, k, ledger.System); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.ActorForKey(ctx, spub); a != nil {
		t.Fatal("removed key resolved")
	}
	if err := s.RemoveSSHKey(ctx, k, ledger.System); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("second removal: %v", err)
	}
	for _, typ := range []string{"io.ore.identity.ssh_key.added", "io.ore.identity.ssh_key.removed"} {
		evs, _ := s.DB.Events(ctx, ledger.EventsOptions{Type: typ})
		if len(evs) != 1 || !strings.Contains(string(evs[0].Data), k.Fingerprint) {
			t.Fatalf("%s: %d events %+v", typ, len(evs), evs)
		}
	}
}
