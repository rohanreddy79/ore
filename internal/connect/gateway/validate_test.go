// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseEnvelope(t *testing.T) {
	ok := `{"version":"0","session":{"agent_id":"a","session_id":"s","repository_id":"r"},"requests":[{"kind":"open_issue","title":"t","body":""}]}`
	cases := []struct {
		name, body, wantErr string
	}{
		{"valid", ok, ""},
		{"not json", `{`, "envelope"},
		{"wrong version", strings.Replace(ok, `"version":"0"`, `"version":"1"`, 1), "version must be"},
		{"missing session field", strings.Replace(ok, `"repository_id":"r"`, `"repository_id":""`, 1), "session.agent_id"},
		{"unknown envelope field", strings.Replace(ok, `"version":"0"`, `"version":"0","extra":1`, 1), "unknown field"},
		{"no requests", strings.Replace(ok, `[{"kind":"open_issue","title":"t","body":""}]`, `[]`, 1), "at least 1"},
		{"too many requests", `{"version":"0","session":{"agent_id":"a","session_id":"s","repository_id":"r"},"requests":[` + strings.Repeat(`{"kind":"comment"},`, 50) + `{"kind":"comment"}]}`, "at most 50"},
		{"trailing data", ok + ` {}`, "trailing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseEnvelope([]byte(c.body))
			if c.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr) || !IsValidationError(err)) {
				t.Fatalf("want validation error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestParseRequest(t *testing.T) {
	prov := `"provenance":{"author_kind":"agent","agent_id":"bot","model":"m"}`
	cases := []struct {
		name, body, wantErr string
	}{
		{"comment ok", `{"kind":"comment","target":"issues/1","body":"hi"}`, ""},
		{"comment at limit", `{"kind":"comment","target":"issues/1","body":"` + strings.Repeat("x", MaxCommentBytes) + `"}`, ""},
		{"comment over limit", `{"kind":"comment","target":"issues/1","body":"` + strings.Repeat("x", MaxCommentBytes+1) + `"}`, "limit is 65536"},
		{"comment missing body", `{"kind":"comment","target":"issues/1"}`, "body is required"},
		{"comment unknown property", `{"kind":"comment","target":"issues/1","body":"","token":"secret"}`, `unknown property "token"`},
		{"comment wrong type", `{"kind":"comment","target":1,"body":""}`, "comment:"},
		{"label ok", `{"kind":"label","target":"issues/1","add":["bug"]}`, ""},
		{"label missing add", `{"kind":"label","target":"issues/1","remove":["bug"]}`, "add is required"},
		{"open_issue ok", `{"kind":"open_issue","title":"t","body":"b","labels":["x"]}`, ""},
		{"open_issue blank title", `{"kind":"open_issue","title":"  ","body":"b"}`, "title must not be empty"},
		{"open_issue long title", `{"kind":"open_issue","title":"` + strings.Repeat("t", MaxTitleLength+1) + `","body":"b"}`, "longer than 256"},
		{"open_change ok", `{"kind":"open_change","branch":"feat","title":"t","body":"b",` + prov + `}`, ""},
		{"open_change with base and draft", `{"kind":"open_change","branch":"feat","base":"main","title":"t","body":"b","draft":false,` + prov + `}`, ""},
		{"open_change missing provenance", `{"kind":"open_change","branch":"feat","title":"t","body":"b"}`, "provenance is required"},
		{"open_change bad author_kind", `{"kind":"open_change","branch":"feat","title":"t","body":"b","provenance":{"author_kind":"robot","agent_id":"a","model":"m"}}`, "author_kind"},
		{"open_change missing model", `{"kind":"open_change","branch":"feat","title":"t","body":"b","provenance":{"author_kind":"agent","agent_id":"a"}}`, "model are required"},
		{"push ok shape", `{"kind":"push","branch":"feat","bundle":"obj://x",` + prov + `}`, ""},
		{"review ok", `{"kind":"review","target":"changes/1","verdict":"comment","findings":[]}`, ""},
		{"review bad verdict", `{"kind":"review","target":"changes/1","verdict":"ship","findings":[]}`, "verdict"},
		{"request_review ok", `{"kind":"request_review","target":"changes/1","reviewers":["alice"]}`, ""},
		{"request_review no reviewers", `{"kind":"request_review","target":"changes/1","reviewers":[]}`, "reviewers must not be empty"},
		{"request_deployment ok", `{"kind":"request_deployment","release":"abc","environment":"preview"}`, ""},
		{"request_deployment missing env", `{"kind":"request_deployment","release":"abc"}`, "environment is required"},
		{"unknown kind", `{"kind":"delete_repo"}`, "unknown kind"},
		{"missing kind", `{"target":"issues/1"}`, "kind is required"},
		{"not an object", `"comment"`, "must be an object"},
		{"null required", `{"kind":"open_issue","title":null,"body":""}`, "title is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := ParseRequest(json.RawMessage(c.body))
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if req.Kind == "" {
					t.Fatal("kind not populated")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) || !IsValidationError(err) {
				t.Fatalf("want validation error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestKindsCoverSchema(t *testing.T) {
	for _, k := range Kinds {
		if _, ok := kindFields[k]; !ok {
			t.Fatalf("kind %s has no field spec", k)
		}
	}
	if len(Kinds) != len(kindFields) {
		t.Fatalf("Kinds (%d) and kindFields (%d) differ", len(Kinds), len(kindFields))
	}
}
