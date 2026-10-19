// SPDX-License-Identifier: AGPL-3.0-only

package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// Client talks to one Ore server with a bearer token.
type Client struct {
	Base  string
	Token string
	HTTP  *http.Client
}

// NewClient returns a client for base with token.
func NewClient(base, token string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), Token: token, HTTP: &http.Client{Timeout: 30 * time.Minute}}
}

// APIError is an HTTP error answer; Body is the decoded JSON when it was JSON.
type APIError struct {
	Status  int
	Message string
	Body    map[string]any
}

func (e *APIError) Error() string { return e.Message }

// ErrUnreachable wraps transport failures: the server is down, the network
// is off, the name does not resolve. Saves queue on it.
var ErrUnreachable = errors.New("server unreachable")

// Unreachable reports whether err means the server could not be reached.
func Unreachable(err error) bool { return errors.Is(err, ErrUnreachable) }

// do performs a JSON request. out may be *string (raw body), *http.Header
// is not supported; nil discards the body.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return c.transportErr(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return c.transportErr(err)
	}
	if resp.StatusCode >= 400 {
		if resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504 {
			return fmt.Errorf("%w: %s %s: HTTP %d", ErrUnreachable, method, path, resp.StatusCode)
		}
		ae := &APIError{Status: resp.StatusCode, Message: fmt.Sprintf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))}
		var m map[string]any
		if json.Unmarshal(data, &m) == nil {
			ae.Body = m
			if msg, ok := m["error"].(string); ok && msg != "" {
				ae.Message = fmt.Sprintf("%s %s: %s", method, path, msg)
			}
		}
		return ae
	}
	switch o := out.(type) {
	case nil:
		return nil
	case *string:
		*o = string(data)
		return nil
	default:
		return json.Unmarshal(data, out)
	}
}

func (c *Client) transportErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var ne net.Error
	if errors.As(err, &ne) || strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "no such host") ||
		strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "connection reset") || strings.Contains(err.Error(), "network is unreachable") {
		return fmt.Errorf("%w: %s: %v", ErrUnreachable, c.Base, err)
	}
	return err
}

