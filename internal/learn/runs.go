// SPDX-License-Identifier: AGPL-3.0-only

package learn

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/review"
)

// Identity is the part of the identity service the engine needs: whether a
// learner may read a repository (in_place runs) or write to an org (scratch
// repositories). Nil means the single-token deployment where every caller
// may do both.
type Identity interface {
	Authorize(ctx context.Context, actor ledger.Actor, repo ledger.Repo, write bool) error
}

// Service is the lesson engine.
type Service struct {
	DB       *ledger.DB
	ReposDir string
	Catalog  *Catalog
	// Review, when set, lets scratch repositories carry a review policy.
	Review *review.Service
	// Identity, when set, enforces read access for in_place runs and write
	// access for scratch repositories.
	Identity Identity
	// OrgsFor, when set, lists the orgs a learner belongs to (for the
	// scratch repository's org); nil falls back to every org.
	OrgsFor func(ctx context.Context, learner ledger.Actor) ([]string, error)
	Log     *slog.Logger

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// Run statuses.
const (
	StatusActive    = "active"
	StatusCompleted = "completed"
	StatusAbandoned = "abandoned"
)

// Run is a lesson run: one learner working through one lesson on one repository.
type Run struct {
	ID            string         `json:"id"`
	OrgID         string         `json:"org_id"`
	RepoID        string         `json:"repo_id"`
	Repo          string         `json:"repo"`
	Learner       ledger.Actor   `json:"learner"`
	LessonID      string         `json:"lesson_id"`
	LessonVersion string         `json:"lesson_version"`
	Mode          string         `json:"mode"`
	Language      string         `json:"language"`
	Step          int            `json:"step"` // 1-based current step; len(steps)+1 when completed
	StepID        string         `json:"step_id,omitempty"`
	Status        string         `json:"status"`
	Attempts      int            `json:"attempts"`
	StepHints     int            `json:"step_hints"`
	HintsUsed     int            `json:"hints_used"`
	Evidence      map[string]any `json:"evidence"`
	LastEventSeq  int64          `json:"last_event_seq"`
	StartedAt     string         `json:"started_at"`
	UpdatedAt     string         `json:"updated_at"`
	CompletedAt   *string        `json:"completed_at,omitempty"`
}

// Progress is a learner's state on one lesson.
type Progress struct {
	OrgID         string       `json:"org_id"`
	Learner       ledger.Actor `json:"learner"`
	LessonID      string       `json:"lesson_id"`
	Status        string       `json:"status"` // in_progress | completed
	RunID         string       `json:"run_id"`
	LessonVersion string       `json:"lesson_version"`
	CompletedAt   *string      `json:"completed_at,omitempty"`
	UpdatedAt     string       `json:"updated_at"`
}

// Errors the routes map to status codes.
var (
	ErrNotFound       = ledger.ErrNotFound
	ErrBadRequest     = errors.New("bad request")
	ErrHintsExhausted = errors.New("hints exhausted")
)

// RefusedError is a 409: the run cannot start or continue as asked.
type RefusedError struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	Prerequisite string `json:"prerequisite,omitempty"`
}

func (e *RefusedError) Error() string { return e.Message }

// ForbiddenError is a 403.
type ForbiddenError struct{ Reason string }

func (e *ForbiddenError) Error() string { return e.Reason }

