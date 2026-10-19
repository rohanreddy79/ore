// SPDX-License-Identifier: AGPL-3.0-only

package actions

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"strings"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
)

// CompilerVersion is recorded in every IR's source block.
const CompilerVersion = "0.1.0"

// DefaultImage runs ubuntu-* jobs when ORE_ACTIONS_IMAGE is not set. The
// catthehacker images are the de-facto standard for running Actions
// workflows outside GitHub (act uses them) and carry the usual toolchains.
const DefaultImage = "ghcr.io/catthehacker/ubuntu:act-latest"

// Divergence codes the compiler emits. Each is documented in
// docs/changes/pipelines-actions.md.
const (
	DivTriggerUnsupportedEvent    = "trigger.unsupported-event"
	DivTriggerScheduleUnscheduled = "trigger.schedule-unscheduled"
	DivTriggerUnknownFilter       = "trigger.unknown-filter"
	DivTriggerNone                = "trigger.none"
	DivWorkflowUnknownKey         = "workflow.unknown-key"
	DivWorkflowNoJobs             = "workflow.no-jobs"
	DivJobUnknownKey              = "job.unknown-key"
	DivJobReusableWorkflow        = "job.reusable-workflow"
	DivJobConcurrencyIgnored      = "job.concurrency-ignored"
	DivJobContainerOptions        = "job.container-options-ignored"
	DivJobServicesUnsupported     = "job.services-unsupported"
	DivJobEnvironmentDynamic      = "job.environment-dynamic"
	DivJobContinueOnErrorDynamic  = "job.continue-on-error-dynamic"
	DivNeedsUnknown               = "needs.unknown"
	DivNeedsCycle                 = "needs.cycle"
	DivRunsOnUnsupportedOS        = "runs-on.unsupported-os"
	DivRunsOnLabelsMapped         = "runs-on.labels-mapped"
	DivRunsOnMissing              = "runs-on.missing"
	DivRunsOnDynamic              = "runs-on.dynamic"
	DivMatrixEmpty                = "matrix.empty"
	DivMatrixDynamic              = "matrix.dynamic"
	DivStepUnknownKey             = "step.unknown-key"
	DivStepUnsupportedShell       = "step.unsupported-shell"
	DivStepContinueOnErrorDynamic = "step.continue-on-error-dynamic"
	DivUsesLocalAction            = "uses.local-action"
	DivUsesDockerAction           = "uses.docker-action"
	DivUsesMalformed              = "uses.malformed"
	DivUsesRefNotFound            = "uses.ref-not-found"
	DivUsesUnresolved             = "uses.unresolved"
	DivUsesUnsupportedAction      = "uses.unsupported-action"
	DivUsesInputUnsupported       = "uses.input-unsupported"
	DivUsesInputMissing           = "uses.input-missing"
	DivCachePathUnsupported       = "cache.path-unsupported"
	DivArtifactInvalidName        = "artifact.invalid-name"
	DivExprParseError             = "expr.parse-error"
	DivExprUnknownContext         = "expr.unknown-context"
	DivExprUnknownFunction        = "expr.unknown-function"
	DivExprObjectFilter           = "expr.object-filter"
	DivSecretsDynamicReference    = "secrets.dynamic-reference"
	DivSecretsGitHubToken         = "secrets.github-token"
)

// Compiler turns workflow files into IR.
type Compiler struct {
	// Resolver pins uses: references; nil records every reference as unresolved.
	Resolver Resolver
	// Executor is the kind the tasks will run on: docker (default) or host.
	Executor string
	// Image overrides the image for every ubuntu-* runner (ORE_ACTIONS_IMAGE).
	Image string
	// ImageDigest, when set, resolves an image reference to its digest.
	ImageDigest func(ctx context.Context, ref string) string
}

// Options describe the repository and commit the IR is compiled for.
type Options struct {
	Repo   ledger.Repo
	Commit string
	Trust  string
}

var knownContexts = map[string]bool{"github": true, "env": true, "vars": true, "job": true, "jobs": true, "steps": true, "runner": true, "secrets": true, "strategy": true, "matrix": true, "needs": true, "inputs": true}
var knownFunctions = map[string]bool{"contains": true, "startswith": true, "endswith": true, "format": true, "join": true, "tojson": true, "fromjson": true, "hashfiles": true, "success": true, "always": true, "cancelled": true, "failure": true}
var pureFunctions = map[string]bool{"contains": true, "startswith": true, "endswith": true, "format": true, "join": true, "tojson": true, "fromjson": true}

type compilation struct {
	c        *Compiler
	ctx      context.Context
	path     string
	stem     string
	w        *Workflow
	opts     Options
	div      []pipelines.Divergence
	refusals []string
	actions  map[string]pipelines.ActionRef
	secrets  map[string]bool
	compiled []*pipelines.IRTask
}

func (cp *compilation) diverge(code, loc, format string, a ...any) {
	cp.div = append(cp.div, pipelines.Divergence{Code: code, Message: fmt.Sprintf(format, a...), Location: loc})
}

func (cp *compilation) refuse(code, loc, format string, a ...any) {
	cp.diverge(code, loc, format, a...)
	cp.refusals = append(cp.refusals, fmt.Sprintf("%s: %s", loc, fmt.Sprintf(format, a...)))
}

