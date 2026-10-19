// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Limits mirror spec/safe-outputs/safe-outputs.schema.json.
const (
	MaxRequests      = 50
	MaxTitleLength   = 256
	MaxCommentBytes  = 65536
	SchemaVersion    = "0"
	maxReviewerCount = 50
)

// Envelope is a safe-outputs request (schema version 0).
type Envelope struct {
	Version  string            `json:"version"`
	Session  SessionRef        `json:"session"`
	Requests []json.RawMessage `json:"requests"`
}

// SessionRef names the session a request claims to come from.
type SessionRef struct {
	AgentID      string `json:"agent_id"`
	SessionID    string `json:"session_id"`
	RepositoryID string `json:"repository_id"`
}

// Provenance is the structured authorship record carried by changes.
type Provenance struct {
	AuthorKind string `json:"author_kind"`
	AgentID    string `json:"agent_id"`
	Model      string `json:"model"`
	SessionID  string `json:"session_id,omitempty"`
	PromptHash string `json:"prompt_hash,omitempty"`
	HumanOwner string `json:"human_owner,omitempty"`
}

// Request is one validated write request. Exactly the fields of the matching
// schema variant are populated; Kind selects the variant.
type Request struct {
	Kind string `json:"kind"`

	// comment, review, request_review, label
	Target string `json:"target,omitempty"`
	// comment, open_issue, open_change
	Body string `json:"body,omitempty"`
	// open_issue, open_change
	Title string `json:"title,omitempty"`
	// open_issue
	Labels []string `json:"labels,omitempty"`
	// open_change, push
	Branch string `json:"branch,omitempty"`
	// open_change
	Base       string      `json:"base,omitempty"`
	Draft      *bool       `json:"draft,omitempty"`
	Provenance *Provenance `json:"provenance,omitempty"`
	// push
	Bundle string `json:"bundle,omitempty"`
	// request_review
	Reviewers []string `json:"reviewers,omitempty"`
	// request_deployment
	Release     string `json:"release,omitempty"`
	Environment string `json:"environment,omitempty"`
	// label
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
	// review
	Verdict  string            `json:"verdict,omitempty"`
	Findings []json.RawMessage `json:"findings,omitempty"`
}

// ValidationError reports why an envelope or a request does not match the schema.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return "invalid request: " + e.Msg }

func invalid(format string, a ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, a...)}
}

// IsValidationError reports whether err is a schema-level failure.
func IsValidationError(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

// ParseEnvelope decodes and validates the envelope shape: version, session
// and the request count. The individual requests are validated by
// ParseRequest so that one bad request refuses only itself.
func ParseEnvelope(body []byte) (Envelope, error) {
	var env Envelope
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		return env, invalid("envelope: %v", err)
	}
	if dec.More() {
		return env, invalid("envelope: trailing data")
	}
	if env.Version != SchemaVersion {
		return env, invalid("version must be %q", SchemaVersion)
	}
	if env.Session.AgentID == "" || env.Session.SessionID == "" || env.Session.RepositoryID == "" {
		return env, invalid("session.agent_id, session.session_id and session.repository_id are required")
	}
	if len(env.Requests) == 0 {
		return env, invalid("requests must contain at least 1 item")
	}
	if len(env.Requests) > MaxRequests {
		return env, invalid("requests must contain at most %d items", MaxRequests)
	}
	return env, nil
}

// kindFields lists the properties each schema variant allows (additionalProperties: false)
// and which of them are required.
var kindFields = map[string]struct{ allowed, required []string }{
	"comment":            {[]string{"kind", "target", "body"}, []string{"target", "body"}},
	"label":              {[]string{"kind", "target", "add", "remove"}, []string{"target", "add"}},
	"open_issue":         {[]string{"kind", "title", "body", "labels"}, []string{"title", "body"}},
	"open_change":        {[]string{"kind", "branch", "base", "title", "body", "draft", "provenance"}, []string{"branch", "title", "body", "provenance"}},
	"push":               {[]string{"kind", "branch", "bundle", "provenance"}, []string{"branch", "bundle", "provenance"}},
	"review":             {[]string{"kind", "target", "verdict", "findings"}, []string{"target", "findings"}},
	"request_review":     {[]string{"kind", "target", "reviewers"}, []string{"target", "reviewers"}},
	"request_deployment": {[]string{"kind", "release", "environment"}, []string{"release", "environment"}},
}

