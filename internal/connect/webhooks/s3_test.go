// SPDX-License-Identifier: AGPL-3.0-only

package webhooks

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// The two examples from "Authenticating Requests: Using Query Parameters /
// Authorization Header (AWS Signature Version 4)" in the S3 documentation:
// GET Object and PUT Object, with the documented access key, secret, date
// and expected signatures.
func TestSigV4AWSExamples(t *testing.T) {
	const secret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	// GET /test.txt with Range: bytes=0-9, empty payload
	req, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	empty := sha256Hex(nil)
	sig, signed, scope := SigV4Signature(req, secret, "us-east-1", "s3", empty, "20130524T000000Z")
	if signed != "host;range;x-amz-content-sha256;x-amz-date" || scope != "20130524/us-east-1/s3/aws4_request" {
		t.Fatalf("signed %s scope %s", signed, scope)
	}
	if sig != "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41" {
		t.Fatalf("GET signature %s", sig)
	}
	// PUT /test$file.text with a storage class and a Date header, payload "Welcome to Amazon S3."
	body := []byte("Welcome to Amazon S3.")
	req, _ = http.NewRequest("PUT", "https://examplebucket.s3.amazonaws.com/test$file.text", strings.NewReader(string(body)))
	req.Header.Set("Date", "Fri, 24 May 2013 00:00:00 GMT")
	req.Header.Set("x-amz-storage-class", "REDUCED_REDUNDANCY")
	sig, signed, _ = SigV4Signature(req, secret, "us-east-1", "s3", sha256Hex(body), "20130524T000000Z")
	if signed != "date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class" {
		t.Fatalf("signed %s", signed)
	}
	if sig != "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd" {
		t.Fatalf("PUT signature %s", sig)
	}
}

func TestSigV4SignSetsHeaders(t *testing.T) {
	req, _ := http.NewRequest("PUT", "http://127.0.0.1:9000/bucket/audit/2026/01/01/events-1-2.ndjson", strings.NewReader("x"))
	req.Header.Set("Content-Type", "application/x-ndjson")
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	SigV4Sign(req, "AKIA", "secret", "us-east-1", "s3", sha256Hex([]byte("x")), now)
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIA/20260101/us-east-1/s3/aws4_request, SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date, Signature=") {
		t.Fatalf("authorization %s", auth)
	}
	if req.Header.Get("x-amz-date") != "20260101T120000Z" || req.Header.Get("x-amz-content-sha256") != sha256Hex([]byte("x")) {
		t.Fatalf("headers %v", req.Header)
	}
	// the receiver recomputes the same signature from the request
	want, _, _ := SigV4Signature(req, "secret", "us-east-1", "s3", req.Header.Get("x-amz-content-sha256"), req.Header.Get("x-amz-date"))
	if !strings.HasSuffix(auth, "Signature="+want) {
		t.Fatal("receiver recomputation differs")
	}
	if canonicalURI("/a b/c$d/%7E") != "/a%20b/c%24d/~" {
		t.Fatalf("canonical uri %s", canonicalURI("/a b/c$d/%7E"))
	}
	u, _ := (S3Config{Bucket: "b", Region: "eu-west-1"}).objectURL("k/1")
	if u != "https://b.s3.eu-west-1.amazonaws.com/k/1" {
		t.Fatalf("virtual host url %s", u)
	}
	u, _ = (S3Config{Bucket: "b", Endpoint: "http://minio:9000/"}).objectURL("k/1")
	if u != "http://minio:9000/b/k/1" {
		t.Fatalf("path style url %s", u)
	}
}
