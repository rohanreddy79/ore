// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

func (s *Server) mountOpsUI(mux *http.ServeMux) {
	s.page(mux, "GET /{org}/{name}/deployments", s.uiDeployments)
	s.page(mux, "GET /{org}/{name}/deployments/{id}", s.uiDeployment)
	s.page(mux, "POST /ui/{org}/{name}/deployments/{id}/approve", s.uiApprove)
	s.page(mux, "POST /ui/{org}/{name}/deployments/{id}/reject", s.uiReject)
	s.page(mux, "POST /ui/{org}/{name}/deployments/{id}/extend", s.uiExtend)
	s.page(mux, "GET /{org}/{name}/environments", s.uiEnvironments)
	s.page(mux, "POST /ui/{org}/{name}/environments", s.uiCreateEnvironment)
	s.page(mux, "POST /ui/{org}/{name}/environments/{env}/deploy", s.uiEnvDeploy)
	s.page(mux, "POST /ui/{org}/{name}/environments/{env}/rollback", s.uiEnvRollback)
	s.page(mux, "POST /ui/{org}/{name}/environments/{env}/stop", s.uiEnvStop)
	s.page(mux, "POST /ui/{org}/{name}/environments/{env}/delete", s.uiEnvDelete)
	s.page(mux, "GET /{org}/{name}/dora", s.uiDORA)
	s.page(mux, "GET /{org}/{name}/github", s.uiGitHub)
	s.page(mux, "POST /ui/{org}/{name}/github/link", s.uiGitHubLink)
	s.page(mux, "POST /ui/{org}/{name}/github/sync", s.uiGitHubSync)
	s.page(mux, "POST /ui/{org}/{name}/github/unlink", s.uiGitHubUnlink)
	s.page(mux, "GET /{org}/{name}/gateway", s.uiGateway)
	s.page(mux, "POST /ui/{org}/{name}/gateway/sessions", s.uiGatewayOpen)
	s.page(mux, "POST /ui/{org}/{name}/gateway/sessions/{id}/close", s.uiGatewayClose)
}

// ---- deployments ----

func (s *Server) uiDeployments(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	all, _ := s.DB.Deployments(r.Context(), repo, 1000)
	env := r.URL.Query().Get("environment")
	var deps []ledger.Deployment
	for _, d := range all {
		if env == "" || d.Environment == env {
			deps = append(deps, d)
		}
	}
	p, lo, hi := paginate(r, len(deps))
	data := map[string]any{"Title": "Deployments · " + repo.Org + "/" + repo.Name, "Deployments": deps[lo:hi], "Pager": p, "Environment": env}
	if svc := s.delivery(); svc != nil {
		envs, _ := svc.Environments(r.Context(), repo)
		data["Environments"] = envs
	}
	s.repoRender(w, r, a, repo, "deployments", "deployments", data)
	return nil
}

func (s *Server) deploymentOf(r *http.Request, repo ledger.Repo) (delivery.DeploymentView, error) {
	id := r.PathValue("id")
	if svc := s.delivery(); svc != nil {
		v, err := svc.View(r.Context(), id)
		if err != nil || v.RepoID != repo.ID {
			return v, ledger.ErrNotFound
		}
		return v, nil
	}
	d, err := s.DB.Deployment(r.Context(), id)
	if err != nil || d.RepoID != repo.ID {
		return delivery.DeploymentView{}, ledger.ErrNotFound
	}
	return delivery.DeploymentView{Deployment: d}, nil
}

func (s *Server) uiDeployment(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	v, err := s.deploymentOf(r, repo)
	if err != nil {
		return err
	}
	data := map[string]any{"Title": "Deployment " + v.ID, "D": v, "Awaiting": v.Status == "awaiting_approval", "Preview": v.TTL != nil}
	if svc := s.delivery(); svc != nil {
		if env, err := svc.Environment(r.Context(), repo, v.Environment); err == nil {
			data["Env"] = env
			data["CanApprove"] = delivery.CanApprove(env, a)
		}
	}
	s.repoRender(w, r, a, repo, "deployments", "deployment", data)
	return nil
}

func (s *Server) needDelivery() (*delivery.Service, error) {
	svc := s.delivery()
	if svc == nil {
		return nil, fmt.Errorf("%w: the delivery environments extension is not attached", errBadRequest)
	}
	return svc, nil
}

func (s *Server) uiApprove(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	svc, err := s.needDelivery()
	if err != nil {
		return err
	}
	d, err := svc.Approve(context.WithoutCancel(r.Context()), repo, r.PathValue("id"), a, r.FormValue("comment"))
	if err != nil {
		return err
	}
	s.redirectFlash(w, r, fmt.Sprintf("%s/deployments/%s", repoPath(repo), d.ID), "Approved; deployment is "+d.Status+".", "")
	return nil
}

