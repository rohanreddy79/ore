// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/orevcs/ore/internal/agent"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/review"
)

// FleetSummary is the hook the agent-fleet slice may implement on
// s.Services["agent.fleet"]: the repository overview shows budgets and
// evidence from it and hides the section when it is absent.
type FleetSummary interface {
	RepoSummary(ctx context.Context, repo ledger.Repo) (map[string]any, error)
}

func (s *Server) mountRepoUI(mux *http.ServeMux) {
	s.page(mux, "GET /{org}/{name}", s.uiRepo)
	s.page(mux, "POST /ui/{org}/{name}/level", s.uiSetLevel)
	s.page(mux, "POST /ui/{org}/{name}/agent", s.uiRunAgent)
	s.page(mux, "POST /ui/{org}/{name}/runs", s.uiRunPipeline)
	s.page(mux, "POST /ui/{org}/{name}/deploy", s.uiDeploy)
	s.page(mux, "POST /ui/{org}/{name}/rollback", s.uiRollback)

	s.page(mux, "GET /{org}/{name}/issues", s.uiIssues)
	s.page(mux, "POST /ui/{org}/{name}/issues", s.uiNewIssue)
	s.page(mux, "GET /{org}/{name}/issues/{n}", s.uiIssue)
	s.page(mux, "POST /ui/{org}/{name}/issues/{n}/comments", s.uiIssueComment)
	s.page(mux, "POST /ui/{org}/{name}/issues/{n}/close", s.uiCloseIssue)

	s.page(mux, "GET /{org}/{name}/changes", s.uiChanges)
	s.page(mux, "POST /ui/{org}/{name}/changes", s.uiOpenChange)
	s.page(mux, "GET /{org}/{name}/changes/{n}", s.uiChange)
	s.page(mux, "POST /ui/{org}/{name}/changes/{n}/comments", s.uiChangeComment)
	s.page(mux, "POST /ui/{org}/{name}/changes/{n}/comments/{id}/resolve", s.uiResolveComment)
	s.page(mux, "POST /ui/{org}/{name}/changes/{n}/reviews", s.uiReview)
	s.page(mux, "POST /ui/{org}/{name}/changes/{n}/land", s.uiLand)
	s.page(mux, "POST /ui/{org}/{name}/changes/{n}/merge", s.uiLand)
	s.page(mux, "POST /ui/{org}/{name}/changes/{n}/verify", s.uiVerify)
	s.page(mux, "POST /ui/{org}/{name}/changes/{n}/close", s.uiCloseChange)

	s.page(mux, "GET /{org}/{name}/stacks", s.uiStacks)
	s.page(mux, "POST /ui/{org}/{name}/stacks", s.uiOpenStack)
	s.page(mux, "GET /{org}/{name}/stacks/{id}", s.uiStack)
	s.page(mux, "POST /ui/{org}/{name}/stacks/{id}/land", s.uiLandStack)

	s.page(mux, "GET /{org}/{name}/runs", s.uiRuns)
	s.page(mux, "GET /{org}/{name}/runs/{id}", s.uiRun)
	s.page(mux, "GET /{org}/{name}/agent", s.uiAgentRuns)
	s.page(mux, "GET /{org}/{name}/agent/{id}", s.uiAgentRun)
}

// ---- overview ----

