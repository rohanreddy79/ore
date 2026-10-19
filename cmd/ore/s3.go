// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// s3Target is s3://bucket/prefix.
type s3Target struct{ bucket, prefix string }

func parseS3(dest string) (s3Target, bool) {
	if !strings.HasPrefix(dest, "s3://") {
		return s3Target{}, false
	}
	rest := strings.TrimPrefix(dest, "s3://")
	bucket, prefix, _ := strings.Cut(rest, "/")
	return s3Target{bucket: bucket, prefix: strings.Trim(prefix, "/")}, bucket != ""
}

// s3Client signs requests with AWS Signature Version 4; no SDK. Credentials
// and region come from the usual AWS_* variables; AWS_ENDPOINT_URL (or
// AWS_ENDPOINT_URL_S3) points at MinIO, Ceph or any S3-compatible store with
// path-style addressing.
type s3Client struct {
	endpoint     string
	region       string
	accessKey    string
	secretKey    string
	sessionToken string
	http         *http.Client
	now          func() time.Time
}

func newS3ClientFromEnv() (*s3Client, error) {
	c := &s3Client{
		accessKey:    os.Getenv("AWS_ACCESS_KEY_ID"),
		secretKey:    os.Getenv("AWS_SECRET_ACCESS_KEY"),
		sessionToken: os.Getenv("AWS_SESSION_TOKEN"),
		region:       envOr("AWS_REGION", envOr("AWS_DEFAULT_REGION", "us-east-1")),
		endpoint:     envOr("AWS_ENDPOINT_URL_S3", os.Getenv("AWS_ENDPOINT_URL")),
		http:         &http.Client{Timeout: 5 * time.Minute},
		now:          time.Now,
	}
	if c.accessKey == "" || c.secretKey == "" {
		return nil, errors.New("s3: set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY (and AWS_REGION; AWS_ENDPOINT_URL for S3-compatible stores)")
	}
	return c, nil
}

func (c *s3Client) objectURL(bucket, key string) (*url.URL, error) {
	key = strings.TrimPrefix(key, "/")
	if c.endpoint != "" {
		return url.Parse(strings.TrimRight(c.endpoint, "/") + "/" + bucket + "/" + escapePath(key))
	}
	return url.Parse(fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucket, c.region, escapePath(key)))
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = strings.ReplaceAll(url.PathEscape(s), "+", "%2B")
	}
	return strings.Join(parts, "/")
}

// put uploads one object.
func (c *s3Client) put(ctx context.Context, bucket, key string, body []byte, contentType string) error {
	u, err := c.objectURL(bucket, key)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	c.sign(req, body)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("s3 put %s: HTTP %d: %s", key, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// sign adds the SigV4 Authorization header (service s3, signed payload).
func (c *s3Client) sign(req *http.Request, body []byte) {
	t := c.now().UTC()
	amzDate := t.Format("20060102T150405Z")
	date := t.Format("20060102")
	payload := sha256.Sum256(body)
	payloadHex := hex.EncodeToString(payload[:])
	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHex)
	if c.sessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.sessionToken)
	}
	signed := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	if c.sessionToken != "" {
		signed = append(signed, "x-amz-security-token")
	}
	if req.Header.Get("Content-Type") != "" {
		signed = append([]string{"content-type"}, signed...)
	}
	var canonHeaders strings.Builder
	for _, h := range signed {
		v := req.Header.Get(h)
		if h == "host" {
			v = req.URL.Host
		}
		canonHeaders.WriteString(h + ":" + strings.TrimSpace(v) + "\n")
	}
	canonPath := req.URL.EscapedPath()
	if canonPath == "" {
		canonPath = "/"
	}
	canonical := strings.Join([]string{req.Method, canonPath, canonicalQuery(req.URL.Query()), canonHeaders.String(), strings.Join(signed, ";"), payloadHex}, "\n")
	scope := date + "/" + c.region + "/s3/aws4_request"
	canonHash := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, hex.EncodeToString(canonHash[:])}, "\n")
	kDate := hmacSHA256([]byte("AWS4"+c.secretKey), date)
	kRegion := hmacSHA256(kDate, c.region)
	kService := hmacSHA256(kRegion, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(kSigning, toSign))
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", c.accessKey, scope, strings.Join(signed, ";"), sig))
}

func canonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	return strings.ReplaceAll(q.Encode(), "+", "%20")
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// uploadDir puts every file under dir at prefix/<relative path>, the manifest
// last so its presence means the backup is complete.
func (c *s3Client) uploadDir(ctx context.Context, t s3Target, dir string, report func(key string, size int)) error {
	put := func(rel string, data []byte) error {
		key := path.Join(t.prefix, rel)
		if report != nil {
			report(key, len(data))
		}
		return c.put(ctx, t.bucket, key, data, contentTypeFor(rel))
	}
	if err := walkFiles(dir, put); err != nil {
		return err
	}
	mf, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return err
	}
	return put("manifest.json", mf)
}

func contentTypeFor(name string) string {
	switch {
	case strings.HasSuffix(name, ".json"):
		return "application/json"
	case strings.HasSuffix(name, ".jsonl"):
		return "application/x-ndjson"
	}
	return "application/octet-stream"
}
