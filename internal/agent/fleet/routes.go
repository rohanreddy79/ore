// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/orevcs/ore/internal/agent"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/web"
)

// Routes mounts the fleet API: instance scope under /api/v0/fleet/ and
// repository scope under /api/v0/repos/{org}/{name}/fleet/.
func Routes(mux *http.ServeMux, s *web.Server, svc *Service) {
	instance := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			actor := s.RequireActor(w, r)
			if actor == nil {
				return
			}
			if err := fn(w, r, *actor); err != nil {
				writeErr(w, err)
			}
		})
	}
	repoRoute := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, actor ledger.Actor) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			actor := s.RequireActor(w, r)
			if actor == nil {
				return
			}
			repo, err := s.DB.RepoByName(r.Context(), r.PathValue("org"), r.PathValue("name"))
			if err != nil {
				writeErr(w, err)
				return
			}
			if err := fn(w, r, repo, *actor); err != nil {
				writeErr(w, err)
			}
		})
	}
	const base = "/api/v0/repos/{org}/{name}/fleet"

	// instance scope
	instance("GET /api/v0/fleet", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		paused, reason, err := svc.InstancePaused(r.Context())
		if err != nil {
			return err
		}
		seq, _ := svc.cursor(r.Context())
		rows, err := s.DB.Query(r.Context(), `SELECT r.id, o.name, r.name, f.pause_reason FROM fleet_settings f JOIN repos r ON r.id = f.repo_id JOIN orgs o ON o.id = r.org_id WHERE f.paused = 1 ORDER BY o.name, r.name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		pausedRepos := []map[string]string{}
		for rows.Next() {
			var id, org, name, why string
			if rows.Scan(&id, &org, &name, &why) == nil {
				pausedRepos = append(pausedRepos, map[string]string{"repo_id": id, "repo": org + "/" + name, "reason": why})
			}
		}
		return web.JSON(w, 200, map[string]any{"paused": paused, "pause_reason": reason, "watcher": map[string]any{"id": svc.watcherID(), "after_seq": seq}, "paused_repositories": pausedRepos, "roles": RoleNames})
	})
	instance("POST /api/v0/fleet/pause", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		var in struct {
			Reason string `json:"reason"`
		}
		if err := decode(r, &in, true); err != nil {
			return err
		}
		if err := svc.Pause(r.Context(), nil, in.Reason, a); err != nil {
			return err
		}
		return web.JSON(w, 200, map[string]any{"paused": true, "scope": "instance", "reason": in.Reason})
	})
	instance("POST /api/v0/fleet/resume", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
		if err := svc.Resume(r.Context(), nil, a); err != nil {
			return err
		}
		return web.JSON(w, 200, map[string]any{"paused": false, "scope": "instance"})
	})

	// repository scope
	repoRoute("GET "+base, func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		st, err := svc.RepoStatus(r.Context(), repo)
		if err != nil {
			return err
		}
		return web.JSON(w, 200, st)
	})
	repoRoute("POST "+base+"/pause", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		var in struct {
			Reason string `json:"reason"`
		}
		if err := decode(r, &in, true); err != nil {
			return err
		}
		if err := svc.Pause(r.Context(), &repo, in.Reason, a); err != nil {
			return err
		}
		return web.JSON(w, 200, map[string]any{"paused": true, "scope": "repository", "repo": repo.Org + "/" + repo.Name, "reason": in.Reason})
	})
	repoRoute("POST "+base+"/resume", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		if err := svc.Resume(r.Context(), &repo, a); err != nil {
			return err
		}
		return web.JSON(w, 200, map[string]any{"paused": false, "scope": "repository", "repo": repo.Org + "/" + repo.Name})
	})
	repoRoute("GET "+base+"/settings", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		st, err := svc.Settings(r.Context(), repo)
		if err != nil {
			return err
		}
		return web.JSON(w, 200, st)
	})
	repoRoute("PUT "+base+"/settings", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		var in SettingsInput
		if err := decode(r, &in, false); err != nil {
			return err
		}
		st, err := svc.SetSettings(r.Context(), repo, in, a)
		if err != nil {
			return err
		}
		return web.JSON(w, 200, st)
	})
	repoRoute("GET "+base+"/roles", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		roles, err := svc.Roles(r.Context(), repo)
		if err != nil {
			return err
		}
		return web.JSON(w, 200, roles)
	})
	repoRoute("GET "+base+"/roles/{role}", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		role, err := svc.role(r.Context(), repo, r.PathValue("role"))
		if err != nil {
			return err
		}
		return web.JSON(w, 200, role)
	})
	repoRoute("PUT "+base+"/roles/{role}", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		var in RoleInput
		if err := decode(r, &in, false); err != nil {
			return err
		}
		role, err := svc.SetRole(r.Context(), repo, r.PathValue("role"), in, a)
		if err != nil {
			return err
		}
		return web.JSON(w, 200, role)
	})
	repoRoute("GET "+base+"/budgets", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		list, err := svc.Budgets(r.Context(), repo)
		if err != nil {
			return err
		}
		return web.JSON(w, 200, list)
	})
	repoRoute("PUT "+base+"/budgets", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		var in struct {
			Principal string `json:"principal"`
			Budget    Budget `json:"budget"`
		}
		if err := decode(r, &in, false); err != nil {
			return err
		}
		st, err := svc.SetBudget(r.Context(), repo, in.Principal, in.Budget, a)
		if err != nil {
			return err
		}
		return web.JSON(w, 200, st)
	})
	repoRoute("GET "+base+"/evidence", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		ev, rec, err := svc.Evidence(r.Context(), repo)
		if err != nil {
			return err
		}
		return web.JSON(w, 200, map[string]any{"evidence": ev, "recommendation": rec})
	})
	repoRoute("POST "+base+"/evidence/apply", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		ev, rec, applied, err := svc.ApplyRecommendation(r.Context(), repo, a)
		if err != nil {
			return err
		}
		repo, _ = s.DB.RepoByID(r.Context(), repo.ID)
		return web.JSON(w, 200, map[string]any{"evidence": ev, "recommendation": rec, "applied": applied, "autonomy_level": repo.AutonomyLevel})
	})
	repoRoute("GET "+base+"/signals", func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		list, err := svc.Signals(r.Context(), repo, limit)
		if err != nil {
			return err
		}
		return web.JSON(w, 200, list)
	})
	flag := func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
		n, _ := strconv.Atoi(r.PathValue("n"))
		ch, err := s.DB.Change(r.Context(), repo, n)
		if err != nil {
			return err
		}
		var in struct {
			Reason string `json:"reason"`
		}
		if err := decode(r, &in, true); err != nil {
			return err
		}
		f, rec, applied, err := svc.FlagChange(r.Context(), repo, ch, in.Reason, a)
		if err != nil {
			return err
		}
		repo, _ = s.DB.RepoByID(r.Context(), repo.ID)
		return web.JSON(w, http.StatusCreated, map[string]any{"flag": f, "recommendation": rec, "applied": applied, "autonomy_level": repo.AutonomyLevel})
	}
	repoRoute("POST "+base+"/changes/{n}/flag", flag)
	repoRoute("POST /api/v0/repos/{org}/{name}/changes/{n}/flag", flag)
}

// decode reads a strict JSON body; optional allows an empty body.
func decode(r *http.Request, v any, optional bool) error {
	err := web.Decode(r, v)
	if err == nil {
		return nil
	}
	if optional && errors.Is(err, io.EOF) {
		return nil
	}
	var se *json.SyntaxError
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &se) {
		return fmt.Errorf("%w: invalid JSON body: %v", ErrInvalid, err)
	}
	return fmt.Errorf("%w: %v", ErrInvalid, err)
}

func writeErr(w http.ResponseWriter, err error) {
	var be *BudgetError
	switch {
	case errors.Is(err, ledger.ErrNotFound):
		web.JSONError(w, http.StatusNotFound, err)
	case errors.Is(err, ErrInvalid):
		web.JSONError(w, http.StatusBadRequest, err)
	case errors.Is(err, ErrConflict), errors.Is(err, ErrPaused), errors.Is(err, agent.ErrRefused), errors.As(err, &be):
		web.JSONError(w, http.StatusConflict, err)
	default:
		web.JSONError(w, http.StatusInternalServerError, err)
	}
}