func (s *Server) uiRepo(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	issues, _ := s.DB.Issues(ctx, repo)
	changes, _ := s.DB.Changes(ctx, repo)
	runs, _ := s.DB.PipelineRuns(ctx, repo, 5)
	deps, _ := s.DB.Deployments(ctx, repo, 5)
	aruns, _ := s.DB.AgentRuns(ctx, repo, 5)
	oi, oc := 0, 0
	for _, i := range issues {
		if i.State == "open" {
			oi++
		}
	}
	for _, c := range changes {
		if c.State == "open" {
			oc++
		}
	}
	data := map[string]any{"Levels": Levels, "OpenIssues": oi, "OpenChanges": oc, "Issues": len(issues), "ChangeCount": len(changes),
		"Runs": runs, "Deployments": deps, "AgentRuns": aruns,
		"Stream": fmt.Sprintf("/api/v0/events/stream?after=%d&subject_prefix=%s/%s/", s.latestSeq(ctx), repo.Org, repo.Name)}
	if svc := s.delivery(); svc != nil {
		envs, _ := svc.Environments(ctx, repo)
		data["Environments"] = envs
		if d, err := svc.ComputeDORA(ctx, repo, 30); err == nil {
			data["DORA"] = d
		}
	}
	if fleet, ok := s.Services["agent.fleet"].(FleetSummary); ok {
		if sum, err := fleet.RepoSummary(ctx, repo); err == nil && len(sum) > 0 {
			data["Fleet"] = sortedKV(sum)
		}
	}
	evs, _ := s.eventsPage(ctx, eventFilter{SubjectPrefix: repo.Org + "/" + repo.Name + "/", Limit: 15})
	data["Events"] = evs
	if pol, err := s.Review.Policy(ctx, repo); err == nil {
		data["Policy"] = pol
	}
	s.repoRender(w, r, a, repo, "overview", "repo", data)
	return nil
}

// kv is a key/value pair for generic sections (fleet budgets and evidence).
type kv struct {
	Key   string
	Value string
}

