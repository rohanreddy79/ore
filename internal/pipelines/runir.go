// SPDX-License-Identifier: AGPL-3.0-only

package pipelines

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// This file executes compiled IR pipelines: one fresh workspace (and, on the
// docker executor, one container) per task, tasks in the order the compiler
// emitted (topological), steps with the Actions step model (conditions,
// continue-on-error, timeouts, $GITHUB_OUTPUT/$GITHUB_ENV/$GITHUB_PATH/
// $GITHUB_STEP_SUMMARY, ::workflow-commands::), secrets injected only into
// the step process that references them and masked in the log, natively
// implemented uses: steps through RegisterNativeStep, and a span-like
// attribute set per task recorded in the run's task JSON.

// Paths the task process sees on the docker executor.
const (
	ContainerWorkspace = "/ore/workspace"
	ContainerRunner    = "/ore/runner"
	ContainerToolCache = "/opt/hostedtoolcache"
)

// TaskRunResult is the per-task record stored in the run's task JSON. It is
// a superset of TaskResult (the native v0 pipeline record).
type TaskRunResult struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Image       string `json:"image"`
	Status      string `json:"status"` // succeeded | failed | skipped
	ExitCode    int    `json:"exit_code"`
	DurationMS  int64  `json:"duration_ms"`
	Reason      string `json:"reason,omitempty"`
	Pipeline    string `json:"pipeline,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
	CacheHit    bool   `json:"cache_hit"`
	// ContinueOnError marks a task whose failure does not fail the run
	// (jobs.<id>.continue-on-error).
	ContinueOnError bool              `json:"continue_on_error,omitempty"`
	MatrixKey       map[string]any    `json:"matrix_key,omitempty"`
	Steps           []StepRunResult   `json:"steps,omitempty"`
	Outputs         map[string]string `json:"outputs,omitempty"`
	Summary         string            `json:"summary,omitempty"`
	Annotations     []Annotation      `json:"annotations,omitempty"`
	Divergences     []Divergence      `json:"divergences,omitempty"`
	Attributes      map[string]any    `json:"attributes,omitempty"`
}

// StepRunResult records one step.
type StepRunResult struct {
	Index      int               `json:"index"`
	ID         string            `json:"id,omitempty"`
	Name       string            `json:"name,omitempty"`
	Uses       string            `json:"uses,omitempty"`
	Outcome    string            `json:"outcome"`    // success | failure | skipped
	Conclusion string            `json:"conclusion"` // success | failure | skipped (success when continue-on-error absorbed a failure)
	ExitCode   int               `json:"exit_code"`
	DurationMS int64             `json:"duration_ms"`
	Reason     string            `json:"reason,omitempty"`
	Outputs    map[string]string `json:"outputs,omitempty"`
}

// Annotation is an ::error::/::warning::/::notice:: from a step.
type Annotation struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Step    int    `json:"step,omitempty"`
}

type changeInfo struct {
	Number    int
	Title     string
	SourceRef string
	TargetRef string
	BaseOID   string
	HeadOID   string
}

type runContext struct {
	rn        *Runner
	ctx       context.Context
	repo      ledger.Repo
	run       *ledger.PipelineRun
	actor     ledger.Actor
	commit    string
	tree      string
	logw      *prefixWriter
	masker    *Masker
	event     string
	ref       string
	refName   string
	refType   string
	baseRef   string
	headRef   string
	change    *changeInfo
	trust     string
	runNumber int
	results   map[string]*TaskRunResult
	tasks     map[string]*IRTask
	secrets   map[string]map[string]string // environment -> name -> value
	changed   []string
	changedOK bool
	seq       int
}

type taskRef struct {
	ir   *IR
	task *IRTask
}

func (rn *Runner) executorKind() string {
	if rn.Executor == "host" {
		return "host"
	}
	return "docker"
}

func (rn *Runner) dataDir() string {
	if rn.DataDir != "" {
		return rn.DataDir
	}
	return filepath.Dir(rn.LogsDir)
}

// runFrontend compiles the tree with the Frontend and runs the result. It
// reports handled=false when the front-end found nothing to compile.
func (rn *Runner) runFrontend(ctx context.Context, repo ledger.Repo, commit, work string, logw *prefixWriter) (string, any, bool) {
	ri := runFromContext(ctx)
	if ri == nil {
		return "", nil, false
	}
	rc := &runContext{rn: rn, ctx: ctx, repo: repo, run: ri.run, actor: ri.actor, commit: commit, tree: work, logw: logw,
		masker: &Masker{}, results: map[string]*TaskRunResult{}, tasks: map[string]*IRTask{}, secrets: map[string]map[string]string{}}
	rc.detectEvent()
	plans, err := rn.Frontend.Compile(ctx, CompileRequest{Work: work, Repo: repo, Commit: commit, Executor: rn.executorKind(), Trust: rc.trust, Event: rc.event})
	if err != nil {
		logw.line("ore", "compile: "+err.Error())
		return "error", []TaskRunResult{}, true
	}
	if len(plans) == 0 {
		return "", nil, false
	}
	rc.storePlans(plans)
	logw.line("ore", fmt.Sprintf("pipeline: %d workflow(s) compiled for event %s on %s (commit %s, trust scope %s)", len(plans), rc.event, rc.ref, short(commit), rc.trust))
	var tasks []taskRef
	refused := false
	for _, p := range plans {
		if p.IR != nil && p.IR.Source != nil {
			for _, d := range p.IR.Source.Divergences {
				logw.line("ore", fmt.Sprintf("divergence %s: %s: %s (%s)", p.Source, d.Code, d.Message, d.Location))
			}
		}
		matched, why := false, "not parsed"
		if p.IR != nil {
			matched, why = rc.matchTrigger(p.IR)
		}
		if p.Refused != "" {
			if matched || p.IR == nil {
				logw.line("ore", fmt.Sprintf("refused %s: %s", p.Source, p.Refused))
				refused = true
			} else {
				logw.line("ore", fmt.Sprintf("%s is refused (%s) but does not match this event: %s", p.Source, p.Refused, why))
			}
			continue
		}
		if !matched {
			logw.line("ore", fmt.Sprintf("%s: skipped, %s", p.Source, why))
			continue
		}
		logw.line("ore", fmt.Sprintf("%s: %s, %d task(s)", p.Source, why, len(p.IR.Tasks)))
		for i := range p.IR.Tasks {
			tasks = append(tasks, taskRef{ir: p.IR, task: &p.IR.Tasks[i]})
			rc.tasks[p.IR.Tasks[i].ID] = &p.IR.Tasks[i]
		}
	}
	if refused {
		logw.line("ore", "the run was refused before any task started")
		return "failed", []TaskRunResult{}, true
	}
	if len(tasks) == 0 {
		logw.line("ore", "no workflow matches this event; nothing to run")
		return "succeeded", []TaskRunResult{}, true
	}
	if err := rc.preflightSecrets(tasks); err != nil {
		logw.line("ore", "refused: "+err.Error())
		logw.line("ore", "the run was refused before any task started")
		return "failed", []TaskRunResult{}, true
	}
	results := make([]TaskRunResult, 0, len(tasks))
	status := "succeeded"
	for _, tr := range tasks {
		res := rc.runTask(tr.ir, tr.task)
		rc.results[tr.task.ID] = res
		results = append(results, *res)
		if res.Status == "failed" && !res.ContinueOnError {
			status = "failed"
		}
	}
	return status, results, true
}

func short(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}

func (rc *runContext) repoPath() string {
	return filepath.Join(rc.rn.ReposDir, rc.repo.Org, rc.repo.Name+".git")
}

func (rc *runContext) detectEvent() {
	rp := rc.repoPath()
	rc.event, rc.refType = "push", "branch"
	rc.ref = "refs/heads/" + rc.repo.DefaultBranch
	if rc.run != nil && rc.run.ChangeID != nil {
		var c changeInfo
		err := rc.rn.DB.QueryRow(rc.ctx, `SELECT number, title, source_ref, target_ref, base_oid, head_oid FROM changes WHERE id = ?`, *rc.run.ChangeID).
			Scan(&c.Number, &c.Title, &c.SourceRef, &c.TargetRef, &c.BaseOID, &c.HeadOID)
		if err == nil {
			rc.change = &c
			rc.event = "pull_request"
			rc.ref = fmt.Sprintf("refs/pull/%d/merge", c.Number)
			rc.refName = fmt.Sprintf("%d/merge", c.Number)
			rc.headRef = strings.TrimPrefix(c.SourceRef, "refs/heads/")
			rc.baseRef = strings.TrimPrefix(c.TargetRef, "refs/heads/")
		}
	}
	if rc.change == nil {
		refs, _ := gitx.Refs(rc.ctx, rp)
		var names []string
		for r, oid := range refs {
			if oid == rc.commit {
				names = append(names, r)
			}
		}
		sort.Strings(names)
		chosen := ""
		for _, n := range names {
			if n == "refs/heads/"+rc.repo.DefaultBranch {
				chosen = n
				break
			}
		}
		if chosen == "" {
			for _, n := range names {
				if strings.HasPrefix(n, "refs/heads/") {
					chosen = n
					break
				}
			}
		}
		if chosen == "" {
			for _, n := range names {
				if strings.HasPrefix(n, "refs/tags/") {
					chosen = n
					break
				}
			}
		}
		if chosen != "" {
			rc.ref = chosen
		}
		if strings.HasPrefix(rc.ref, "refs/tags/") {
			rc.refType = "tag"
			rc.refName = strings.TrimPrefix(rc.ref, "refs/tags/")
		} else {
			rc.refName = strings.TrimPrefix(rc.ref, "refs/heads/")
		}
	}
	rc.trust = trustFromContext(rc.ctx)
	if rc.trust == "" {
		if gitx.IsAncestor(rc.ctx, rp, rc.commit, "refs/heads/"+rc.repo.DefaultBranch) {
			rc.trust = TrustDefault
		} else {
			rc.trust = TrustInternal
		}
	}
	var n int
	if err := rc.rn.DB.QueryRow(rc.ctx, `SELECT COUNT(1) FROM pipeline_runs WHERE repo_id = ?`, rc.repo.ID).Scan(&n); err == nil && n > 0 {
		rc.runNumber = n
	} else {
		rc.runNumber = 1
	}
}

func (rc *runContext) storePlans(plans []Plan) {
	if rc.run == nil {
		return
	}
	for _, p := range plans {
		irJSON, divJSON, frontend := "null", "[]", ""
		if p.IR != nil {
			b, _ := json.Marshal(p.IR)
			irJSON = string(b)
			if p.IR.Source != nil {
				frontend = p.IR.Source.Frontend
				if p.IR.Source.Divergences != nil {
					d, _ := json.Marshal(p.IR.Source.Divergences)
					divJSON = string(d)
				}
			}
		}
		_, err := rc.rn.DB.Exec(rc.ctx, `INSERT INTO pipeline_plans(id, org_id, repo_id, run_id, source, frontend, ir, divergences, refused, created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			ledger.NewID("plan"), rc.repo.OrgID, rc.repo.ID, rc.run.ID, p.Source, frontend, irJSON, divJSON, p.Refused, ledger.Now())
		if err != nil {
			rc.logw.line("ore", "store plan "+p.Source+": "+err.Error())
		}
	}
}

