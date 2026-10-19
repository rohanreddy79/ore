// SPDX-License-Identifier: AGPL-3.0-only

// Package agent runs the loop (docs/12): signal → plan → change → verify →
// land → deploy → observe, with every step recorded on the ledger. The agent
// itself is an adapter: any command that edits files in a workspace. Ore
// commits with provenance, opens the change, verifies it, and lands or
// deploys it according to the repository's autonomy level (ADR 0015).
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
	"github.com/orevcs/ore/internal/review"
)

// DefaultAdapter runs Claude Code non-interactively on the prompt in $ORE_PROMPT.
const DefaultAdapter = `claude -p "$ORE_PROMPT" --permission-mode acceptEdits`

// Loop runs agent passes.
type Loop struct {
	DB       *ledger.DB
	ReposDir string
	LogsDir  string
	Runner   *pipelines.Runner
	Deployer *delivery.Deployer
	Review   *review.Service
	// AdapterCmd is run with sh -c in the workspace; default DefaultAdapter.
	AdapterCmd string
	// AdapterName labels provenance (e.g. "claude-code", "demo-script").
	AdapterName string
	Timeout     time.Duration

	// Governor, when set, governs the loop: admission and per-step gates
	// (budgets, the kill switch), per-repository roles, and the adversarial
	// reviewer before landing. nil is the ungoverned loop. See governor.go.
	Governor Governor
}

// Request describes a loop pass.
type Request struct {
	Issue  *ledger.Issue
	Prompt string // used when Issue is nil, or appended to the issue
}

// Start records the run and executes it in the background.
func (l *Loop) Start(ctx context.Context, repo ledger.Repo, req Request, actor ledger.Actor) (ledger.AgentRun, error) {
	run, err := l.begin(ctx, repo, req, actor)
	if err != nil {
		return run, err
	}
	go l.execute(context.Background(), repo, req, &run, actor)
	return run, nil
}

// RunSync executes a pass and waits for it (tests and the CLI).
func (l *Loop) RunSync(ctx context.Context, repo ledger.Repo, req Request, actor ledger.Actor) (ledger.AgentRun, error) {
	run, err := l.begin(ctx, repo, req, actor)
	if err != nil {
		return run, err
	}
	l.execute(ctx, repo, req, &run, actor)
	return l.DB.AgentRun(ctx, run.ID)
}

func (l *Loop) begin(ctx context.Context, repo ledger.Repo, req Request, actor ledger.Actor) (ledger.AgentRun, error) {
	name := l.AdapterName
	if name == "" {
		name = "claude-code"
	}
	if spec, ok := l.roleSpec(ctx, repo, "implementer"); ok && spec.AdapterName != "" {
		name = spec.AdapterName
	}
	if l.Governor != nil {
		if err := l.Governor.Admit(ctx, repo, ledger.Actor{Kind: "agent", ID: name}); err != nil {
			return ledger.AgentRun{Adapter: name}, fmt.Errorf("%w: %w", ErrRefused, err)
		}
	}
	run := ledger.AgentRun{Adapter: name}
	if req.Issue != nil {
		run.IssueID = &req.Issue.ID
	}
	if err := os.MkdirAll(l.LogsDir, 0o755); err != nil {
		return run, err
	}
	if err := l.DB.InsertAgentRun(ctx, repo, &run, actor); err != nil {
		return run, err
	}
	run.LogPath = filepath.Join(l.LogsDir, run.ID+".log")
	return run, nil
}

type stepLog struct {
	f *os.File
}

func (s *stepLog) printf(format string, a ...any) {
	fmt.Fprintf(s.f, time.Now().UTC().Format("15:04:05.000")+" "+format+"\n", a...)
}

func (l *Loop) step(ctx context.Context, repo ledger.Repo, run *ledger.AgentRun, step string, lg *stepLog, actor ledger.Actor) {
	run.Step = step
	lg.printf("[ore] step: %s", step)
	_ = l.DB.UpdateAgentRun(ctx, repo, run, actor)
}

func (l *Loop) finish(ctx context.Context, repo ledger.Repo, run *ledger.AgentRun, status string, errText string, lg *stepLog, actor ledger.Actor) {
	run.Status, run.Error = status, errText
	if errText != "" {
		lg.printf("[ore] %s: %s", status, errText)
	} else {
		lg.printf("[ore] %s", status)
	}
	_ = l.DB.UpdateAgentRun(ctx, repo, run, actor)
}

