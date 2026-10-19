// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// The GitHub payload shapes the importer reads (list endpoints).

type ghIssueFull struct {
	ghIssue
	Labels    []ghLabel `json:"labels"`
	Milestone *struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	} `json:"milestone"`
	Comments  int    `json:"comments"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	ClosedAt  string `json:"closed_at"`
}

type ghComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	User      ghUser `json:"user"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	HTMLURL   string `json:"html_url"`
}

type ghPullFull struct {
	ghPullRequest
	MergedAt  string `json:"merged_at"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	ClosedAt  string `json:"closed_at"`
}

type ghReviewComment struct {
	ID                  int64  `json:"id"`
	PullRequestReviewID int64  `json:"pull_request_review_id"`
	InReplyToID         int64  `json:"in_reply_to_id"`
	Path                string `json:"path"`
	Line                *int   `json:"line"`
	OriginalLine        *int   `json:"original_line"`
	StartLine           *int   `json:"start_line"`
	Position            *int   `json:"position"`
	OriginalPosition    *int   `json:"original_position"`
	CommitID            string `json:"commit_id"`
	OriginalCommitID    string `json:"original_commit_id"`
	Body                string `json:"body"`
	User                ghUser `json:"user"`
	CreatedAt           string `json:"created_at"`
	HTMLURL             string `json:"html_url"`
}

type ghReview struct {
	ID          int64  `json:"id"`
	User        ghUser `json:"user"`
	Body        string `json:"body"`
	State       string `json:"state"` // APPROVED | CHANGES_REQUESTED | COMMENTED | DISMISSED | PENDING
	CommitID    string `json:"commit_id"`
	SubmittedAt string `json:"submitted_at"`
	HTMLURL     string `json:"html_url"`
}

type ghTimelineEvent struct {
	Event     string `json:"event"`
	CommitID  string `json:"commit_id"`
	SHA       string `json:"sha"`
	CreatedAt string `json:"created_at"`
	Actor     ghUser `json:"actor"`
	// Newer payloads carry the commits around a force push.
	BeforeCommit *struct {
		SHA string `json:"sha"`
	} `json:"before_commit"`
	AfterCommit *struct {
		SHA string `json:"sha"`
	} `json:"after_commit"`
}

type ghAsset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	ContentType        string `json:"content_type"`
	URL                string `json:"url"`
	BrowserDownloadURL string `json:"browser_download_url"`
	CreatedAt          string `json:"created_at"`
}

type ghRelease struct {
	ID          int64     `json:"id"`
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	Author      ghUser    `json:"author"`
	HTMLURL     string    `json:"html_url"`
	CreatedAt   string    `json:"created_at"`
	PublishedAt string    `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
}

type ghLabel struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
	Default     bool   `json:"default"`
}

type ghMilestone struct {
	ID           int64  `json:"id"`
	Number       int    `json:"number"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	State        string `json:"state"`
	DueOn        string `json:"due_on"`
	CreatedAt    string `json:"created_at"`
	ClosedAt     string `json:"closed_at"`
	OpenIssues   int    `json:"open_issues"`
	ClosedIssues int    `json:"closed_issues"`
}

type ghHook struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	Active bool     `json:"active"`
	Events []string `json:"events"`
	Config struct {
		URL         string `json:"url"`
		ContentType string `json:"content_type"`
		InsecureSSL string `json:"insecure_ssl"`
	} `json:"config"`
	CreatedAt string `json:"created_at"`
}

type ghContributor struct {
	Login         string `json:"login"`
	ID            int64  `json:"id"`
	Type          string `json:"type"`
	Contributions int    `json:"contributions"`
}

type ghContent struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	SHA      string `json:"sha"`
	Size     int64  `json:"size"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

type ghRepoFull struct {
	repoResponse
	Size            int64 `json:"size"`
	HasWiki         bool  `json:"has_wiki"`
	HasIssues       bool  `json:"has_issues"`
	OpenIssuesCount int   `json:"open_issues_count"`
	Archived        bool  `json:"archived"`
	Fork            bool  `json:"fork"`
}

// doRaw performs a request and returns the headers and body, with the same
// rate-limit and error mapping as do.
func (c *Client) doRaw(ctx context.Context, method, path string, in any, accept string) (http.Header, []byte, error) {
	tok, err := c.Token()
	if err != nil {
		return nil, nil, err
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, nil, err
		}
		body = strings.NewReader(string(b))
	}
	full := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		full = c.BaseURL + path
	}
	req, err := http.NewRequestWithContext(ctx, method, full, body)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", c.UserAgent)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0") {
		return resp.Header, nil, &RateLimitError{RetryAfter: retryAfter(resp), Method: method, Path: path}
	}
	if resp.StatusCode >= 400 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(data))
		}
		return resp.Header, nil, &APIError{Status: resp.StatusCode, Message: e.Message, Method: method, Path: path}
	}
	return resp.Header, data, nil
}

