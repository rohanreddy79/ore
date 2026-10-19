// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"fmt"
	"net/http"

	"github.com/orevcs/ore/internal/ledger"
)

// mountUI mounts every HTML route. Pages are server-rendered; forms post to
// /ui/... handlers that call the services (or the slices' API routes) and
// redirect back with a flash.
func (s *Server) mountUI(mux *http.ServeMux) {
	s.mountAuth(mux)
	s.page(mux, "GET /{$}", s.uiIndex)
	s.page(mux, "GET /events", s.uiLedger)
	s.mountIdentityUI(mux)
	s.mountRepoUI(mux)
	s.mountOpsUI(mux)
}

func (s *Server) uiIndex(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repos, err := s.DB.Repos(r.Context(), "")
	if err != nil {
		return err
	}
	evs, _ := s.eventsPage(r.Context(), eventFilter{Limit: 25})
	s.render(w, r, &a, http.StatusOK, "index", map[string]any{"Title": "Repositories", "Repos": repos, "Events": evs, "Stream": fmt.Sprintf("/api/v0/events/stream?after=%d", s.latestSeq(r.Context()))})
	return nil
}

// navTab is one tab of the repository navigation.
type navTab struct{ Key, Name, Href string }

func repoTabs(repo ledger.Repo) []navTab {
	p := repoPath(repo)
	return []navTab{
		{"overview", "Overview", p}, {"issues", "Issues", p + "/issues"}, {"changes", "Changes", p + "/changes"},
		{"stacks", "Stacks", p + "/stacks"}, {"runs", "Pipeline runs", p + "/runs"}, {"agent", "Agent runs", p + "/agent"},
		{"deployments", "Deployments", p + "/deployments"}, {"environments", "Environments", p + "/environments"},
		{"dora", "DORA", p + "/dora"}, {"github", "GitHub", p + "/github"}, {"gateway", "Gateway", p + "/gateway"},
	}
}

// repoRender renders a repository page with the navigation filled in.
func (s *Server) repoRender(w http.ResponseWriter, r *http.Request, a ledger.Actor, repo ledger.Repo, tab, name string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["Repo"] = repo
	data["RepoPath"] = repoPath(repo)
	data["Tab"] = tab
	data["Tabs"] = repoTabs(repo)
	if _, ok := data["Title"]; !ok {
		data["Title"] = repo.Org + "/" + repo.Name
	}
	s.render(w, r, &a, http.StatusOK, name, data)
}