func sortedKV(m map[string]any) []kv {
	out := make([]kv, 0, len(m))
	for k, v := range m {
		var val string
		switch t := v.(type) {
		case string:
			val = t
		default:
			b, _ := json.Marshal(v)
			val = string(b)
		}
		out = append(out, kv{k, val})
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Key < out[j-1].Key; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func (s *Server) uiSetLevel(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	if err := s.DB.SetAutonomyLevel(r.Context(), repo, atoi(r.FormValue("level")), r.FormValue("reason"), a); err != nil {
		return err
	}
	s.redirectFlash(w, r, repoPath(repo), "Autonomy level set to L"+r.FormValue("level")+".", "")
	return nil
}

func (s *Server) uiRunAgent(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	if s.Loop == nil {
		return fmt.Errorf("%w: the agent loop is not configured on this server", errBadRequest)
	}
	req := agent.Request{Prompt: r.FormValue("prompt")}
	if n := r.FormValue("issue"); n != "" {
		issue, err := s.DB.Issue(r.Context(), repo, atoi(n))
		if err != nil {
			return err
		}
		req.Issue = &issue
	}
	if req.Issue == nil && strings.TrimSpace(req.Prompt) == "" {
		return fmt.Errorf("%w: an issue or a prompt is required", errBadRequest)
	}
	run, err := s.Loop.Start(r.Context(), repo, req, a)
	if err != nil {
		return err
	}
	s.redirectFlash(w, r, fmt.Sprintf("%s/agent/%s", repoPath(repo), run.ID), "Agent run started.", "")
	return nil
}

func (s *Server) uiRunPipeline(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	if s.Runner == nil {
		return fmt.Errorf("%w: no pipeline runner is configured", errBadRequest)
	}
	ref := r.FormValue("ref")
	if ref == "" {
		ref = repo.DefaultBranch
	}
	oid, err := gitx.RevParse(r.Context(), s.repoPath(repo), ref)
	if err != nil {
		return fmt.Errorf("%w: %v", errBadRequest, err)
	}
	run := ledger.PipelineRun{CommitOID: oid}
	if err := s.DB.InsertPipelineRun(r.Context(), repo, &run, a); err != nil {
		return err
	}
	go s.runQueued(repo, run, a)
	s.redirectFlash(w, r, fmt.Sprintf("%s/runs/%s", repoPath(repo), run.ID), "Pipeline run queued.", "")
	return nil
}

func (s *Server) uiDeploy(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	if s.Deployer == nil {
		return fmt.Errorf("%w: no deployer is configured", errBadRequest)
	}
	ref := r.FormValue("ref")
	if ref == "" {
		ref = repo.DefaultBranch
	}
	oid, err := gitx.RevParse(r.Context(), s.repoPath(repo), ref)
	if err != nil {
		return fmt.Errorf("%w: %v", errBadRequest, err)
	}
	env := r.FormValue("environment")
	if env == "" {
		env = "preview"
	}
	go s.Deployer.Deploy(context.Background(), repo, oid, env, a, nil)
	s.redirectFlash(w, r, repoPath(repo)+"/deployments", "Deployment to "+env+" requested.", "")
	return nil
}

func (s *Server) uiRollback(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	if s.Deployer == nil {
		return fmt.Errorf("%w: no deployer is configured", errBadRequest)
	}
	env := r.FormValue("environment")
	if env == "" {
		env = "production"
	}
	if _, err := s.Deployer.Rollback(context.WithoutCancel(r.Context()), repo, env, a); err != nil {
		return err
	}
	s.redirectFlash(w, r, repoPath(repo)+"/deployments", "Rollback of "+env+" requested.", "")
	return nil
}

// ---- issues ----

func (s *Server) uiIssues(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	all, err := s.DB.Issues(r.Context(), repo)
	if err != nil {
		return err
	}
	state := r.URL.Query().Get("state")
	var issues []ledger.Issue
	for _, i := range all {
		if state == "" || state == "all" || i.State == state {
			issues = append(issues, i)
		}
	}
	p, lo, hi := paginate(r, len(issues))
	s.repoRender(w, r, a, repo, "issues", "issues", map[string]any{"Title": "Issues · " + repo.Org + "/" + repo.Name, "Issues": issues[lo:hi], "Pager": p, "State": state})
	return nil
}

func (s *Server) uiNewIssue(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		return fmt.Errorf("%w: a title is required", errBadRequest)
	}
	issue, err := s.DB.CreateIssue(r.Context(), repo, title, r.FormValue("body"), a)
	if err != nil {
		return err
	}
	s.redirectFlash(w, r, fmt.Sprintf("%s/issues/%d", repoPath(repo), issue.Number), "Issue filed.", "")
	return nil
}

// issueComment is a comment on an issue (the gateway's comments table).
type issueComment struct {
	ID        string       `json:"id"`
	OrgID     string       `json:"org_id"`
	RepoID    string       `json:"repo_id"`
	Kind      string       `json:"target_kind"`
	TargetID  string       `json:"target_id"`
	Number    int          `json:"target_number"`
	Body      string       `json:"body"`
	Actor     ledger.Actor `json:"actor"`
	SessionID string       `json:"session_id,omitempty"`
	CreatedAt string       `json:"created_at"`
}

// issueComments reads an issue's comments; ok is false when the comments
// table (the gateway slice's migration) is absent.
func (s *Server) issueComments(ctx context.Context, repo ledger.Repo, issue ledger.Issue) ([]issueComment, bool) {
	if !s.tableExists(ctx, "comments") {
		return nil, false
	}
	rows, err := s.DB.Query(ctx, `SELECT id, body, actor_kind, actor_id, session_id, created_at FROM comments WHERE repo_id = ? AND target_kind = 'issue' AND target_id = ? ORDER BY created_at`, repo.ID, issue.ID)
	if err != nil {
		return nil, true
	}
	defer rows.Close()
	out := []issueComment{}
	for rows.Next() {
		c := issueComment{OrgID: repo.OrgID, RepoID: repo.ID, Kind: "issue", TargetID: issue.ID, Number: issue.Number}
		if err := rows.Scan(&c.ID, &c.Body, &c.Actor.Kind, &c.Actor.ID, &c.SessionID, &c.CreatedAt); err != nil {
			break
		}
		out = append(out, c)
	}
	return out, true
}

// postIssueComment writes a human comment the way the gateway writes agent
// comments: the same table, the same io.ore.comment.created event.
func (s *Server) postIssueComment(ctx context.Context, repo ledger.Repo, issue ledger.Issue, body string, actor ledger.Actor) error {
	if !s.tableExists(ctx, "comments") {
		return fmt.Errorf("%w: comments need the gateway extension", errBadRequest)
	}
	c := issueComment{ID: ledger.NewID("cmt"), OrgID: repo.OrgID, RepoID: repo.ID, Kind: "issue", TargetID: issue.ID, Number: issue.Number, Body: body, Actor: actor, CreatedAt: ledger.Now()}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, org_id, repo_id, target_kind, target_id, target_number, body, actor_kind, actor_id, session_id, created_at) VALUES(?,?,?,?,?,?,?,?,?,'',?)`,
		c.ID, c.OrgID, c.RepoID, c.Kind, c.TargetID, c.Number, c.Body, c.Actor.Kind, c.Actor.ID, c.CreatedAt); err != nil {
		return err
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.comment.created", fmt.Sprintf("%s/%s/issues/%d/comments/%s", repo.Org, repo.Name, issue.Number, c.ID), actor, c); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) uiIssue(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	issue, err := s.DB.Issue(r.Context(), repo, atoi(r.PathValue("n")))
	if err != nil {
		return err
	}
	comments, ok := s.issueComments(r.Context(), repo, issue)
	data := map[string]any{"Title": fmt.Sprintf("Issue #%d · %s/%s", issue.Number, repo.Org, repo.Name), "Issue": issue, "Comments": comments, "CommentsAvailable": ok}
	var html string
	if s.tableExists(r.Context(), "github_issue_map") {
		s.DB.QueryRow(r.Context(), `SELECT html_url FROM github_issue_map WHERE issue_id = ?`, issue.ID).Scan(&html)
	}
	data["GitHubURL"] = html
	aruns, _ := s.DB.AgentRuns(r.Context(), repo, 200)
	var mine []ledger.AgentRun
	for _, ar := range aruns {
		if ar.IssueID != nil && *ar.IssueID == issue.ID {
			mine = append(mine, ar)
		}
	}
	data["AgentRuns"] = mine
	s.repoRender(w, r, a, repo, "issues", "issue", data)
	return nil
}

func (s *Server) uiIssueComment(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	issue, err := s.DB.Issue(r.Context(), repo, atoi(r.PathValue("n")))
	if err != nil {
		return err
	}
	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		return fmt.Errorf("%w: a comment body is required", errBadRequest)
	}
	if len(body) > 65536 {
		return fmt.Errorf("%w: comments are at most 65536 bytes", errBadRequest)
	}
	if err := s.postIssueComment(r.Context(), repo, issue, body, a); err != nil {
		return err
	}
	s.redirectFlash(w, r, fmt.Sprintf("%s/issues/%d", repoPath(repo), issue.Number), "Comment posted.", "")
	return nil
}

func (s *Server) uiCloseIssue(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	issue, err := s.DB.Issue(r.Context(), repo, atoi(r.PathValue("n")))
	if err != nil {
		return err
	}
	if err := s.DB.CloseIssue(r.Context(), repo, issue, r.FormValue("reason"), a); err != nil {
		return err
	}
	s.redirectFlash(w, r, fmt.Sprintf("%s/issues/%d", repoPath(repo), issue.Number), "Issue closed.", "")
	return nil
}

// ---- changes ----

func (s *Server) uiChanges(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	all, err := s.DB.Changes(r.Context(), repo)
	if err != nil {
		return err
	}
	state := r.URL.Query().Get("state")
	var changes []ledger.Change
	for _, c := range all {
		if state == "" || state == "all" || c.State == state {
			changes = append(changes, c)
		}
	}
	p, lo, hi := paginate(r, len(changes))
	refs, _ := gitx.Refs(r.Context(), s.repoPath(repo))
	var branches []string
	for ref := range refs {
		if b, ok := strings.CutPrefix(ref, "refs/heads/"); ok && b != repo.DefaultBranch {
			branches = append(branches, b)
		}
	}
	for i := 1; i < len(branches); i++ {
		for j := i; j > 0 && branches[j] < branches[j-1]; j-- {
			branches[j], branches[j-1] = branches[j-1], branches[j]
		}
	}
	s.repoRender(w, r, a, repo, "changes", "changes", map[string]any{"Title": "Changes · " + repo.Org + "/" + repo.Name, "Changes": changes[lo:hi], "Pager": p, "State": state, "Branches": branches})
	return nil
}

func (s *Server) uiOpenChange(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	src := strings.TrimSpace(r.FormValue("source"))
	if src == "" {
		return fmt.Errorf("%w: a source branch is required", errBadRequest)
	}
	ch, err := s.Review.Open(r.Context(), repo, review.OpenRequest{SourceRef: src, TargetRef: r.FormValue("target"), Title: r.FormValue("title"), Body: r.FormValue("body")}, a)
	if err != nil {
		return err
	}
	s.redirectFlash(w, r, fmt.Sprintf("%s/changes/%d", repoPath(repo), ch.Number), "Change opened.", "")
	return nil
}

// lane groups comments for the collapsible lanes.
type lane struct {
	Name     string
	Comments []review.Comment
	Open     bool
}

func (s *Server) changeOf(r *http.Request) (ledger.Repo, ledger.Change, error) {
	repo, err := s.repoFor(r)
	if err != nil {
		return repo, ledger.Change{}, err
	}
	ch, err := s.DB.Change(r.Context(), repo, atoi(r.PathValue("n")))
	return repo, ch, err
}

func (s *Server) uiChange(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, ch, err := s.changeOf(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	q := r.URL.Query()
	if synced, _, err := s.Review.SyncPatchSets(ctx, repo, ch); err == nil {
		ch = synced
	}
	data := map[string]any{"Title": fmt.Sprintf("Change #%d · %s/%s", ch.Number, repo.Org, repo.Name), "Change": ch, "Strategies": []string{review.StrategyQueue, review.StrategyLandWhenGreen, review.StrategyOptimistic}}
	patchSets, _ := s.Review.PatchSets(ctx, repo, ch)
	data["PatchSets"] = patchSets
	var selected *review.PatchSet
	if n, _ := strconv.Atoi(q.Get("ps")); n > 0 {
		for i := range patchSets {
			if patchSets[i].Number == n {
				selected = &patchSets[i]
			}
		}
	} else if len(patchSets) > 0 {
		selected = &patchSets[len(patchSets)-1]
	}
	data["Selected"] = selected
	var diff string
	var diffErr error
	if selected != nil {
		diff, diffErr = gitx.Diff(ctx, s.repoPath(repo), selected.BaseOID, selected.HeadOID)
	} else {
		diff, diffErr = s.Review.Diff(ctx, repo, ch)
	}
	if diffErr != nil {
		data["DiffError"] = diffErr.Error()
	}
	html, st := renderDiff(diff)
	data["Diff"], data["DiffStats"] = html, st
	from, _ := strconv.Atoi(q.Get("from"))
	to, _ := strconv.Atoi(q.Get("to"))
	data["From"], data["To"] = from, to
	if from > 0 && to > 0 {
		if id, err := s.Review.Interdiff(ctx, repo, ch, from, to, false); err == nil {
			ih, ist := renderDiff(id.Diff)
			data["Interdiff"], data["InterdiffHTML"], data["InterdiffStats"] = id, ih, ist
		} else {
			data["InterdiffError"] = err.Error()
		}
	}
	comments, _ := s.Review.Comments(ctx, repo, ch)
	lanes := []lane{{Name: review.LaneHuman, Open: true}, {Name: review.LaneAgent, Open: true}, {Name: review.LaneBot}}
	for _, c := range comments {
		for i := range lanes {
			if lanes[i].Name == c.Lane {
				lanes[i].Comments = append(lanes[i].Comments, c)
			}
		}
	}
	data["Lanes"] = lanes
	data["CommentCount"] = len(comments)
	reviews, _ := s.Review.Reviews(ctx, repo, ch)
	data["Reviews"] = reviews
	if st, err := s.Review.Approvals(ctx, repo, ch); err == nil {
		data["Approvals"] = st
	}
	if pol, err := s.Review.Policy(ctx, repo); err == nil {
		data["Policy"] = pol
	}
	landings, _ := s.Review.Landings(ctx, repo, ch)
	data["Landings"] = landings
	if stacks, err := s.Review.Stacks(ctx, repo); err == nil {
		var mine []review.Stack
		for _, st := range stacks {
			for _, id := range st.ChangeIDs {
				if id == ch.ID {
					mine = append(mine, st)
					break
				}
			}
		}
		data["Stacks"] = mine
	}
	all, _ := s.DB.PipelineRuns(ctx, repo, 200)
	var runs []ledger.PipelineRun
	for _, p := range all {
		if p.ChangeID != nil && *p.ChangeID == ch.ID {
			runs = append(runs, p)
		}
	}
	data["Runs"] = runs
	var prov map[string]any
	if json.Unmarshal([]byte(ch.Provenance), &prov) == nil {
		data["Provenance"] = sortedKV(prov)
	}
	s.repoRender(w, r, a, repo, "changes", "change", data)
	return nil
}

func (s *Server) changeURL(repo ledger.Repo, ch ledger.Change) string {
	return fmt.Sprintf("%s/changes/%d", repoPath(repo), ch.Number)
}

func (s *Server) uiChangeComment(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, ch, err := s.changeOf(r)
	if err != nil {
		return err
	}
	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		return fmt.Errorf("%w: a comment body is required", errBadRequest)
	}
	req := review.CommentRequest{Body: body, Path: strings.TrimSpace(r.FormValue("path")), Lane: r.FormValue("lane")}
	req.PatchSet, _ = strconv.Atoi(r.FormValue("patch_set"))
	req.StartLine, _ = strconv.Atoi(r.FormValue("start_line"))
	req.EndLine, _ = strconv.Atoi(r.FormValue("end_line"))
	if _, err := s.Review.PostComment(r.Context(), repo, ch, req, a); err != nil {
		return err
	}
	s.redirectFlash(w, r, s.changeURL(repo, ch), "Comment posted.", "")
	return nil
}

func (s *Server) uiResolveComment(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, ch, err := s.changeOf(r)
	if err != nil {
		return err
	}
	resolved := r.FormValue("resolved") != "0"
	if _, err := s.Review.ResolveComment(r.Context(), repo, ch, r.PathValue("id"), resolved, a); err != nil {
		return err
	}
	msg := "Comment resolved."
	if !resolved {
		msg = "Comment reopened."
	}
	s.redirectFlash(w, r, s.changeURL(repo, ch), msg, "")
	return nil
}

func (s *Server) uiReview(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, ch, err := s.changeOf(r)
	if err != nil {
		return err
	}
	req := review.ReviewRequest{Verdict: r.FormValue("verdict"), Body: r.FormValue("body"), Lane: r.FormValue("lane")}
	req.PatchSet, _ = strconv.Atoi(r.FormValue("patch_set"))
	if f := strings.TrimSpace(r.FormValue("findings")); f != "" {
		if err := json.Unmarshal([]byte(f), &req.Findings); err != nil {
			return fmt.Errorf("%w: findings must be a JSON array of {severity, confidence, path, message}: %v", errBadRequest, err)
		}
	}
	if _, err := s.Review.SubmitReview(r.Context(), repo, ch, req, a); err != nil {
		return err
	}
	s.redirectFlash(w, r, s.changeURL(repo, ch), "Review submitted: "+req.Verdict+".", "")
	return nil
}

func (s *Server) uiLand(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, ch, err := s.changeOf(r)
	if err != nil {
		return err
	}
	strategy := r.FormValue("strategy")
	landing, err := s.Review.Land(context.WithoutCancel(r.Context()), repo, ch, review.LandRequest{Strategy: strategy}, a)
	if err != nil {
		s.redirectFlash(w, r, s.changeURL(repo, ch), "", err.Error())
		return nil
	}
	s.redirectFlash(w, r, s.changeURL(repo, ch), fmt.Sprintf("Landing %s: %s.", landing.Strategy, landing.Status), "")
	return nil
}

func (s *Server) uiVerify(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, ch, err := s.changeOf(r)
	if err != nil {
		return err
	}
	if s.Runner == nil {
		return fmt.Errorf("%w: no pipeline runner is configured", errBadRequest)
	}
	run := ledger.PipelineRun{CommitOID: ch.HeadOID, ChangeID: &ch.ID}
	if err := s.DB.InsertPipelineRun(r.Context(), repo, &run, a); err != nil {
		return err
	}
	go s.runQueued(repo, run, a)
	s.redirectFlash(w, r, fmt.Sprintf("%s/runs/%s", repoPath(repo), run.ID), "Verification run queued on the change's head.", "")
	return nil
}

func (s *Server) uiCloseChange(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, ch, err := s.changeOf(r)
	if err != nil {
		return err
	}
	if err := s.DB.CloseChange(r.Context(), repo, ch, r.FormValue("reason"), a); err != nil {
		return err
	}
	s.redirectFlash(w, r, s.changeURL(repo, ch), "Change closed.", "")
	return nil
}

// ---- stacks ----

func (s *Server) uiStacks(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	stacks, _ := s.Review.Stacks(r.Context(), repo)
	p, lo, hi := paginate(r, len(stacks))
	s.repoRender(w, r, a, repo, "stacks", "stacks", map[string]any{"Title": "Stacks · " + repo.Org + "/" + repo.Name, "Stacks": stacks[lo:hi], "Pager": p, "Strategies": []string{review.StrategyQueue, review.StrategyLandWhenGreen, review.StrategyOptimistic}})
	return nil
}

func (s *Server) uiOpenStack(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	var branches []string
	for _, b := range strings.Split(r.FormValue("branches"), "\n") {
		if b = strings.TrimSpace(b); b != "" {
			branches = append(branches, b)
		}
	}
	if len(branches) == 0 {
		return fmt.Errorf("%w: list the branches bottom first, one per line", errBadRequest)
	}
	st, err := s.Review.OpenStack(r.Context(), repo, review.StackRequest{Branches: branches, TargetRef: r.FormValue("target")}, a)
	if err != nil {
		return err
	}
	s.redirectFlash(w, r, fmt.Sprintf("%s/stacks/%s", repoPath(repo), st.ID), "Stack opened.", "")
	return nil
}

func (s *Server) uiStack(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	st, err := s.Review.Stack(r.Context(), repo, r.PathValue("id"))
	if err != nil {
		return err
	}
	s.repoRender(w, r, a, repo, "stacks", "stack", map[string]any{"Title": "Stack " + st.ID, "Stack": st, "Strategies": []string{review.StrategyQueue, review.StrategyLandWhenGreen, review.StrategyOptimistic}})
	return nil
}

func (s *Server) uiLandStack(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	st, err := s.Review.Stack(r.Context(), repo, r.PathValue("id"))
	if err != nil {
		return err
	}
	back := fmt.Sprintf("%s/stacks/%s", repoPath(repo), st.ID)
	st, _, err = s.Review.LandStack(context.WithoutCancel(r.Context()), repo, st, review.LandRequest{Strategy: r.FormValue("strategy")}, a)
	if err != nil {
		s.redirectFlash(w, r, back, "", err.Error())
		return nil
	}
	s.redirectFlash(w, r, back, "Stack "+st.State+".", "")
	return nil
}

// ---- pipeline runs ----

// taskView is the per-task record of a run, decoded loosely so both the
// v0 task shape and the richer workflow shape render.
type taskView struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Image       string            `json:"image"`
	Status      string            `json:"status"`
	ExitCode    int               `json:"exit_code"`
	DurationMS  int64             `json:"duration_ms"`
	Reason      string            `json:"reason"`
	ContentHash string            `json:"content_hash"`
	CacheHit    bool              `json:"cache_hit"`
	MatrixKey   map[string]any    `json:"matrix_key"`
	Steps       []json.RawMessage `json:"steps"`
	Divergences []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"divergences"`
	Annotations []struct {
		Level   string `json:"level"`
		Message string `json:"message"`
		File    string `json:"file"`
		Line    int    `json:"line"`
	} `json:"annotations"`
	Summary string `json:"summary"`
}