func (l *Loop) execute(ctx context.Context, repo ledger.Repo, req Request, run *ledger.AgentRun, actor ledger.Actor) {
	f, err := os.Create(run.LogPath)
	if err != nil {
		l.finish(ctx, repo, run, "failed", "log: "+err.Error(), &stepLog{f: os.Stderr}, actor)
		return
	}
	defer f.Close()
	lg := &stepLog{f: f}
	agentActor := ledger.Actor{Kind: "agent", ID: run.Adapter}
	rp := filepath.Join(l.ReposDir, repo.Org, repo.Name+".git")

	// 1. plan: gather context for the adapter
	l.step(ctx, repo, run, "plan", lg, actor)
	if !l.gate(ctx, repo, run, agentActor, "plan", lg, actor) {
		return
	}
	prompt, verifyCmd := l.compose(ctx, rp, repo, req)
	lg.printf("[plan] prompt:\n%s", indent(prompt))

	// 2. change: workspace, adapter, commit, push
	l.step(ctx, repo, run, "change", lg, actor)
	if !l.gate(ctx, repo, run, agentActor, "change", lg, actor) {
		return
	}
	work, err := os.MkdirTemp("", "ore-agent-*")
	if err != nil {
		l.finish(ctx, repo, run, "failed", err.Error(), lg, actor)
		return
	}
	defer os.RemoveAll(work)
	if _, err := gitx.Run(ctx, "", nil, "clone", "--quiet", "--branch", repo.DefaultBranch, rp, work); err != nil {
		l.finish(ctx, repo, run, "failed", "clone: "+err.Error(), lg, actor)
		return
	}
	short := run.ID[len(run.ID)-6:]
	branch := "ore/agent/" + short
	if req.Issue != nil {
		branch = fmt.Sprintf("ore/agent/issue-%d-%s", req.Issue.Number, short)
	}
	run.Branch = branch
	if _, err := gitx.Run(ctx, work, nil, "switch", "-c", branch); err != nil {
		l.finish(ctx, repo, run, "failed", err.Error(), lg, actor)
		return
	}
	if err := l.runAdapter(ctx, work, prompt, verifyCmd, repo, branch, req, lg); err != nil {
		l.finish(ctx, repo, run, "failed", "adapter: "+err.Error(), lg, actor)
		return
	}
	if _, err := gitx.Run(ctx, work, nil, "add", "-A"); err != nil {
		l.finish(ctx, repo, run, "failed", err.Error(), lg, actor)
		return
	}
	status, _ := gitx.Run(ctx, work, nil, "status", "--porcelain")
	if strings.TrimSpace(status) == "" {
		l.finish(ctx, repo, run, "failed", "the agent made no changes", lg, actor)
		return
	}
	title := "Agent change"
	if req.Issue != nil {
		title = req.Issue.Title
	} else if req.Prompt != "" {
		title = firstLine(req.Prompt)
	}
	prov := map[string]string{"author_kind": "agent", "agent_id": run.Adapter, "session_id": run.ID}
	provJSON, _ := json.Marshal(prov)
	msg := title + "\n\n"
	if req.Issue != nil {
		msg += fmt.Sprintf("Fixes #%d\n\n", req.Issue.Number)
	}
	msg += "Ore-Agent: " + run.Adapter + "\nOre-Session: " + run.ID + "\n"
	env := []string{"GIT_AUTHOR_NAME=Ore Agent", "GIT_AUTHOR_EMAIL=agent@ore.invalid", "GIT_COMMITTER_NAME=Ore Agent", "GIT_COMMITTER_EMAIL=agent@ore.invalid"}
	if _, err := gitx.RunStdin(ctx, work, env, []byte(msg), "commit", "--quiet", "-F", "-"); err != nil {
		l.finish(ctx, repo, run, "failed", err.Error(), lg, actor)
		return
	}
	before, _ := gitx.Refs(ctx, rp)
	if _, err := gitx.Run(ctx, work, nil, "push", "--quiet", "origin", "HEAD:refs/heads/"+branch); err != nil {
		l.finish(ctx, repo, run, "failed", "push: "+err.Error(), lg, actor)
		return
	}
	after, _ := gitx.Refs(ctx, rp)
	for _, u := range gitx.DiffRefs(before, after) {
		_ = l.DB.RecordRefUpdate(ctx, repo, u.Ref, u.Old, u.New, "agent", agentActor)
	}
	lg.printf("[change] pushed %s", branch)

	// 3. verify: open the change, run the pipeline, deploy a preview
	l.step(ctx, repo, run, "verify", lg, actor)
	if !l.gate(ctx, repo, run, agentActor, "verify", lg, actor) {
		return
	}
	ch, err := l.Review.Open(ctx, repo, review.OpenRequest{SourceRef: branch, Title: title, Body: msg, Provenance: string(provJSON)}, agentActor)
	if err != nil {
		l.finish(ctx, repo, run, "failed", "open change: "+err.Error(), lg, actor)
		return
	}
	run.ChangeID = &ch.ID
	lg.printf("[verify] change #%d (%s) base %s head %s", ch.Number, ch.ChangeID, ch.BaseOID[:8], ch.HeadOID[:8])
	prun, err := l.Runner.Run(ctx, repo, ch.HeadOID, &ch.ID, agentActor)
	if err != nil {
		l.finish(ctx, repo, run, "failed", "pipeline: "+err.Error(), lg, actor)
		return
	}
	run.PipelineRunID = &prun.ID
	lg.printf("[verify] pipeline %s: %s", prun.ID, prun.Status)
	if prun.Status != "succeeded" {
		_ = l.DB.UpdateAgentRun(ctx, repo, run, actor)
		l.finish(ctx, repo, run, "failed", "verification failed: pipeline "+prun.Status+" (see run log)", lg, actor)
		return
	}
	if repo.AutonomyLevel >= 2 && l.hasDockerfile(ctx, rp, ch.HeadOID) {
		if dep, err := l.Deployer.Deploy(ctx, repo, ch.HeadOID, "preview", agentActor, nil); err != nil {
			lg.printf("[verify] preview deployment failed: %v", err)
		} else {
			run.DeploymentID = &dep.ID
			lg.printf("[verify] preview at %s", dep.URL)
		}
	}

	// 4. land
	if repo.AutonomyLevel < 3 {
		l.step(ctx, repo, run, "awaiting-human", lg, actor)
		l.finish(ctx, repo, run, "blocked", fmt.Sprintf("autonomy level %d: a human must land change #%d", repo.AutonomyLevel, ch.Number), lg, actor)
		return
	}
	// 3b. adversarial review (L3+, when the repository has a reviewer role)
	if l.Governor != nil {
		l.step(ctx, repo, run, "review", lg, actor)
		if !l.gate(ctx, repo, run, agentActor, "review", lg, actor) {
			return
		}
		var ok bool
		if ch, prun, ok = l.adversarialReview(ctx, repo, run, ch, prun, work, rp, branch, prompt, verifyCmd, req, lg, actor, agentActor); !ok {
			return
		}
		run.ChangeID = &ch.ID
	}
	l.step(ctx, repo, run, "land", lg, actor)
	if !l.gate(ctx, repo, run, agentActor, "land", lg, actor) {
		return
	}
	landing, err := l.land(ctx, repo, ch, &prun, agentActor)
	if err != nil {
		status, msg := landOutcome(err)
		l.finish(ctx, repo, run, status, msg, lg, actor)
		return
	}
	mergeOID := *landing.MergeOID
	lg.printf("[land] merged into %s as %s (strategy %s, landing %s)", ch.TargetRef, mergeOID[:8], landing.Strategy, landing.ID)

	// 5. deploy to production at L4+
	if repo.AutonomyLevel >= 4 && l.hasDockerfile(ctx, rp, mergeOID) {
		l.step(ctx, repo, run, "deploy", lg, actor)
		if !l.gate(ctx, repo, run, agentActor, "deploy", lg, actor) {
			return
		}
		dep, err := l.Deployer.Deploy(ctx, repo, mergeOID, "production", agentActor, nil)
		if status, msg, done := deployOutcome(dep, err); done {
			if dep.ID != "" {
				run.DeploymentID = &dep.ID
			}
			l.finish(ctx, repo, run, status, msg, lg, actor)
			return
		}
		run.DeploymentID = &dep.ID
		lg.printf("[deploy] production at %s", dep.URL)
	}

	// 6. observe: close the signal with evidence
	l.step(ctx, repo, run, "observe", lg, actor)
	if !l.gate(ctx, repo, run, agentActor, "observe", lg, actor) {
		return
	}
	if req.Issue != nil {
		_ = l.DB.CloseIssue(ctx, repo, *req.Issue, fmt.Sprintf("landed by change #%d (%s) in agent run %s", ch.Number, mergeOID[:8], run.ID), agentActor)
	}
	l.finish(ctx, repo, run, "completed", "", lg, actor)
}