// pagePath appends page and per_page to a path that may already have a query.
func pagePath(path string, page, per int) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%sper_page=%d&page=%d", path, sep, per, page)
}

const perPage = 100

// getPage fetches one page of a list endpoint into out and returns the headers.
func (c *Client) getPage(ctx context.Context, path string, page int, out any) (http.Header, error) {
	h, data, err := c.doRaw(ctx, http.MethodGet, pagePath(path, page, perPage), nil, "")
	if err != nil {
		return h, err
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return h, fmt.Errorf("github: %s: %w", path, err)
		}
	}
	return h, nil
}

var lastPageRe = regexp.MustCompile(`<[^>]*[?&]page=(\d+)[^>]*>;\s*rel="last"`)

// lastPage reads the rel="last" page number from a Link header (0 when absent).
func lastPage(h http.Header) int {
	m := lastPageRe.FindStringSubmatch(h.Get("Link"))
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// count returns the number of items of a list endpoint from its first and
// last page (two requests) instead of walking every page.
func (c *Client) count(ctx context.Context, path string) (int, error) {
	var first []json.RawMessage
	h, err := c.getPage(ctx, path, 1, &first)
	if err != nil {
		return 0, err
	}
	last := lastPage(h)
	if last <= 1 {
		return len(first), nil
	}
	var tail []json.RawMessage
	if _, err := c.getPage(ctx, path, last, &tail); err != nil {
		return 0, err
	}
	return (last-1)*perPage + len(tail), nil
}

// listAll walks every page of a list endpoint (for small lists: labels, milestones, releases, hooks, contributors).
func listAll[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	var out []T
	for page := 1; page < 1000; page++ {
		var items []T
		if _, err := c.getPage(ctx, path, page, &items); err != nil {
			return out, err
		}
		out = append(out, items...)
		if len(items) < perPage {
			break
		}
	}
	return out, nil
}

func repoPath(owner, name string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

// RepoFull fetches the repository with the fields the planner needs.
func (c *Client) RepoFull(ctx context.Context, owner, name string) (ghRepoFull, error) {
	var r ghRepoFull
	_, data, err := c.doRaw(ctx, http.MethodGet, repoPath(owner, name), nil, "")
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(data, &r)
}

// Contents reads a file or directory through the contents API. ref "" is the default branch.
func (c *Client) Contents(ctx context.Context, owner, name, path, ref string) (file *ghContent, dir []ghContent, err error) {
	p := repoPath(owner, name) + "/contents/" + strings.TrimPrefix(path, "/")
	if ref != "" {
		p += "?ref=" + url.QueryEscape(ref)
	}
	_, data, err := c.doRaw(ctx, http.MethodGet, p, nil, "")
	if err != nil {
		return nil, nil, err
	}
	if len(data) > 0 && data[0] == '[' {
		return nil, dir, json.Unmarshal(data, &dir)
	}
	var f ghContent
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, nil, err
	}
	return &f, nil, nil
}

// Decoded returns a file's content.
func (f *ghContent) Decoded() ([]byte, error) {
	if f.Encoding == "base64" {
		return base64.StdEncoding.DecodeString(strings.ReplaceAll(f.Content, "\n", ""))
	}
	return []byte(f.Content), nil
}

// BranchProtection reports whether a branch is protected: (settings, true) or (nil, false) on 404.
func (c *Client) BranchProtection(ctx context.Context, owner, name, branch string) (map[string]any, bool, error) {
	_, data, err := c.doRaw(ctx, http.MethodGet, repoPath(owner, name)+"/branches/"+url.PathEscape(branch)+"/protection", nil, "")
	if err != nil {
		var apiErr *APIError
		if asAPI(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil, false, nil
		}
		return nil, false, err
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	return m, true, nil
}

func asAPI(err error, target **APIError) bool {
	e, ok := err.(*APIError)
	if ok {
		*target = e
	}
	return ok
}

// Download streams a release asset (or any URL) to dest and returns its size and SHA-256.
func (c *Client) Download(ctx context.Context, assetURL, dest string) (int64, string, error) {
	tok, err := c.Token()
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("download %s: %w", assetURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return 0, "", &RateLimitError{RetryAfter: retryAfter(resp), Method: "GET", Path: assetURL}
	}
	if resp.StatusCode >= 400 {
		return 0, "", &APIError{Status: resp.StatusCode, Message: "asset download failed", Method: "GET", Path: assetURL}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, "", err
	}
	f, err := os.Create(dest)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dest)
		return 0, "", fmt.Errorf("download %s: %w", assetURL, err)
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}
