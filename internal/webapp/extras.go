// SPDX-License-Identifier: AGPL-3.0-only

package webapp

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"

	"github.com/orevcs/ore/internal/githttp"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/web"
)

// features tells the app which extensions this server runs, so sections whose
// API is absent are hidden with a hint instead of failing.
func features(s *web.Server, info *Info) map[string]any {
	names := registry.Names()
	has := func(n string) bool {
		for _, x := range names {
			if x == n {
				return true
			}
		}
		return false
	}
	out := map[string]any{
		"extensions":        names,
		"webapp":            info,
		"identity":          has("identity"),
		"review_v1":         has("review.v1"),
		"environments":      has("delivery.environments"),
		"pipelines_actions": has("pipelines.actions"),
		"gateway":           has("connect.gateway"),
		"mcp":               has("connect.mcp"),
		"github":            has("fpi.github"),
		"fleet":             has("agent.fleet"),
		"learn":             has("learn"),
		"webhooks":          has("connect.webhooks"),
		"base_url":          s.BaseURL,
		"levels":            web.Levels,
	}
	if sshd, ok := s.Services["identity.sshd"].(interface{ Addr() net.Addr }); ok && sshd != nil && sshd.Addr() != nil {
		out["ssh_addr"] = sshd.Addr().String()
	}
	return out
}

func repoPath(s *web.Server, repo ledger.Repo) string {
	return githttp.RepoPath(s.ReposDir, repo.Org, repo.Name)
}

// CommitDetail is one commit with its patch.
type CommitDetail struct {
	OID     string   `json:"oid"`
	Author  string   `json:"author"`
	Email   string   `json:"email"`
	Date    string   `json:"date"`
	Parents []string `json:"parents"`
	Subject string   `json:"subject"`
	Body    string   `json:"body"`
	Stat    string   `json:"stat"`
	Diff    string   `json:"diff"`
}

func commitDetail(w http.ResponseWriter, r *http.Request, s *web.Server, repo ledger.Repo, _ ledger.Actor) error {
	oid := r.PathValue("oid")
	if !validRev(oid) {
		return fmt.Errorf("%w: bad revision", errBadRequest)
	}
	rp := repoPath(s, repo)
	ctx := r.Context()
	meta, err := gitx.Run(ctx, rp, nil, "show", "--no-patch", "--format=%H%x1f%an%x1f%ae%x1f%aI%x1f%P%x1f%s%x1f%b", oid)
	if err != nil {
		return err
	}
	f := strings.SplitN(strings.TrimRight(meta, "\n"), "\x1f", 7)
	if len(f) < 7 {
		return fmt.Errorf("unexpected git output")
	}
	d := CommitDetail{OID: f[0], Author: f[1], Email: f[2], Date: f[3], Subject: f[5], Body: strings.TrimSpace(f[6])}
	if p := strings.TrimSpace(f[4]); p != "" {
		d.Parents = strings.Fields(p)
	} else {
		d.Parents = []string{}
	}
	args := []string{"diff-tree", "-p", "-M", "--root", "--no-color"}
	if len(d.Parents) > 1 {
		args = []string{"diff", "-M", "--no-color", d.Parents[0], d.OID}
	} else {
		args = append(args, d.OID)
	}
	if d.Diff, err = gitx.Run(ctx, rp, nil, args...); err != nil {
		return err
	}
	if len(d.Parents) > 1 {
		d.Stat, _ = gitx.Run(ctx, rp, nil, "diff", "--stat", "--no-color", d.Parents[0], d.OID)
	} else {
		d.Stat, _ = gitx.Run(ctx, rp, nil, "diff-tree", "--stat", "--root", "--no-color", d.OID)
	}
	if len(d.Parents) == 0 {
		// diff-tree prints the commit header line first for a root commit; drop it.
		if i := strings.Index(d.Diff, "\ndiff --git"); i >= 0 && !strings.HasPrefix(d.Diff, "diff --git") {
			d.Diff = d.Diff[i+1:]
		}
		if i := strings.Index(d.Stat, "\n"); i >= 0 && !strings.HasPrefix(d.Stat, " ") {
			d.Stat = d.Stat[i+1:]
		}
	}
	return web.JSON(w, 200, d)
}

// Blob is a file at a revision with enough metadata to render it well.
type Blob struct {
	Path       string         `json:"path"`
	Ref        string         `json:"ref"`
	Size       int            `json:"size"`
	Lines      int            `json:"lines"`
	Binary     bool           `json:"binary"`
	Truncated  bool           `json:"truncated"`
	Content    string         `json:"content"`
	LastCommit *gitx.LogEntry `json:"last_commit,omitempty"`
}

const blobLimit = 2 << 20