// Compile compiles one workflow file. The returned error is a refusal: the
// planner will not run this pipeline (a needs cycle, an unknown job in
// needs, a uses: reference that does not exist, a workflow without jobs).
// The IR is still returned with its divergences whenever the file parsed.
func (c *Compiler) Compile(ctx context.Context, filePath string, src []byte, opts Options) (*pipelines.IR, error) {
	w, err := Parse(src)
	if err != nil {
		return nil, err
	}
	stem := strings.TrimSuffix(strings.TrimSuffix(path.Base(filePath), ".yml"), ".yaml")
	cp := &compilation{c: c, ctx: ctx, path: filePath, stem: stem, w: w, opts: opts, actions: map[string]pipelines.ActionRef{}, secrets: map[string]bool{}}
	ir := &pipelines.IR{
		Version:       pipelines.IRVersion,
		ID:            filePath,
		Repository:    pipelines.RepositoryRef{ID: opts.Repo.ID, OrgID: opts.Repo.OrgID},
		PolicyContext: &pipelines.PolicyContext{RefTrust: opts.Trust},
		Tasks:         []pipelines.IRTask{},
	}
	for _, u := range w.Unknown {
		cp.diverge(DivWorkflowUnknownKey, u, "top-level key %q is not supported and is ignored", u)
	}
	ir.Trigger = cp.compileTrigger()
	if w.Concurrency != nil {
		ir.Concurrency = &pipelines.Concurrency{Group: w.Concurrency.Group, CancelInProgress: w.Concurrency.CancelInProgress}
	}
	ir.Tasks = cp.compileJobs()
	ir.Source = &pipelines.Source{Frontend: "actions-yaml", Path: filePath, CompilerVersion: CompilerVersion, Divergences: cp.div}
	if ir.Source.Divergences == nil {
		ir.Source.Divergences = []pipelines.Divergence{}
	}
	ir.Hash()
	if len(cp.refusals) > 0 {
		return ir, errors.New(strings.Join(cp.refusals, "; "))
	}
	return ir, nil
}

// ---- triggers ----

func globToRegex(p string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); i++ {
		switch {
		case strings.HasPrefix(p[i:], "**"):
			b.WriteString(".*")
			i++
		case p[i] == '*':
			b.WriteString("[^/]*")
		case p[i] == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(p[i : i+1]))
		}
	}
	b.WriteString("$")
	return b.String()
}

func celMatches(field string, patterns []string) string {
	parts := make([]string, 0, len(patterns))
	for _, p := range patterns {
		parts = append(parts, fmt.Sprintf("matches(%s, '%s')", field, strings.ReplaceAll(globToRegex(p), "'", "\\'")))
	}
	return "(" + strings.Join(parts, " || ") + ")"
}

func celPaths(paths, ignore []string) []string {
	var terms []string
	if len(paths) > 0 {
		terms = append(terms, "event.paths.exists(p, "+celMatches("p", paths)+")")
	}
	if len(ignore) > 0 {
		terms = append(terms, "event.paths.exists(p, !"+celMatches("p", ignore)+")")
	}
	return terms
}

func (cp *compilation) compileTrigger() pipelines.Trigger {
	tr := pipelines.Trigger{}
	var terms []string
	best := 99
	events := map[string]int{"push": 0, "pull_request": 1, "workflow_dispatch": 2, "schedule": 3}
	ledgerEvent := map[string]string{"push": "io.ore.ref.updated", "pull_request": "io.ore.change.patchset.pushed", "workflow_dispatch": "io.ore.pipeline.dispatch", "schedule": "io.ore.schedule.fired"}
	for _, ev := range cp.w.On {
		loc := "on." + ev.Name
		for _, u := range ev.Unknown {
			cp.diverge(DivTriggerUnknownFilter, loc+"."+u, "filter %q on %s is not supported and is ignored", u, ev.Name)
		}
		te := pipelines.TriggerEvent{Name: ev.Name, Branches: ev.Branches, BranchesIgnore: ev.BranchesIgnore, Tags: ev.Tags, TagsIgnore: ev.TagsIgnore, Paths: ev.Paths, PathsIgnore: ev.PathsIgnore, Types: ev.Types, Cron: ev.Cron}
		switch ev.Name {
		case "push":
			t := []string{"event.type == 'io.ore.ref.updated'"}
			if len(ev.Branches) > 0 {
				t = append(t, celMatches("event.ref_name", ev.Branches))
			}
			if len(ev.BranchesIgnore) > 0 {
				t = append(t, "!"+celMatches("event.ref_name", ev.BranchesIgnore))
			}
			if len(ev.Tags) > 0 {
				t = append(t, celMatches("event.ref_name", ev.Tags)+" && startsWith(event.ref, 'refs/tags/')")
			}
			if len(ev.TagsIgnore) > 0 {
				t = append(t, "!"+celMatches("event.ref_name", ev.TagsIgnore))
			}
			t = append(t, celPaths(ev.Paths, ev.PathsIgnore)...)
			terms = append(terms, "("+strings.Join(t, " && ")+")")
		case "pull_request":
			t := []string{"event.type == 'io.ore.change.patchset.pushed'"}
			if len(ev.Branches) > 0 {
				t = append(t, celMatches("event.change.target_ref_name", ev.Branches))
			}
			if len(ev.BranchesIgnore) > 0 {
				t = append(t, "!"+celMatches("event.change.target_ref_name", ev.BranchesIgnore))
			}
			t = append(t, celPaths(ev.Paths, ev.PathsIgnore)...)
			terms = append(terms, "("+strings.Join(t, " && ")+")")
		case "workflow_dispatch":
			terms = append(terms, fmt.Sprintf("(event.type == 'io.ore.pipeline.dispatch' && event.pipeline == '%s')", cp.path))
			if len(ev.Inputs) > 0 {
				tr.Inputs = map[string]pipelines.TriggerInput{}
				for _, in := range ev.Inputs {
					typ := in.Type
					if typ == "" || typ == "environment" {
						typ = "string"
					}
					tr.Inputs[in.Name] = pipelines.TriggerInput{Type: typ, Default: in.Default, Options: in.Options, Required: in.Required}
				}
			}
		case "schedule":
			cp.diverge(DivTriggerScheduleUnscheduled, loc, "schedule %v is parsed and recorded but Ore v0 does not fire scheduled runs", ev.Cron)
			crons := make([]string, 0, len(ev.Cron))
			for _, c := range ev.Cron {
				crons = append(crons, "'"+c+"'")
			}
			terms = append(terms, fmt.Sprintf("(event.type == 'io.ore.schedule.fired' && event.cron in [%s])", strings.Join(crons, ", ")))
		default:
			cp.diverge(DivTriggerUnsupportedEvent, loc, "event %q is not supported; the workflow does not run for it on Ore", ev.Name)
			continue
		}
		tr.Events = append(tr.Events, te)
		if p := events[ev.Name]; p < best {
			best = p
			tr.Event = ledgerEvent[ev.Name]
		}
	}
	if tr.Event == "" {
		tr.Event = "io.ore.pipeline.dispatch"
		if len(cp.w.On) == 0 {
			cp.diverge(DivTriggerNone, "on", "the workflow has no `on` triggers; it can only be dispatched")
		}
	}
	if len(terms) > 0 {
		tr.Filters = &pipelines.TriggerFilters{CEL: strings.Join(terms, " || ")}
	}
	return tr
}

