// SPDX-License-Identifier: AGPL-3.0-only

package environments

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/web"
)

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Routes mounts the slice's REST API under /api/v0/repos/{org}/{name}/....
func Routes(mux *http.ServeMux, s *web.Server, svc *delivery.Service) {
	h := handlers{s: s, svc: svc}
	api := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, repo ledger.Repo, actor ledger.Actor) error) {
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
	api("GET /api/v0/repos/{org}/{name}/environments", h.list)
	api("POST /api/v0/repos/{org}/{name}/environments", h.create)
	api("GET /api/v0/repos/{org}/{name}/environments/{env}", h.get)
	api("PUT /api/v0/repos/{org}/{name}/environments/{env}", h.update)
	api("DELETE /api/v0/repos/{org}/{name}/environments/{env}", h.delete)
	api("POST /api/v0/repos/{org}/{name}/environments/{env}/deployments", h.deploy)
	api("GET /api/v0/repos/{org}/{name}/environments/{env}/deployments", h.envDeployments)
	api("POST /api/v0/repos/{org}/{name}/environments/{env}/rollback", h.rollback)
	api("POST /api/v0/repos/{org}/{name}/environments/{env}/stop", h.stop)
	api("GET /api/v0/repos/{org}/{name}/deployments/{id}", h.deployment)
	api("POST /api/v0/repos/{org}/{name}/deployments/{id}/approve", h.approve)
	api("POST /api/v0/repos/{org}/{name}/deployments/{id}/reject", h.reject)
	api("POST /api/v0/repos/{org}/{name}/deployments/{id}/extend", h.extend)
	api("GET /api/v0/repos/{org}/{name}/dora", h.dora)
}

type handlers struct {
	s   *web.Server
	svc *delivery.Service
}

// writeErr maps the slice's error types to status codes. A refused deployment
// (403) and a conflict (409) carry the recorded decision when there is one.
func writeErr(w http.ResponseWriter, err error) {
	var refused *delivery.RefusedError
	var conflict *delivery.ConflictError
	var trans *delivery.TransitionError
	switch {
	case errors.Is(err, ledger.ErrNotFound):
		web.JSONError(w, http.StatusNotFound, err)
	case errors.Is(err, delivery.ErrInvalid):
		web.JSONError(w, http.StatusBadRequest, err)
	case errors.Is(err, delivery.ErrForbidden):
		web.JSONError(w, http.StatusForbidden, err)
	case errors.As(err, &refused):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "deployment_id": refused.DeploymentID, "status": delivery.StateRefused, "policy_decision": refused.Decision})
	case errors.As(err, &conflict), errors.As(err, &trans):
		web.JSONError(w, http.StatusConflict, err)
	default:
		web.JSONError(w, http.StatusInternalServerError, err)
	}
}

type envInput struct {
	Name   string          `json:"name"`
	Kind   string          `json:"kind"`
	Target json.RawMessage `json:"target"`
	Policy json.RawMessage `json:"policy"`
}

func strict(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (h handlers) list(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
	envs, err := h.svc.Environments(r.Context(), repo)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, envs)
}

func (h handlers) create(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
	var in envInput
	if err := web.Decode(r, &in); err != nil {
		return fmt.Errorf("%w: %v", delivery.ErrInvalid, err)
	}
	if !nameRE.MatchString(in.Name) {
		return fmt.Errorf("%w: name is required ([a-z0-9._-], 64 max)", delivery.ErrInvalid)
	}
	if in.Kind == "" {
		in.Kind = delivery.KindForName(in.Name)
	}
	if !delivery.ValidKind(in.Kind) {
		return fmt.Errorf("%w: kind must be production|staging|preview|ephemeral", delivery.ErrInvalid)
	}
	env := delivery.Environment{Name: in.Name, Kind: in.Kind, Target: delivery.Target{Kind: delivery.TargetLocalDocker}, Policy: delivery.DefaultPolicy(in.Kind)}
	if err := strict(in.Target, &env.Target); err != nil {
		return fmt.Errorf("%w: target: %v", delivery.ErrInvalid, err)
	}
	if err := strict(in.Policy, &env.Policy); err != nil {
		return fmt.Errorf("%w: policy: %v", delivery.ErrInvalid, err)
	}
	if err := h.svc.CreateEnvironment(r.Context(), repo, &env, a); err != nil {
		return err
	}
	return web.JSON(w, http.StatusCreated, env)
}