// storedPlan mirrors the pipelines/actions plan route.
type storedPlan struct {
	Source      string `json:"source"`
	Frontend    string `json:"frontend"`
	Refused     string `json:"refused"`
	Divergences []struct {
		Code     string `json:"code"`
		Message  string `json:"message"`
		Location string `json:"location"`
	} `json:"divergences"`
	IR struct {
		Tasks []struct {
			ID       string   `json:"id"`
			Name     string   `json:"name"`
			Needs    []string `json:"needs"`
			Executor struct {
				Image string `json:"image"`
			} `json:"executor"`
			Inputs struct {
				Caches []struct {
					Key string `json:"key"`
				} `json:"caches"`
			} `json:"inputs"`
		} `json:"tasks"`
	} `json:"ir"`
}

// artifactView mirrors the artifacts route.
type artifactView struct {
	Name   string `json:"name"`
	TaskID string `json:"task_id"`
	Size   int64  `json:"size"`
	Files  int    `json:"files"`
	Digest string `json:"digest"`
	Href   string `json:"-"`
}

func (s *Server) uiRuns(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	runs, _ := s.DB.PipelineRuns(r.Context(), repo, 1000)
	p, lo, hi := paginate(r, len(runs))
	s.repoRender(w, r, a, repo, "runs", "runs", map[string]any{"Title": "Pipeline runs · " + repo.Org + "/" + repo.Name, "Runs": runs[lo:hi], "Pager": p})
	return nil
}