// Kinds lists the request kinds the schema defines, in schema order.
var Kinds = []string{"comment", "label", "open_issue", "open_change", "push", "review", "request_review", "request_deployment"}

// ParseRequest validates one request against its schema variant: known kind,
// no unknown properties, required properties present and typed, length limits.
func ParseRequest(raw json.RawMessage) (Request, error) {
	var props map[string]json.RawMessage
	if err := json.Unmarshal(raw, &props); err != nil || props == nil {
		return Request{}, invalid("request must be an object")
	}
	kindRaw, ok := props["kind"]
	if !ok {
		return Request{}, invalid("kind is required")
	}
	var kind string
	if err := json.Unmarshal(kindRaw, &kind); err != nil {
		return Request{}, invalid("kind must be a string")
	}
	spec, ok := kindFields[kind]
	if !ok {
		return Request{}, invalid("unknown kind %q (one of %s)", kind, strings.Join(Kinds, ", "))
	}
	for name := range props {
		if !contains(spec.allowed, name) {
			return Request{}, invalid("%s: unknown property %q", kind, name)
		}
	}
	for _, name := range spec.required {
		if v, ok := props[name]; !ok || string(v) == "null" {
			return Request{}, invalid("%s: %s is required", kind, name)
		}
	}
	var req Request
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return Request{}, invalid("%s: %v", kind, err)
	}
	switch kind {
	case "comment":
		if req.Target == "" {
			return req, invalid("comment: target must not be empty")
		}
		if len(req.Body) > MaxCommentBytes {
			return req, invalid("comment: body is %d bytes, the limit is %d", len(req.Body), MaxCommentBytes)
		}
	case "open_issue":
		if strings.TrimSpace(req.Title) == "" {
			return req, invalid("open_issue: title must not be empty")
		}
		if len([]rune(req.Title)) > MaxTitleLength {
			return req, invalid("open_issue: title is longer than %d characters", MaxTitleLength)
		}
	case "open_change":
		if req.Branch == "" {
			return req, invalid("open_change: branch must not be empty")
		}
		if err := validProvenance(req.Provenance); err != nil {
			return req, invalid("open_change: %v", err)
		}
	case "push":
		if err := validProvenance(req.Provenance); err != nil {
			return req, invalid("push: %v", err)
		}
	case "request_review":
		if req.Target == "" {
			return req, invalid("request_review: target must not be empty")
		}
		if len(req.Reviewers) == 0 {
			return req, invalid("request_review: reviewers must not be empty")
		}
		if len(req.Reviewers) > maxReviewerCount {
			return req, invalid("request_review: at most %d reviewers", maxReviewerCount)
		}
		for _, r := range req.Reviewers {
			if strings.TrimSpace(r) == "" {
				return req, invalid("request_review: reviewers must not contain empty names")
			}
		}
	case "request_deployment":
		if req.Release == "" || req.Environment == "" {
			return req, invalid("request_deployment: release and environment must not be empty")
		}
	case "review":
		switch req.Verdict {
		case "", "comment", "request_changes", "approve":
		default:
			return req, invalid("review: verdict must be comment, request_changes or approve")
		}
	}
	return req, nil
}

func validProvenance(p *Provenance) error {
	if p == nil {
		return errors.New("provenance is required")
	}
	switch p.AuthorKind {
	case "agent", "human_with_agent":
	default:
		return errors.New("provenance.author_kind must be agent or human_with_agent")
	}
	if p.AgentID == "" || p.Model == "" {
		return errors.New("provenance.agent_id and provenance.model are required")
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