func (s *Service) lock(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks == nil {
		s.locks = map[string]*sync.Mutex{}
	}
	m, ok := s.locks[id]
	if !ok {
		m = &sync.Mutex{}
		s.locks[id] = m
	}
	return m
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func runSubject(repoOrg, repoName, id string) string {
	return repoOrg + "/" + repoName + "/learn/runs/" + id
}

// ---- storage ----

const runCols = `id, org_id, repo_id, learner_kind, learner_id, lesson_id, lesson_version, mode, language, step, status, attempts, step_hints, hints_used, evidence, last_event_seq, started_at, updated_at, completed_at`

func scanRun(sc interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var evidence string
	err := sc.Scan(&r.ID, &r.OrgID, &r.RepoID, &r.Learner.Kind, &r.Learner.ID, &r.LessonID, &r.LessonVersion, &r.Mode, &r.Language, &r.Step, &r.Status, &r.Attempts, &r.StepHints, &r.HintsUsed, &evidence, &r.LastEventSeq, &r.StartedAt, &r.UpdatedAt, &r.CompletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.Evidence = map[string]any{}
	_ = json.Unmarshal([]byte(evidence), &r.Evidence)
	return r, nil
}

// Run loads a run by id.
func (s *Service) Run(ctx context.Context, id string) (Run, error) {
	r, err := scanRun(s.DB.QueryRow(ctx, `SELECT `+runCols+` FROM lesson_runs WHERE id = ?`, id))
	if err != nil {
		return r, err
	}
	s.decorate(ctx, &r)
	return r, nil
}

func (s *Service) decorate(ctx context.Context, r *Run) {
	if repo, err := s.DB.RepoByID(ctx, r.RepoID); err == nil {
		r.Repo = repo.Org + "/" + repo.Name
	}
	if l, ok := s.Catalog.Lesson(r.LessonID); ok && r.Step >= 1 && r.Step <= len(l.Manifest.Steps) {
		r.StepID = l.Manifest.Steps[r.Step-1].ID
	}
}

// Runs lists a learner's runs (all repositories), newest first.
func (s *Service) Runs(ctx context.Context, learner ledger.Actor, status string) ([]Run, error) {
	q := `SELECT ` + runCols + ` FROM lesson_runs WHERE learner_kind = ? AND learner_id = ?`
	args := []any{learner.Kind, learner.ID}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	return s.queryRuns(ctx, q+` ORDER BY started_at DESC`, args...)
}

// ActiveRunsForRepo lists the active runs on a repository.
func (s *Service) ActiveRunsForRepo(ctx context.Context, repoID string) ([]Run, error) {
	return s.queryRuns(ctx, `SELECT `+runCols+` FROM lesson_runs WHERE repo_id = ? AND status = ? ORDER BY started_at`, repoID, StatusActive)
}

func (s *Service) queryRuns(ctx context.Context, q string, args ...any) ([]Run, error) {
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		s.decorate(ctx, &out[i])
	}
	return out, nil
}

func (s *Service) activeRun(ctx context.Context, learner ledger.Actor, lessonID string) (Run, bool) {
	r, err := scanRun(s.DB.QueryRow(ctx, `SELECT `+runCols+` FROM lesson_runs WHERE learner_kind = ? AND learner_id = ? AND lesson_id = ? AND status = ? ORDER BY started_at DESC LIMIT 1`, learner.Kind, learner.ID, lessonID, StatusActive))
	if err != nil {
		return Run{}, false
	}
	s.decorate(ctx, &r)
	return r, true
}

// ProgressFor lists a learner's progress across lessons.
func (s *Service) ProgressFor(ctx context.Context, learner ledger.Actor) ([]Progress, error) {
	rows, err := s.DB.Query(ctx, `SELECT org_id, learner_kind, learner_id, lesson_id, status, run_id, lesson_version, completed_at, updated_at FROM lesson_progress WHERE learner_kind = ? AND learner_id = ? ORDER BY updated_at DESC`, learner.Kind, learner.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Progress{}
	for rows.Next() {
		var p Progress
		if err := rows.Scan(&p.OrgID, &p.Learner.Kind, &p.Learner.ID, &p.LessonID, &p.Status, &p.RunID, &p.LessonVersion, &p.CompletedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Completed reports whether the learner completed the lesson in any org.
func (s *Service) Completed(ctx context.Context, learner ledger.Actor, lessonID string) (bool, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT COUNT(1) FROM lesson_progress WHERE learner_kind = ? AND learner_id = ? AND lesson_id = ? AND status = 'completed'`, learner.Kind, learner.ID, lessonID).Scan(&n)
	return n > 0, err
}

func upsertProgress(ctx context.Context, tx *ledger.Tx, r Run, status string, completedAt *string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO lesson_progress(org_id, learner_kind, learner_id, lesson_id, status, run_id, lesson_version, completed_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(org_id, learner_kind, learner_id, lesson_id) DO UPDATE SET status = excluded.status, run_id = excluded.run_id, lesson_version = excluded.lesson_version, completed_at = COALESCE(excluded.completed_at, lesson_progress.completed_at), updated_at = excluded.updated_at`,
		r.OrgID, r.Learner.Kind, r.Learner.ID, r.LessonID, status, r.ID, r.LessonVersion, completedAt, ledger.Now())
	return err
}

func (s *Service) saveRun(ctx context.Context, tx *ledger.Tx, r *Run) error {
	r.UpdatedAt = ledger.Now()
	ev, _ := json.Marshal(r.Evidence)
	_, err := tx.ExecContext(ctx, `UPDATE lesson_runs SET step = ?, status = ?, attempts = ?, step_hints = ?, hints_used = ?, evidence = ?, last_event_seq = ?, updated_at = ?, completed_at = ? WHERE id = ?`,
		r.Step, r.Status, r.Attempts, r.StepHints, r.HintsUsed, string(ev), r.LastEventSeq, r.UpdatedAt, r.CompletedAt, r.ID)
	return err
}

// ---- starting ----

// StartRequest is POST /api/v0/learn/runs.
type StartRequest struct {
	Lesson   string `json:"lesson"`
	Repo     string `json:"repo,omitempty"` // org/name for in_place runs
	Org      string `json:"org,omitempty"`  // for scratch repositories (default: the learner's first org)
	Language string `json:"lang,omitempty"`
}

// StartResult is the response.
type StartResult struct {
	Run               Run    `json:"run"`
	Resumed           bool   `json:"resumed"`
	LanguageFallback  bool   `json:"language_fallback,omitempty"`
	RequestedLanguage string `json:"requested_language,omitempty"`
	Note              string `json:"note,omitempty"`
}

var learnerSafeRe = regexp.MustCompile(`[^a-z0-9-]+`)

// Start begins (or resumes) a lesson for the learner.
func (s *Service) Start(ctx context.Context, req StartRequest, learner ledger.Actor) (StartResult, error) {
	lesson, ok := s.Catalog.Lesson(req.Lesson)
	if !ok {
		return StartResult{}, fmt.Errorf("%w: lesson %q", ErrNotFound, req.Lesson)
	}
	for _, p := range lesson.Manifest.Prerequisites {
		done, err := s.Completed(ctx, learner, p)
		if err != nil {
			return StartResult{}, err
		}
		if !done {
			title := p
			if pl, ok := s.Catalog.Lesson(p); ok {
				title = pl.Title(req.Language)
			}
			return StartResult{}, &RefusedError{Code: "prerequisite", Message: fmt.Sprintf("complete %q (%s) first", title, p), Prerequisite: p}
		}
	}
	res := StartResult{}
	if existing, ok := s.activeRun(ctx, learner, lesson.Manifest.ID); ok {
		res.Run, res.Resumed = existing, true
		res.Note = "you already have an active run of this lesson; resuming it at step " + fmt.Sprint(existing.Step)
		return res, nil
	}
	lang := req.Language
	if lang == "" {
		lang = "en"
	}
	if _, fallback := lesson.Text(lang); fallback {
		res.LanguageFallback, res.RequestedLanguage = true, lang
		res.Note = fmt.Sprintf("this lesson is not available in %s yet; showing English (available: %s)", lang, strings.Join(lesson.Languages(), ", "))
		lang = "en"
	}
	var repo ledger.Repo
	var err error
	mode := lesson.Manifest.Mode
	switch {
	case req.Repo != "":
		org, name, ok := strings.Cut(req.Repo, "/")
		if !ok {
			return StartResult{}, fmt.Errorf("%w: repo must be org/name", ErrBadRequest)
		}
		repo, err = s.DB.RepoByName(ctx, org, name)
		if err != nil {
			return StartResult{}, fmt.Errorf("%w: repository %s", ErrNotFound, req.Repo)
		}
		if s.Identity != nil {
			if err := s.Identity.Authorize(ctx, learner, repo, false); err != nil {
				return StartResult{}, &ForbiddenError{Reason: fmt.Sprintf("%s cannot read %s: %v", learner.ID, req.Repo, err)}
			}
		}
		mode = ModeInPlace
	case mode == ModeScratch:
		repo, err = s.createScratchRepo(ctx, lesson, req.Org, learner)
		if err != nil {
			return StartResult{}, err
		}
	default:
		return StartResult{}, fmt.Errorf("%w: lesson %s runs in place; name the repository to run it on (repo: org/name)", ErrBadRequest, lesson.Manifest.ID)
	}
	now := ledger.Now()
	// events before the run started (the template's own commits, earlier
	// work) are not attempts: the worker only reacts to later ones
	var seq int64
	_ = s.DB.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&seq)
	run := Run{ID: ledger.NewID("lrun"), OrgID: repo.OrgID, RepoID: repo.ID, Repo: repo.Org + "/" + repo.Name, Learner: learner, LessonID: lesson.Manifest.ID, LessonVersion: lesson.Manifest.Version,
		Mode: mode, Language: lang, Step: 1, StepID: lesson.Manifest.Steps[0].ID, Status: StatusActive, Evidence: map[string]any{}, LastEventSeq: seq, StartedAt: now, UpdatedAt: now}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return StartResult{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO lesson_runs(`+runCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.ID, run.OrgID, run.RepoID, learner.Kind, learner.ID, run.LessonID, run.LessonVersion, run.Mode, run.Language, run.Step, run.Status, 0, 0, 0, "{}", seq, now, now, nil); err != nil {
		return StartResult{}, err
	}
	if err := upsertProgress(ctx, tx, run, "in_progress", nil); err != nil {
		return StartResult{}, err
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.lesson.run.started", runSubject(repo.Org, repo.Name, run.ID), learner, run); err != nil {
		return StartResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return StartResult{}, err
	}
	// pick up where the repository already is
	if out, err := s.Check(ctx, run.ID, "start"); err == nil {
		run = out.Run
	}
	res.Run = run
	return res, nil
}

func (s *Service) createScratchRepo(ctx context.Context, lesson *Lesson, orgName string, learner ledger.Actor) (ledger.Repo, error) {
	var org ledger.Org
	if orgName == "" {
		var names []string
		var err error
		if s.OrgsFor != nil {
			names, err = s.OrgsFor(ctx, learner)
			if err != nil {
				return ledger.Repo{}, err
			}
		}
		if len(names) == 0 {
			// no membership known (single-token mode, or a site owner who is
			// not an org member): every org is a candidate and the write
			// check below decides
			orgs, err := s.DB.Orgs(ctx)
			if err != nil {
				return ledger.Repo{}, err
			}
			for _, o := range orgs {
				names = append(names, o.Name)
			}
		}
		if len(names) == 0 {
			return ledger.Repo{}, fmt.Errorf("%w: no organization to create the scratch repository in; create one or name it (org)", ErrBadRequest)
		}
		orgName = names[0]
	}
	org, err := s.DB.OrgByName(ctx, orgName)
	if err != nil {
		return ledger.Repo{}, fmt.Errorf("%w: organization %s", ErrNotFound, orgName)
	}
	if s.Identity != nil {
		if err := s.Identity.Authorize(ctx, learner, ledger.Repo{OrgID: org.ID, Org: org.Name}, true); err != nil {
			return ledger.Repo{}, &ForbiddenError{Reason: fmt.Sprintf("%s cannot create repositories in %s: %v", learner.ID, orgName, err)}
		}
	}
	base := "learn-" + lesson.Manifest.Slug() + "-" + strings.Trim(learnerSafeRe.ReplaceAllString(strings.ToLower(learner.ID), "-"), "-")
	if len(base) > 60 {
		base = base[:60]
	}
	name := base
	for n := 2; ; n++ {
		if _, err := s.DB.RepoByName(ctx, org.Name, name); errors.Is(err, ledger.ErrNotFound) {
			break
		}
		name = fmt.Sprintf("%s-%d", base, n)
	}
	repo, err := s.DB.CreateRepo(ctx, org, name, "main", 2, 8080, learner)
	if err != nil {
		return ledger.Repo{}, err
	}
	rp := filepath.Join(s.ReposDir, org.Name, name+".git")
	if err := gitx.InitBare(ctx, rp, "main"); err != nil {
		return ledger.Repo{}, err
	}
	author := gitx.Author{Name: "Ore Learn", Email: "learn@ore.invalid"}
	t := lesson.Manifest.Template
	var edits []gitx.FileEdit
	for _, f := range t.Files {
		edits = append(edits, gitx.FileEdit{Path: f.Path, Content: []byte(f.Content)})
	}
	oid, err := gitx.CommitFiles(ctx, rp, "", edits, "learn: "+lesson.Title("en")+" (lesson template)", author)
	if err != nil {
		return ledger.Repo{}, err
	}
	if err := gitx.UpdateRef(ctx, rp, "refs/heads/main", oid, gitx.ZeroOID); err != nil {
		return ledger.Repo{}, err
	}
	_ = s.DB.RecordRefUpdate(ctx, repo, "refs/heads/main", gitx.ZeroOID, oid, "learn", ledger.System)
	for _, c := range t.Commits {
		ref := "refs/heads/main"
		if c.Branch != "" {
			ref = c.Branch
			if !strings.HasPrefix(ref, "refs/") {
				ref = "refs/heads/" + ref
			}
		}
		parent, _ := gitx.RevParse(ctx, rp, ref)
		old := parent
		if parent == "" {
			parent, _ = gitx.RevParse(ctx, rp, "refs/heads/main")
			old = gitx.ZeroOID
		}
		var edits []gitx.FileEdit
		for _, f := range c.Files {
			edits = append(edits, gitx.FileEdit{Path: f.Path, Content: []byte(f.Content)})
		}
		oid, err := gitx.CommitFiles(ctx, rp, parent, edits, c.Message, author)
		if err != nil {
			return ledger.Repo{}, err
		}
		if err := gitx.UpdateRef(ctx, rp, ref, oid, old); err != nil {
			return ledger.Repo{}, err
		}
		_ = s.DB.RecordRefUpdate(ctx, repo, ref, old, oid, "learn", ledger.System)
	}
	if len(t.ReviewPolicy) > 0 && s.Review != nil {
		p, _ := s.Review.Policy(ctx, repo)
		if v, ok := t.ReviewPolicy["required_approvals"]; ok {
			switch n := v.(type) {
			case int:
				p.RequiredApprovals = n
			case float64:
				p.RequiredApprovals = int(n)
			}
		}
		if v, ok := t.ReviewPolicy["allow_agent_approvals"].(bool); ok {
			p.AllowAgentApprovals = v
		}
		if _, err := s.Review.SetPolicy(ctx, repo, p, ledger.System); err != nil {
			s.log().Warn("learn: review policy", "repo", repo.Org+"/"+repo.Name, "err", err)
		}
	}
	return repo, nil
}

// ---- checking and advancing ----

// CheckOutcome is the result of evaluating a run's current step.
type CheckOutcome struct {
	Run       Run          `json:"run"`
	Passed    bool         `json:"passed"`
	Advanced  []string     `json:"advanced"` // step ids completed by this check
	Completed bool         `json:"completed"`
	Result    AssertResult `json:"result"`
	HintReady bool         `json:"hint_ready"`
	Error     string       `json:"error,omitempty"`
}

// Check evaluates the current step (and the following ones while they
// pass), advancing the run and emitting io.ore.lesson.run.step.completed /
// io.ore.lesson.run.completed. cause is "check" (explicit), "event" (the
// worker) or "start".
func (s *Service) Check(ctx context.Context, runID, cause string) (CheckOutcome, error) {
	m := s.lock(runID)
	m.Lock()
	defer m.Unlock()
	run, err := s.Run(ctx, runID)
	if err != nil {
		return CheckOutcome{}, err
	}
	out := CheckOutcome{Run: run, Advanced: []string{}}
	if run.Status != StatusActive {
		out.Passed = run.Status == StatusCompleted
		out.Completed = run.Status == StatusCompleted
		return out, nil
	}
	lesson, ok := s.Catalog.Lesson(run.LessonID)
	if !ok {
		return out, fmt.Errorf("%w: lesson %s is no longer in the catalog", ErrNotFound, run.LessonID)
	}
	repo, err := s.DB.RepoByID(ctx, run.RepoID)
	if err != nil {
		return out, fmt.Errorf("the run's repository no longer exists (%s)", run.RepoID)
	}
	env := &AssertEnv{Ctx: ctx, DB: s.DB, ReposDir: s.ReposDir, Repo: repo, Learner: run.Learner}
	for run.Status == StatusActive {
		step := lesson.Manifest.Steps[run.Step-1]
		expr, err := ParseAssert(step.Assert.Query)
		if err != nil {
			return out, fmt.Errorf("lesson %s step %s: %w", run.LessonID, step.ID, err)
		}
		res, err := env.Evaluate(expr)
		if err != nil {
			out.Error = err.Error()
			return out, err
		}
		out.Result = res
		if !res.Passed {
			if cause != "start" {
				run.Attempts++
				tx, err := s.DB.Begin(ctx)
				if err != nil {
					return out, err
				}
				if err := s.saveRun(ctx, tx, &run); err != nil {
					tx.Rollback()
					return out, err
				}
				if err := tx.Commit(); err != nil {
					return out, err
				}
			}
			out.Run = run
			out.HintReady = run.Attempts >= step.HintAfterFailures
			return out, nil
		}
		// the step passed
		out.Advanced = append(out.Advanced, step.ID)
		run.Evidence[step.ID] = res.Checks
		run.Attempts, run.StepHints = 0, 0
		tx, err := s.DB.Begin(ctx)
		if err != nil {
			return out, err
		}
		data := map[string]any{"run_id": run.ID, "lesson_id": run.LessonID, "step": run.Step, "step_id": step.ID, "checks": res.Checks, "cause": cause}
		if _, err := tx.Emit(run.OrgID, "io.ore.lesson.run.step.completed", runSubject(repo.Org, repo.Name, run.ID), run.Learner, data); err != nil {
			tx.Rollback()
			return out, err
		}
		run.Step++
		if run.Step > len(lesson.Manifest.Steps) {
			now := ledger.Now()
			run.Status, run.CompletedAt = StatusCompleted, &now
			credential := ""
			if lesson.Manifest.OnComplete != nil {
				credential = lesson.Manifest.OnComplete.Credential
			}
			run.Evidence["credential"] = credential
			if err := upsertProgress(ctx, tx, run, "completed", &now); err != nil {
				tx.Rollback()
				return out, err
			}
			if _, err := tx.Emit(run.OrgID, "io.ore.lesson.run.completed", runSubject(repo.Org, repo.Name, run.ID), run.Learner, map[string]any{"run_id": run.ID, "lesson_id": run.LessonID, "lesson_version": run.LessonVersion, "credential": credential, "artifact": lesson.Manifest.Artifact, "evidence": run.Evidence, "hints_used": run.HintsUsed}); err != nil {
				tx.Rollback()
				return out, err
			}
			out.Completed = true
		}
		if err := s.saveRun(ctx, tx, &run); err != nil {
			tx.Rollback()
			return out, err
		}
		if err := tx.Commit(); err != nil {
			return out, err
		}
		s.decorate(ctx, &run)
		out.Run, out.Passed = run, true
	}
	return out, nil
}

// Hint is one hint for the current step.
type Hint struct {
	Step      int    `json:"step"`
	StepID    string `json:"step_id"`
	Index     int    `json:"index"` // 1-based
	Text      string `json:"text"`
	Remaining int    `json:"remaining"`
	Language  string `json:"language"`
}

// NextHint returns the next unused hint for the current step and records it.
func (s *Service) NextHint(ctx context.Context, runID string) (Hint, error) {
	m := s.lock(runID)
	m.Lock()
	defer m.Unlock()
	run, err := s.Run(ctx, runID)
	if err != nil {
		return Hint{}, err
	}
	if run.Status != StatusActive {
		return Hint{}, &RefusedError{Code: "not-active", Message: "the run is " + run.Status}
	}
	lesson, ok := s.Catalog.Lesson(run.LessonID)
	if !ok {
		return Hint{}, ErrNotFound
	}
	text, _ := lesson.Text(run.Language)
	hints := text.Steps[run.Step-1].Hints
	if run.StepHints >= len(hints) {
		return Hint{}, fmt.Errorf("%w: all %d hint(s) of step %d were shown", ErrHintsExhausted, len(hints), run.Step)
	}
	h := Hint{Step: run.Step, StepID: lesson.Manifest.Steps[run.Step-1].ID, Index: run.StepHints + 1, Text: hints[run.StepHints], Remaining: len(hints) - run.StepHints - 1, Language: text.Language}
	run.StepHints++
	run.HintsUsed++
	// SQLite serializes on one connection: every read happens before the transaction
	repo, _ := s.DB.RepoByID(ctx, run.RepoID)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Hint{}, err
	}
	defer tx.Rollback()
	if err := s.saveRun(ctx, tx, &run); err != nil {
		return Hint{}, err
	}
	if _, err := tx.Emit(run.OrgID, "io.ore.lesson.run.hint.used", runSubject(repo.Org, repo.Name, run.ID), run.Learner, map[string]any{"run_id": run.ID, "step": run.Step, "hint": h.Index}); err != nil {
		return Hint{}, err
	}
	return h, tx.Commit()
}

// RunView is GET /api/v0/learn/runs/{id}: the run with its current step text.
type RunView struct {
	Run           Run       `json:"run"`
	LessonTitle   string    `json:"lesson_title"`
	Artifact      string    `json:"artifact"`
	Steps         int       `json:"steps"`
	Current       *StepText `json:"current_step,omitempty"`
	HintsTotal    int       `json:"hints_total"`
	HintsShown    int       `json:"hints_shown"`
	HintsLeft     int       `json:"hints_left"`
	HintUnlocked  bool      `json:"hint_unlocked"`
	AutoHint      string    `json:"auto_hint,omitempty"`
	Language      string    `json:"language"`
	Fallback      bool      `json:"language_fallback,omitempty"`
	StepsMarkdown string    `json:"-"`
}

// View assembles the learner's view of a run.
func (s *Service) View(ctx context.Context, run Run, lang string) (RunView, error) {
	lesson, ok := s.Catalog.Lesson(run.LessonID)
	if !ok {
		return RunView{}, ErrNotFound
	}
	if lang == "" {
		lang = run.Language
	}
	text, fallback := lesson.Text(lang)
	v := RunView{Run: run, LessonTitle: lesson.Title(lang), Artifact: lesson.Manifest.Artifact, Steps: len(lesson.Manifest.Steps), Language: text.Language, Fallback: fallback}
	if run.Status == StatusActive && run.Step >= 1 && run.Step <= len(text.Steps) {
		st := text.Steps[run.Step-1]
		shown := StepText{Number: st.Number, Title: st.Title, Body: st.Body, Hints: st.Hints[:min(run.StepHints, len(st.Hints))]}
		v.Current = &shown
		v.HintsTotal, v.HintsShown, v.HintsLeft = len(st.Hints), run.StepHints, len(st.Hints)-run.StepHints
		v.HintUnlocked = run.Attempts >= lesson.Manifest.Steps[run.Step-1].HintAfterFailures
		if v.HintUnlocked && run.StepHints == 0 && len(st.Hints) > 0 && run.Attempts > 0 {
			v.AutoHint = st.Hints[0]
		}
	}
	return v, nil
}