func (l *Loop) hasDockerfile(ctx context.Context, rp, rev string) bool {
	files, err := gitx.ListTree(ctx, rp, rev)
	if err != nil {
		return false
	}
	for _, f := range files {
		if f == "Dockerfile" {
			return true
		}
	}
	return false
}

func (l *Loop) compose(ctx context.Context, rp string, repo ledger.Repo, req Request) (prompt, verifyCmd string) {
	var b strings.Builder
	b.WriteString("You are working in a git checkout of " + repo.Org + "/" + repo.Name + " on a branch Ore created for you.\n")
	if req.Issue != nil {
		fmt.Fprintf(&b, "Resolve issue #%d: %s\n\n%s\n\n", req.Issue.Number, req.Issue.Title, strings.TrimSpace(req.Issue.Body))
	}
	if req.Prompt != "" {
		b.WriteString(strings.TrimSpace(req.Prompt) + "\n\n")
	}
	files, _ := gitx.ListTree(ctx, rp, "refs/heads/"+repo.DefaultBranch)
	if len(files) > 0 {
		b.WriteString("Files: " + strings.Join(limit(files, 60), ", ") + "\n")
	}
	if raw, err := gitx.ReadFile(ctx, rp, "refs/heads/"+repo.DefaultBranch, pipelines.SpecPath); err == nil {
		var s pipelines.Spec
		if yaml.Unmarshal(raw, &s) == nil && len(s.Tasks) > 0 {
			verifyCmd = s.Tasks[0].Run
		}
	}
	if verifyCmd == "" {
		for _, f := range files {
			if f == "go.mod" {
				verifyCmd = "go test ./..."
			}
		}
	}
	if verifyCmd != "" {
		b.WriteString("Verify with: " + verifyCmd + "\n")
	}
	b.WriteString("Make the smallest correct change. Do not commit, push, or create branches: Ore commits for you with provenance, opens the change, verifies it in a container, and lands it under the repository's policy.\n")
	if spec, ok := l.roleSpec(ctx, repo, "implementer"); ok && strings.TrimSpace(spec.PromptProfile) != "" {
		b.WriteString("\n" + strings.TrimSpace(spec.PromptProfile) + "\n")
	}
	return b.String(), verifyCmd
}

