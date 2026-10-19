// SPDX-License-Identifier: AGPL-3.0-only

package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// S3Config names an S3-compatible bucket. With Endpoint set, requests are
// path-style (<endpoint>/<bucket>/<key>); without it, the AWS virtual-host
// form (https://<bucket>.s3.<region>.amazonaws.com/<key>).
type S3Config struct {
	Bucket   string `json:"bucket"`
	Endpoint string `json:"endpoint,omitempty"`
	Region   string `json:"region,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
}

func (c S3Config) objectURL(key string) (string, error) {
	region := c.Region
	if region == "" {
		region = "us-east-1"
	}
	if c.Endpoint != "" {
		return strings.TrimRight(c.Endpoint, "/") + "/" + c.Bucket + "/" + key, nil
	}
	if c.Bucket == "" {
		return "", fmt.Errorf("s3: bucket is required")
	}
	return "https://" + c.Bucket + ".s3." + region + ".amazonaws.com/" + key, nil
}

// S3Put uploads one object with AWS Signature Version 4 (standard library only).
func S3Put(ctx context.Context, client *http.Client, cfg S3Config, accessKey, secretKey, key string, body []byte, contentType string, now time.Time) error {
	if accessKey == "" || secretKey == "" {
		return fmt.Errorf("s3: credentials are not configured")
	}
	u, err := cfg.objectURL(key)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", contentType)
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	SigV4Sign(req, accessKey, secretKey, region, "s3", sha256Hex(body), now)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("s3: PUT %s: HTTP %d: %s", key, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// SigV4Sign adds x-amz-date, x-amz-content-sha256 and Authorization to req
// (AWS Signature Version 4, header authentication, no session token).
func SigV4Sign(req *http.Request, accessKey, secretKey, region, service, payloadHash string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Host = req.URL.Host
	sig, signed, scope := SigV4Signature(req, secretKey, region, service, payloadHash, amzDate)
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", accessKey, scope, signed, sig))
}

// SigV4Signature computes the signature of req as sent. The signed headers
// are host, every x-amz-* header, and content-type, date and range when
// present (AWS's own examples sign exactly these). It returns the hex
// signature, the signed-headers list and the credential scope; a receiver
// recomputes it to verify.
func SigV4Signature(req *http.Request, secretKey, region, service, payloadHash, amzDate string) (sig, signedHeaders, scope string) {
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	headers := map[string]string{"host": host, "x-amz-date": amzDate, "x-amz-content-sha256": payloadHash}
	for name, vals := range req.Header {
		lname := strings.ToLower(name)
		if len(vals) == 0 {
			continue
		}
		if strings.HasPrefix(lname, "x-amz-") || lname == "content-type" || lname == "date" || lname == "range" {
			headers[lname] = strings.Join(vals, ",")
		}
	}
	names := make([]string, 0, len(headers))
	for n := range headers {
		names = append(names, n)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, n := range names {
		canonHeaders.WriteString(n + ":" + strings.Join(strings.Fields(headers[n]), " ") + "\n")
	}
	signedHeaders = strings.Join(names, ";")
	canonicalRequest := strings.Join([]string{req.Method, canonicalURI(req.URL.EscapedPath()), canonicalQuery(req.URL.Query()), canonHeaders.String(), signedHeaders, payloadHash}, "\n")
	date := amzDate[:8]
	scope = date + "/" + region + "/" + service + "/aws4_request"
	stringToSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, sha256Hex([]byte(canonicalRequest))}, "\n")
	k := hmacSHA256([]byte("AWS4"+secretKey), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	return hex.EncodeToString(hmacSHA256(k, stringToSign)), signedHeaders, scope
}

// canonicalURI re-encodes an escaped path segment by segment the way S3
// expects (unreserved characters bare, everything else %XX, '/' kept).
func canonicalURI(p string) string {
	if p == "" {
		return "/"
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if u, err := url.PathUnescape(s); err == nil {
			s = u
		}
		segs[i] = awsEscape(s)
	}
	return strings.Join(segs, "/")
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := append([]string{}, q[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, awsEscape(k)+"="+awsEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// awsEscape percent-encodes everything but the unreserved characters, with uppercase hex.
func awsEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
