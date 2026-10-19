// SPDX-License-Identifier: AGPL-3.0-only

package webhooks

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Standard Webhooks (https://www.standardwebhooks.com): the signed content
// is "<msg id>.<unix timestamp>.<body>", the signature is base64 of
// HMAC-SHA256 under the secret's bytes, carried as "v1,<base64>"; the
// header holds several space-separated signatures while a secret rotates.
const (
	SecretPrefix     = "whsec_"
	HeaderID         = "webhook-id"
	HeaderTime       = "webhook-timestamp"
	HeaderSig        = "webhook-signature"
	DefaultTolerance = 5 * time.Minute
)

// NewSecret returns a fresh whsec_ secret (24 random bytes, base64).
func NewSecret() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return SecretPrefix + base64.StdEncoding.EncodeToString(b[:])
}

// secretBytes decodes a whsec_ secret; a secret without the prefix is used as raw bytes.
func secretBytes(secret string) ([]byte, error) {
	if strings.HasPrefix(secret, SecretPrefix) {
		b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, SecretPrefix))
		if err != nil {
			return nil, fmt.Errorf("secret: %w", err)
		}
		return b, nil
	}
	return []byte(secret), nil
}

// Sign computes the v1 signature of one message under one secret.
func Sign(secret, msgID string, ts time.Time, body []byte) (string, error) {
	key, err := secretBytes(secret)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msgID + "." + strconv.FormatInt(ts.Unix(), 10) + "."))
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

// SignAll renders the webhook-signature header value for every secret, newest first.
func SignAll(secrets []string, msgID string, ts time.Time, body []byte) (string, error) {
	sigs := make([]string, 0, len(secrets))
	for _, s := range secrets {
		sig, err := Sign(s, msgID, ts, body)
		if err != nil {
			return "", err
		}
		sigs = append(sigs, sig)
	}
	return strings.Join(sigs, " "), nil
}

// ErrSignature is returned when no signature in the header matches.
var ErrSignature = errors.New("webhook signature mismatch")

// ErrTimestamp is returned when webhook-timestamp is outside the tolerance.
var ErrTimestamp = errors.New("webhook timestamp outside tolerance")

// Verify checks a received message against a secret: the timestamp must be
// within tolerance of now and at least one v1 signature must match. This is
// what a receiver does; the tests use it against the dispatcher.
func Verify(secret, msgID, tsHeader, sigHeader string, body []byte, now time.Time, tolerance time.Duration) error {
	unix, err := strconv.ParseInt(strings.TrimSpace(tsHeader), 10, 64)
	if err != nil {
		return ErrTimestamp
	}
	ts := time.Unix(unix, 0)
	if tolerance > 0 && (now.Sub(ts) > tolerance || ts.Sub(now) > tolerance) {
		return ErrTimestamp
	}
	want, err := Sign(secret, msgID, ts, body)
	if err != nil {
		return err
	}
	for _, sig := range strings.Fields(sigHeader) {
		if !strings.HasPrefix(sig, "v1,") {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) == 1 {
			return nil
		}
	}
	return ErrSignature
}