// ---- jobs ----

func (cp *compilation) compileJobs() []pipelines.IRTask {
	tasks := []pipelines.IRTask{}
	if len(cp.w.Jobs) == 0 {
		cp.refuse(DivWorkflowNoJobs, "jobs", "the workflow declares no jobs")
		return tasks
	}
	ids := map[string]bool{}
	for _, j := range cp.w.Jobs {
		ids[j.ID] = true
	}
	for _, j := range cp.w.Jobs {
		for _, n := range j.Job.Needs {
			if !ids[n] {
				cp.refuse(DivNeedsUnknown, "jobs."+j.ID+".needs", "job %q needs unknown job %q", j.ID, n)
			}
		}
	}
	order, cycle := topoOrder(cp.w.Jobs)
	if len(cycle) > 0 {
		cp.refuse(DivNeedsCycle, "jobs", "needs form a cycle among %s", strings.Join(cycle, ", "))
	}
	instances := map[string][]string{}
	for _, id := range order {
		var job Job
		for _, j := range cp.w.Jobs {
			if j.ID == id {
				job = j.Job
			}
		}
		ts := cp.compileJob(id, job, instances)
		for _, t := range ts {
			instances[id] = append(instances[id], t.ID)
		}
		tasks = append(tasks, ts...)
		cp.compiled = cp.compiled[:0]
		for i := range tasks {
			cp.compiled = append(cp.compiled, &tasks[i])
		}
	}
	return tasks
}

// topoOrder orders jobs so every job follows its needs, keeping file order
// among ready jobs; jobs on a cycle are returned separately.
func topoOrder(jobs []JobEntry) ([]string, []string) {
	indeg := map[string]int{}
	deps := map[string][]string{}
	for _, j := range jobs {
		indeg[j.ID] = 0
	}
	for _, j := range jobs {
		for _, n := range j.Job.Needs {
			if _, ok := indeg[n]; ok {
				indeg[j.ID]++
				deps[n] = append(deps[n], j.ID)
			}
		}
	}
	var order []string
	done := map[string]bool{}
	for len(order) < len(jobs) {
		progressed := false
		for _, j := range jobs {
			if done[j.ID] || indeg[j.ID] != 0 {
				continue
			}
			done[j.ID] = true
			order = append(order, j.ID)
			for _, d := range deps[j.ID] {
				indeg[d]--
			}
			progressed = true
			break
		}
		if !progressed {
			break
		}
	}
	var cycle []string
	for _, j := range jobs {
		if !done[j.ID] {
			cycle = append(cycle, j.ID)
		}
	}
	return order, cycle
}

type jobCtx struct {
	id     string
	job    Job
	loc    string
	combo  map[string]any
	keys   []string
	env    *pipelines.ExprEnv
	shell  string
	wd     string
	image  string
	osName string
}

// static reports whether an expression can be evaluated at plan time: it
// uses only the matrix and strategy contexts and pure functions.
func static(expr string) bool {
	a, err := pipelines.Analyze(expr)
	if err != nil {
		return false
	}
	for _, c := range a.Contexts {
		if c != "matrix" && c != "strategy" {
			return false
		}
	}
	for _, f := range a.Functions {
		if !pureFunctions[f] {
			return false
		}
	}
	return !a.HasFilter
}

func (cp *compilation) evalStatic(jc *jobCtx, s, loc string) string {
	if !pipelines.HasInterpolation(s) {
		return s
	}
	cp.checkInterpolations(s, loc)
	out, err := jc.env.InterpolateWith(s, static)
	if err != nil {
		cp.diverge(DivExprParseError, loc, "%v", err)
		return s
	}
	return out
}

