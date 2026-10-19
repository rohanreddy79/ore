// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/gitx"
)

// fakeImportData is what the importer reads beyond the adapter's endpoints.
type fakeImportData struct {
	pulls          []map[string]any
	issueComments  map[int][]map[string]any
	reviewComments map[int][]map[string]any
	reviews        map[int][]map[string]any
	timeline       map[int][]map[string]any
	releases       []map[string]any
	assets         map[int64][]byte
	assetFail      map[int64]int
	labels         []map[string]any
	milestones     []map[string]any
	hooks          []map[string]any
	contributors   []map[string]any
	protected      bool
	hasWiki        bool
	hooksForbidden bool
}

func (r *fakeRepo) data() *fakeImportData {
	if r.imp == nil {
		r.imp = &fakeImportData{issueComments: map[int][]map[string]any{}, reviewComments: map[int][]map[string]any{}, reviews: map[int][]map[string]any{}, timeline: map[int][]map[string]any{}, assets: map[int64][]byte{}, assetFail: map[int64]int{}}
	}
	return r.imp
}

// paginate honors page/per_page and sets GitHub-style Link headers.
func paginate(w http.ResponseWriter, r *http.Request, items []map[string]any) {
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if per <= 0 || per > 100 {
		per = 30
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page <= 0 {
		page = 1
	}
	last := (len(items) + per - 1) / per
	if last == 0 {
		last = 1
	}
	start := (page - 1) * per
	if start > len(items) {
		start = len(items)
	}
	end := start + per
	if end > len(items) {
		end = len(items)
	}
	var links []string
	q := r.URL.Query()
	mk := func(p int, rel string) string {
		q.Set("page", strconv.Itoa(p))
		return fmt.Sprintf(`<http://fake%s?%s>; rel="%s"`, r.URL.Path, q.Encode(), rel)
	}
	if page < last {
		links = append(links, mk(page+1, "next"), mk(last, "last"))
	}
	if page > 1 {
		links = append(links, mk(1, "first"), mk(page-1, "prev"))
	}
	if len(links) > 0 {
		w.Header().Set("Link", strings.Join(links, ", "))
	}
	out := items[start:end]
	if out == nil {
		out = []map[string]any{}
	}
	json.NewEncoder(w).Encode(out)
}

func number(m map[string]any) int {
	switch v := m["number"].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

// importRoutes adds the endpoints the planner and the importer use.
func (f *fakeGitHub) importRoutes(api func(string, func(http.ResponseWriter, *http.Request, *fakeRepo))) {
	api("GET /repos/{owner}/{name}/issues", func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) {
		state := r.URL.Query().Get("state")
		if state == "" {
			state = "open"
		}
		var all []map[string]any
		for _, i := range repo.issues {
			all = append(all, i)
		}
		if repo.imp != nil {
			for _, p := range repo.imp.pulls {
				i := map[string]any{}
				for k, v := range p {
					i[k] = v
				}
				i["pull_request"] = map[string]any{"url": "pr"}
				all = append(all, i)
			}
		}
		var out []map[string]any
		for _, i := range all {
			if state == "all" || i["state"] == state {
				out = append(out, i)
			}
		}
		asc := r.URL.Query().Get("direction") == "asc"
		sort.Slice(out, func(a, b int) bool {
			if asc {
				return number(out[a]) < number(out[b])
			}
			return number(out[a]) > number(out[b])
		})
		paginate(w, r, out)
	})
	api("GET /repos/{owner}/{name}/pulls", func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) {
		state := r.URL.Query().Get("state")
		if state == "" {
			state = "open"
		}
		var out []map[string]any
		for _, p := range repo.data().pulls {
			if state == "all" || p["state"] == state {
				out = append(out, p)
			}
		}
		sort.Slice(out, func(a, b int) bool { return number(out[a]) < number(out[b]) })
		paginate(w, r, out)
	})
	perNumber := func(pick func(d *fakeImportData, n int) []map[string]any) func(http.ResponseWriter, *http.Request, *fakeRepo) {
		return func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) {
			n, _ := strconv.Atoi(r.PathValue("n"))
			paginate(w, r, pick(repo.data(), n))
		}
	}
	api("GET /repos/{owner}/{name}/issues/{n}/comments", perNumber(func(d *fakeImportData, n int) []map[string]any { return d.issueComments[n] }))
	api("GET /repos/{owner}/{name}/issues/{n}/timeline", perNumber(func(d *fakeImportData, n int) []map[string]any { return d.timeline[n] }))
	api("GET /repos/{owner}/{name}/pulls/{n}/comments", perNumber(func(d *fakeImportData, n int) []map[string]any { return d.reviewComments[n] }))
	api("GET /repos/{owner}/{name}/pulls/{n}/reviews", perNumber(func(d *fakeImportData, n int) []map[string]any { return d.reviews[n] }))
	api("GET /repos/{owner}/{name}/releases", func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) {
		paginate(w, r, repo.data().releases)
	})
	api("GET /repos/{owner}/{name}/releases/assets/{id}", func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		d := repo.data()
		if st := d.assetFail[id]; st > 0 {
			w.WriteHeader(st)
			fmt.Fprint(w, `{"message":"asset unavailable"}`)
			return
		}
		b, ok := d.assets[id]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if r.Header.Get("Accept") != "application/octet-stream" {
			w.WriteHeader(406)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(b)
	})
	api("GET /repos/{owner}/{name}/labels", func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) { paginate(w, r, repo.data().labels) })
	api("GET /repos/{owner}/{name}/milestones", func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) { paginate(w, r, repo.data().milestones) })
	api("GET /repos/{owner}/{name}/contributors", func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) { paginate(w, r, repo.data().contributors) })
	api("GET /repos/{owner}/{name}/hooks", func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) {
		if repo.data().hooksForbidden {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found","documentation_url":"https://docs.github.com/rest/repos/webhooks#list-repository-webhooks"}`)
			return
		}
		paginate(w, r, repo.data().hooks)
	})
	api("GET /repos/{owner}/{name}/branches/{branch}/protection", func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) {
		if !repo.data().protected || r.PathValue("branch") != repo.defaultBranch {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Branch not protected","documentation_url":"https://docs.github.com/rest/branches/branch-protection#get-branch-protection"}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"required_status_checks":        map[string]any{"strict": true, "contexts": []string{"ci"}},
			"enforce_admins":                map[string]any{"enabled": true},
			"required_pull_request_reviews": map[string]any{"required_approving_review_count": 1},
			"allow_force_pushes":            map[string]any{"enabled": false},
			"allow_deletions":               map[string]any{"enabled": false},
		})
	})
	api("GET /repos/{owner}/{name}/contents/{path...}", func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) {
		ref := r.URL.Query().Get("ref")
		if ref == "" {
			ref = "refs/heads/" + repo.defaultBranch
		}
		p := r.PathValue("path")
		ctx := context.Background()
		files, err := gitx.ListTree(ctx, repo.path, ref)
		if err != nil {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message":"Not Found"}`)
			return
		}
		var dir []map[string]any
		for _, fp := range files {
			if fp == p {
				b, err := gitx.ReadFile(ctx, repo.path, ref, p)
				if err != nil {
					w.WriteHeader(500)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"type": "file", "name": fp[strings.LastIndex(fp, "/")+1:], "path": fp, "size": len(b), "sha": "deadbeef", "encoding": "base64", "content": base64.StdEncoding.EncodeToString(b)})
				return
			}
			if strings.HasPrefix(fp, p+"/") && !strings.Contains(strings.TrimPrefix(fp, p+"/"), "/") {
				dir = append(dir, map[string]any{"type": "file", "name": strings.TrimPrefix(fp, p+"/"), "path": fp, "size": 1, "sha": "deadbeef"})
			}
		}
		if dir == nil {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message":"Not Found","documentation_url":"https://docs.github.com/rest/repos/contents#get-repository-content"}`)
			return
		}
		json.NewEncoder(w).Encode(dir)
	})
}