// changedFiles lists the paths the commit (or the change) touches.
func (rc *runContext) changedFiles() []string {
	if rc.changedOK {
		return rc.changed
	}
	rc.changedOK = true
	rp := rc.repoPath()
	var out string
	if rc.change != nil && rc.change.BaseOID != "" {
		out, _ = gitx.Run(rc.ctx, rp, nil, "diff", "--name-only", rc.change.BaseOID, rc.commit)
	} else {
		out, _ = gitx.Run(rc.ctx, rp, nil, "diff-tree", "--root", "--no-commit-id", "--name-only", "-r", rc.commit)
	}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" {
			rc.changed = append(rc.changed, l)
		}
	}
	return rc.changed
}

func globAny(patterns []string, names ...string) bool {
	for _, p := range patterns {
		for _, n := range names {
			if MatchGlob(p, n) {
				return true
			}
		}
	}
	return false
}

// filterRef applies include/ignore glob lists to a ref name (short and full).
func filterRef(include, ignore []string, name, full string) bool {
	if len(include) > 0 && !globAny(include, name, full) {
		return false
	}
	if len(ignore) > 0 && globAny(ignore, name, full) {
		return false
	}
	return true
}

func (rc *runContext) pathsMatch(paths, ignore []string) bool {
	if len(paths) == 0 && len(ignore) == 0 {
		return true
	}
	changed := rc.changedFiles()
	if len(changed) == 0 {
		return true
	}
	if len(paths) > 0 {
		hit := false
		for _, f := range changed {
			if globAny(paths, f) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if len(ignore) > 0 {
		for _, f := range changed {
			if !globAny(ignore, f) {
				return true
			}
		}
		return false
	}
	return true
}

// matchTrigger decides whether the pipeline's `on` selects this run.
func (rc *runContext) matchTrigger(ir *IR) (bool, string) {
	for _, ev := range ir.Trigger.Events {
		if ev.Name != rc.event {
			continue
		}
		switch rc.event {
		case "push":
			hasB := len(ev.Branches) > 0 || len(ev.BranchesIgnore) > 0
			hasT := len(ev.Tags) > 0 || len(ev.TagsIgnore) > 0
			if rc.refType == "tag" {
				if hasB && !hasT {
					continue
				}
				if !filterRef(ev.Tags, ev.TagsIgnore, rc.refName, rc.ref) {
					continue
				}
			} else {
				if hasT && !hasB {
					continue
				}
				if !filterRef(ev.Branches, ev.BranchesIgnore, rc.refName, rc.ref) {
					continue
				}
			}
			if !rc.pathsMatch(ev.Paths, ev.PathsIgnore) {
				return false, "push paths filter does not match the changed files"
			}
			return true, "on push to " + rc.ref
		case "pull_request":
			if len(ev.Types) > 0 {
				ok := false
				for _, t := range ev.Types {
					if t == "opened" || t == "synchronize" || t == "reopened" {
						ok = true
					}
				}
				if !ok {
					continue
				}
			}
			if !filterRef(ev.Branches, ev.BranchesIgnore, rc.baseRef, "refs/heads/"+rc.baseRef) {
				continue
			}
			if !rc.pathsMatch(ev.Paths, ev.PathsIgnore) {
				return false, "pull_request paths filter does not match the changed files"
			}
			return true, fmt.Sprintf("on pull_request #%d (%s -> %s)", rc.change.Number, rc.headRef, rc.baseRef)
		}
	}
	return false, fmt.Sprintf("no `on` trigger matches event %s on %s", rc.event, rc.ref)
}

func paramString(t *IRTask, key string) string {
	if t.Inputs.Params == nil {
		return ""
	}
	v, _ := t.Inputs.Params[key].(string)
	return v
}

func paramStringMap(t *IRTask, key string) map[string]string {
	out := map[string]string{}
	if t.Inputs.Params == nil {
		return out
	}
	switch m := t.Inputs.Params[key].(type) {
	case map[string]string:
		for k, v := range m {
			out[k] = v
		}
	case map[string]any:
		for k, v := range m {
			out[k] = ToString(v)
		}
	}
	return out
}

func paramBool(t *IRTask, key string, def bool) bool {
	if t.Inputs.Params == nil {
		return def
	}
	if v, ok := t.Inputs.Params[key].(bool); ok {
		return v
	}
	return def
}

// preflightSecrets resolves every secret any selected task references and
// refuses the run before it starts when one is missing.
func (rc *runContext) preflightSecrets(tasks []taskRef) error {
	var missing []string
	for _, tr := range tasks {
		env := paramString(tr.task, "environment")
		for _, s := range tr.task.Inputs.Secrets {
			name := s.Name
			if name == "" {
				name = s.Path[strings.LastIndex(s.Path, "/")+1:]
			}
			if rc.secrets[env] == nil {
				rc.secrets[env] = map[string]string{}
			}
			if _, done := rc.secrets[env][name]; done {
				continue
			}
			if strings.EqualFold(name, "GITHUB_TOKEN") {
				rc.secrets[env][name] = ""
				continue
			}
			if rc.rn.Stores == nil || rc.rn.Stores.Secrets == nil {
				return fmt.Errorf("task %s references secret %s but no secret store is configured", tr.task.ID, name)
			}
			v, ok, err := rc.rn.Stores.Secrets.Get(rc.ctx, rc.repo, env, name)
			if err != nil {
				return err
			}
			if !ok {
				where := "repository"
				if env != "" {
					where = fmt.Sprintf("environment %q or repository", env)
				}
				missing = append(missing, fmt.Sprintf("%s (referenced by task %s; not defined for the %s)", name, tr.task.ID, where))
				continue
			}
			rc.secrets[env][name] = v
			rc.masker.Add(v)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("secrets not defined: %s; set them with POST /api/v0/repos/%s/%s/secrets", strings.Join(missing, "; "), rc.repo.Org, rc.repo.Name)
	}
	return nil
}

func (rc *runContext) secretsContext(env string) map[string]any {
	out := map[string]any{"GITHUB_TOKEN": ""}
	for k, v := range rc.secrets[""] {
		out[k] = v
	}
	if env != "" {
		for k, v := range rc.secrets[env] {
			out[k] = v
		}
	}
	return out
}

func (rc *runContext) emit(typ, subject string, data any) {
	tx, err := rc.rn.DB.Begin(rc.ctx)
	if err != nil {
		return
	}
	defer tx.Rollback()
	if _, err := tx.Emit(rc.repo.OrgID, typ, subject, rc.actor, data); err != nil {
		return
	}
	_ = tx.Commit()
}

// ---- one task ----

type postHook struct {
	name   string
	always bool
	fn     func(ctx context.Context) error
}

type taskState struct {
	rc        *runContext
	ir        *IR
	t         *IRTask
	res       *TaskRunResult
	dir       string
	ws        string
	runner    string
	home      string
	container string
	ctrPath   string
	ctrHome   string
	ctrArch   string
	jobEnv    map[string]string
	fileEnv   map[string]string
	pathAdd   []string
	steps     map[string]map[string]any
	failed    bool
	posts     []postHook
	log       io.Writer
}

func (rc *runContext) runTask(ir *IR, t *IRTask) *TaskRunResult {
	rc.seq++
	start := time.Now()
	res := &TaskRunResult{ID: t.ID, Name: t.Name, Status: "succeeded", Pipeline: ir.ID, ContentHash: t.ContentHash, MatrixKey: t.MatrixKey, Outputs: map[string]string{}, ContinueOnError: paramBool(t, "continue_on_error", false)}
	if t.Inputs.Image != nil {
		res.Image = t.Inputs.Image.Ref
	}
	jobID := paramString(t, "job")
	if ir.Source != nil {
		for _, d := range ir.Source.Divergences {
			if jobID != "" && (d.Location == "jobs."+jobID || strings.HasPrefix(d.Location, "jobs."+jobID+".")) {
				res.Divergences = append(res.Divergences, d)
			}
		}
	}
	ts := &taskState{rc: rc, ir: ir, t: t, res: res, jobEnv: map[string]string{}, fileEnv: map[string]string{}, steps: map[string]map[string]any{}}
	ts.log = &maskedWriter{p: rc.logw, id: t.ID, masker: rc.masker}
	subject := rc.repo.Org + "/" + rc.repo.Name + "/runs/" + rc.run.ID + "/tasks/" + t.ID
	finish := func(status, reason string, code int) *TaskRunResult {
		res.Status, res.Reason, res.ExitCode = status, reason, code
		res.DurationMS = time.Since(start).Milliseconds()
		res.Attributes = rc.attributes(ir, t, res, start)
		rc.logw.line(t.ID, fmt.Sprintf("finished: %s%s (exit %d, %d ms)", status, iff(reason != "", " ("+reason+")", ""), code, res.DurationMS))
		rc.emit("io.ore.pipeline.run.task.finished", subject, map[string]any{"task_id": t.ID, "status": status, "reason": reason, "content_hash": t.ContentHash, "cache_hit": false, "duration_ms": res.DurationMS, "attributes": res.Attributes})
		return res
	}
	rc.logw.line(t.ID, fmt.Sprintf("task %s%s: %d step(s), image %s, content hash %s", t.ID, iff(t.Name != "" && t.Name != t.ID, " ("+t.Name+")", ""), len(t.Steps), iff(res.Image != "", res.Image, "(host)"), t.ContentHash))
	rc.emit("io.ore.pipeline.run.task.started", subject, map[string]any{"task_id": t.ID, "name": t.Name, "content_hash": t.ContentHash, "pipeline": ir.ID})

	// needs
	needsFailed, needsSkipped := false, false
	for _, n := range t.Needs {
		r, ok := rc.results[n]
		switch {
		case !ok:
			needsFailed = true
			rc.logw.line(t.ID, "needs "+n+": no result (not run)")
		case r.Status == "failed":
			needsFailed = true
		case r.Status == "skipped":
			needsSkipped = true
		}
	}
	// fail-fast: a sibling matrix instance failed
	if jobID != "" && paramBool(t, "fail_fast", true) && len(t.MatrixKey) > 0 {
		for id, r := range rc.results {
			if other, ok := rc.tasks[id]; ok && paramString(other, "job") == jobID && paramString(other, "workflow") == paramString(t, "workflow") && r.Status == "failed" {
				rc.logw.line(t.ID, "skipped: fail-fast, matrix instance "+id+" failed")
				return finish("skipped", "fail-fast", 0)
			}
		}
	}
	cond := strings.TrimSpace(t.Condition)
	if cond == "" {
		cond = "success()"
	}
	env := ts.exprEnv(nil, !needsFailed && !needsSkipped, needsFailed)
	ok, err := env.EvalBool(cond)
	if err != nil {
		rc.logw.line(t.ID, "condition `"+cond+"`: "+err.Error())
		return finish("failed", "condition-error", 1)
	}
	if !ok {
		why := "condition `" + cond + "` is false"
		if needsFailed && cond == "success()" {
			why = "a needed task failed"
		} else if needsSkipped && cond == "success()" {
			why = "a needed task was skipped"
		}
		rc.logw.line(t.ID, "skipped: "+why)
		return finish("skipped", "condition-false", 0)
	}
	if why := paramString(t, "skip_reason"); why != "" {
		rc.logw.line(t.ID, "skipped: "+why+" (recorded as a divergence, not run)")
		return finish("skipped", "unsupported", 0)
	}
	if t.Executor.OS != "" && t.Executor.OS != "linux" {
		rc.logw.line(t.ID, "skipped: no executor for runs-on os "+t.Executor.OS+" (only linux containers and the host)")
		return finish("skipped", "unsupported-os", 0)
	}

	// workspace
	dir, err := os.MkdirTemp("", "ore-task-*")
	if err != nil {
		rc.logw.line(t.ID, "workspace: "+err.Error())
		return finish("failed", "workspace", 1)
	}
	defer os.RemoveAll(dir)
	ts.dir, ts.ws, ts.runner, ts.home = dir, filepath.Join(dir, "workspace"), filepath.Join(dir, "runner"), filepath.Join(dir, "home")
	for _, d := range []string{ts.ws, ts.runner, ts.home, filepath.Join(ts.runner, "tmp"), filepath.Join(ts.runner, "steps")} {
		os.MkdirAll(d, 0o755)
	}
	if err := ts.materialize(); err != nil {
		rc.logw.line(t.ID, "materialize: "+err.Error())
		return finish("failed", "materialize", 1)
	}
	timeout := time.Duration(DefaultTaskTimeoutSeconds) * time.Second
	if t.Limits != nil && t.Limits.TimeoutSeconds > 0 {
		timeout = time.Duration(t.Limits.TimeoutSeconds) * time.Second
	} else if rc.rn.Timeout > 0 {
		timeout = rc.rn.Timeout
	}
	ctx, cancel := context.WithTimeout(rc.ctx, timeout)
	defer cancel()
	if rc.rn.executorKind() == "docker" {
		if err := ts.startContainer(ctx); err != nil {
			rc.logw.line(t.ID, "container: "+err.Error())
			return finish("failed", "container", 1)
		}
		defer ts.stopContainer()
	}
	ts.writeEventJSON()
	ts.jobEnv = ts.defaultEnv()
	jobEnvEnv := ts.exprEnv(ts.jobEnv, true, false)
	userEnv := paramStringMap(t, "env")
	keys := sortedKeys(userEnv)
	for _, k := range keys {
		v, err := jobEnvEnv.Interpolate(userEnv[k])
		if err != nil {
			rc.logw.line(t.ID, "env "+k+": "+err.Error())
			return finish("failed", "env", 1)
		}
		ts.jobEnv[k] = v
	}

	// steps
	exit := 0
	for i := range t.Steps {
		if ctx.Err() != nil {
			// the task deadline fired: the remaining steps are cancelled
			res.Steps = append(res.Steps, StepRunResult{Index: i + 1, ID: t.Steps[i].ID, Name: t.Steps[i].Name, Uses: t.Steps[i].Uses, Outcome: "skipped", Conclusion: "skipped", Reason: "cancelled"})
			continue
		}
		sr := ts.runStep(ctx, i, &t.Steps[i])
		res.Steps = append(res.Steps, sr)
		if sr.Conclusion == "failure" {
			exit = sr.ExitCode
			if exit == 0 {
				exit = 1
			}
		}
	}
	// post hooks (reverse order)
	for i := len(ts.posts) - 1; i >= 0; i-- {
		p := ts.posts[i]
		if ts.failed && !p.always {
			rc.logw.line(t.ID, "post "+p.name+": skipped (task failed)")
			continue
		}
		if err := p.fn(ctx); err != nil {
			rc.logw.line(t.ID, "post "+p.name+": "+err.Error())
			ts.warn("post %s: %v", p.name, err)
		}
	}
	// outputs and summary
	if t.Outputs != nil && len(t.Outputs.Values) > 0 {
		oe := ts.exprEnv(ts.mergedEnv(), !ts.failed, ts.failed)
		for _, k := range sortedKeys(t.Outputs.Values) {
			v, err := oe.Interpolate(t.Outputs.Values[k])
			if err != nil {
				ts.warn("output %s: %v", k, err)
				continue
			}
			res.Outputs[k] = rc.masker.Mask(v)
		}
	}
	if b, err := os.ReadFile(filepath.Join(ts.runner, "summary.md")); err == nil && len(b) > 0 {
		if len(b) > 65536 {
			b = b[:65536]
		}
		res.Summary = rc.masker.Mask(string(b))
	}
	if ts.failed {
		reason := ""
		for _, s := range res.Steps {
			if s.Reason == "timeout" && s.Conclusion == "failure" {
				reason = "timeout"
			}
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			reason = "timeout"
		}
		return finish("failed", reason, exit)
	}
	return finish("succeeded", "", 0)
}

func iff(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (ts *taskState) warn(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	ts.res.Annotations = append(ts.res.Annotations, Annotation{Level: "warning", Message: ts.rc.masker.Mask(msg)})
	ts.rc.logw.line(ts.t.ID, "warning: "+ts.rc.masker.Mask(msg))
}

func (ts *taskState) materialize() error {
	rp := ts.rc.repoPath()
	needGit := false
	for _, s := range ts.t.Steps {
		if repo, _, _ := SplitUses(s.Uses); strings.EqualFold(repo, "actions/checkout") {
			needGit = true
		}
	}
	if needGit {
		os.RemoveAll(ts.ws)
		if _, err := gitx.Run(ts.rc.ctx, "", nil, "clone", "--quiet", "--no-checkout", rp, ts.ws); err == nil {
			if _, err := gitx.Run(ts.rc.ctx, ts.ws, nil, "checkout", "--quiet", "--detach", ts.rc.commit); err == nil {
				return nil
			}
		}
		os.RemoveAll(ts.ws)
		os.MkdirAll(ts.ws, 0o755)
	}
	return gitx.Archive(ts.rc.ctx, rp, ts.rc.commit, ts.ws)
}

func (ts *taskState) isDocker() bool { return ts.container != "" }

// taskPath maps a host path under the workspace or runner dir to the path
// the task process sees.
func (ts *taskState) taskPath(hostPath string) string {
	if !ts.isDocker() {
		return hostPath
	}
	for host, ctr := range map[string]string{ts.ws: ContainerWorkspace, ts.runner: ContainerRunner} {
		if hostPath == host {
			return ctr
		}
		if rel, err := filepath.Rel(host, hostPath); err == nil && !strings.HasPrefix(rel, "..") {
			return ctr + "/" + filepath.ToSlash(rel)
		}
	}
	return hostPath
}

func (ts *taskState) hostPath(taskPath string) (string, bool) {
	if !ts.isDocker() {
		if rel, err := filepath.Rel(ts.ws, taskPath); err == nil && !strings.HasPrefix(rel, "..") {
			return taskPath, true
		}
		return taskPath, filepath.IsAbs(taskPath) && strings.HasPrefix(taskPath, ts.dir)
	}
	for ctr, host := range map[string]string{ContainerWorkspace: ts.ws, ContainerRunner: ts.runner} {
		if taskPath == ctr {
			return host, true
		}
		if strings.HasPrefix(taskPath, ctr+"/") {
			return filepath.Join(host, filepath.FromSlash(strings.TrimPrefix(taskPath, ctr+"/"))), true
		}
	}
	return "", false
}

func (ts *taskState) toolCacheHost() string {
	p := filepath.Join(ts.rc.rn.dataDir(), "toolcache")
	os.MkdirAll(p, 0o755)
	return p
}

func (ts *taskState) startContainer(ctx context.Context) error {
	image := ""
	if ts.t.Inputs.Image != nil {
		image = ts.t.Inputs.Image.Ref
	}
	if image == "" {
		image = "alpine:3"
	}
	name := fmt.Sprintf("ore-%s-%d", strings.ToLower(ts.rc.run.ID), ts.rc.seq)
	ts.rc.logw.line(ts.t.ID, "starting container "+name+" from "+image)
	args := []string{"run", "-d", "--name", name, "--network", "bridge",
		"-v", ts.ws + ":" + ContainerWorkspace, "-v", ts.runner + ":" + ContainerRunner, "-v", ts.toolCacheHost() + ":" + ContainerToolCache,
		"-w", ContainerWorkspace, "--entrypoint", "sh", image, "-c", "while :; do sleep 3600; done"}
	if _, err := dockerCmd(ctx, args...); err != nil {
		return err
	}
	ts.container = name
	out, err := dockerCmd(ctx, "exec", name, "sh", "-c", `echo "$PATH"; echo "$HOME"; uname -m`)
	if err != nil {
		ts.stopContainer()
		return err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) >= 3 {
		ts.ctrPath, ts.ctrHome, ts.ctrArch = lines[0], lines[1], lines[2]
	}
	return nil
}

func (ts *taskState) stopContainer() {
	if ts.container == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = dockerCmd(ctx, "rm", "-f", ts.container)
	ts.container = ""
}

func dockerCmd(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func (ts *taskState) runnerOS() string {
	if ts.isDocker() {
		return "Linux"
	}
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	}
	return "Linux"
}

func (ts *taskState) runnerArch() string {
	m := ts.ctrArch
	if !ts.isDocker() {
		m = runtime.GOARCH
	}
	switch m {
	case "x86_64", "amd64":
		return "X64"
	case "aarch64", "arm64":
		return "ARM64"
	case "i386", "386":
		return "X86"
	}
	return strings.ToUpper(m)
}

func (ts *taskState) writeEventJSON() {
	b, _ := json.MarshalIndent(ts.rc.eventPayload(), "", "  ")
	os.WriteFile(filepath.Join(ts.runner, "event.json"), b, 0o644)
}

func (rc *runContext) eventPayload() map[string]any {
	repo := map[string]any{"name": rc.repo.Name, "full_name": rc.repo.Org + "/" + rc.repo.Name, "default_branch": rc.repo.DefaultBranch, "owner": map[string]any{"login": rc.repo.Org}}
	if rc.change != nil {
		return map[string]any{
			"action": "synchronize", "number": float64(rc.change.Number), "repository": repo,
			"pull_request": map[string]any{"number": float64(rc.change.Number), "title": rc.change.Title,
				"head": map[string]any{"ref": rc.headRef, "sha": rc.commit}, "base": map[string]any{"ref": rc.baseRef, "sha": rc.change.BaseOID}},
		}
	}
	return map[string]any{"ref": rc.ref, "after": rc.commit, "before": "", "repository": repo, "head_commit": map[string]any{"id": rc.commit}}
}

func (ts *taskState) defaultEnv() map[string]string {
	rc := ts.rc
	ws, runner := ts.taskPath(ts.ws), ts.taskPath(ts.runner)
	tools := ts.toolCacheHost()
	if ts.isDocker() {
		tools = ContainerToolCache
	}
	env := map[string]string{
		"CI": "true", "GITHUB_ACTIONS": "true", "ORE": "true", "ORE_RUN_ID": rc.run.ID, "ORE_TRUST_SCOPE": rc.trust,
		"GITHUB_WORKFLOW": paramString(ts.t, "workflow_name"), "GITHUB_RUN_ID": rc.run.ID, "GITHUB_RUN_NUMBER": strconv.Itoa(rc.runNumber), "GITHUB_RUN_ATTEMPT": "1",
		"GITHUB_JOB": paramString(ts.t, "job"), "GITHUB_ACTOR": rc.actor.ID, "GITHUB_TRIGGERING_ACTOR": rc.actor.ID,
		"GITHUB_REPOSITORY": rc.repo.Org + "/" + rc.repo.Name, "GITHUB_REPOSITORY_OWNER": rc.repo.Org,
		"GITHUB_EVENT_NAME": rc.event, "GITHUB_SHA": rc.commit, "GITHUB_REF": rc.ref, "GITHUB_REF_NAME": rc.refName, "GITHUB_REF_TYPE": rc.refType,
		"GITHUB_BASE_REF": rc.baseRef, "GITHUB_HEAD_REF": rc.headRef, "GITHUB_WORKSPACE": ws,
		"GITHUB_SERVER_URL": "http://ore.local", "GITHUB_API_URL": "http://ore.local/api/v0",
		"GITHUB_EVENT_PATH": runner + "/event.json", "GITHUB_STEP_SUMMARY": runner + "/summary.md", "GITHUB_ACTION": "__run",
		"RUNNER_OS": ts.runnerOS(), "RUNNER_ARCH": ts.runnerArch(), "RUNNER_TEMP": runner + "/tmp", "RUNNER_TOOL_CACHE": tools, "RUNNER_NAME": "ore",
	}
	if ts.isDocker() {
		env["GIT_CONFIG_COUNT"], env["GIT_CONFIG_KEY_0"], env["GIT_CONFIG_VALUE_0"] = "1", "safe.directory", "*"
	}
	return env
}

// mergedEnv is the environment before a step's own env: defaults, the
// workflow/job env, then $GITHUB_ENV additions.
func (ts *taskState) mergedEnv() map[string]string {
	out := map[string]string{}
	for k, v := range ts.jobEnv {
		out[k] = v
	}
	for k, v := range ts.fileEnv {
		out[k] = v
	}
	return out
}

func toAnyMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (rc *runContext) githubContext(ts *taskState) map[string]any {
	ws := ""
	if ts != nil && ts.ws != "" {
		ws = ts.taskPath(ts.ws)
	}
	workflow, job := "", ""
	if ts != nil {
		workflow, job = paramString(ts.t, "workflow_name"), paramString(ts.t, "job")
	}
	return map[string]any{
		"event_name": rc.event, "event": rc.eventPayload(), "ref": rc.ref, "ref_name": rc.refName, "ref_type": rc.refType, "ref_protected": rc.trust == TrustDefault,
		"sha": rc.commit, "repository": rc.repo.Org + "/" + rc.repo.Name, "repository_owner": rc.repo.Org, "actor": rc.actor.ID, "triggering_actor": rc.actor.ID,
		"run_id": rc.run.ID, "run_number": float64(rc.runNumber), "run_attempt": float64(1), "workflow": workflow, "job": job, "workspace": ws,
		"base_ref": rc.baseRef, "head_ref": rc.headRef, "server_url": "http://ore.local", "api_url": "http://ore.local/api/v0", "token": "",
	}
}

func (rc *runContext) needsContext(t *IRTask) map[string]any {
	out := map[string]any{}
	for _, n := range t.Needs {
		other, ok := rc.tasks[n]
		if !ok {
			continue
		}
		key := paramString(other, "job")
		if key == "" {
			key = n
		}
		entry, _ := out[key].(map[string]any)
		if entry == nil {
			entry = map[string]any{"result": "success", "outputs": map[string]any{}}
			out[key] = entry
		}
		r := rc.results[n]
		switch {
		case r == nil:
			entry["result"] = "skipped"
		case r.Status == "failed":
			entry["result"] = "failure"
		case r.Status == "skipped" && entry["result"] != "failure":
			entry["result"] = "skipped"
		}
		if r != nil {
			outs := entry["outputs"].(map[string]any)
			for k, v := range r.Outputs {
				outs[k] = v
			}
		}
	}
	return out
}

func (rc *runContext) inputsContext(ir *IR) map[string]any {
	out := map[string]any{}
	for k, in := range ir.Trigger.Inputs {
		if in.Default != nil {
			out[k] = in.Default
		}
	}
	return out
}

// exprEnv builds the expression environment. env is the `env` context;
// success/failure reflect the task (or, for the job condition, its needs).
func (ts *taskState) exprEnv(env map[string]string, success, failure bool) *ExprEnv {
	rc := ts.rc
	e := NewExprEnv()
	e.Contexts["github"] = rc.githubContext(ts)
	e.Contexts["env"] = toAnyMap(env)
	matrix := map[string]any{}
	for k, v := range ts.t.MatrixKey {
		matrix[k] = v
	}
	e.Contexts["matrix"] = matrix
	e.Contexts["secrets"] = rc.secretsContext(paramString(ts.t, "environment"))
	e.Contexts["steps"] = ts.stepsContext()
	e.Contexts["needs"] = rc.needsContext(ts.t)
	e.Contexts["inputs"] = rc.inputsContext(ts.ir)
	e.Contexts["vars"] = map[string]any{}
	e.Contexts["runner"] = map[string]any{"os": ts.runnerOS(), "arch": ts.runnerArch(), "name": "ore", "temp": env["RUNNER_TEMP"], "tool_cache": env["RUNNER_TOOL_CACHE"], "debug": ""}
	status := "success"
	if failure {
		status = "failure"
	}
	e.Contexts["job"] = map[string]any{"status": status, "container": map[string]any{"id": ts.container, "network": "bridge"}, "services": map[string]any{}}
	strategy := map[string]any{"fail-fast": paramBool(ts.t, "fail_fast", true), "max-parallel": float64(1), "job-index": float64(0), "job-total": float64(1)}
	if ts.t.Inputs.Params != nil {
		if s, ok := ts.t.Inputs.Params["strategy"].(map[string]any); ok {
			for k, v := range s {
				strategy[k] = v
			}
		}
	}
	e.Contexts["strategy"] = strategy
	if ts.ws != "" {
		e.Funcs["hashFiles"] = HashFilesFunc(ts.ws)
	}
	e.Funcs["success"] = func([]any) (any, error) { return success, nil }
	e.Funcs["failure"] = func([]any) (any, error) { return failure, nil }
	e.Funcs["always"] = func([]any) (any, error) { return true, nil }
	e.Funcs["cancelled"] = func([]any) (any, error) { return rc.ctx.Err() != nil, nil }
	return e
}

func (ts *taskState) stepsContext() map[string]any {
	out := map[string]any{}
	for id, s := range ts.steps {
		out[id] = s
	}
	return out
}

// ---- one step ----

func (ts *taskState) runStep(ctx context.Context, i int, st *Step) StepRunResult {
	rc := ts.rc
	start := time.Now()
	sr := StepRunResult{Index: i + 1, ID: st.ID, Name: st.Name, Uses: st.Uses, Outputs: map[string]string{}}
	label := st.Name
	if label == "" {
		if st.Uses != "" {
			label = st.Uses
		} else {
			label = firstLineOf(st.Run)
		}
	}
	ctxID := st.ID
	if ctxID == "" {
		ctxID = fmt.Sprintf("__step%d", i+1)
	}
	record := func(outcome, reason string, code int) StepRunResult {
		sr.Outcome, sr.Reason, sr.ExitCode = outcome, reason, code
		sr.Conclusion = outcome
		if outcome == "failure" && st.ContinueOnError {
			sr.Conclusion = "success"
		}
		if sr.Conclusion == "failure" {
			ts.failed = true
		}
		sr.DurationMS = time.Since(start).Milliseconds()
		// later steps see the real outputs through the steps context; the
		// stored record is masked
		outs := map[string]any{}
		for k, v := range sr.Outputs {
			outs[k] = v
			sr.Outputs[k] = rc.masker.Mask(v)
		}
		ts.steps[ctxID] = map[string]any{"outputs": outs, "outcome": sr.Outcome, "conclusion": sr.Conclusion}
		if outcome != "skipped" {
			rc.logw.line(ts.t.ID, fmt.Sprintf("step %d finished: %s%s (exit %d, %d ms)", i+1, sr.Conclusion, iff(sr.Conclusion != sr.Outcome, " (outcome "+sr.Outcome+", continue-on-error)", "")+iff(reason != "", " ("+reason+")", ""), code, sr.DurationMS))
		}
		return sr
	}
	cond := strings.TrimSpace(st.Condition)
	if cond == "" {
		cond = "success()"
	}
	base := ts.mergedEnv()
	condEnv := ts.exprEnv(base, !ts.failed, ts.failed)
	ok, err := condEnv.EvalBool(cond)
	if err != nil {
		rc.logw.line(ts.t.ID, fmt.Sprintf("step %d (%s): condition `%s`: %v", i+1, label, cond, err))
		return record("failure", "condition-error", 1)
	}
	if !ok {
		rc.logw.line(ts.t.ID, fmt.Sprintf("step %d (%s): skipped, `%s` is false", i+1, label, cond))
		return record("skipped", "condition-false", 0)
	}
	rc.logw.line(ts.t.ID, fmt.Sprintf("step %d/%d: %s", i+1, len(ts.t.Steps), label))

	// step files
	stepDir := filepath.Join(ts.runner, "steps", strconv.Itoa(i+1))
	os.MkdirAll(stepDir, 0o755)
	for _, f := range []string{"output", "env", "path", "state"} {
		os.WriteFile(filepath.Join(stepDir, f), nil, 0o644)
	}
	stepEnv := base
	for _, k := range sortedKeys(st.Env) {
		v, err := condEnv.Interpolate(st.Env[k])
		if err != nil {
			rc.logw.line(ts.t.ID, fmt.Sprintf("env %s: %v", k, err))
			return record("failure", "env", 1)
		}
		stepEnv[k] = v
	}
	tdir := ts.taskPath(stepDir)
	stepEnv["GITHUB_OUTPUT"], stepEnv["GITHUB_ENV"], stepEnv["GITHUB_PATH"], stepEnv["GITHUB_STATE"] = tdir+"/output", tdir+"/env", tdir+"/path", tdir+"/state"
	stepEnv["GITHUB_ACTION"] = ctxID
	if len(ts.pathAdd) > 0 {
		basePath := ts.ctrPath
		if !ts.isDocker() {
			basePath = os.Getenv("PATH")
		}
		stepEnv["PATH"] = strings.Join(ts.pathAdd, ":") + ":" + basePath
	}
	ee := ts.exprEnv(stepEnv, !ts.failed, ts.failed)
	timeout := time.Duration(0)
	if st.TimeoutSeconds > 0 {
		timeout = time.Duration(st.TimeoutSeconds) * time.Second
	}
	sctx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		sctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
		stepEnv["ORE_STEP_TIMEOUT"] = strconv.Itoa(int(timeout.Seconds()) + 1)
	}
	sw := &stepWriter{ts: ts, sr: &sr, index: i + 1}
	var code int
	var runErr error
	if st.Uses != "" {
		code, runErr = ts.runUses(sctx, st, &sr, stepEnv, ee, sw)
	} else {
		code, runErr = ts.runShell(sctx, i+1, st, stepEnv, ee, sw)
	}
	sw.flush()
	ts.collectFiles(stepDir, &sr)
	if sr.Reason == "unsupported-action" {
		return record("skipped", sr.Reason, 0)
	}
	if errors.Is(sctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		rc.logw.line(ts.t.ID, fmt.Sprintf("step %d timed out after %s; killed", i+1, timeout))
		return record("failure", "timeout", 124)
	}
	if ctx.Err() != nil {
		rc.logw.line(ts.t.ID, fmt.Sprintf("step %d cancelled: %v", i+1, ctx.Err()))
		return record("failure", "timeout", 124)
	}
	if runErr != nil {
		rc.logw.line(ts.t.ID, fmt.Sprintf("step %d error: %v", i+1, rc.masker.Mask(runErr.Error())))
		if code == 0 {
			code = 1
		}
		return record("failure", "error", code)
	}
	if code != 0 {
		return record("failure", "", code)
	}
	return record("success", "", 0)
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " ..."
	}
	if len(s) > 80 {
		s = s[:80] + "..."
	}
	return s
}

// collectFiles applies $GITHUB_OUTPUT, $GITHUB_ENV and $GITHUB_PATH after a step.
func (ts *taskState) collectFiles(stepDir string, sr *StepRunResult) {
	if b, err := os.ReadFile(filepath.Join(stepDir, "output")); err == nil {
		for k, v := range parseKeyValues(string(b)) {
			sr.Outputs[k] = v
		}
	}
	if b, err := os.ReadFile(filepath.Join(stepDir, "env")); err == nil {
		for k, v := range parseKeyValues(string(b)) {
			ts.fileEnv[k] = v
		}
	}
	if b, err := os.ReadFile(filepath.Join(stepDir, "path")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				ts.pathAdd = append([]string{l}, ts.pathAdd...)
			}
		}
	}
}