func (cp *compilation) checkExpr(expr, loc string) *pipelines.Analysis {
	a, err := pipelines.Analyze(expr)
	if err != nil {
		cp.diverge(DivExprParseError, loc, "cannot parse expression `%s`: %v", strings.TrimSpace(expr), err)
		return nil
	}
	for _, c := range a.Contexts {
		if !knownContexts[c] {
			cp.diverge(DivExprUnknownContext, loc, "expression `%s` uses unknown context %q", strings.TrimSpace(expr), c)
		}
	}
	for _, f := range a.Functions {
		if !knownFunctions[f] {
			cp.diverge(DivExprUnknownFunction, loc, "expression `%s` calls unknown function %s()", strings.TrimSpace(expr), f)
		}
	}
	if a.HasFilter {
		cp.diverge(DivExprObjectFilter, loc, "expression `%s` uses the object filter (*), which is not supported", strings.TrimSpace(expr))
	}
	cp.collectSecrets(a, expr, loc)
	return a
}

func (cp *compilation) checkInterpolations(s, loc string) {
	exprs, err := pipelines.Interpolations(s)
	if err != nil {
		cp.diverge(DivExprParseError, loc, "%v", err)
		return
	}
	for _, e := range exprs {
		cp.checkExpr(e, loc)
	}
}

func (cp *compilation) collectSecrets(a *pipelines.Analysis, expr, loc string) {
	usesSecrets := false
	for _, c := range a.Contexts {
		if c == "secrets" {
			usesSecrets = true
		}
	}
	if !usesSecrets {
		return
	}
	found := false
	for _, r := range a.Refs {
		if strings.HasPrefix(strings.ToLower(r), "secrets.") {
			parts := strings.SplitN(r, ".", 3)
			name := parts[1]
			found = true
			if strings.EqualFold(name, "GITHUB_TOKEN") {
				cp.diverge(DivSecretsGitHubToken, loc, "secrets.GITHUB_TOKEN has no equivalent on Ore v0; an empty value is injected")
				continue
			}
			cp.secrets[name] = true
		}
	}
	if !found {
		cp.diverge(DivSecretsDynamicReference, loc, "expression `%s` reads the secrets context dynamically; only secrets.NAME references are injected", strings.TrimSpace(expr))
	}
}

func (cp *compilation) compileJob(id string, job Job, instances map[string][]string) []pipelines.IRTask {
	loc := "jobs." + id
	for _, u := range job.Unknown {
		cp.diverge(DivJobUnknownKey, loc+"."+u, "key %q is not supported and is ignored", u)
	}
	needs := []string{}
	for _, n := range job.Needs {
		needs = append(needs, instances[n]...)
	}
	if job.Uses != "" {
		cp.diverge(DivJobReusableWorkflow, loc+".uses", "reusable workflow %q is not supported in v0; the job is skipped and its dependents see it as skipped", job.Uses)
		t := pipelines.IRTask{ID: cp.stem + "/" + id, Name: firstNonEmpty(job.Name, id), Needs: needs, Steps: []pipelines.Step{},
			Executor: pipelines.Executor{Kind: cp.executorKind(), OS: "linux", Arch: "x64"},
			Inputs:   pipelines.Inputs{Source: &pipelines.SourceInput{Commit: cp.opts.Commit, Submodules: "none"}, Params: map[string]any{"job": id, "workflow": cp.stem, "workflow_name": cp.workflowName(), "skip_reason": "reusable workflow " + job.Uses + " is not supported", "uses": job.Uses}}}
		return []pipelines.IRTask{t}
	}
	if job.Concurrency != nil {
		cp.diverge(DivJobConcurrencyIgnored, loc+".concurrency", "job-level concurrency is ignored in v0 (runs are sequential)")
	}
	if job.Container != nil && (job.Container.Options != "" || job.Container.HasCredentials || len(job.Container.Ports) > 0 || len(job.Container.Volumes) > 0) {
		cp.diverge(DivJobContainerOptions, loc+".container", "container options, credentials, ports and volumes are ignored; only the image is used")
	}
	for _, s := range job.Services {
		cp.diverge(DivJobServicesUnsupported, loc+".services."+s.Name, "service container %q (%s) is not started in v0; steps that need it will fail", s.Name, s.Image)
	}
	combos, keys := cp.expandMatrix(job.Strategy, loc)
	if combos != nil && len(combos) == 0 {
		cp.diverge(DivMatrixEmpty, loc+".strategy.matrix", "no matrix combination remains after exclude; the job has no instances and does not run")
		return nil
	}
	hasMatrix := combos != nil
	if !hasMatrix {
		combos = []map[string]any{nil}
	}
	var tasks []pipelines.IRTask
	for i, combo := range combos {
		jc := &jobCtx{id: id, job: job, loc: loc, combo: combo, keys: keys}
		jc.env = pipelines.NewExprEnv()
		m := map[string]any{}
		for k, v := range combo {
			m[k] = v
		}
		jc.env.Contexts["matrix"] = m
		jc.env.Contexts["strategy"] = map[string]any{"fail-fast": failFast(job.Strategy), "max-parallel": float64(maxParallel(job.Strategy)), "job-index": float64(i), "job-total": float64(len(combos))}
		t := cp.compileInstance(jc, i, len(combos), needs, hasMatrix)
		tasks = append(tasks, t)
	}
	return tasks
}

func failFast(s *Strategy) bool {
	if s == nil || s.FailFast == nil {
		return true
	}
	return *s.FailFast
}

func maxParallel(s *Strategy) int {
	if s == nil || s.MaxParallel <= 0 {
		return 1
	}
	return s.MaxParallel
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

func (cp *compilation) workflowName() string {
	return firstNonEmpty(cp.w.Name, cp.path)
}

func (cp *compilation) executorKind() string {
	if cp.c.Executor == "host" {
		return "host"
	}
	return "docker"
}

func idSafe(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, " ", "_"), "/", "-")
	return strings.ReplaceAll(s, ",", "_")
}