// ---- fixture builders ----

func user(login string) map[string]any {
	return map[string]any{"login": login, "id": int64(len(login) * 1000), "type": "User"}
}

func (r *fakeRepo) addIssue(number int, title, body, state, author, created string, labels []string, comments []map[string]any) {
	var ls []map[string]any
	for _, l := range labels {
		ls = append(ls, map[string]any{"id": int64(100 + len(l)), "name": l, "color": "d73a4a"})
	}
	if ls == nil {
		ls = []map[string]any{}
	}
	i := map[string]any{"id": int64(7000 + number), "number": number, "title": title, "body": body, "state": state, "user": user(author), "labels": ls,
		"comments": len(comments), "created_at": created, "updated_at": created, "html_url": fmt.Sprintf("https://github.com/%s/%s/issues/%d", r.owner, r.name, number)}
	if state == "closed" {
		i["closed_at"] = created
		i["state_reason"] = "completed"
	}
	r.issues = append(r.issues, i)
	for k, c := range comments {
		if c["id"] == nil {
			c["id"] = int64(number*100 + k + 1)
		}
		r.data().issueComments[number] = append(r.data().issueComments[number], c)
	}
}

func comment(author, body, created string) map[string]any {
	return map[string]any{"body": body, "user": user(author), "created_at": created, "updated_at": created}
}