func (s *Server) uiRun(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	run, err := s.DB.PipelineRun(r.Context(), r.PathValue("id"))
	if err != nil || run.RepoID != repo.ID {
		return ledger.ErrNotFound
	}
	var tasks []taskView
	json.Unmarshal([]byte(run.Tasks), &tasks)
	logText, offset, truncated := loadLog(run.LogPath)
	data := map[string]any{"Title": "Pipeline run " + run.ID, "Run": run, "Tasks": tasks, "Log": logText, "LogOffset": offset, "LogTruncated": truncated,
		"StreamURL": fmt.Sprintf("/api/v0/repos/%s/%s/runs/%s/log/stream", repo.Org, repo.Name, run.ID),
		"Live":      run.Status == "queued" || run.Status == "running"}
	base := fmt.Sprintf("/api/v0/repos/%s/%s/runs/%s", repo.Org, repo.Name, run.ID)
	if res := s.api(r, http.MethodGet, base+"/plan", nil); res.OK() {
		var plans []storedPlan
		if json.Unmarshal(res.Body, &plans) == nil {
			data["Plans"] = plans
		}
	}
	if res := s.api(r, http.MethodGet, base+"/artifacts", nil); res.OK() {
		var arts []artifactView
		if json.Unmarshal(res.Body, &arts) == nil {
			for i := range arts {
				arts[i].Href = base + "/artifacts/" + arts[i].Name
			}
			data["Artifacts"] = arts
		}
	}
	if run.ChangeID != nil {
		changes, _ := s.DB.Changes(r.Context(), repo)
		for _, c := range changes {
			if c.ID == *run.ChangeID {
				data["Change"] = c
			}
		}
	}
	s.repoRender(w, r, a, repo, "runs", "run", data)
	return nil
}