func (h handlers) get(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
	env, err := h.svc.Environment(r.Context(), repo, r.PathValue("env"))
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, env)
}

func (h handlers) update(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
	env, err := h.svc.Environment(r.Context(), repo, r.PathValue("env"))
	if err != nil {
		return err
	}
	var in envInput
	if err := web.Decode(r, &in); err != nil {
		return fmt.Errorf("%w: %v", delivery.ErrInvalid, err)
	}
	if in.Name != "" && in.Name != env.Name {
		return fmt.Errorf("%w: environments cannot be renamed", delivery.ErrInvalid)
	}
	if in.Kind != "" {
		env.Kind = in.Kind
	}
	if err := strict(in.Target, &env.Target); err != nil {
		return fmt.Errorf("%w: target: %v", delivery.ErrInvalid, err)
	}
	if err := strict(in.Policy, &env.Policy); err != nil {
		return fmt.Errorf("%w: policy: %v", delivery.ErrInvalid, err)
	}
	if err := h.svc.UpdateEnvironment(r.Context(), repo, &env, a); err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, env)
}

func (h handlers) delete(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
	env, err := h.svc.Environment(r.Context(), repo, r.PathValue("env"))
	if err != nil {
		return err
	}
	if err := h.svc.DeleteEnvironment(r.Context(), repo, env, a); err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": env.Name})
}

// resolveRef turns a ref, branch name or commit into (full ref, commit).
func (h handlers) resolveRef(r *http.Request, repo ledger.Repo, ref string) (string, string, error) {
	rp := filepath.Join(h.s.ReposDir, repo.Org, repo.Name+".git")
	if ref == "" {
		ref = "refs/heads/" + repo.DefaultBranch
	}
	if len(ref) == 40 && strings.Trim(ref, "0123456789abcdef") == "" {
		oid, err := gitx.RevParse(r.Context(), rp, ref)
		if err != nil {
			return "", "", fmt.Errorf("%w: %v", delivery.ErrInvalid, err)
		}
		return h.svc.RefFor(r.Context(), repo, oid), oid, nil
	}
	full := ref
	if !strings.HasPrefix(full, "refs/") {
		full = "refs/heads/" + ref
	}
	oid, err := gitx.RevParse(r.Context(), rp, full)
	if err != nil {
		if oid2, err2 := gitx.RevParse(r.Context(), rp, ref); err2 == nil {
			return ref, oid2, nil
		}
		return "", "", fmt.Errorf("%w: %v", delivery.ErrInvalid, err)
	}
	return full, oid, nil
}

func (h handlers) deploy(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
	env, err := h.svc.Environment(r.Context(), repo, r.PathValue("env"))
	if err != nil {
		return err
	}
	var in struct {
		Ref string `json:"ref"`
	}
	if r.ContentLength != 0 {
		if err := web.Decode(r, &in); err != nil {
			return fmt.Errorf("%w: %v", delivery.ErrInvalid, err)
		}
	}
	ref, oid, err := h.resolveRef(r, repo, in.Ref)
	if err != nil {
		return err
	}
	dep, err := h.svc.Request(r.Context(), repo, env, delivery.DeployRequest{Ref: ref, CommitOID: oid}, a)
	return h.respondDeployment(w, r, dep, err)
}