// parseKeyValues reads k=v lines and k<<DELIM heredocs.
func parseKeyValues(s string) map[string]string {
	out := map[string]string{}
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "" {
			continue
		}
		if k, delim, ok := strings.Cut(l, "<<"); ok && !strings.Contains(k, "=") {
			var vals []string
			for i++; i < len(lines) && lines[i] != delim; i++ {
				vals = append(vals, lines[i])
			}
			out[strings.TrimSpace(k)] = strings.Join(vals, "\n")
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok {
			out[strings.TrimSpace(k)] = v
		}
	}
	return out
}

// shellCommand renders the command line that runs script for a shell.
func shellCommand(shell, script string) (string, bool) {
	switch strings.TrimSpace(shell) {
	case "", "bash":
		return `if command -v bash >/dev/null 2>&1; then exec $T bash --noprofile --norc -eo pipefail "$S"; fi; exec $T sh -e "$S"`, true
	case "sh":
		return `exec $T sh -e "$S"`, true
	case "python":
		return `if command -v python3 >/dev/null 2>&1; then exec $T python3 "$S"; fi; exec $T python "$S"`, true
	case "pwsh", "powershell", "cmd":
		return "", false
	}
	if strings.Contains(shell, "{0}") {
		return "exec $T " + strings.ReplaceAll(shell, "{0}", `"$S"`), true
	}
	return "exec $T " + shell + ` "$S"`, true
}

