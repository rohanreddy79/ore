// SPDX-License-Identifier: AGPL-3.0-only

package pipelines

import (
	"context"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/orevcs/ore/internal/ledger"
)

// Frontend compiles authoring sources in a materialized tree (for example
// .github/workflows/*.yml) into IR pipelines. The Runner consults it when a
// tree has no .ore/pipeline.yaml; the Actions compiler in
// internal/pipelines/actions registers itself as the Runner's Frontend.
type Frontend interface {
	// Compile returns one Plan per source found in req.Work, or an empty
	// slice when the tree has nothing this front-end understands.
	Compile(ctx context.Context, req CompileRequest) ([]Plan, error)
}

// CompileRequest describes the run a front-end compiles for.
type CompileRequest struct {
	Work     string // materialized tree
	Repo     ledger.Repo
	Commit   string
	Executor string // docker | host
	Trust    string // trust scope of the run (TrustDefault, ...)
	Event    string // push | pull_request
}

// Plan is one compiled source. Refused is set when the planner will not run
// it (a needs cycle, an unknown job in needs, a uses: reference that does
// not exist); IR is still filled where parsing succeeded, with its
// divergences, so the reason is stored on the run.
type Plan struct {
	Source  string `json:"source"`
	IR      *IR    `json:"ir,omitempty"`
	Refused string `json:"refused,omitempty"`
}

// Stores are the native services IR tasks use.
type Stores struct {
	Cache     *CacheStore
	Artifacts *ArtifactStore
	Secrets   *SecretStore
}

// NewStores creates the stores under dataDir.
func NewStores(db *ledger.DB, dataDir string) *Stores {
	return &Stores{
		Cache:     &CacheStore{Dir: filepath.Join(dataDir, "cache")},
		Artifacts: &ArtifactStore{Dir: filepath.Join(dataDir, "artifacts"), DB: db},
		Secrets:   &SecretStore{DB: db, KeyPath: filepath.Join(dataDir, "secrets.key")},
	}
}

// StepContext is what a natively implemented `uses:` step sees and may do.
type StepContext struct {
	Ctx       context.Context
	Repo      ledger.Repo
	RunID     string
	TaskID    string
	Scope     string            // trust scope of the run
	Executor  string            // host | docker
	Workspace string            // host path of the workspace
	With      map[string]string // interpolated inputs, keys as written
	Env       map[string]string // the step's environment (task-side paths)
	Log       io.Writer         // masked task log
	Outputs   map[string]string // outputs to set (steps.<id>.outputs.*)
	Stores    *Stores
	// Exec runs a shell snippet in the task's environment (the container or
	// the host) with the step's env, streaming stdin and stdout.
	Exec func(ctx context.Context, script string, stdin io.Reader, stdout io.Writer) (int, error)
	// AddMask hides a value in the task log from now on.
	AddMask func(string)
	// Post registers a hook to run after the last step (post steps run in
	// reverse order; only when the task succeeded unless always is set).
	Post func(name string, always bool, fn func(ctx context.Context) error)
	// Warn writes a warning annotation and log line.
	Warn func(format string, a ...any)
	// TaskPath maps a host path under the workspace or the runner dir to
	// the path the task process sees (identical on the host executor).
	TaskPath func(hostPath string) string
	// HostPath is the inverse of TaskPath for paths under the workspace.
	HostPath func(taskPath string) (string, bool)
}

// NativeStep implements a `uses:` action natively; it returns an error to
// fail the step.
type NativeStep func(sc *StepContext) error

var (
	nativeMu    sync.RWMutex
	nativeSteps = map[string]NativeStep{}
)

// RegisterNativeStep makes fn the implementation of uses: <name>@<ref>,
// where name is "owner/repo" or "owner/repo/path".
func RegisterNativeStep(name string, fn NativeStep) {
	nativeMu.Lock()
	defer nativeMu.Unlock()
	nativeSteps[strings.ToLower(name)] = fn
}

// NativeStepFor finds the implementation for a uses: reference, trying
// owner/repo/path then owner/repo.
func NativeStepFor(uses string) (NativeStep, bool) {
	repo, path, _ := SplitUses(uses)
	if repo == "" {
		return nil, false
	}
	nativeMu.RLock()
	defer nativeMu.RUnlock()
	if path != "" {
		if fn, ok := nativeSteps[strings.ToLower(repo+"/"+path)]; ok {
			return fn, true
		}
	}
	fn, ok := nativeSteps[strings.ToLower(repo)]
	return fn, ok
}

// NativeStepNames lists the registered native actions (sorted).
func NativeStepNames() []string {
	nativeMu.RLock()
	defer nativeMu.RUnlock()
	out := make([]string, 0, len(nativeSteps))
	for n := range nativeSteps {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// SplitUses parses owner/repo[/path]@ref. Local (./x) and docker://
// references return an empty repo.
func SplitUses(uses string) (repo, path, ref string) {
	if strings.HasPrefix(uses, "./") || strings.HasPrefix(uses, "docker://") {
		return "", "", ""
	}
	spec, ref, _ := strings.Cut(uses, "@")
	parts := strings.SplitN(spec, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", ref
	}
	repo = parts[0] + "/" + parts[1]
	if len(parts) == 3 {
		path = parts[2]
	}
	return repo, path, ref
}

// ---- per-run context carried through Execute ----

type ctxKey int

const (
	runKey ctxKey = iota + 1
	trustKey
)

type runInfo struct {
	run   *ledger.PipelineRun
	actor ledger.Actor
}

// WithTrust overrides the trust scope the run would otherwise derive from
// the commit's position (default branch or not); tests use it to run as a
// fork.
func WithTrust(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, trustKey, scope)
}

func trustFromContext(ctx context.Context) string {
	s, _ := ctx.Value(trustKey).(string)
	return s
}

func withRun(ctx context.Context, run *ledger.PipelineRun, actor ledger.Actor) context.Context {
	return context.WithValue(ctx, runKey, &runInfo{run: run, actor: actor})
}

func runFromContext(ctx context.Context) *runInfo {
	ri, _ := ctx.Value(runKey).(*runInfo)
	return ri
}