func blob(w http.ResponseWriter, r *http.Request, s *web.Server, repo ledger.Repo, _ ledger.Actor) error {
	q := r.URL.Query()
	ref, p := q.Get("ref"), q.Get("path")
	if ref == "" {
		ref = "refs/heads/" + repo.DefaultBranch
	}
	if p == "" || strings.Contains(p, "..") || !validRev(ref) {
		return fmt.Errorf("%w: ref and path are required", errBadRequest)
	}
	rp := repoPath(s, repo)
	b, err := gitx.ReadFile(r.Context(), rp, ref, p)
	if err != nil {
		return err
	}
	out := Blob{Path: p, Ref: ref, Size: len(b)}
	head := b
	if len(head) > 8000 {
		head = head[:8000]
	}
	out.Binary = bytes.IndexByte(head, 0) >= 0
	if !out.Binary {
		if len(b) > blobLimit {
			b = b[:blobLimit]
			out.Truncated = true
		}
		out.Content = string(b)
		out.Lines = bytes.Count(b, []byte("\n"))
		if len(b) > 0 && b[len(b)-1] != '\n' {
			out.Lines++
		}
	}
	if entries, err := gitx.Log(r.Context(), rp, ref, 1); err == nil && len(entries) == 1 {
		if lc, err := gitx.Run(r.Context(), rp, nil, "log", "-n1", "--format=%H%x1f%an%x1f%ae%x1f%aI%x1f%s", ref, "--", p); err == nil {
			f := strings.Split(strings.TrimSpace(lc), "\x1f")
			if len(f) == 5 {
				out.LastCommit = &gitx.LogEntry{OID: f[0], Author: f[1], Email: f[2], Date: f[3], Subject: f[4]}
			}
		}
		_ = entries
	}
	return web.JSON(w, 200, out)
}

// compare diffs two revisions (branches, tags or commits).
func compare(w http.ResponseWriter, r *http.Request, s *web.Server, repo ledger.Repo, _ ledger.Actor) error {
	q := r.URL.Query()
	base, head := q.Get("base"), q.Get("head")
	if !validRev(base) || !validRev(head) {
		return fmt.Errorf("%w: base and head are required", errBadRequest)
	}
	rp := repoPath(s, repo)
	ctx := r.Context()
	b, err := gitx.RevParse(ctx, rp, base)
	if err != nil {
		return fmt.Errorf("%w: base: %v", ledger.ErrNotFound, err)
	}
	h, err := gitx.RevParse(ctx, rp, head)
	if err != nil {
		return fmt.Errorf("%w: head: %v", ledger.ErrNotFound, err)
	}
	mb, _ := gitx.MergeBase(ctx, rp, b, h)
	if mb == "" {
		mb = b
	}
	diff, err := gitx.Diff(ctx, rp, mb, h)
	if err != nil {
		return err
	}
	stat, _ := gitx.DiffStat(ctx, rp, mb, h)
	log, _ := gitx.Log(ctx, rp, mb+".."+h, 100)
	if log == nil {
		log = []gitx.LogEntry{}
	}
	return web.JSON(w, 200, map[string]any{"base": b, "head": h, "merge_base": mb, "diff": diff, "stat": stat, "commits": log})
}

// search is the command palette's backend: repositories by name, issues and
// changes by title, across every repository the caller can list.
func search(s *web.Server, w http.ResponseWriter, r *http.Request) error {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	type hit struct {
		Kind   string `json:"kind"`
		Repo   string `json:"repo"`
		Number int    `json:"number,omitempty"`
		Title  string `json:"title"`
		State  string `json:"state,omitempty"`
		URL    string `json:"url"`
	}
	hits := []hit{}
	if q == "" {
		return web.JSON(w, 200, map[string]any{"hits": hits})
	}
	repos, err := s.DB.Repos(r.Context(), "")
	if err != nil {
		return err
	}
	const cap = 40
	for _, repo := range repos {
		full := repo.Org + "/" + repo.Name
		if strings.Contains(strings.ToLower(full), q) {
			hits = append(hits, hit{Kind: "repo", Repo: full, Title: full, URL: Prefix + full})
		}
	}
	match := func(title string, n int) bool {
		return strings.Contains(strings.ToLower(title), q) || (strings.HasPrefix(q, "#") && fmt.Sprint(n) == q[1:])
	}
	for _, repo := range repos {
		if len(hits) >= cap {
			break
		}
		full := repo.Org + "/" + repo.Name
		if issues, err := s.DB.Issues(r.Context(), repo); err == nil {
			for _, i := range issues {
				if match(i.Title, i.Number) {
					hits = append(hits, hit{Kind: "issue", Repo: full, Number: i.Number, Title: i.Title, State: i.State, URL: fmt.Sprintf("%s%s/issues/%d", Prefix, full, i.Number)})
				}
			}
		}
		if changes, err := s.DB.Changes(r.Context(), repo); err == nil {
			for _, c := range changes {
				if match(c.Title, c.Number) {
					hits = append(hits, hit{Kind: "change", Repo: full, Number: c.Number, Title: c.Title, State: c.State, URL: fmt.Sprintf("%s%s/changes/%d", Prefix, full, c.Number)})
				}
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		rank := func(h hit) int {
			switch h.Kind {
			case "repo":
				return 0
			case "change":
				return 1
			}
			return 2
		}
		if rank(hits[i]) != rank(hits[j]) {
			return rank(hits[i]) < rank(hits[j])
		}
		return hits[i].Number > hits[j].Number
	})
	if len(hits) > cap {
		hits = hits[:cap]
	}
	return web.JSON(w, 200, map[string]any{"hits": hits})
}

// validRev rejects revision strings git would misread as options or paths.
func validRev(rev string) bool {
	if rev == "" || len(rev) > 256 || strings.HasPrefix(rev, "-") || strings.Contains(rev, "..") || strings.ContainsAny(rev, " \t\n:\\") {
		return false
	}
	return true
}

var _ = context.Background