func (cp *compilation) compileInstance(jc *jobCtx, index, total int, needs []string, hasMatrix bool) pipelines.IRTask {
	job, id, loc := jc.job, jc.id, jc.loc
	name := cp.evalStatic(jc, firstNonEmpty(job.Name, id), loc+".name")
	taskID := cp.stem + "/" + id
	if hasMatrix {
		var vals, kvs []string
		for _, k := range jc.keys {
			if v, ok := jc.combo[k]; ok {
				vals = append(vals, pipelines.ToString(v))
				kvs = append(kvs, k+"="+idSafe(pipelines.ToString(v)))
			}
		}
		if !strings.Contains(job.Name, "${{") {
			name += " (" + strings.Join(vals, ", ") + ")"
		}
		taskID += "/" + strings.Join(kvs, ",")
	}
	t := pipelines.IRTask{ID: taskID, Name: name, Needs: needs, MatrixKey: jc.combo, Steps: []pipelines.Step{}}
	params := map[string]any{"job": id, "workflow": cp.stem, "workflow_name": cp.workflowName(), "fail_fast": failFast(job.Strategy),
		"strategy": map[string]any{"fail-fast": failFast(job.Strategy), "max-parallel": float64(maxParallel(job.Strategy)), "job-index": float64(index), "job-total": float64(total)}}
	// runs-on
	labels := make([]string, 0, len(job.RunsOn))
	for _, l := range job.RunsOn {
		if pipelines.HasInterpolation(l) {
			v := cp.evalStatic(jc, l, loc+".runs-on")
			if pipelines.HasInterpolation(v) {
				cp.diverge(DivRunsOnDynamic, loc+".runs-on", "runs-on %q cannot be evaluated at plan time; mapped to the default linux image", l)
				v = "ubuntu-latest"
			}
			l = v
		}
		labels = append(labels, l)
	}
	jc.image, jc.osName = cp.runnerImage(labels, loc+".runs-on")
	t.Executor = pipelines.Executor{Kind: cp.executorKind(), OS: jc.osName, Arch: "x64", Labels: labels}
	if job.Container != nil && job.Container.Image != "" {
		jc.image = cp.evalStatic(jc, job.Container.Image, loc+".container.image")
		for k, v := range job.Container.Env {
			job.Env[k] = v
		}
	}
	t.Inputs.Source = &pipelines.SourceInput{Commit: cp.opts.Commit, Submodules: "none"}
	t.Inputs.Image = &pipelines.ImageInput{Ref: jc.image}
	if cp.c.ImageDigest != nil {
		t.Inputs.Image.Digest = cp.c.ImageDigest(cp.ctx, jc.image)
	}
	for _, s := range job.Services {
		t.Inputs.Services = append(t.Inputs.Services, pipelines.Service{Name: s.Name, Image: s.Image, Env: s.Env, Ports: s.Ports})
	}
	// condition
	if strings.TrimSpace(job.If) != "" {
		cond := pipelines.StripExpr(job.If)
		cp.checkExpr(cond, loc+".if")
		t.Condition = cond
	}
	// env
	env := map[string]string{}
	for k, v := range cp.w.Env {
		env[k] = cp.evalStatic(jc, v, "env."+k)
	}
	for k, v := range job.Env {
		env[k] = cp.evalStatic(jc, v, loc+".env."+k)
	}
	params["env"] = env
	// environment
	if job.Environment != "" {
		e := cp.evalStatic(jc, job.Environment, loc+".environment")
		if pipelines.HasInterpolation(e) {
			cp.diverge(DivJobEnvironmentDynamic, loc+".environment", "environment %q cannot be evaluated at plan time; secrets are resolved from the repository scope", job.Environment)
			e = ""
		}
		params["environment"] = e
		if e != "" {
			params["environment_url"] = job.EnvironmentURL
		}
	}
	// limits
	t.Limits = &pipelines.Limits{TimeoutSeconds: pipelines.DefaultTaskTimeoutSeconds}
	if job.TimeoutMinutes > 0 {
		t.Limits.TimeoutSeconds = minutesToSeconds(job.TimeoutMinutes)
	}
	// continue-on-error
	if job.ContinueOnError.Set {
		params["continue_on_error"] = cp.staticBool(jc, job.ContinueOnError, loc+".continue-on-error", DivJobContinueOnErrorDynamic)
	}
	// permissions
	if job.Permissions != nil {
		t.Permissions = job.Permissions
	} else if cp.w.Permissions != nil {
		t.Permissions = cp.w.Permissions
	}
	// defaults
	jc.shell = firstNonEmpty(job.Defaults.Shell, cp.w.Defaults.Shell)
	jc.wd = firstNonEmpty(job.Defaults.WorkingDirectory, cp.w.Defaults.WorkingDirectory)
	// outputs
	t.Outputs = &pipelines.Outputs{Summary: true}
	if len(job.Outputs) > 0 {
		t.Outputs.Values = map[string]string{}
		for _, kv := range job.Outputs {
			cp.checkInterpolations(kv.Value, loc+".outputs."+kv.Key)
			t.Outputs.Values[kv.Key] = cp.evalStatic(jc, kv.Value, loc+".outputs."+kv.Key)
		}
	}
	// steps
	for i, st := range job.Steps {
		t.Steps = append(t.Steps, cp.compileStep(jc, &t, i, st))
	}
	// secrets referenced anywhere in this task, by reference only
	for _, name := range sortedKeys(cp.secrets) {
		t.Inputs.Secrets = append(t.Inputs.Secrets, pipelines.SecretRef{Path: "repos/" + cp.opts.Repo.Org + "/" + cp.opts.Repo.Name + "/secrets/" + name, Mount: "env", Name: name})
	}
	cp.secrets = map[string]bool{}
	for _, a := range sortedKeys(cp.actions) {
		t.Inputs.Actions = append(t.Inputs.Actions, cp.actions[a])
	}
	cp.actions = map[string]pipelines.ActionRef{}
	t.Inputs.Params = params
	return t
}