func (s *Server) uiReject(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	svc, err := s.needDelivery()
	if err != nil {
		return err
	}
	d, err := svc.Reject(r.Context(), repo, r.PathValue("id"), a, r.FormValue("comment"))
	if err != nil {
		return err
	}
	s.redirectFlash(w, r, fmt.Sprintf("%s/deployments/%s", repoPath(repo), d.ID), "Rejected.", "")
	return nil
}

func (s *Server) uiExtend(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	svc, err := s.needDelivery()
	if err != nil {
		return err
	}
	hours, _ := strconv.ParseFloat(r.FormValue("hours"), 64)
	if hours <= 0 {
		hours = 2
	}
	ttl, clamped, err := svc.Extend(r.Context(), repo, r.PathValue("id"), time.Duration(hours*float64(time.Hour)), a)
	if err != nil {
		return err
	}
	msg := "Preview extended until " + ttl.ExpiresAt + "."
	if clamped {
		msg += " (clamped to the 24 h maximum)"
	}
	s.redirectFlash(w, r, fmt.Sprintf("%s/deployments/%s", repoPath(repo), r.PathValue("id")), msg, "")
	return nil
}

// ---- environments ----

// envView is an environment with what the page shows next to it.
type envView struct {
	delivery.Environment
	Current  *ledger.Deployment
	Awaiting []ledger.Deployment
}

func (s *Server) uiEnvironments(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	data := map[string]any{"Title": "Environments · " + repo.Org + "/" + repo.Name, "Kinds": []string{delivery.KindProduction, "staging", "preview", "ephemeral"}}
	svc := s.delivery()
	data["Available"] = svc != nil
	if svc != nil {
		envs, _ := svc.Environments(r.Context(), repo)
		deps, _ := s.DB.Deployments(r.Context(), repo, 500)
		var views []envView
		for _, e := range envs {
			v := envView{Environment: e}
			if cur, err := s.DB.CurrentDeployment(r.Context(), repo, e.Name); err == nil {
				v.Current = &cur
			}
			for _, d := range deps {
				if d.Environment == e.Name && d.Status == "awaiting_approval" {
					v.Awaiting = append(v.Awaiting, d)
				}
			}
			views = append(views, v)
		}
		data["Envs"] = views
	}
	s.repoRender(w, r, a, repo, "environments", "environments", data)
	return nil
}

func (s *Server) uiCreateEnvironment(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	svc, err := s.needDelivery()
	if err != nil {
		return err
	}
	kind := r.FormValue("kind")
	if kind == "" {
		kind = delivery.KindForName(r.FormValue("name"))
	}
	env := delivery.Environment{Name: strings.TrimSpace(r.FormValue("name")), Kind: kind, Policy: delivery.DefaultPolicy(kind)}
	env.Target = delivery.Target{Kind: r.FormValue("target_kind"), Host: r.FormValue("host"), User: r.FormValue("user"), KeyRef: r.FormValue("key_ref")}
	env.Target.Port, _ = strconv.Atoi(r.FormValue("port"))
	if env.Target.Kind == "" {
		env.Target.Kind = delivery.TargetLocalDocker
	}
	p := &env.Policy
	if v := r.FormValue("required_approvals"); v != "" {
		p.RequiredApprovals, _ = strconv.Atoi(v)
	}
	if v := r.FormValue("wait_seconds"); v != "" {
		p.WaitSeconds, _ = strconv.Atoi(v)
	}
	if v := r.FormValue("health_timeout_seconds"); v != "" {
		p.HealthTimeoutSeconds, _ = strconv.Atoi(v)
	}
	if v := r.FormValue("preview_ttl_seconds"); v != "" {
		p.PreviewTTLSeconds, _ = strconv.Atoi(v)
	}
	if v := strings.TrimSpace(r.FormValue("health_path")); v != "" {
		p.HealthPath = v
	}
	p.Approvers = splitCSV(r.FormValue("approvers"))
	p.AllowedRefs = splitCSV(r.FormValue("allowed_refs"))
	p.AutoRollback = r.FormValue("auto_rollback") != ""
	p.RequireGreenPipeline = r.FormValue("require_green_pipeline") != ""
	p.AllowAgentDeploys = r.FormValue("allow_agent_deploys") != ""
	if err := svc.CreateEnvironment(r.Context(), repo, &env, a); err != nil {
		return err
	}
	s.redirectFlash(w, r, repoPath(repo)+"/environments", "Environment "+env.Name+" created.", "")
	return nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) envOf(r *http.Request, repo ledger.Repo) (*delivery.Service, delivery.Environment, error) {
	svc, err := s.needDelivery()
	if err != nil {
		return nil, delivery.Environment{}, err
	}
	env, err := svc.Environment(r.Context(), repo, r.PathValue("env"))
	return svc, env, err
}

