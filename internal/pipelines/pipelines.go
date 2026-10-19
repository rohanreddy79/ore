// SPDX-License-Identifier: AGPL-3.0-only

// Package pipelines runs a repository's verification tasks on a commit. v0
// reads .ore/pipeline.yaml (or infers a default), materializes the tree from
// the bare repository, and runs each task in a container (or on the host when
// configured for tests). Every run and task is recorded on the ledger.
package pipelines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// Spec is the v0 pipeline file: .ore/pipeline.yaml.
type Spec struct {
	Version int    `yaml:"version" json:"version"`
	Tasks   []Task `yaml:"tasks" json:"tasks"`
}

// Task is one verification step.
type Task struct {
	ID    string            `yaml:"id" json:"id"`
	Image string            `yaml:"image" json:"image"`
	Run   string            `yaml:"run" json:"run"`
	Env   map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}

// TaskResult is recorded per task.
type TaskResult struct {
	ID         string `json:"id"`
	Image      string `json:"image"`
	Status     string `json:"status"` // succeeded | failed | skipped
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
}

// SpecPath is where the pipeline file lives in the repository.
const SpecPath = ".ore/pipeline.yaml"

// LoadSpec reads the pipeline file from a materialized tree, or infers one.
func LoadSpec(dir string) (Spec, bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, SpecPath))
	if err == nil {
		var s Spec
		if err := yaml.Unmarshal(b, &s); err != nil {
			return Spec{}, true, fmt.Errorf("parse %s: %w", SpecPath, err)
		}
		if len(s.Tasks) == 0 {
			return Spec{}, true, fmt.Errorf("%s declares no tasks", SpecPath)
		}
		for i := range s.Tasks {
			if s.Tasks[i].ID == "" {
				s.Tasks[i].ID = fmt.Sprintf("task-%d", i+1)
			}
			if s.Tasks[i].Run == "" {
				return Spec{}, true, fmt.Errorf("task %s has no run command", s.Tasks[i].ID)
			}
		}
		return s, true, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Spec{}, false, err
	}
	return Infer(dir), false, nil
}

// Infer builds a default spec from the repository's shape.
func Infer(dir string) Spec {
	exists := func(n string) bool { _, err := os.Stat(filepath.Join(dir, n)); return err == nil }
	switch {
	case exists("go.mod"):
		return Spec{Version: 0, Tasks: []Task{{ID: "test", Image: "golang:1.25", Run: "go vet ./... && go test ./..."}}}
	case exists("package.json"):
		return Spec{Version: 0, Tasks: []Task{{ID: "test", Image: "node:22", Run: "npm ci && npm test"}}}
	case exists("pyproject.toml") || exists("requirements.txt"):
		return Spec{Version: 0, Tasks: []Task{{ID: "test", Image: "python:3.13", Run: "pip install -q -r requirements.txt 2>/dev/null || true; python -m pytest -q"}}}
	case exists("Cargo.toml"):
		return Spec{Version: 0, Tasks: []Task{{ID: "test", Image: "rust:1", Run: "cargo test"}}}
	}
	return Spec{Version: 0}
}

// Runner executes pipeline runs.
type Runner struct {
	DB       *ledger.DB
	ReposDir string
	LogsDir  string
	// Executor is "docker" (default) or "host" (tests and trusted solo use only).
	Executor string
	// Timeout per task.
	Timeout time.Duration
}

// Run records a run and executes it, returning the final record.
func (rn *Runner) Run(ctx context.Context, repo ledger.Repo, commitOID string, changeID *string, actor ledger.Actor) (ledger.PipelineRun, error) {
	run := ledger.PipelineRun{CommitOID: commitOID, ChangeID: changeID}
	if err := rn.DB.InsertPipelineRun(ctx, repo, &run, actor); err != nil {
		return run, err
	}
	if err := rn.Execute(ctx, repo, &run, actor); err != nil {
		return run, err
	}
	return run, nil
}