func minutesToSeconds(m float64) int {
	s := int(math.Ceil(m * 60))
	if s < 1 {
		s = 1
	}
	return s
}

func (cp *compilation) staticBool(jc *jobCtx, v StrOrBool, loc, dynCode string) bool {
	if v.Expr == "" {
		return v.Bool
	}
	expr := pipelines.StripExpr(v.Expr)
	cp.checkExpr(expr, loc)
	if !static(expr) {
		cp.diverge(dynCode, loc, "`%s` cannot be evaluated at plan time; treated as false", expr)
		return false
	}
	b, err := jc.env.EvalBool(expr)
	if err != nil {
		cp.diverge(DivExprParseError, loc, "%v", err)
		return false
	}
	return b
}

// runnerImage maps runs-on labels to an image and an OS.
func (cp *compilation) runnerImage(labels []string, loc string) (string, string) {
	if len(labels) == 0 {
		cp.diverge(DivRunsOnMissing, loc, "runs-on is missing; mapped to the default linux image")
		return cp.ubuntuImage("latest"), "linux"
	}
	image, osName := "", ""
	for _, l := range labels {
		ll := strings.ToLower(l)
		switch {
		case strings.HasPrefix(ll, "ubuntu-"):
			image, osName = cp.ubuntuImage(strings.TrimPrefix(ll, "ubuntu-")), "linux"
		case strings.HasPrefix(ll, "macos-"):
			cp.diverge(DivRunsOnUnsupportedOS, loc, "runs-on %s: Ore v0 runs linux containers only; the job is skipped", l)
			if osName == "" {
				osName = "macos"
			}
		case strings.HasPrefix(ll, "windows-"):
			cp.diverge(DivRunsOnUnsupportedOS, loc, "runs-on %s: Ore v0 runs linux containers only; the job is skipped", l)
			if osName == "" {
				osName = "windows"
			}
		default:
			cp.diverge(DivRunsOnLabelsMapped, loc, "runs-on label %q has no runner pool on Ore v0; mapped to the default linux image", l)
			if osName == "" {
				osName = "linux"
			}
		}
	}
	if image == "" && osName == "linux" {
		image = cp.ubuntuImage("latest")
	}
	return image, osName
}

func (cp *compilation) ubuntuImage(version string) string {
	if cp.c.Image != "" {
		return cp.c.Image
	}
	switch version {
	case "latest", "":
		return DefaultImage
	}
	return "ghcr.io/catthehacker/ubuntu:act-" + version
}

// ---- matrix ----