func (ts *taskState) runShell(ctx context.Context, n int, st *Step, env map[string]string, ee *ExprEnv, out io.Writer) (int, error) {
	rc := ts.rc
	script, err := ee.Interpolate(st.Run)
	if err != nil {
		return 1, err
	}
	wd, err := ee.Interpolate(st.WorkingDirectory)
	if err != nil {
		return 1, err
	}
	cmdline, ok := shellCommand(st.Shell, "")
	if !ok {
		return 1, fmt.Errorf("shell %q is not available on this executor (bash, sh, python or a custom command with {0})", st.Shell)
	}
	stepDir := filepath.Join(ts.runner, "steps", strconv.Itoa(n))
	ext := ".sh"
	if strings.TrimSpace(st.Shell) == "python" {
		ext = ".py"
	}
	scriptHost := filepath.Join(stepDir, "script"+ext)
	if err := os.WriteFile(scriptHost, []byte(script+"\n"), 0o600); err != nil {
		return 1, err
	}
	wrapper := "#!/bin/sh\nS=" + shellQuote(ts.taskPath(scriptHost)) + "\nT=\"\"\n" +
		`if [ -n "$ORE_STEP_TIMEOUT" ] && command -v timeout >/dev/null 2>&1; then T="timeout -s KILL $ORE_STEP_TIMEOUT"; fi` + "\n" + cmdline + "\n"
	wrapperHost := filepath.Join(stepDir, "run.sh")
	if err := os.WriteFile(wrapperHost, []byte(wrapper), 0o700); err != nil {
		return 1, err
	}
	workdir := ts.taskPath(ts.ws)
	if wd != "" {
		if filepath.IsAbs(wd) {
			workdir = wd
		} else {
			workdir = workdir + "/" + strings.TrimPrefix(wd, "./")
		}
	}
	if !ts.isDocker() {
		if _, err := os.Stat(workdir); err != nil {
			return 1, fmt.Errorf("working-directory %s: %w", wd, err)
		}
	}
	_ = rc
	return ts.exec(ctx, "sh "+shellQuote(ts.taskPath(wrapperHost)), workdir, env, nil, out, out)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// exec runs a shell snippet in the task environment with env.
func (ts *taskState) exec(ctx context.Context, script, workdir string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	var cmd *exec.Cmd
	if ts.isDocker() {
		args := []string{"exec", "-i"}
		if workdir != "" {
			args = append(args, "-w", workdir)
		}
		var cmdEnv []string
		for _, k := range sortedKeys(env) {
			v := env[k]
			if ts.rc.masker.Contains(v) {
				args = append(args, "-e", k)
				cmdEnv = append(cmdEnv, k+"="+v)
			} else {
				args = append(args, "-e", k+"="+v)
			}
		}
		args = append(args, ts.container, "sh", "-c", script)
		cmd = exec.CommandContext(ctx, "docker", args...)
		cmd.Env = append(os.Environ(), cmdEnv...)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", script)
		cmd.Dir = workdir
		cmd.Env = ts.hostEnv(env)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				return cmd.Process.Kill()
			}
			return nil
		}
	}
	cmd.WaitDelay = 3 * time.Second
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// hostEnv is the host executor's process environment: the server's
// environment minus runner-specific variables, an isolated HOME, then env.
func (ts *taskState) hostEnv(env map[string]string) []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "GITHUB_") || strings.HasPrefix(k, "RUNNER_") || strings.HasPrefix(k, "ORE_") || k == "CI" || k == "HOME" || k == "TMPDIR" {
			continue
		}
		if _, set := env[k]; set {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "HOME="+ts.home, "TMPDIR="+filepath.Join(ts.runner, "tmp"))
	for _, k := range sortedKeys(env) {
		out = append(out, k+"="+env[k])
	}
	return out
}