// Status is the HTTP status of an API error, or 0.
func Status(err error) int {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

func (c *Client) repoPath(org, repo string) string { return "/api/v0/repos/" + org + "/" + repo }

// Repo fetches a repository.
func (c *Client) Repo(ctx context.Context, org, repo string) (ledger.Repo, error) {
	var r ledger.Repo
	err := c.do(ctx, "GET", c.repoPath(org, repo), nil, &r)
	return r, err
}

// Refs fetches a repository's refs.
func (c *Client) Refs(ctx context.Context, org, repo string) (map[string]string, error) {
	var refs map[string]string
	err := c.do(ctx, "GET", c.repoPath(org, repo)+"/refs", nil, &refs)
	return refs, err
}

// Change fetches a change by number.
func (c *Client) Change(ctx context.Context, org, repo string, n int) (ledger.Change, error) {
	var ch ledger.Change
	err := c.do(ctx, "GET", fmt.Sprintf("%s/changes/%d", c.repoPath(org, repo), n), nil, &ch)
	return ch, err
}

// CommitRequest is POST .../commits.
type CommitRequest struct {
	Branch  string     `json:"branch"`
	Message string     `json:"message"`
	Base    string     `json:"base,omitempty"`
	Merge   string     `json:"merge,omitempty"`
	Files   []FileEdit `json:"files"`
}

// CommitResult is the answer.
type CommitResult struct {
	Commit string `json:"commit"`
	Ref    string `json:"ref"`
	Parent string `json:"parent"`
}

// ErrBranchMoved means the compare-and-swap base no longer matches the tip.
var ErrBranchMoved = errors.New("branch moved")

// ErrMergeConflict means a merge needs resolutions.
var ErrMergeConflict = errors.New("merge conflicts")

// MergeConflictError lists the paths a server-side merge could not resolve.
type MergeConflictError struct {
	Conflicts  []string
	Unresolved []string
	Tip        string
}

func (e *MergeConflictError) Error() string {
	return fmt.Sprintf("merge conflicts in %d path(s)", len(e.Unresolved))
}

// Unwrap exposes ErrMergeConflict.
func (e *MergeConflictError) Unwrap() error { return ErrMergeConflict }

// Commit creates a server-side commit. A stale base returns ErrBranchMoved
// (wrapped); unresolved merge conflicts return a *MergeConflictError.
func (c *Client) Commit(ctx context.Context, org, repo string, req CommitRequest) (CommitResult, error) {
	if req.Files == nil {
		req.Files = []FileEdit{}
	}
	var res CommitResult
	err := c.do(ctx, "POST", c.repoPath(org, repo)+"/commits", req, &res)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == 409 && ae.Body != nil {
		if msg, _ := ae.Body["error"].(string); strings.HasPrefix(msg, "branch moved") {
			tip, _ := ae.Body["tip"].(string)
			return res, fmt.Errorf("%w: tip %s", ErrBranchMoved, tip)
		}
		if _, ok := ae.Body["unresolved"]; ok {
			me := &MergeConflictError{Tip: str(ae.Body["tip"])}
			for _, v := range list(ae.Body["conflicts"]) {
				me.Conflicts = append(me.Conflicts, v)
			}
			for _, v := range list(ae.Body["unresolved"]) {
				me.Unresolved = append(me.Unresolved, v)
			}
			return res, me
		}
	}
	return res, err
}

func str(v any) string { s, _ := v.(string); return s }

func list(v any) []string {
	arr, _ := v.([]any)
	var out []string
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// OpenChange opens a change from a branch.
func (c *Client) OpenChange(ctx context.Context, org, repo, branch, title, body string) (ledger.Change, error) {
	var ch ledger.Change
	err := c.do(ctx, "POST", c.repoPath(org, repo)+"/changes", map[string]string{"source_ref": "refs/heads/" + branch, "title": title, "body": body}, &ch)
	return ch, err
}

// DiffResult is GET .../diff.
type DiffResult struct {
	From      string           `json:"from"`
	To        string           `json:"to"`
	MergeBase string           `json:"merge_base"`
	Files     []gitx.DiffEntry `json:"files"`
}

// Diff lists the paths that differ between two commits, within paths.
func (c *Client) Diff(ctx context.Context, org, repo, from, to string, paths []string) (DiffResult, error) {
	q := url.Values{"from": {from}, "to": {to}}
	for _, p := range paths {
		q.Add("path", p)
	}
	var d DiffResult
	err := c.do(ctx, "GET", c.repoPath(org, repo)+"/diff?"+q.Encode(), nil, &d)
	return d, err
}

// Archive opens a tar stream of ref: explicit paths, the whole tree (all),
// or the active area (neither). It returns the stream, the resolved commit
// and the paths the server chose; the caller closes the stream and checks
// its trailer through ArchiveError.
func (c *Client) Archive(ctx context.Context, org, repo, ref string, paths []string, all bool) (*http.Response, string, []string, error) {
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	for _, p := range paths {
		q.Add("path", p)
	}
	if all {
		q.Set("all", "1")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.Base+c.repoPath(org, repo)+"/archive?"+q.Encode(), nil)
	if err != nil {
		return nil, "", nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", nil, c.transportErr(err)
	}
	if resp.StatusCode != 200 {
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		ae := &APIError{Status: resp.StatusCode, Message: fmt.Sprintf("GET archive: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))}
		var m map[string]any
		if json.Unmarshal(data, &m) == nil {
			if msg, ok := m["error"].(string); ok {
				ae.Message = "GET archive: " + msg
			}
		}
		return nil, "", nil, ae
	}
	var chosen []string
	if raw := resp.Header.Get("X-Ore-Archive-Paths"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &chosen)
	}
	return resp, resp.Header.Get("X-Ore-Commit"), chosen, nil
}

// ArchiveError reads the stream's trailer once the body is drained.
func ArchiveError(resp *http.Response) error {
	if msg := resp.Trailer.Get("X-Ore-Archive-Error"); msg != "" {
		return errors.New("archive: " + msg)
	}
	return nil
}

// File reads one file at ref.
func (c *Client) File(ctx context.Context, org, repo, ref, path string) ([]byte, error) {
	q := url.Values{"ref": {ref}, "path": {path}}
	var s string
	if err := c.do(ctx, "GET", c.repoPath(org, repo)+"/file?"+q.Encode(), nil, &s); err != nil {
		return nil, err
	}
	return []byte(s), nil
}

// Land asks for a landing with a strategy through review v1; a server
// without it answers 404 and the caller falls back to Merge.
func (c *Client) Land(ctx context.Context, org, repo string, n int, strategy string) (map[string]any, error) {
	var out map[string]any
	err := c.do(ctx, "POST", fmt.Sprintf("%s/changes/%d/land", c.repoPath(org, repo), n), map[string]string{"strategy": strategy}, &out)
	return out, err
}

// Merge is the v0 landing (queue strategy, no policy).
func (c *Client) Merge(ctx context.Context, org, repo string, n int) (map[string]any, error) {
	var out map[string]any
	err := c.do(ctx, "POST", fmt.Sprintf("%s/changes/%d/merge", c.repoPath(org, repo), n), map[string]string{}, &out)
	return out, err
}

// PatchSets lists a change's patch sets (empty on a server without review v1).
func (c *Client) PatchSets(ctx context.Context, org, repo string, n int) ([]map[string]any, error) {
	var out []map[string]any
	err := c.do(ctx, "GET", fmt.Sprintf("%s/changes/%d/patchsets", c.repoPath(org, repo), n), nil, &out)
	if Status(err) == 404 {
		return nil, nil
	}
	return out, err
}

// Runs lists recent pipeline runs.
func (c *Client) Runs(ctx context.Context, org, repo string) ([]ledger.PipelineRun, error) {
	var out []ledger.PipelineRun
	err := c.do(ctx, "GET", c.repoPath(org, repo)+"/runs", nil, &out)
	return out, err
}

// Deployments lists recent deployments.
func (c *Client) Deployments(ctx context.Context, org, repo string) ([]ledger.Deployment, error) {
	var out []ledger.Deployment
	err := c.do(ctx, "GET", c.repoPath(org, repo)+"/deployments", nil, &out)
	return out, err
}

// Whoami returns the actor behind the token through the identity extension,
// or "" on a server without it.
func (c *Client) Whoami(ctx context.Context) (string, error) {
	var me struct {
		Actor ledger.Actor `json:"actor"`
	}
	err := c.do(ctx, "GET", "/api/v0/identity/me", nil, &me)
	if Status(err) == 404 {
		return "", c.do(ctx, "GET", "/api/v0/orgs", nil, nil)
	}
	return me.Actor.ID, err
}