// addPull creates a branch with one commit, refs/pull/N/head and the pull JSON.
func (r *fakeRepo) addPull(t *testing.T, number int, branch, title, author, created string, files map[string]string, merged bool) string {
	t.Helper()
	base := r.tip(t, "refs/heads/"+r.defaultBranch)
	head := r.commit(t, branch, title, files)
	r.setRef(t, fmt.Sprintf("refs/pull/%d/head", number), head)
	p := map[string]any{"id": int64(9000 + number), "number": number, "state": "open", "title": title, "body": "Body of " + title, "user": user(author), "draft": false,
		"created_at": created, "updated_at": created, "html_url": fmt.Sprintf("https://github.com/%s/%s/pull/%d", r.owner, r.name, number),
		"head": map[string]any{"ref": branch, "sha": head, "label": author + ":" + branch}, "base": map[string]any{"ref": r.defaultBranch, "sha": base, "label": r.owner + ":" + r.defaultBranch},
		"merged_at": nil, "merge_commit_sha": nil, "closed_at": nil}
	if merged {
		mergeOID := r.commit(t, r.defaultBranch, "Merge pull request #"+strconv.Itoa(number), files)
		p["state"], p["merged_at"], p["closed_at"], p["merge_commit_sha"] = "closed", created, created, mergeOID
	}
	r.data().pulls = append(r.data().pulls, p)
	return head
}

func (r *fakeRepo) addRelease(id int64, tag, name, body, author, published string, assets map[string][]byte) {
	d := r.data()
	rel := map[string]any{"id": id, "tag_name": tag, "name": name, "body": body, "draft": false, "prerelease": false, "author": user(author), "created_at": published, "published_at": published,
		"html_url": fmt.Sprintf("https://github.com/%s/%s/releases/tag/%s", r.owner, r.name, tag)}
	var list []map[string]any
	names := make([]string, 0, len(assets))
	for n := range assets {
		names = append(names, n)
	}
	sort.Strings(names)
	for i, n := range names {
		aid := id*10 + int64(i) + 1
		d.assets[aid] = assets[n]
		list = append(list, map[string]any{"id": aid, "name": n, "size": len(assets[n]), "content_type": "application/octet-stream", "url": "", "browser_download_url": "", "created_at": published})
	}
	if list == nil {
		list = []map[string]any{}
	}
	rel["assets"] = list
	d.releases = append(d.releases, rel)
}

// assetURLs points each asset's url at this fake (needs the server URL).
func (f *fakeGitHub) finishAssets(r *fakeRepo) {
	for _, rel := range r.data().releases {
		for _, a := range rel["assets"].([]map[string]any) {
			a["url"] = fmt.Sprintf("%s/repos/%s/%s/releases/assets/%d", f.srv.URL, r.owner, r.name, a["id"].(int64))
			a["browser_download_url"] = a["url"]
		}
	}
}

