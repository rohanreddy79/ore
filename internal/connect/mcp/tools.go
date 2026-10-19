// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/orevcs/ore/internal/connect/gateway"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// Tool is one MCP tool: a descriptor and a handler.
type Tool struct {
	Name        string
	Title       string
	Description string
	InputSchema map[string]any
	ReadOnly    bool
	Handler     func(ctx context.Context, c *Call) (*Result, error)
}

// Call is one tool invocation.
type Call struct {
	Server    *Server
	Principal gateway.Principal
	Args      map[string]any
	Request   *http.Request
}

// Result is a tool's structured output plus its text rendering. When Text is
// empty the structured content is rendered as indented JSON.
type Result struct {
	Structured map[string]any
	Text       string
}

func (r *Result) result() map[string]any {
	text := r.Text
	if text == "" {
		b, _ := json.MarshalIndent(r.Structured, "", "  ")
		text = string(b)
	}
	out := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
	if r.Structured != nil {
		out["structuredContent"] = r.Structured
	}
	return out
}

func structured(v map[string]any) (*Result, error) { return &Result{Structured: v}, nil }

// tools is the registry, keyed by name. The list order is by name.
var tools = map[string]*Tool{}

func register(t *Tool) { tools[t.Name] = t }

// ---- argument helpers ----

func (c *Call) str(name string) string {
	switch v := c.Args[name].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func (c *Call) integer(name string, def int) (int, error) {
	switch v := c.Args[name].(type) {
	case nil:
		return def, nil
	case float64:
		if v != math.Trunc(v) {
			return 0, toolErr("%s must be an integer", name)
		}
		return int(v), nil
	case string:
		if strings.TrimSpace(v) == "" {
			return def, nil
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, toolErr("%s must be an integer", name)
		}
		return n, nil
	}
	return 0, toolErr("%s must be an integer", name)
}

func (c *Call) boolean(name string) (*bool, error) {
	switch v := c.Args[name].(type) {
	case nil:
		return nil, nil
	case bool:
		return &v, nil
	case string:
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, toolErr("%s must be a boolean", name)
		}
		return &b, nil
	}
	return nil, toolErr("%s must be a boolean", name)
}