func (l *Loop) runAdapter(ctx context.Context, work, prompt, verifyCmd string, repo ledger.Repo, branch string, req Request, lg *stepLog, extraEnv ...string) error {
	cmdline := l.AdapterCmd
	if spec, ok := l.roleSpec(ctx, repo, "implementer"); ok && spec.AdapterCmd != "" {
		cmdline = spec.AdapterCmd
	}
	if cmdline == "" {
		cmdline = DefaultAdapter
	}
	timeout := l.Timeout
	if timeout == 0 {
		timeout = 20 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdline)
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"ORE_PROMPT="+prompt, "ORE_REPO="+repo.Org+"/"+repo.Name, "ORE_BRANCH="+branch, "ORE_VERIFY="+verifyCmd,
		"GIT_TERMINAL_PROMPT=0", "CI=1")
	if req.Issue != nil {
		cmd.Env = append(cmd.Env, fmt.Sprintf("ORE_ISSUE_NUMBER=%d", req.Issue.Number), "ORE_ISSUE_TITLE="+req.Issue.Title, "ORE_ISSUE_BODY="+req.Issue.Body)
	}
	cmd.Env = append(cmd.Env, extraEnv...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	lg.printf("[change] adapter: %s", cmdline)
	err := cmd.Run()
	if out.Len() > 0 {
		lg.printf("[adapter]\n%s", indent(out.String()))
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("timed out after %s", timeout)
	}
	return err
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = "    " + lines[i]
	}
	return strings.Join(lines, "\n")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 72 {
		s = s[:72]
	}
	return s
}

func limit(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