func (s *Server) uiEnvDeploy(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	svc, env, err := s.envOf(r, repo)
	if err != nil {
		return err
	}
	ref := strings.TrimSpace(r.FormValue("ref"))
	if ref == "" {
		ref = repo.DefaultBranch
	}
	full := ref
	if !strings.HasPrefix(full, "refs/") && len(full) != 40 {
		full = "refs/heads/" + full
	}
	oid, err := gitx.RevParse(r.Context(), s.repoPath(repo), ref)
	if err != nil {
		return fmt.Errorf("%w: %v", errBadRequest, err)
	}
	req := delivery.DeployRequest{CommitOID: oid}
	if strings.HasPrefix(full, "refs/") {
		req.Ref = full
	}
	d, err := svc.Request(context.WithoutCancel(r.Context()), repo, env, req, a)
	if err != nil {
		return err
	}
	s.redirectFlash(w, r, fmt.Sprintf("%s/deployments/%s", repoPath(repo), d.ID), "Deployment "+d.Status+".", "")
	return nil
}

func (s *Server) uiEnvRollback(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	svc, env, err := s.envOf(r, repo)
	if err != nil {
		return err
	}
	d, err := svc.Rollback(context.WithoutCancel(r.Context()), repo, env, a)
	if err != nil {
		return err
	}
	s.redirectFlash(w, r, fmt.Sprintf("%s/deployments/%s", repoPath(repo), d.ID), "Rollback "+d.Status+".", "")
	return nil
}

func (s *Server) uiEnvStop(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	svc, env, err := s.envOf(r, repo)
	if err != nil {
		return err
	}
	if err := svc.Stop(context.WithoutCancel(r.Context()), repo, env, a, r.FormValue("reason")); err != nil {
		return err
	}
	s.redirectFlash(w, r, repoPath(repo)+"/environments", env.Name+" stopped.", "")
	return nil
}

func (s *Server) uiEnvDelete(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	svc, env, err := s.envOf(r, repo)
	if err != nil {
		return err
	}
	if err := svc.DeleteEnvironment(r.Context(), repo, env, a); err != nil {
		return err
	}
	s.redirectFlash(w, r, repoPath(repo)+"/environments", "Environment "+env.Name+" deleted; its deployments stay on the ledger.", "")
	return nil
}

// ---- DORA ----

func (s *Server) uiDORA(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 30
	}
	data := map[string]any{"Title": "DORA · " + repo.Org + "/" + repo.Name, "Days": days}
	if svc := s.delivery(); svc != nil {
		d, err := svc.ComputeDORA(r.Context(), repo, days)
		if err != nil {
			return err
		}
		data["DORA"] = d
	}
	s.repoRender(w, r, a, repo, "dora", "dora", data)
	return nil
}

// ---- GitHub ----

// ghLink is the part of the adapter's link response the page shows.
type ghLink struct {
	Owner          string `json:"owner"`
	Name           string `json:"name"`
	HTMLURL        string `json:"html_url"`
	DefaultBranch  string `json:"default_branch"`
	Secret         string `json:"secret"`
	LastSyncAt     string `json:"last_sync_at"`
	LastSyncStatus string `json:"last_sync_status"`
	LastSyncError  string `json:"last_sync_error"`
	NextSyncAt     string `json:"next_sync_at"`
	CreatedAt      string `json:"created_at"`
}

func (s *Server) uiGitHub(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	data := map[string]any{"Title": "GitHub · " + repo.Org + "/" + repo.Name}
	res := s.api(r, http.MethodGet, fmt.Sprintf("/api/v0/repos/%s/%s/github/link", repo.Org, repo.Name), nil)
	data["Available"] = res.Code != http.StatusNotFound || json.Valid(res.Body)
	if res.OK() {
		var raw map[string]json.RawMessage
		json.Unmarshal(res.Body, &raw)
		var link ghLink
		if l, ok := raw["link"]; ok {
			json.Unmarshal(l, &link)
		} else {
			json.Unmarshal(res.Body, &link)
		}
		var hook string
		if h, ok := raw["webhook_url"]; ok {
			json.Unmarshal(h, &hook)
		}
		if hook == "" {
			hook = s.BaseURL + "/hooks/github"
		}
		data["Link"], data["WebhookURL"] = link, hook
	} else if res.Code != http.StatusNotFound {
		data["Error"] = res.Err().Error()
	}
	evs, _ := s.eventsPage(r.Context(), eventFilter{Type: "io.ore.github.", SubjectPrefix: repo.Org + "/" + repo.Name, Limit: 20})
	data["Events"] = evs
	s.repoRender(w, r, a, repo, "github", "github", data)
	return nil
}

