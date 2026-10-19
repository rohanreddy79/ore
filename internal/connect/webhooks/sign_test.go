// SPDX-License-Identifier: AGPL-3.0-only

package webhooks

import (
	"strings"
	"testing"
	"time"
)

// The standard-webhooks reference libraries ship this example (secret,
// message id, timestamp, payload). The first 30 characters of the published
// signature, "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uF", are reproduced here;
// the full value pins this implementation.
func TestSignVector(t *testing.T) {
	sig, err := Sign("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw", "msg_p5jXN8AQM9LWM0D4loKWxJek", time.Unix(1614265330, 0), []byte(`{"test": 2432232314}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sig, "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uF") {
		t.Fatalf("signature does not match the reference prefix: %s", sig)
	}
	if sig != "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=" {
		t.Fatalf("signature changed: %s", sig)
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	secret := NewSecret()
	if !strings.HasPrefix(secret, SecretPrefix) || len(secret) != len(SecretPrefix)+32 {
		t.Fatalf("secret %q", secret)
	}
	body := []byte(`{"specversion":"1.0","type":"io.ore.issue.opened"}`)
	now := time.Unix(1700000000, 0)
	sig, err := Sign(secret, "evt_1", now, body)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(secret, "evt_1", "1700000000", sig, body, now.Add(time.Minute), DefaultTolerance); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// tampered body, wrong id, wrong secret, stale timestamp
	if err := Verify(secret, "evt_1", "1700000000", sig, []byte(`{}`), now, DefaultTolerance); err != ErrSignature {
		t.Fatalf("tampered body: %v", err)
	}
	if err := Verify(secret, "evt_2", "1700000000", sig, body, now, DefaultTolerance); err != ErrSignature {
		t.Fatalf("wrong id: %v", err)
	}
	if err := Verify(NewSecret(), "evt_1", "1700000000", sig, body, now, DefaultTolerance); err != ErrSignature {
		t.Fatalf("wrong secret: %v", err)
	}
	if err := Verify(secret, "evt_1", "1700000000", sig, body, now.Add(time.Hour), DefaultTolerance); err != ErrTimestamp {
		t.Fatalf("stale: %v", err)
	}
	if err := Verify(secret, "evt_1", "soon", sig, body, now, DefaultTolerance); err != ErrTimestamp {
		t.Fatalf("bad timestamp: %v", err)
	}
	if _, err := Sign("whsec_!!!", "x", now, body); err == nil {
		t.Fatal("bad base64 secret accepted")
	}
}

func TestSignAllDuringRotation(t *testing.T) {
	old, cur := NewSecret(), NewSecret()
	now := time.Unix(1700000000, 0)
	body := []byte("{}")
	header, err := SignAll([]string{cur, old}, "evt_9", now, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Fields(header)) != 2 {
		t.Fatalf("header %q", header)
	}
	for _, s := range []string{old, cur} {
		if err := Verify(s, "evt_9", "1700000000", header, body, now, 0); err != nil {
			t.Fatalf("verify with %s: %v", s[:10], err)
		}
	}
	// unknown versions are skipped, not accepted
	if err := Verify(cur, "evt_9", "1700000000", "v2,abc "+strings.Fields(header)[1], body, now, 0); err != ErrSignature {
		t.Fatalf("v2 + old: %v", err)
	}
}