func (c *Call) strings(name string) []string {
	var out []string
	switch v := c.Args[name].(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case string:
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// repo resolves the repository a call targets: the repo argument (org/name),
// or the session's repository. A session may not read outside its scope.
func (c *Call) repo(ctx context.Context) (ledger.Repo, error) {
	arg := c.str("repo")
	if s := c.Principal.Session; s != nil {
		if arg != "" && arg != s.Repo && arg != s.RepoID {
			return ledger.Repo{}, &ToolError{Message: fmt.Sprintf("%s is outside the session scope (%s)", arg, s.Scope), Data: map[string]any{"code": gateway.CodeOutsideScope, "scope": s.Scope}}
		}
		return c.Server.Web.DB.RepoByID(ctx, s.RepoID)
	}
	if arg == "" {
		return ledger.Repo{}, toolErr("repo is required (org/name)")
	}
	org, name, ok := strings.Cut(arg, "/")
	if !ok {
		return ledger.Repo{}, toolErr("repo must be org/name, got %q", arg)
	}
	repo, err := c.Server.Web.DB.RepoByName(ctx, org, name)
	if err != nil {
		return repo, toolErr("repository %s: %v", arg, err)
	}
	return repo, nil
}

func (c *Call) repoPath(repo ledger.Repo) string {
	return filepath.Join(c.Server.Web.ReposDir, repo.Org, repo.Name+".git")
}

func (c *Call) ref(repo ledger.Repo) string {
	ref := c.str("ref")
	if ref == "" {
		return "refs/heads/" + repo.DefaultBranch
	}
	return ref
}

// ---- schema helpers ----

func schema(required []string, props map[string]any) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var repoProp = map[string]any{"type": "string", "description": "Repository as org/name. Optional in a session scoped to one repository."}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

// ---- write path ----

// write submits one safe-outputs request through the gateway for the
// session behind the call and shapes the decision as a tool result. A repo
// argument outside the session's scope is passed on as the envelope's
// repository so the gateway refuses it and records the refusal.
func (c *Call) write(ctx context.Context, req map[string]any) (*Result, error) {
	s := c.Principal.Session
	if s == nil {
		return nil, &ToolError{Message: "writes over MCP require a gateway session token: POST /api/v0/gateway/sessions {\"agent_id\": ..., \"repo\": \"org/name\"} with the admin token, then connect with the returned token",
			Data: map[string]any{"code": "session_required"}}
	}
	repoID := s.RepoID
	if arg := c.str("repo"); arg != "" && arg != s.Repo && arg != s.RepoID {
		repoID = arg
	}
	env := map[string]any{
		"version":  gateway.SchemaVersion,
		"session":  map[string]any{"agent_id": s.AgentID, "session_id": s.ID, "repository_id": repoID},
		"requests": []any{req},
	}
	body, _ := json.Marshal(env)
	resp, err := c.Server.Gateway.Submit(ctx, *s, body)
	if err != nil {
		return nil, err
	}
	if len(resp.Results) == 0 {
		return nil, toolErr("gateway returned no decision")
	}
	res := resp.Results[0]
	var m map[string]any
	b, _ := json.Marshal(res)
	json.Unmarshal(b, &m)
	if res.Status != "executed" {
		return nil, &ToolError{Message: fmt.Sprintf("refused (%s): %s", res.Code, res.Reason), Data: m}
	}
	return structured(m)
}

// ---- registration ----

func init() {
	register(&Tool{Name: "context_whoami", Title: "Who am I", ReadOnly: true,
		Description: "The caller's identity: actor, session scope (if any), rate-limit counters, and whether writes are possible.",
		InputSchema: schema(nil, map[string]any{}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			out := map[string]any{"actor": c.Principal.Actor, "read_only": c.Server.ReadOnly, "server": map[string]any{"name": "ore", "version": c.Server.version(), "protocol": ProtocolVersion}}
			switch {
			case c.Server.ReadOnly:
				out["writes"] = "disabled"
			case c.Principal.Session != nil:
				out["writes"] = "gateway"
			default:
				out["writes"] = "session_required"
			}
			if s := c.Principal.Session; s != nil {
				cur, err := c.Server.Gateway.Session(ctx, s.ID)
				if err != nil {
					return nil, err
				}
				limit, window := c.Server.Gateway.RateLimit, c.Server.Gateway.RateWindow
				if limit <= 0 {
					limit = gateway.DefaultRateLimit
				}
				if window <= 0 {
					window = gateway.DefaultRateWindow
				}
				out["session"] = cur
				out["rate_limit"] = map[string]any{"writes": limit, "window": window.String(), "used_in_window": cur.WindowWrites}
			}
			return structured(out)
		}})

	register(&Tool{Name: "repos_list", Title: "List repositories", ReadOnly: true,
		Description: "List repositories, optionally within one org. A scoped session sees only its repository.",
		InputSchema: schema(nil, map[string]any{"org": strProp("Org name to filter by")}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			if s := c.Principal.Session; s != nil {
				repo, err := c.Server.Web.DB.RepoByID(ctx, s.RepoID)
				if err != nil {
					return nil, err
				}
				return structured(map[string]any{"repos": []ledger.Repo{repo}, "scope": s.Scope})
			}
			repos, err := c.Server.Web.DB.Repos(ctx, c.str("org"))
			if err != nil {
				return nil, err
			}
			if repos == nil {
				repos = []ledger.Repo{}
			}
			return structured(map[string]any{"repos": repos})
		}})

	register(&Tool{Name: "repos_get", Title: "Get repository", ReadOnly: true,
		Description: "A repository's metadata (default branch, autonomy level) and its refs.",
		InputSchema: schema(nil, map[string]any{"repo": repoProp}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			repo, err := c.repo(ctx)
			if err != nil {
				return nil, err
			}
			refs, err := gitx.Refs(ctx, c.repoPath(repo))
			if err != nil {
				return nil, err
			}
			return structured(map[string]any{"repo": repo, "refs": refs})
		}})

	register(&Tool{Name: "repos_tree", Title: "List files", ReadOnly: true,
		Description: "The paths in a repository at a ref (default: the default branch).",
		InputSchema: schema(nil, map[string]any{"repo": repoProp, "ref": strProp("Branch, tag or commit (default: the default branch)")}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			repo, err := c.repo(ctx)
			if err != nil {
				return nil, err
			}
			ref := c.ref(repo)
			files, err := gitx.ListTree(ctx, c.repoPath(repo), ref)
			if err != nil {
				return nil, toolErr("tree %s: %v", ref, err)
			}
			if files == nil {
				files = []string{}
			}
			return structured(map[string]any{"repo": repo.Org + "/" + repo.Name, "ref": ref, "paths": files})
		}})

	register(&Tool{Name: "repos_file", Title: "Read file", ReadOnly: true,
		Description: "A file's content at a ref (default: the default branch). Content over 512 KiB is truncated.",
		InputSchema: schema([]string{"path"}, map[string]any{"repo": repoProp, "path": strProp("Path within the repository"), "ref": strProp("Branch, tag or commit")}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			repo, err := c.repo(ctx)
			if err != nil {
				return nil, err
			}
			path := c.str("path")
			if path == "" || strings.Contains(path, "..") {
				return nil, toolErr("path is required and may not contain ..")
			}
			ref := c.ref(repo)
			b, err := gitx.ReadFile(ctx, c.repoPath(repo), ref, path)
			if err != nil {
				return nil, toolErr("file %s at %s: %v", path, ref, err)
			}
			const max = 512 << 10
			truncated := len(b) > max
			if truncated {
				b = b[:max]
			}
			return &Result{Text: string(b), Structured: map[string]any{"repo": repo.Org + "/" + repo.Name, "ref": ref, "path": path, "size": len(b), "truncated": truncated, "content": string(b)}}, nil
		}})

	register(&Tool{Name: "repos_log", Title: "Commit log", ReadOnly: true,
		Description: "Commits reachable from a ref, newest first.",
		InputSchema: schema(nil, map[string]any{"repo": repoProp, "ref": strProp("Branch, tag or commit (default: the default branch)"), "limit": intProp("Max commits (default 50, max 500)")}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			repo, err := c.repo(ctx)
			if err != nil {
				return nil, err
			}
			n, err := c.integer("limit", 50)
			if err != nil {
				return nil, err
			}
			if n <= 0 || n > 500 {
				n = 50
			}
			ref := c.ref(repo)
			entries, err := gitx.Log(ctx, c.repoPath(repo), ref, n)
			if err != nil {
				return nil, toolErr("log %s: %v", ref, err)
			}
			if entries == nil {
				entries = []gitx.LogEntry{}
			}
			return structured(map[string]any{"repo": repo.Org + "/" + repo.Name, "ref": ref, "commits": entries})
		}})

	register(&Tool{Name: "changes_list", Title: "List changes", ReadOnly: true,
		Description: "A repository's changes (the unit of review), newest first; filter by state (open, merged, closed).",
		InputSchema: schema(nil, map[string]any{"repo": repoProp, "state": strProp("open, merged or closed")}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			repo, err := c.repo(ctx)
			if err != nil {
				return nil, err
			}
			all, err := c.Server.Web.DB.Changes(ctx, repo)
			if err != nil {
				return nil, err
			}
			out := []ledger.Change{}
			for _, ch := range all {
				if st := c.str("state"); st == "" || st == ch.State {
					out = append(out, ch)
				}
			}
			return structured(map[string]any{"repo": repo.Org + "/" + repo.Name, "changes": out})
		}})

	register(&Tool{Name: "changes_get", Title: "Get change", ReadOnly: true,
		Description: "One change by number, with its diff stat, pipeline runs and comments.",
		InputSchema: schema([]string{"number"}, map[string]any{"repo": repoProp, "number": intProp("Change number")}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			repo, err := c.repo(ctx)
			if err != nil {
				return nil, err
			}
			n, err := c.integer("number", 0)
			if err != nil {
				return nil, err
			}
			ch, err := c.Server.Web.DB.Change(ctx, repo, n)
			if err != nil {
				return nil, toolErr("change #%d: %v", n, err)
			}
			stat, _ := c.Server.Web.Review.DiffStat(ctx, repo, ch)
			all, _ := c.Server.Web.DB.PipelineRuns(ctx, repo, 200)
			runs := []ledger.PipelineRun{}
			for _, p := range all {
				if p.ChangeID != nil && *p.ChangeID == ch.ID {
					runs = append(runs, p)
				}
			}
			comments, err := c.Server.Gateway.Comments(ctx, repo, "change", ch.ID)
			if err != nil {
				return nil, err
			}
			return structured(map[string]any{"repo": repo.Org + "/" + repo.Name, "change": ch, "diffstat": strings.TrimSpace(stat), "runs": runs, "comments": comments})
		}})

	register(&Tool{Name: "changes_diff", Title: "Change diff", ReadOnly: true,
		Description: "The unified diff of a change against its merge base.",
		InputSchema: schema([]string{"number"}, map[string]any{"repo": repoProp, "number": intProp("Change number")}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			repo, err := c.repo(ctx)
			if err != nil {
				return nil, err
			}
			n, err := c.integer("number", 0)
			if err != nil {
				return nil, err
			}
			ch, err := c.Server.Web.DB.Change(ctx, repo, n)
			if err != nil {
				return nil, toolErr("change #%d: %v", n, err)
			}
			diff, err := c.Server.Web.Review.Diff(ctx, repo, ch)
			if err != nil {
				return nil, err
			}
			return &Result{Text: diff, Structured: map[string]any{"repo": repo.Org + "/" + repo.Name, "change": ch.Number, "base": ch.BaseOID, "head": ch.HeadOID, "diff": diff}}, nil
		}})

	register(&Tool{Name: "issues_list", Title: "List issues", ReadOnly: true,
		Description: "A repository's issues, newest first; filter by state (open, closed).",
		InputSchema: schema(nil, map[string]any{"repo": repoProp, "state": strProp("open or closed")}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			repo, err := c.repo(ctx)
			if err != nil {
				return nil, err
			}
			all, err := c.Server.Web.DB.Issues(ctx, repo)
			if err != nil {
				return nil, err
			}
			out := []ledger.Issue{}
			for _, i := range all {
				if st := c.str("state"); st == "" || st == i.State {
					out = append(out, i)
				}
			}
			return structured(map[string]any{"repo": repo.Org + "/" + repo.Name, "issues": out})
		}})

	register(&Tool{Name: "issues_get", Title: "Get issue", ReadOnly: true,
		Description: "One issue by number, with its comments.",
		InputSchema: schema([]string{"number"}, map[string]any{"repo": repoProp, "number": intProp("Issue number")}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			repo, err := c.repo(ctx)
			if err != nil {
				return nil, err
			}
			n, err := c.integer("number", 0)
			if err != nil {
				return nil, err
			}
			issue, err := c.Server.Web.DB.Issue(ctx, repo, n)
			if err != nil {
				return nil, toolErr("issue #%d: %v", n, err)
			}
			comments, err := c.Server.Gateway.Comments(ctx, repo, "issue", issue.ID)
			if err != nil {
				return nil, err
			}
			return structured(map[string]any{"repo": repo.Org + "/" + repo.Name, "issue": issue, "comments": comments})
		}})

	register(&Tool{Name: "pipelines_runs", Title: "Pipeline runs", ReadOnly: true,
		Description: "A repository's pipeline runs, newest first.",
		InputSchema: schema(nil, map[string]any{"repo": repoProp, "limit": intProp("Max runs (default 50)")}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			repo, err := c.repo(ctx)
			if err != nil {
				return nil, err
			}
			n, err := c.integer("limit", 50)
			if err != nil {
				return nil, err
			}
			runs, err := c.Server.Web.DB.PipelineRuns(ctx, repo, n)
			if err != nil {
				return nil, err
			}
			if runs == nil {
				runs = []ledger.PipelineRun{}
			}
			return structured(map[string]any{"repo": repo.Org + "/" + repo.Name, "runs": runs})
		}})

	register(&Tool{Name: "pipelines_run_log", Title: "Pipeline run log", ReadOnly: true,
		Description: "A pipeline run's log by line range: start (1-based) and count (default 200, max 2000).",
		InputSchema: schema([]string{"run"}, map[string]any{"repo": repoProp, "run": strProp("Run id (prun_…)"), "start": intProp("First line, 1-based (default 1)"), "count": intProp("Lines to return (default 200, max 2000)")}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			repo, err := c.repo(ctx)
			if err != nil {
				return nil, err
			}
			run, err := c.Server.Web.DB.PipelineRun(ctx, c.str("run"))
			if err != nil || run.RepoID != repo.ID {
				return nil, toolErr("run %q not found in %s/%s", c.str("run"), repo.Org, repo.Name)
			}
			start, err := c.integer("start", 1)
			if err != nil {
				return nil, err
			}
			count, err := c.integer("count", 200)
			if err != nil {
				return nil, err
			}
			if start < 1 {
				start = 1
			}
			if count <= 0 || count > 2000 {
				count = 200
			}
			var lines []string
			if run.LogPath != "" {
				b, err := os.ReadFile(run.LogPath)
				if err != nil {
					return nil, toolErr("log: %v", err)
				}
				lines = strings.Split(strings.TrimRight(string(b), "\n"), "\n")
				if len(lines) == 1 && lines[0] == "" {
					lines = nil
				}
			}
			total := len(lines)
			from := start - 1
			if from > total {
				from = total
			}
			to := from + count
			if to > total {
				to = total
			}
			page := append([]string{}, lines[from:to]...)
			return &Result{Text: strings.Join(page, "\n"), Structured: map[string]any{"run": run.ID, "status": run.Status, "start": start, "count": len(page), "total_lines": total, "truncated": to < total, "lines": page}}, nil
		}})

	register(&Tool{Name: "deployments_list", Title: "List deployments", ReadOnly: true,
		Description: "A repository's deployments, newest first.",
		InputSchema: schema(nil, map[string]any{"repo": repoProp, "limit": intProp("Max deployments (default 50)")}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			repo, err := c.repo(ctx)
			if err != nil {
				return nil, err
			}
			n, err := c.integer("limit", 50)
			if err != nil {
				return nil, err
			}
			deps, err := c.Server.Web.DB.Deployments(ctx, repo, n)
			if err != nil {
				return nil, err
			}
			if deps == nil {
				deps = []ledger.Deployment{}
			}
			return structured(map[string]any{"repo": repo.Org + "/" + repo.Name, "deployments": deps})
		}})

	register(&Tool{Name: "ledger_events", Title: "Ledger events", ReadOnly: true,
		Description: "CloudEvents from the ledger in sequence order: after (seq), type (exact, or a prefix ending in '.'), subject, limit. A scoped session sees only its repository's events.",
		InputSchema: schema(nil, map[string]any{"repo": repoProp, "after": intProp("Return events with seq greater than this"), "type": strProp("Event type, or a prefix ending in '.' such as io.ore.issue."), "subject": strProp("Exact subject, e.g. acme/app/issues/1"), "limit": intProp("Max events (default 200, max 1000)")}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			after, err := c.integer("after", 0)
			if err != nil {
				return nil, err
			}
			limit, err := c.integer("limit", 200)
			if err != nil {
				return nil, err
			}
			if limit <= 0 || limit > 1000 {
				limit = 200
			}
			opt := ledger.EventsOptions{AfterSeq: int64(after), Type: c.str("type"), Subject: c.str("subject"), Limit: limit}
			prefix := ""
			if c.Principal.Session != nil || c.str("repo") != "" {
				repo, err := c.repo(ctx)
				if err != nil {
					return nil, err
				}
				opt.OrgID, prefix = repo.OrgID, repo.Org+"/"+repo.Name
				if prefix != "" {
					opt.Limit = 1000
				}
			}
			evs, err := c.Server.Web.DB.Events(ctx, opt)
			if err != nil {
				return nil, err
			}
			out := []ledger.Event{}
			for _, ev := range evs {
				if prefix != "" && ev.Subject != prefix && !strings.HasPrefix(ev.Subject, prefix+"/") {
					continue
				}
				out = append(out, ev)
				if len(out) == limit {
					break
				}
			}
			next := int64(after)
			if len(out) > 0 {
				next = out[len(out)-1].Seq
			}
			return structured(map[string]any{"events": out, "next_after": next})
		}})

	// ---- writes: every one of these is a safe-outputs request through the gateway ----

	register(&Tool{Name: "issues_open", Title: "Open issue",
		Description: "Open an issue (safe-outputs kind open_issue). Routed through the gateway: scoped, rate-limited, recorded.",
		InputSchema: schema([]string{"title"}, map[string]any{"repo": repoProp, "title": strProp("Title (max 256 characters)"), "body": strProp("Body (markdown)"), "labels": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			req := map[string]any{"kind": "open_issue", "title": c.str("title"), "body": c.str("body")}
			if labels := c.strings("labels"); labels != nil {
				req["labels"] = labels
			}
			return c.write(ctx, req)
		}})

	register(&Tool{Name: "changes_open", Title: "Open change",
		Description: "Open a change from an existing branch (safe-outputs kind open_change). The branch must exist and may not be the protected default branch; base defaults to the default branch. If the branch already has an open change, that change is returned (existing: true). Provenance is recorded from the session; pass model to name the model.",
		InputSchema: schema([]string{"branch", "title"}, map[string]any{"repo": repoProp, "branch": strProp("Source branch (must already exist)"), "base": strProp("Target branch (default: the default branch)"), "title": strProp("Title"), "body": strProp("Description: rationale and validation"), "draft": map[string]any{"type": "boolean", "description": "Draft (default true)"}, "model": strProp("Vendor/model identifier for provenance")}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			draft, err := c.boolean("draft")
			if err != nil {
				return nil, err
			}
			model := c.str("model")
			if model == "" {
				model = "unspecified"
			}
			agentID := ""
			if s := c.Principal.Session; s != nil {
				agentID = s.AgentID
			}
			req := map[string]any{"kind": "open_change", "branch": c.str("branch"), "title": c.str("title"), "body": c.str("body"),
				"provenance": map[string]any{"author_kind": "agent", "agent_id": agentID, "model": model}}
			if base := c.str("base"); base != "" {
				req["base"] = base
			}
			if draft != nil {
				req["draft"] = *draft
			}
			return c.write(ctx, req)
		}})

	register(&Tool{Name: "changes_request_review", Title: "Request review",
		Description: "Ask named reviewers to review a change (safe-outputs kind request_review); recorded as io.ore.change.review.requested.",
		InputSchema: schema([]string{"number", "reviewers"}, map[string]any{"repo": repoProp, "number": intProp("Change number"), "reviewers": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Reviewer ids"}}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			n, err := c.integer("number", 0)
			if err != nil {
				return nil, err
			}
			reviewers := c.strings("reviewers")
			if reviewers == nil {
				reviewers = []string{}
			}
			return c.write(ctx, map[string]any{"kind": "request_review", "target": fmt.Sprintf("changes/%d", n), "reviewers": reviewers})
		}})

	register(&Tool{Name: "deployments_request", Title: "Request deployment",
		Description: "Request a deployment of a release (a commit or branch) to an environment (safe-outputs kind request_deployment). Creates a request on the ledger; policy and approvals still apply, nothing is deployed by this call.",
		InputSchema: schema([]string{"release", "environment"}, map[string]any{"repo": repoProp, "release": strProp("Commit or branch"), "environment": strProp("preview or production")}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			return c.write(ctx, map[string]any{"kind": "request_deployment", "release": c.str("release"), "environment": c.str("environment")})
		}})

	register(&Tool{Name: "issues_comment", Title: "Comment",
		Description: "Comment on an issue or a change (safe-outputs kind comment; body at most 65536 bytes). Give issue or change (a number), or target as issues/N or changes/N.",
		InputSchema: schema([]string{"body"}, map[string]any{"repo": repoProp, "issue": intProp("Issue number"), "change": intProp("Change number"), "target": strProp("issues/N or changes/N (alternative to issue/change)"), "body": strProp("Comment body (markdown, max 65536 bytes)")}),
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			target := c.str("target")
			if n, err := c.integer("issue", 0); err != nil {
				return nil, err
			} else if n > 0 {
				target = fmt.Sprintf("issues/%d", n)
			}
			if n, err := c.integer("change", 0); err != nil {
				return nil, err
			} else if n > 0 {
				target = fmt.Sprintf("changes/%d", n)
			}
			if target == "" {
				return nil, toolErr("issue, change or target is required")
			}
			body, _ := c.Args["body"].(string)
			return c.write(ctx, map[string]any{"kind": "comment", "target": target, "body": body})
		}})
}