func (s *Server) uiGitHubLink(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	owner, name := strings.TrimSpace(r.FormValue("owner")), strings.TrimSpace(r.FormValue("name"))
	if o, n, ok := strings.Cut(r.FormValue("repository"), "/"); ok && owner == "" {
		owner, name = strings.TrimSpace(o), strings.TrimSpace(n)
	}
	if owner == "" || name == "" {
		return fmt.Errorf("%w: owner and name are required", errBadRequest)
	}
	res := s.api(r, http.MethodPost, fmt.Sprintf("/api/v0/repos/%s/%s/github/link", repo.Org, repo.Name), map[string]string{"owner": owner, "name": name})
	if err := res.Err(); err != nil {
		return err
	}
	s.redirectFlash(w, r, repoPath(repo)+"/github", "Linked to "+owner+"/"+name+" and mirrored.", "")
	return nil
}

func (s *Server) uiGitHubSync(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	res := s.api(r, http.MethodPost, fmt.Sprintf("/api/v0/repos/%s/%s/github/sync", repo.Org, repo.Name), map[string]any{})
	if err := res.Err(); err != nil {
		return err
	}
	var out struct {
		Status  string `json:"status"`
		Updates []any  `json:"updates"`
	}
	json.Unmarshal(res.Body, &out)
	msg := "Sync " + out.Status
	if out.Status == "" {
		msg = "Sync requested"
	}
	s.redirectFlash(w, r, repoPath(repo)+"/github", fmt.Sprintf("%s (%d ref updates).", msg, len(out.Updates)), "")
	return nil
}

func (s *Server) uiGitHubUnlink(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	res := s.api(r, http.MethodDelete, fmt.Sprintf("/api/v0/repos/%s/%s/github/link", repo.Org, repo.Name), nil)
	if err := res.Err(); err != nil {
		return err
	}
	s.redirectFlash(w, r, repoPath(repo)+"/github", "Unlinked.", "")
	return nil
}

// ---- gateway ----

// gwSession mirrors the gateway's session JSON.
type gwSession struct {
	ID           string  `json:"id"`
	AgentID      string  `json:"agent_id"`
	Repo         string  `json:"repo"`
	Scope        string  `json:"scope"`
	State        string  `json:"state"`
	Writes       int     `json:"writes"`
	Refused      int     `json:"refused"`
	WindowWrites int     `json:"window_writes"`
	CreatedAt    string  `json:"created_at"`
	ClosedAt     *string `json:"closed_at"`
}

func (s *Server) gatewayData(r *http.Request, repo ledger.Repo) map[string]any {
	data := map[string]any{"Title": "Gateway · " + repo.Org + "/" + repo.Name, "MCPURL": s.BaseURL + "/mcp"}
	res := s.api(r, http.MethodGet, "/api/v0/gateway/sessions?repo="+repo.Org+"/"+repo.Name, nil)
	data["Available"] = res.Code != http.StatusNotFound || json.Valid(res.Body)
	switch {
	case res.OK():
		var sessions []gwSession
		json.Unmarshal(res.Body, &sessions)
		data["Sessions"] = sessions
	case res.Code == http.StatusForbidden, res.Code == http.StatusUnauthorized:
		data["Forbidden"] = true
	case res.Code != http.StatusNotFound:
		data["Error"] = res.Err().Error()
	}
	evs, _ := s.eventsPage(r.Context(), eventFilter{Type: "io.ore.agent.session.", SubjectPrefix: repo.Org + "/" + repo.Name + "/gateway/", Limit: 20})
	data["Events"] = evs
	return data
}

func (s *Server) uiGateway(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	s.repoRender(w, r, a, repo, "gateway", "gateway", s.gatewayData(r, repo))
	return nil
}

func (s *Server) uiGatewayOpen(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	agentID := strings.TrimSpace(r.FormValue("agent_id"))
	if agentID == "" {
		return fmt.Errorf("%w: an agent id is required", errBadRequest)
	}
	res := s.api(r, http.MethodPost, "/api/v0/gateway/sessions", map[string]string{"agent_id": agentID, "repo": repo.Org + "/" + repo.Name})
	if err := res.Err(); err != nil {
		return err
	}
	var out struct {
		Session gwSession `json:"session"`
		Token   string    `json:"token"`
	}
	json.Unmarshal(res.Body, &out)
	data := s.gatewayData(r, repo)
	data["NewSession"], data["NewToken"] = out.Session, out.Token
	s.repoRender(w, r, a, repo, "gateway", "gateway", data) // the token is shown once, on this response
	return nil
}

func (s *Server) uiGatewayClose(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repoFor(r)
	if err != nil {
		return err
	}
	res := s.api(r, http.MethodPost, "/api/v0/gateway/"+r.PathValue("id")+"/close", map[string]string{"reason": r.FormValue("reason")})
	if err := res.Err(); err != nil {
		return err
	}
	s.redirectFlash(w, r, repoPath(repo)+"/gateway", "Session closed.", "")
	return nil
}
