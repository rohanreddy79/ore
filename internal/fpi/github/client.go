// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/fpi"
)

// ErrNoToken is returned when the adapter has no credential.
var ErrNoToken = errors.New("github: no token configured: set " + EnvToken + " (a personal access token or an installation token) or " + EnvTokenFile)

// TokenSource yields the current token.
type TokenSource func() (string, error)

// StaticToken returns a fixed token.
func StaticToken(t string) TokenSource {
	return func() (string, error) {
		if t == "" {
			return "", ErrNoToken
		}
		return t, nil
	}
}

// FileToken re-reads a token file on every call, so a sidecar that mints
// GitHub App installation tokens (valid for one hour) can rotate it in place.
func FileToken(path string) TokenSource {
	return func() (string, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("github: read %s: %w", EnvTokenFile, err)
		}
		t := strings.TrimSpace(string(b))
		if t == "" {
			return "", ErrNoToken
		}
		return t, nil
	}
}

// APIError is a non-2xx response from GitHub.
type APIError struct {
	Status  int
	Message string
	Method  string
	Path    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github: %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Message)
}

// RateLimitError is a 429, or a 403 with the primary rate limit exhausted.
type RateLimitError struct {
	RetryAfter time.Duration
	Method     string
	Path       string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("github: %s %s: rate limited, retry after %s", e.Method, e.Path, e.RetryAfter)
}

// Client is a minimal GitHub REST client on the standard library.
type Client struct {
	BaseURL   string
	Token     TokenSource
	HTTP      *http.Client
	UserAgent string
}

// NewClient builds a client for the configuration (nil when no token source is configured).
func NewClient(cfg Config) *Client {
	if !cfg.Configured() {
		return nil
	}
	base := cfg.API
	if base == "" {
		base = DefaultAPI
	}
	var ts TokenSource
	if cfg.TokenFile != "" {
		ts = FileToken(cfg.TokenFile)
	} else {
		ts = StaticToken(cfg.Token)
	}
	return &Client{BaseURL: strings.TrimRight(base, "/"), Token: ts, HTTP: &http.Client{Timeout: 30 * time.Second}, UserAgent: "ore-github-adapter"}
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	tok, err := c.Token()
	if err != nil {
		return err
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", c.UserAgent)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0") {
		return &RateLimitError{RetryAfter: retryAfter(resp), Method: method, Path: path}
	}
	if resp.StatusCode >= 400 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(data))
		}
		return &APIError{Status: resp.StatusCode, Message: e.Message, Method: method, Path: path}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func retryAfter(resp *http.Response) time.Duration {
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	if s := resp.Header.Get("X-RateLimit-Reset"); s != "" {
		if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
			if d := time.Until(time.Unix(n, 0)); d > time.Second {
				return d
			}
		}
	}
	return time.Minute
}

type repoResponse struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"html_url"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

// Repo fetches GET /repos/{owner}/{name}.
func (c *Client) Repo(ctx context.Context, owner, name string) (fpi.Repository, error) {
	var r repoResponse
	if err := c.do(ctx, http.MethodGet, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), nil, &r); err != nil {
		return fpi.Repository{}, err
	}
	o := r.Owner.Login
	if o == "" {
		o = owner
	}
	n := r.Name
	if n == "" {
		n = name
	}
	return fpi.Repository{Provider: "github", ID: strconv.FormatInt(r.ID, 10), Owner: o, Name: n, DefaultBranch: r.DefaultBranch, CloneURL: r.CloneURL, HTMLURL: r.HTMLURL, Private: r.Private}, nil
}

// CreateStatus posts a commit status: POST /repos/{owner}/{name}/statuses/{sha}.
func (c *Client) CreateStatus(ctx context.Context, owner, name, sha string, st fpi.Status) error {
	desc := st.Description
	if len(desc) > 140 {
		desc = desc[:137] + "..."
	}
	body := map[string]string{"state": st.State, "context": st.Context, "description": desc}
	if st.TargetURL != "" {
		body["target_url"] = st.TargetURL
	}
	return c.do(ctx, http.MethodPost, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/statuses/"+url.PathEscape(sha), body, nil)
}

// ListIssues reads issues (pull requests are filtered out, as GitHub lists them together).
func (c *Client) ListIssues(ctx context.Context, owner, name string, q fpi.IssueQuery) ([]fpi.Issue, error) {
	state := q.State
	if state == "" {
		state = "open"
	}
	per := q.Limit
	if per <= 0 || per > 100 {
		per = 100
	}
	var raw []ghIssue
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/issues?state=%s&per_page=%d", url.PathEscape(owner), url.PathEscape(name), url.QueryEscape(state), per), nil, &raw); err != nil {
		return nil, err
	}
	var out []fpi.Issue
	for _, i := range raw {
		if i.PullRequest != nil {
			continue
		}
		out = append(out, fpi.Issue{Number: i.Number, Title: i.Title, Body: i.Body, State: i.State, Author: i.User.Login, URL: i.HTMLURL})
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	return out, nil
}

type refResponse struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
}

// ListRefs reads branches and tags: GET /repos/{owner}/{name}/git/matching-refs/{heads,tags}/.
func (c *Client) ListRefs(ctx context.Context, owner, name string) ([]fpi.Ref, error) {
	var out []fpi.Ref
	for _, kind := range []string{"heads", "tags"} {
		var raw []refResponse
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/git/matching-refs/%s/", url.PathEscape(owner), url.PathEscape(name), kind), nil, &raw); err != nil {
			return nil, err
		}
		for _, r := range raw {
			out = append(out, fpi.Ref{Name: r.Ref, OID: r.Object.SHA})
		}
	}
	return out, nil
}

// gitEnv returns the environment that authenticates git transfers to
// cloneURL: the token as HTTP basic credentials in an Authorization header
// scoped to the clone URL's origin (so it is never sent elsewhere), passed
// through git's environment configuration so it never appears on a command
// line or in an error message. Credential helpers are disabled so nothing
// prompts. file:// URLs (tests) need no credentials.
func (c *Client) gitEnv(cloneURL string) ([]string, error) {
	u, err := url.Parse(cloneURL)
	if err != nil {
		return nil, fmt.Errorf("github: clone url %q: %w", cloneURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, nil
	}
	tok, err := c.Token()
	if err != nil {
		return nil, err
	}
	origin := u.Scheme + "://" + u.Host + "/"
	auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
	return []string{
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=http." + origin + ".extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic " + auth,
		"GIT_CONFIG_KEY_1=credential.helper", "GIT_CONFIG_VALUE_1=",
	}, nil
}