// expandMatrix returns nil when the job has no matrix, else the
// combinations (possibly none) and the key order for display.
func (cp *compilation) expandMatrix(s *Strategy, loc string) ([]map[string]any, []string) {
	if s == nil || (s.Matrix == nil && s.MatrixExpr == "") {
		return nil, nil
	}
	mloc := loc + ".strategy.matrix"
	if s.MatrixExpr != "" {
		cp.checkInterpolations(s.MatrixExpr, mloc)
		cp.diverge(DivMatrixDynamic, mloc, "matrix from an expression (%s) cannot be expanded at plan time; the job runs once with an empty matrix", strings.TrimSpace(s.MatrixExpr))
		return []map[string]any{{}}, nil
	}
	m := s.Matrix
	dynamic := map[string]bool{}
	for _, d := range m.DynamicAxes {
		dynamic[d] = true
		cp.diverge(DivMatrixDynamic, mloc+"."+d, "matrix.%s is an expression and cannot be expanded at plan time; the axis is dropped", d)
	}
	var keys []string
	combos := []map[string]any{{}}
	for _, ax := range m.Axes {
		if dynamic[ax.Name] {
			continue
		}
		keys = append(keys, ax.Name)
		var next []map[string]any
		for _, c := range combos {
			for _, v := range ax.Values {
				nc := map[string]any{}
				for k, e := range c {
					nc[k] = e
				}
				nc[ax.Name] = v
				next = append(next, nc)
			}
		}
		combos = next
	}
	if len(m.Axes) == 0 && len(m.Include) > 0 {
		combos = nil
	}
	// exclude before include (so include can add an excluded combination back)
	var kept []map[string]any
	for _, c := range combos {
		excluded := false
		for _, ex := range m.Exclude {
			if matchesAll(ex, c) {
				excluded = true
				break
			}
		}
		if !excluded {
			kept = append(kept, c)
		}
	}
	original := kept
	axisNames := map[string]bool{}
	for _, k := range keys {
		axisNames[k] = true
	}
	extra := map[string]bool{}
	result := append([]map[string]any{}, original...)
	for _, inc := range m.Include {
		matched := false
		for _, c := range original {
			ok := true
			for k, v := range inc {
				if axisNames[k] && pipelines.ToString(c[k]) != pipelines.ToString(v) {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			matched = true
			for k, v := range inc {
				if !axisNames[k] {
					c[k] = v
					if !extra[k] {
						extra[k] = true
						keys = append(keys, k)
					}
				}
			}
		}
		if !matched {
			nc := map[string]any{}
			for k, v := range inc {
				nc[k] = v
				if !axisNames[k] && !extra[k] {
					extra[k] = true
					keys = append(keys, k)
				}
			}
			result = append(result, nc)
		}
	}
	if result == nil {
		result = []map[string]any{}
	}
	return result, keys
}

func matchesAll(sub, c map[string]any) bool {
	for k, v := range sub {
		cv, ok := c[k]
		if !ok || pipelines.ToString(cv) != pipelines.ToString(v) {
			return false
		}
	}
	return true
}

// ---- steps ----

func (cp *compilation) compileStep(jc *jobCtx, t *pipelines.IRTask, i int, st Step) pipelines.Step {
	loc := fmt.Sprintf("%s.steps[%d]", jc.loc, i)
	for _, u := range st.Unknown {
		cp.diverge(DivStepUnknownKey, loc+"."+u, "key %q is not supported and is ignored", u)
	}
	s := pipelines.Step{ID: st.ID, Name: cp.evalStatic(jc, st.Name, loc+".name")}
	if strings.TrimSpace(st.If) != "" {
		cond := pipelines.StripExpr(st.If)
		cp.checkExpr(cond, loc+".if")
		s.Condition = cond
	}
	if len(st.Env) > 0 {
		s.Env = map[string]string{}
		for k, v := range st.Env {
			s.Env[k] = cp.evalStatic(jc, v, loc+".env."+k)
		}
	}
	if st.ContinueOnError.Set {
		s.ContinueOnError = cp.staticBool(jc, st.ContinueOnError, loc+".continue-on-error", DivStepContinueOnErrorDynamic)
	}
	if st.TimeoutMinutes > 0 {
		s.TimeoutSeconds = minutesToSeconds(st.TimeoutMinutes)
	}
	if st.Run != "" {
		s.Run = cp.evalStatic(jc, st.Run, loc+".run")
		s.Shell = firstNonEmpty(st.Shell, jc.shell)
		switch strings.TrimSpace(s.Shell) {
		case "pwsh", "powershell", "cmd":
			cp.diverge(DivStepUnsupportedShell, loc+".shell", "shell %q is not available on linux executors; the step fails at run time", s.Shell)
		}
		s.WorkingDirectory = cp.evalStatic(jc, firstNonEmpty(st.WorkingDirectory, jc.wd), loc+".working-directory")
		return s
	}
	s.Uses = st.Uses
	if len(st.With) > 0 {
		s.With = map[string]any{}
		for _, kv := range st.With {
			s.With[kv.Key] = cp.evalStatic(jc, kv.Value, loc+".with."+kv.Key)
		}
	}
	cp.compileUses(jc, t, &s, loc)
	return s
}

func withString(s *pipelines.Step, key string) string {
	if s.With == nil {
		return ""
	}
	return pipelines.ToString(s.With[key])
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (cp *compilation) compileUses(jc *jobCtx, t *pipelines.IRTask, s *pipelines.Step, loc string) {
	uses := s.Uses
	switch {
	case strings.HasPrefix(uses, "./"):
		cp.diverge(DivUsesLocalAction, loc, "local action %q is not supported in v0; the step is skipped at run time", uses)
		return
	case strings.HasPrefix(uses, "docker://"):
		cp.diverge(DivUsesDockerAction, loc, "docker action %q is not supported in v0; the step is skipped at run time", uses)
		return
	}
	repo, apath, ref := pipelines.SplitUses(uses)
	if repo == "" || ref == "" {
		cp.refuse(DivUsesMalformed, loc, "cannot parse uses: %q (expected owner/repo[/path]@ref)", uses)
		return
	}
	if _, seen := cp.actions[uses]; !seen {
		ar := pipelines.ActionRef{Ref: uses, Repository: repo, Path: apath}
		var res Resolution
		var err error
		if cp.c.Resolver == nil {
			err = ErrResolverUnavailable
		} else {
			res, err = cp.c.Resolver.Resolve(cp.ctx, repo, ref)
		}
		switch {
		case err == nil:
			ar.SHA, ar.Resolution = res.SHA, res.Kind
		case errors.Is(err, ErrRefNotFound):
			ar.Resolution = pipelines.ResolutionUnresolved
			cp.refuse(DivUsesRefNotFound, loc, "%s: ref %q does not exist in %s; the run is refused", uses, ref, repo)
		default:
			ar.Resolution = pipelines.ResolutionUnresolved
			cp.diverge(DivUsesUnresolved, loc, "%s could not be resolved to a commit (%v); recorded as written", uses, err)
		}
		cp.actions[uses] = ar
	}
	name := repo
	if apath != "" {
		name += "/" + apath
	}
	if _, native := pipelines.NativeStepFor(uses); !native {
		cp.diverge(DivUsesUnsupportedAction, loc, "%s is not implemented natively on Ore v0; the step is skipped at run time and recorded here", uses)
		return
	}
	cp.nativeInputs(jc, t, s, strings.ToLower(name), loc)
}

// nativeInputs validates the inputs of a natively implemented action and
// declares the caches and artifacts it uses on the task.
func (cp *compilation) nativeInputs(jc *jobCtx, t *pipelines.IRTask, s *pipelines.Step, name, loc string) {
	switch name {
	case "actions/checkout":
		if r := withString(s, "repository"); r != "" && !strings.EqualFold(r, cp.opts.Repo.Org+"/"+cp.opts.Repo.Name) {
			cp.diverge(DivUsesInputUnsupported, loc+".with.repository", "checkout of another repository (%s) is not supported; the step fails at run time", r)
		}
		if r := withString(s, "ref"); r != "" {
			cp.diverge(DivUsesInputUnsupported, loc+".with.ref", "checkout ref %q is ignored; the run's commit is checked out", r)
		}
		if sm := withString(s, "submodules"); sm != "" && sm != "false" {
			cp.diverge(DivUsesInputUnsupported, loc+".with.submodules", "submodules are not materialized in v0")
		}
	case "actions/setup-go":
		if withString(s, "cache") != "false" {
			t.Inputs.Caches = append(t.Inputs.Caches, pipelines.CacheRef{Key: "setup-go-${{ runner.os }}-go-" + firstNonEmpty(withString(s, "go-version"), "file") + "-${{ hashFiles('**/go.sum') }}", RestoreKeys: []string{"setup-go-${{ runner.os }}-go-"}, Paths: []string{"~/go/pkg/mod", "~/.cache/go-build"}, Scope: cp.opts.Trust})
		}
	case "actions/setup-node":
		if c := withString(s, "cache"); c != "" {
			t.Inputs.Caches = append(t.Inputs.Caches, pipelines.CacheRef{Key: "setup-node-${{ runner.os }}-" + c + "-${{ hashFiles('" + firstNonEmpty(withString(s, "cache-dependency-path"), "**/package-lock.json', '**/yarn.lock', '**/pnpm-lock.yaml") + "') }}", RestoreKeys: []string{"setup-node-${{ runner.os }}-" + c + "-"}, Paths: []string{nodeCacheDir(c)}, Scope: cp.opts.Trust})
		}
	case "actions/setup-python":
		if c := withString(s, "cache"); c != "" {
			t.Inputs.Caches = append(t.Inputs.Caches, pipelines.CacheRef{Key: "setup-python-${{ runner.os }}-" + c + "-${{ hashFiles('" + firstNonEmpty(withString(s, "cache-dependency-path"), "**/requirements*.txt', '**/pyproject.toml") + "') }}", RestoreKeys: []string{"setup-python-${{ runner.os }}-" + c + "-"}, Paths: []string{"~/.cache/pip"}, Scope: cp.opts.Trust})
		}
	case "actions/cache", "actions/cache/restore", "actions/cache/save":
		key := withString(s, "key")
		if key == "" {
			cp.diverge(DivUsesInputMissing, loc+".with.key", "%s requires `key`; the step fails at run time", s.Uses)
		}
		cp.checkInterpolations(key, loc+".with.key")
		paths := splitLines(withString(s, "path"))
		if len(paths) == 0 {
			cp.diverge(DivUsesInputMissing, loc+".with.path", "%s requires `path`; the step fails at run time", s.Uses)
		}
		for _, p := range paths {
			if _, _, err := pipelines.CachePathBase(p); err != nil {
				cp.diverge(DivCachePathUnsupported, loc+".with.path", "%v; the path is skipped", err)
			}
		}
		if name != "actions/cache/save" {
			t.Inputs.Caches = append(t.Inputs.Caches, pipelines.CacheRef{Key: key, RestoreKeys: splitLines(withString(s, "restore-keys")), Paths: paths, Scope: cp.opts.Trust})
		}
		if name != "actions/cache/restore" {
			t.Outputs.Caches = append(t.Outputs.Caches, pipelines.CacheOut{Key: key, Paths: paths})
		}
	case "actions/upload-artifact":
		aname := firstNonEmpty(withString(s, "name"), "artifact")
		if !pipelines.HasInterpolation(aname) {
			if err := pipelines.ValidArtifactName(aname); err != nil {
				cp.diverge(DivArtifactInvalidName, loc+".with.name", "%v; the upload is refused at run time", err)
			}
		}
		paths := splitLines(withString(s, "path"))
		if len(paths) == 0 {
			cp.diverge(DivUsesInputMissing, loc+".with.path", "upload-artifact requires `path`; the step fails at run time")
		}
		t.Outputs.Artifacts = append(t.Outputs.Artifacts, pipelines.ArtifactOut{Name: aname, Paths: paths})
	case "actions/download-artifact":
		for _, k := range []string{"run-id", "repository", "github-token"} {
			if withString(s, k) != "" {
				cp.diverge(DivUsesInputUnsupported, loc+".with."+k, "download-artifact `%s` is not supported; only artifacts of the current run can be downloaded", k)
			}
		}
		aname := withString(s, "name")
		in := pipelines.ArtifactIn{Name: firstNonEmpty(aname, "*"), Path: withString(s, "path")}
		// the producing task, when an earlier job of this workflow uploads the name
		for _, prev := range cp.compiled {
			if prev.Outputs == nil {
				continue
			}
			for _, a := range prev.Outputs.Artifacts {
				if a.Name == aname {
					in.FromTask = prev.ID
				}
			}
		}
		t.Inputs.ArtifactsIn = append(t.Inputs.ArtifactsIn, in)
	}
}

func nodeCacheDir(manager string) string {
	switch manager {
	case "yarn":
		return "~/.cache/yarn"
	case "pnpm":
		return "~/.local/share/pnpm/store"
	}
	return "~/.npm"
}

// SortedDivergenceCodes lists the distinct codes in a divergence list.
func SortedDivergenceCodes(divs []pipelines.Divergence) []string {
	set := map[string]bool{}
	for _, d := range divs {
		set[d.Code] = true
	}
	return sortedKeys(set)
}