// runUses executes a uses: step natively or records a loud skip.
func (ts *taskState) runUses(ctx context.Context, st *Step, sr *StepRunResult, env map[string]string, ee *ExprEnv, out io.Writer) (int, error) {
	rc := ts.rc
	fn, ok := NativeStepFor(st.Uses)
	if !ok {
		reason := "uses.unsupported-action"
		switch {
		case strings.HasPrefix(st.Uses, "./"):
			reason = "uses.local-action"
		case strings.HasPrefix(st.Uses, "docker://"):
			reason = "uses.docker-action"
		}
		rc.logw.line(ts.t.ID, fmt.Sprintf("step skipped: %s is not implemented natively on Ore (%s); the step is recorded as a divergence, not run", st.Uses, reason))
		sr.Reason = "unsupported-action"
		return 0, nil
	}
	with := map[string]string{}
	for k, v := range st.With {
		s, err := ee.Interpolate(ToString(v))
		if err != nil {
			return 1, fmt.Errorf("with.%s: %w", k, err)
		}
		with[k] = s
	}
	sc := &StepContext{
		Ctx: ctx, Repo: rc.repo, RunID: rc.run.ID, TaskID: ts.t.ID, Scope: rc.trust, Executor: rc.rn.executorKind(),
		Workspace: ts.ws, With: with, Env: env, Log: out, Outputs: sr.Outputs, Stores: rc.rn.Stores,
		Exec: func(ctx context.Context, script string, stdin io.Reader, stdout io.Writer) (int, error) {
			if stdout == nil {
				stdout = out
			}
			return ts.exec(ctx, script, ts.taskPath(ts.ws), env, stdin, stdout, out)
		},
		AddMask: rc.masker.Add,
		Post: func(name string, always bool, fn func(ctx context.Context) error) {
			ts.posts = append(ts.posts, postHook{name, always, fn})
		},
		Warn:     ts.warn,
		TaskPath: ts.taskPath,
		HostPath: ts.hostPath,
	}
	if err := fn(sc); err != nil {
		return 1, err
	}
	return 0, nil
}