// seedImportRepo builds a realistic source repository for the importer tests.
func seedImportRepo(t *testing.T, gh *fakeGitHub) *fakeRepo {
	t.Helper()
	up := gh.addRepo("octo", "app", map[string]string{
		"README.md":                 "# app\n\nline 2\nline 3\n",
		".gitattributes":            "*.bin filter=lfs diff=lfs merge=lfs -text\n",
		".github/CODEOWNERS":        "* @octo/core\n",
		".github/workflows/ci.yml":  "name: CI\non: [push]\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: go test ./...\n",
		".github/workflows/odd.yml": "name: Odd\non: [push]\njobs:\n  mac:\n    runs-on: macos-latest\n    steps:\n      - run: echo hi\n",
		"go.mod":                    "module app\n",
	})
	d := up.data()
	d.protected = true
	up.addIssue(1, "Flaky retry in the uploader", "It retries once and gives up.", "open", "octocat", "2026-09-01T10:00:00Z", []string{"bug"}, []map[string]any{
		comment("hubot", "I can reproduce this.", "2026-09-01T11:00:00Z"),
		comment("octocat", "Fix incoming.", "2026-09-02T09:00:00Z"),
	})
	up.addIssue(2, "Old question", "Answered.", "closed", "hubot", "2026-08-15T08:00:00Z", nil, nil)
	head3 := up.addPull(t, 3, "feature/retry", "Retry uploads with backoff", "octocat", "2026-09-03T10:00:00Z", map[string]string{"uploader.go": "package uploader\n\nfunc Retry() {}\n"}, true)
	d.timeline[3] = []map[string]any{
		{"event": "head_ref_force_pushed", "commit_id": "0123456789abcdef0123456789abcdef01234567", "created_at": "2026-09-03T12:00:00Z", "actor": user("octocat")},
		{"event": "committed", "sha": head3},
	}
	d.reviewComments[3] = []map[string]any{
		{"id": int64(501), "path": "uploader.go", "line": 3, "original_line": 3, "position": 3, "original_position": 3, "commit_id": head3, "original_commit_id": head3, "body": "Consider jitter here.", "user": user("hubot"), "created_at": "2026-09-03T13:00:00Z", "pull_request_review_id": int64(801)},
		{"id": int64(502), "path": "uploader.go", "line": nil, "original_line": 9, "position": nil, "original_position": 9, "commit_id": head3, "original_commit_id": "0123456789abcdef0123456789abcdef01234567", "body": "This line went away after the force push.", "user": user("hubot"), "created_at": "2026-09-03T11:30:00Z", "pull_request_review_id": int64(801)},
	}
	d.reviews[3] = []map[string]any{
		{"id": int64(801), "user": user("hubot"), "body": "Looks good overall.", "state": "COMMENTED", "commit_id": head3, "submitted_at": "2026-09-03T13:05:00Z"},
		{"id": int64(802), "user": user("alice"), "body": "", "state": "APPROVED", "commit_id": head3, "submitted_at": "2026-09-03T14:00:00Z"},
		{"id": int64(803), "user": user("alice"), "body": "", "state": "PENDING", "commit_id": head3, "submitted_at": ""},
	}
	d.issueComments[3] = []map[string]any{{"id": int64(301), "body": "Thanks for the review!", "user": user("octocat"), "created_at": "2026-09-03T15:00:00Z"}}
	up.addPull(t, 4, "feature/metrics", "Add metrics", "alice", "2026-09-10T10:00:00Z", map[string]string{"metrics.go": "package metrics\n"}, false)
	up.addIssue(5, "Document the retry policy", "", "open", "alice", "2026-09-12T10:00:00Z", []string{"enhancement"}, nil)
	up.setRef(t, "refs/tags/v1.0.0", up.tip(t, "refs/heads/main"))
	up.addRelease(4001, "v1.0.0", "First release", "Notes for 1.0", "octocat", "2026-09-05T10:00:00Z", map[string][]byte{"app.tar.gz": []byte("binary payload"), "checksums.txt": []byte("abc  app.tar.gz\n")})
	gh.finishAssets(up)
	d.labels = []map[string]any{{"id": int64(103), "name": "bug", "color": "d73a4a", "description": "Something is broken", "default": true}, {"id": int64(111), "name": "enhancement", "color": "a2eeef", "description": "", "default": true}}
	d.milestones = []map[string]any{{"id": int64(2001), "number": 1, "title": "v1", "description": "First", "state": "open", "due_on": "2026-12-01T00:00:00Z", "open_issues": 2, "closed_issues": 1}}
	d.hooks = []map[string]any{{"id": int64(512345678), "name": "web", "active": true, "events": []string{"push", "pull_request"}, "config": map[string]any{"url": "https://ci.example.org/github", "content_type": "json", "insecure_ssl": "0"}, "created_at": "2026-08-01T00:00:00Z"}}
	d.contributors = []map[string]any{{"login": "octocat", "id": int64(1), "type": "User", "contributions": 12}, {"login": "hubot", "id": int64(480938), "type": "Bot", "contributions": 2}}
	return up
}
