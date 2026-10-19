// SPDX-License-Identifier: AGPL-3.0-only

package github

import "testing"

// GitHub's documented example (docs: "Validating webhook deliveries").
func TestSignKnownVector(t *testing.T) {
	const secret, body = "It's a Secret to Everybody", "Hello, World!"
	const want = "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
	if got := Sign(secret, []byte(body)); got != want {
		t.Fatalf("Sign = %s, want %s", got, want)
	}
	if !VerifySignature(secret, []byte(body), want) {
		t.Fatal("documented signature does not verify")
	}
}

func TestVerifySignature(t *testing.T) {
	body := fixture(t, "push.json")
	sig := Sign("s3cret", body)
	cases := map[string]struct {
		secret, sig string
		body        []byte
		ok          bool
	}{
		"valid":            {"s3cret", sig, body, true},
		"wrong secret":     {"other", sig, body, false},
		"tampered body":    {"s3cret", sig, append(append([]byte{}, body[:len(body)-2]...), ' ', '}'), false},
		"missing":          {"s3cret", "", body, false},
		"sha1 only":        {"s3cret", "sha1=" + sig[7:47], body, false},
		"bad hex":          {"s3cret", "sha256=zz", body, false},
		"truncated digest": {"s3cret", sig[:30], body, false},
		"empty secret":     {"", sig, body, false},
	}
	for name, c := range cases {
		if got := VerifySignature(c.secret, c.body, c.sig); got != c.ok {
			t.Errorf("%s: VerifySignature = %v, want %v", name, got, c.ok)
		}
	}
}