func (s *Server) uiAgentRuns(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	runs, _ := s.DB.AgentRuns(r.Context(), repo, 1000)
	p, lo, hi := paginate(r, len(runs))
	s.repoRender(w, r, a, repo, "agent", "agentruns", map[string]any{"Title": "Agent runs · " + repo.Org + "/" + repo.Name, "Runs": runs[lo:hi], "Pager": p})
	return nil
}

func (s *Server) uiAgentRun(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	run, err := s.DB.AgentRun(r.Context(), r.PathValue("id"))
	if err != nil || run.RepoID != repo.ID {
		return ledger.ErrNotFound
	}
	logText, offset, truncated := loadLog(run.LogPath)
	data := map[string]any{"Title": "Agent run " + run.ID, "Run": run, "Log": logText, "LogOffset": offset, "LogTruncated": truncated,
		"StreamURL": fmt.Sprintf("/api/v0/repos/%s/%s/agent/runs/%s/log/stream", repo.Org, repo.Name, run.ID), "Live": run.Status == "running"}
	if run.ChangeID != nil {
		changes, _ := s.DB.Changes(r.Context(), repo)
		for _, c := range changes {
			if c.ID == *run.ChangeID {
				data["Change"] = c
			}
		}
	}
	if run.IssueID != nil {
		issues, _ := s.DB.Issues(r.Context(), repo)
		for _, i := range issues {
			if i.ID == *run.IssueID {
				data["Issue"] = i
			}
		}
	}
	s.repoRender(w, r, a, repo, "agent", "agentrun", data)
	return nil
}

var _ = errors.New