// ---- log writers ----

// maskedWriter writes whole lines to the run log with secrets masked.
type maskedWriter struct {
	p      *prefixWriter
	id     string
	masker *Masker
	buf    []byte
}

func (m *maskedWriter) Write(b []byte) (int, error) {
	m.buf = append(m.buf, b...)
	for {
		i := bytes.IndexByte(m.buf, '\n')
		if i < 0 {
			break
		}
		m.p.line(m.id, m.masker.Mask(string(m.buf[:i])))
		m.buf = m.buf[i+1:]
	}
	return len(b), nil
}

// stepWriter handles ::workflow-commands:: and masking for a step's output.
type stepWriter struct {
	ts        *taskState
	sr        *StepRunResult
	index     int
	buf       []byte
	stopToken string
}

func (w *stepWriter) Write(b []byte) (int, error) {
	w.buf = append(w.buf, b...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.line(strings.TrimRight(string(w.buf[:i]), "\r"))
		w.buf = w.buf[i+1:]
	}
	return len(b), nil
}

func (w *stepWriter) flush() {
	if len(w.buf) > 0 {
		w.line(string(w.buf))
		w.buf = nil
	}
}

func (w *stepWriter) line(l string) {
	ts := w.ts
	if w.stopToken != "" {
		if l == "::"+w.stopToken+"::" {
			w.stopToken = ""
		}
		ts.rc.logw.line(ts.t.ID, ts.rc.masker.Mask(l))
		return
	}
	if cmd, props, msg, ok := parseCommand(l); ok {
		switch cmd {
		case "add-mask":
			ts.rc.masker.Add(msg)
			ts.rc.logw.line(ts.t.ID, "::add-mask::***")
			return
		case "set-output":
			w.sr.Outputs[props["name"]] = msg
		case "error", "warning", "notice":
			a := Annotation{Level: cmd, Message: ts.rc.masker.Mask(msg), File: props["file"], Step: w.index}
			a.Line, _ = strconv.Atoi(props["line"])
			ts.res.Annotations = append(ts.res.Annotations, a)
		case "stop-commands":
			w.stopToken = msg
		case "save-state":
			// state is per action on GitHub; not needed natively
		case "debug", "group", "endgroup", "echo":
		}
	}
	ts.rc.logw.line(ts.t.ID, ts.rc.masker.Mask(l))
}