// respondDeployment answers a request that produced a deployment: 202 while
// awaiting approval, 201 once it ran (status succeeded or failed in the body),
// 403/409 when refused or conflicting (recorded on the ledger).
func (h handlers) respondDeployment(w http.ResponseWriter, r *http.Request, dep ledger.Deployment, err error) error {
	var failed *delivery.FailedError
	if err != nil && !errors.As(err, &failed) {
		return err
	}
	view, verr := h.svc.View(r.Context(), dep.ID)
	if verr != nil {
		return verr
	}
	code := http.StatusCreated
	if dep.Status == delivery.StateAwaitingApproval {
		code = http.StatusAccepted
	}
	return web.JSON(w, code, view)
}

func (h handlers) envDeployments(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
	all, err := h.s.DB.Deployments(r.Context(), repo, 200)
	if err != nil {
		return err
	}
	out := []ledger.Deployment{}
	for _, d := range all {
		if d.Environment == r.PathValue("env") {
			out = append(out, d)
		}
	}
	return web.JSON(w, http.StatusOK, out)
}

func (h handlers) rollback(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
	env, err := h.svc.Environment(r.Context(), repo, r.PathValue("env"))
	if err != nil {
		return err
	}
	dep, err := h.svc.Rollback(r.Context(), repo, env, a)
	return h.respondDeployment(w, r, dep, err)
}

func (h handlers) stop(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
	env, err := h.svc.Environment(r.Context(), repo, r.PathValue("env"))
	if err != nil {
		return err
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength != 0 {
		_ = web.Decode(r, &in)
	}
	if in.Reason == "" {
		in.Reason = "manual"
	}
	if err := h.svc.Stop(r.Context(), repo, env, a, in.Reason); err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]string{"status": "stopped", "environment": env.Name})
}

func (h handlers) deployment(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
	view, err := h.svc.View(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	if view.RepoID != repo.ID {
		return ledger.ErrNotFound
	}
	return web.JSON(w, http.StatusOK, view)
}

type verdictInput struct {
	Comment string `json:"comment"`
}

func (h handlers) approve(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
	var in verdictInput
	if r.ContentLength != 0 {
		_ = web.Decode(r, &in)
	}
	dep, err := h.svc.Approve(r.Context(), repo, r.PathValue("id"), a, in.Comment)
	var failed *delivery.FailedError
	if err != nil && !errors.As(err, &failed) {
		return err
	}
	view, verr := h.svc.View(r.Context(), dep.ID)
	if verr != nil {
		return verr
	}
	return web.JSON(w, http.StatusOK, view)
}

func (h handlers) reject(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
	var in verdictInput
	if r.ContentLength != 0 {
		_ = web.Decode(r, &in)
	}
	dep, err := h.svc.Reject(r.Context(), repo, r.PathValue("id"), a, in.Comment)
	if err != nil {
		return err
	}
	view, verr := h.svc.View(r.Context(), dep.ID)
	if verr != nil {
		return verr
	}
	return web.JSON(w, http.StatusOK, view)
}

func (h handlers) extend(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
	var in struct {
		Seconds int `json:"seconds"`
		Hours   int `json:"hours"`
	}
	if r.ContentLength != 0 {
		if err := web.Decode(r, &in); err != nil {
			return fmt.Errorf("%w: %v", delivery.ErrInvalid, err)
		}
	}
	d := time.Duration(in.Seconds)*time.Second + time.Duration(in.Hours)*time.Hour
	if d <= 0 {
		d = delivery.DefaultPreviewTTL
	}
	ttl, clamped, err := h.svc.Extend(r.Context(), repo, r.PathValue("id"), d, a)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"deployment_id": ttl.DeploymentID, "expires_at": ttl.ExpiresAt, "extensions": ttl.Extensions, "clamped": clamped, "max_seconds": int(delivery.MaxPreviewTTL / time.Second)})
}

func (h handlers) dora(w http.ResponseWriter, r *http.Request, repo ledger.Repo, a ledger.Actor) error {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 30
	}
	out, err := h.svc.ComputeDORA(r.Context(), repo, days)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, out)
}