// Execute runs an already-recorded (queued) run to completion.
func (rn *Runner) Execute(ctx context.Context, repo ledger.Repo, run *ledger.PipelineRun, actor ledger.Actor) error {
	if err := os.MkdirAll(rn.LogsDir, 0o755); err != nil {
		return err
	}
	run.LogPath = filepath.Join(rn.LogsDir, run.ID+".log")
	logf, err := os.Create(run.LogPath)
	if err != nil {
		return err
	}
	defer logf.Close()
	logw := &prefixWriter{w: logf}

	if err := rn.DB.UpdatePipelineRun(ctx, repo, run, "running", "", actor); err != nil {
		return err
	}
	status, results := rn.execute(ctx, repo, run.CommitOID, logw)
	tasks, _ := json.Marshal(results)
	return rn.DB.UpdatePipelineRun(ctx, repo, run, status, string(tasks), actor)
}

func (rn *Runner) execute(ctx context.Context, repo ledger.Repo, commitOID string, logw *prefixWriter) (string, []TaskResult) {
	work, err := os.MkdirTemp("", "ore-run-*")
	if err != nil {
		logw.line("ore", "workspace: "+err.Error())
		return "error", nil
	}
	defer os.RemoveAll(work)
	repoPath := filepath.Join(rn.ReposDir, repo.Org, repo.Name+".git")
	if err := gitx.Archive(ctx, repoPath, commitOID, work); err != nil {
		logw.line("ore", "materialize "+commitOID+": "+err.Error())
		return "error", nil
	}
	spec, explicit, err := LoadSpec(work)
	if err != nil {
		logw.line("ore", err.Error())
		return "error", nil
	}
	if len(spec.Tasks) == 0 {
		logw.line("ore", "no pipeline defined and none inferred; nothing to verify")
		return "succeeded", nil
	}
	if explicit {
		logw.line("ore", "pipeline: "+SpecPath)
	} else {
		logw.line("ore", "pipeline: inferred from repository shape")
	}
	var results []TaskResult
	status := "succeeded"
	for _, t := range spec.Tasks {
		if status != "succeeded" {
			results = append(results, TaskResult{ID: t.ID, Image: t.Image, Status: "skipped"})
			continue
		}
		start := time.Now()
		logw.line(t.ID, "start: "+t.Run)
		code, err := rn.runTask(ctx, work, t, logw)
		res := TaskResult{ID: t.ID, Image: t.Image, ExitCode: code, DurationMS: time.Since(start).Milliseconds(), Status: "succeeded"}
		if err != nil || code != 0 {
			res.Status = "failed"
			status = "failed"
			if err != nil {
				logw.line(t.ID, "error: "+err.Error())
			}
		}
		logw.line(t.ID, fmt.Sprintf("finished: %s (exit %d, %d ms)", res.Status, code, res.DurationMS))
		results = append(results, res)
	}
	return status, results
}

func (rn *Runner) runTask(ctx context.Context, work string, t Task, logw *prefixWriter) (int, error) {
	timeout := rn.Timeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var cmd *exec.Cmd
	switch rn.Executor {
	case "host":
		cmd = exec.CommandContext(ctx, "sh", "-c", t.Run)
		cmd.Dir = work
		cmd.Env = os.Environ()
		for k, v := range t.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	default:
		image := t.Image
		if image == "" {
			image = "alpine:3"
		}
		args := []string{"run", "--rm", "--network", "bridge", "-v", work + ":/work", "-w", "/work"}
		for k, v := range t.Env {
			args = append(args, "-e", k+"="+v)
		}
		args = append(args, image, "sh", "-c", t.Run)
		cmd = exec.CommandContext(ctx, "docker", args...)
	}
	w := logw.task(t.ID)
	cmd.Stdout, cmd.Stderr = w, w
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

// prefixWriter writes "[time] [task] line" records.
type prefixWriter struct{ w io.Writer }

func (p *prefixWriter) line(task, s string) {
	fmt.Fprintf(p.w, "%s [%s] %s\n", time.Now().UTC().Format("15:04:05.000"), task, s)
}

func (p *prefixWriter) task(id string) io.Writer { return &taskWriter{p: p, id: id} }

type taskWriter struct {
	p   *prefixWriter
	id  string
	buf []byte
}

func (t *taskWriter) Write(b []byte) (int, error) {
	t.buf = append(t.buf, b...)
	for {
		i := strings.IndexByte(string(t.buf), '\n')
		if i < 0 {
			break
		}
		t.p.line(t.id, string(t.buf[:i]))
		t.buf = t.buf[i+1:]
	}
	return len(b), nil
}