// parseCommand parses "::cmd k=v,k2=v2::message".
func parseCommand(l string) (cmd string, props map[string]string, msg string, ok bool) {
	if !strings.HasPrefix(l, "::") {
		return "", nil, "", false
	}
	rest := l[2:]
	i := strings.Index(rest, "::")
	if i < 0 {
		return "", nil, "", false
	}
	head, msg := rest[:i], rest[i+2:]
	cmd, propStr, _ := strings.Cut(head, " ")
	props = map[string]string{}
	for _, kv := range strings.Split(propStr, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			props[strings.TrimSpace(k)] = unescapeCommand(v)
		}
	}
	return strings.ToLower(strings.TrimSpace(cmd)), props, unescapeCommand(msg), true
}

func unescapeCommand(s string) string {
	s = strings.ReplaceAll(s, "%0A", "\n")
	s = strings.ReplaceAll(s, "%0D", "\r")
	s = strings.ReplaceAll(s, "%2C", ",")
	s = strings.ReplaceAll(s, "%3A", ":")
	return strings.ReplaceAll(s, "%25", "%")
}

// ---- span attributes ----

func (rc *runContext) attributes(ir *IR, t *IRTask, res *TaskRunResult, start time.Time) map[string]any {
	result := "success"
	switch {
	case res.Status == "skipped":
		result = "skip"
	case res.Reason == "timeout":
		result = "timeout"
	case res.Reason == "condition-error" || res.Reason == "workspace" || res.Reason == "container" || res.Reason == "materialize":
		result = "error"
	case res.Status == "failed":
		result = "failure"
	}
	name := t.Name
	if name == "" {
		name = t.ID
	}
	queued := int64(0)
	if rc.run != nil {
		if created, err := time.Parse("2006-01-02T15:04:05.000Z07:00", rc.run.CreatedAt); err == nil {
			queued = start.Sub(created).Milliseconds()
		}
	}
	end := start.Add(time.Duration(res.DurationMS) * time.Millisecond)
	return map[string]any{
		"cicd.pipeline.name":            ir.ID,
		"cicd.pipeline.run.id":          rc.run.ID,
		"cicd.pipeline.task.name":       name,
		"cicd.pipeline.task.run.id":     rc.run.ID + "/" + t.ID,
		"cicd.pipeline.task.run.result": result,
		"cicd.pipeline.task.type":       taskType(t),
		"vcs.repository.name":           rc.repo.Org + "/" + rc.repo.Name,
		"vcs.ref.head.name":             rc.refName,
		"vcs.ref.head.revision":         rc.commit,
		"ore.content_hash":              t.ContentHash,
		"ore.cache_hit":                 false,
		"ore.trust_scope":               rc.trust,
		"ore.executor":                  rc.rn.executorKind(),
		"ore.image":                     res.Image,
		"span.start_time":               start.UTC().Format(time.RFC3339Nano),
		"span.end_time":                 end.UTC().Format(time.RFC3339Nano),
		"span.duration_ms":              res.DurationMS,
		"span.queue_ms":                 queued,
	}
}

func taskType(t *IRTask) string {
	s := strings.ToLower(t.ID + " " + t.Name)
	switch {
	case strings.Contains(s, "deploy") || strings.Contains(s, "release") || strings.Contains(s, "publish"):
		return "deploy"
	case strings.Contains(s, "test") || strings.Contains(s, "lint") || strings.Contains(s, "check"):
		return "test"
	case strings.Contains(s, "build") || strings.Contains(s, "compile"):
		return "build"
	}
	return "other"
}
