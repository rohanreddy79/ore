// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/web"
)

// ImportRequest is the body of import/plan, import/run and import/verify.
type ImportRequest struct {
	Source  string        `json:"source"`
	Options ImportOptions `json:"options"`
	Job     string        `json:"job,omitempty"`
}

// importRoutes mounts /api/v0/repos/{org}/{name}/import/.
func (svc *Service) importRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v0/repos/{org}/{name}/import/plan", svc.api(svc.handlePlan))
	mux.HandleFunc("POST /api/v0/repos/{org}/{name}/import/run", svc.api(svc.handleRun))
	mux.HandleFunc("GET /api/v0/repos/{org}/{name}/import/jobs", svc.api(svc.handleJobs))
	mux.HandleFunc("GET /api/v0/repos/{org}/{name}/import/jobs/{id}", svc.api(svc.handleJob))
	mux.HandleFunc("POST /api/v0/repos/{org}/{name}/import/jobs/{id}/resume", svc.api(svc.handleResume))
	mux.HandleFunc("POST /api/v0/repos/{org}/{name}/import/verify", svc.api(svc.handleVerify))
	mux.HandleFunc("GET /api/v0/repos/{org}/{name}/import/identities", svc.api(svc.handleIdentities))
	mux.HandleFunc("GET /api/v0/repos/{org}/{name}/import/webhooks", svc.api(svc.handleWebhooks))
	mux.HandleFunc("GET /api/v0/repos/{org}/{name}/import/releases", svc.api(svc.handleReleases))
}

func (svc *Service) handlePlan(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error {
	repo, err := svc.repo(r)
	if err != nil {
		return err
	}
	var req ImportRequest
	if err := web.Decode(r, &req); err != nil || req.Source == "" {
		return fmt.Errorf("%w: source is required (github.com/owner/name)", errBadRequest)
	}
	job, err := svc.PlanJob(r.Context(), repo, req.Source, actor)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"job_id": job.ID, "report": job.Report})
}

func (svc *Service) handleRun(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error {
	repo, err := svc.repo(r)
	if err != nil {
		return err
	}
	var req ImportRequest
	if err := web.Decode(r, &req); err != nil || req.Source == "" {
		return fmt.Errorf("%w: source is required (github.com/owner/name)", errBadRequest)
	}
	job, err := svc.StartImport(r.Context(), repo, req.Source, req.Options, actor)
	if err != nil && job.ID == "" {
		return err
	}
	code := http.StatusAccepted
	if req.Options.Wait {
		code = http.StatusOK
	}
	return web.JSON(w, code, job)
}

func (svc *Service) jobFor(r *http.Request, repo ledger.Repo, id string) (ImportJob, error) {
	job, err := svc.store.job(r.Context(), id)
	if err != nil {
		return job, err
	}
	if job.RepoID != repo.ID {
		return job, ledger.ErrNotFound
	}
	return job, nil
}

func (svc *Service) handleJobs(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error {
	repo, err := svc.repo(r)
	if err != nil {
		return err
	}
	jobs, err := svc.store.jobs(r.Context(), `WHERE repo_id = ? ORDER BY created_at DESC`, repo.ID)
	if err != nil {
		return err
	}
	if jobs == nil {
		jobs = []ImportJob{}
	}
	return web.JSON(w, http.StatusOK, jobs)
}

func (svc *Service) handleJob(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error {
	repo, err := svc.repo(r)
	if err != nil {
		return err
	}
	job, err := svc.jobFor(r, repo, r.PathValue("id"))
	if err != nil {
		return err
	}
	job.Webhooks, _ = svc.store.webhooks(r.Context(), repo.ID)
	return web.JSON(w, http.StatusOK, job)
}

func (svc *Service) handleResume(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error {
	repo, err := svc.repo(r)
	if err != nil {
		return err
	}
	job, err := svc.jobFor(r, repo, r.PathValue("id"))
	if err != nil {
		return err
	}
	var req ImportRequest
	_ = web.Decode(r, &req)
	if req.Options.Wait {
		// The job's status and error carry the outcome, like import/run.
		job, err = svc.RunImport(r.Context(), job.ID)
		if errors.Is(err, errJobBusy) {
			return fmt.Errorf("%w: %v", errBadRequest, err)
		}
		return web.JSON(w, http.StatusOK, job)
	}
	go func() {
		if _, err := svc.RunImport(r.Context(), job.ID); err != nil {
			svc.log().Warn("github import: resume", "job", job.ID, "err", err)
		}
	}()
	return web.JSON(w, http.StatusAccepted, job)
}

func (svc *Service) handleVerify(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error {
	repo, err := svc.repo(r)
	if err != nil {
		return err
	}
	var req ImportRequest
	_ = web.Decode(r, &req)
	var job ImportJob
	if req.Job != "" {
		job, err = svc.jobFor(r, repo, req.Job)
	} else {
		jobs, jerr := svc.store.jobs(r.Context(), `WHERE repo_id = ? AND status = ? ORDER BY created_at DESC LIMIT 1`, repo.ID, JobCompleted)
		if jerr != nil {
			return jerr
		}
		if len(jobs) == 0 {
			return fmt.Errorf("no completed import to verify: %w", ledger.ErrNotFound)
		}
		job = jobs[0]
	}
	if err != nil {
		return err
	}
	rep, err := svc.Verify(r.Context(), repo, job)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, rep)
}

func (svc *Service) handleIdentities(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error {
	repo, err := svc.repo(r)
	if err != nil {
		return err
	}
	ids, err := svc.store.identities(r.Context(), repo.OrgID)
	if err != nil {
		return err
	}
	if ids == nil {
		ids = []ImportedIdentity{}
	}
	return web.JSON(w, http.StatusOK, ids)
}

func (svc *Service) handleWebhooks(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error {
	repo, err := svc.repo(r)
	if err != nil {
		return err
	}
	hooks, err := svc.store.webhooks(r.Context(), repo.ID)
	if err != nil {
		return err
	}
	if hooks == nil {
		hooks = []ImportedWebhook{}
	}
	return web.JSON(w, http.StatusOK, hooks)
}

func (svc *Service) handleReleases(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error {
	repo, err := svc.repo(r)
	if err != nil {
		return err
	}
	rels, err := svc.store.releases(r.Context(), repo.ID)
	if err != nil {
		return err
	}
	if rels == nil {
		rels = []ImportedRelease{}
	}
	return web.JSON(w, http.StatusOK, rels)
}
