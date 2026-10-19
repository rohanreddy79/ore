// SPDX-License-Identifier: AGPL-3.0-only

package fleet

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
	"strings"
	"time"

	"github.com/orevcs/ore/internal/agent"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/review"
)

// BlockConfidence is the confidence at which a critical or high finding
// blocks landing (docs/12 §3.5).
const BlockConfidence = 0.7

// ScriptReviewer is a deterministic stand-in for an AI reviewer, for tests
// and demos: an added line containing FIXME is a high-severity finding with
// confidence 0.9; otherwise one informational finding is written. Use it as a
// role's adapter_cmd through a file: `sh /path/to/reviewer.sh`.
const ScriptReviewer = `#!/bin/sh
# Ore script reviewer: deterministic stand-in for an AI reviewer.
set -e
path=$(grep -m1 '^+++ b/' "$ORE_DIFF_FILE" | sed 's#^+++ b/##')
[ -n "$path" ] || path=.
if grep -q '^+.*FIXME' "$ORE_DIFF_FILE"; then
  printf '[{"severity":"high","confidence":0.9,"category":"correctness","path":"%s","range":{"start_line":1,"end_line":1},"message":"FIXME left in the change","fingerprint":"fixme-%s"}]' "$path" "$path" > "$ORE_FINDINGS_FILE"
else
  printf '[{"severity":"info","confidence":1,"category":"review","path":"%s","message":"no blocking findings"}]' "$path" > "$ORE_FINDINGS_FILE"
fi
`

// Review runs the repository's reviewer role on a verified change: the
// adapter gets the diff, the plan and a path to write findings in the
// spec/safe-outputs Finding shape; the findings are posted as a review in the
// agent lane through review v1; a critical or high finding with confidence
// >= 0.7 blocks landing. Malformed findings fail the review and block landing.
func (s *Service) Review(ctx context.Context, repo ledger.Repo, run *ledger.AgentRun, ch ledger.Change, plan string, log io.Writer) (agent.ReviewOutcome, error) {
	role, err := s.role(ctx, repo, RoleReviewer)
	if err != nil {
		return agent.ReviewOutcome{}, err
	}
	if !role.Enabled || strings.TrimSpace(role.AdapterCmd) == "" {
		return agent.ReviewOutcome{Ran: false}, nil
	}
	if log == nil {
		log = io.Discard
	}
	out := agent.ReviewOutcome{Ran: true, Findings: []review.Finding{}}
	reviewer := ledger.Actor{Kind: "agent", ID: orDefault(role.AdapterName, RoleReviewer)}
	runID := ""
	if run != nil {
		runID = run.ID
	}
	failed := func(why string) (agent.ReviewOutcome, error) {
		out.Failed, out.Blocked, out.Summary = true, true, why
		fmt.Fprintf(log, "%s [review] failed: %s\n", time.Now().UTC().Format("15:04:05.000"), why)
		return out, s.emit(ctx, repo.OrgID, "io.ore.agent.review.failed", changeSubject(repo, ch), reviewer, map[string]any{
			"run_id": runID, "change_id": ch.ChangeID, "number": ch.Number, "head_oid": ch.HeadOID, "error": why,
		})
	}
	diff, err := s.Reviews.Diff(ctx, repo, ch)
	if err != nil {
		return failed("diff: " + err.Error())
	}
	dir, err := os.MkdirTemp("", "ore-review-*")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(dir)
	diffPath, findingsPath, work := filepath.Join(dir, "change.diff"), filepath.Join(dir, "findings.json"), filepath.Join(dir, "work")
	if err := os.WriteFile(diffPath, []byte(diff), 0o644); err != nil {
		return out, err
	}
	rp := filepath.Join(s.ReposDir, repo.Org, repo.Name+".git")
	branch := strings.TrimPrefix(ch.SourceRef, "refs/heads/")
	if _, err := gitx.Run(ctx, "", nil, "clone", "--quiet", "--branch", branch, rp, work); err != nil {
		return failed("checkout for review: " + err.Error())
	}
	timeout := s.ReviewTimeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "sh", "-c", role.AdapterCmd)
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"ORE_PROMPT="+reviewPrompt(repo, ch, plan, role),
		"ORE_PLAN="+plan,
		"ORE_DIFF_FILE="+diffPath,
		"ORE_FINDINGS_FILE="+findingsPath,
		"ORE_REPO="+repo.Org+"/"+repo.Name,
		fmt.Sprintf("ORE_CHANGE_NUMBER=%d", ch.Number),
		"ORE_CHANGE_ID="+ch.ChangeID,
		"ORE_BRANCH="+branch,
		"ORE_ROLE="+RoleReviewer,
		"ORE_RUN_ID="+runID,
		"GIT_TERMINAL_PROMPT=0", "CI=1")
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	fmt.Fprintf(log, "%s [review] reviewer %s: %s\n", time.Now().UTC().Format("15:04:05.000"), reviewer.ID, role.AdapterCmd)
	runErr := cmd.Run()
	if buf.Len() > 0 {
		fmt.Fprintf(log, "%s [reviewer]\n%s\n", time.Now().UTC().Format("15:04:05.000"), indent(buf.String()))
	}
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return failed(fmt.Sprintf("reviewer timed out after %s", timeout))
	}
	if runErr != nil {
		return failed("reviewer adapter: " + runErr.Error())
	}
	findings, err := parseFindings(findingsPath)
	if err != nil {
		return failed("malformed findings: " + err.Error())
	}
	var blocking []review.Finding
	for _, f := range findings {
		if (f.Severity == "critical" || f.Severity == "high") && f.Confidence >= BlockConfidence {
			blocking = append(blocking, f)
		}
	}
	verdict := "approve"
	if len(blocking) > 0 {
		verdict = "request_changes"
	}
	body := fmt.Sprintf("Adversarial review by %s", reviewer.ID)
	if runID != "" {
		body += " in agent run " + runID
	}
	rv, err := s.Reviews.SubmitReview(ctx, repo, ch, review.ReviewRequest{Verdict: verdict, Findings: findings, Body: body, Lane: review.LaneAgent}, reviewer)
	if err != nil {
		return out, fmt.Errorf("post review: %w", err)
	}
	out.ReviewID, out.Findings, out.Blocked = rv.ID, findings, len(blocking) > 0
	out.Summary = summarize(findings, blocking)
	fmt.Fprintf(log, "%s [review] %s: %d finding(s), %d blocking; review %s (lane %s, patch set %d)\n", time.Now().UTC().Format("15:04:05.000"), verdict, len(findings), len(blocking), rv.ID, rv.Lane, rv.PatchSet)
	return out, s.emit(ctx, repo.OrgID, "io.ore.agent.review.completed", changeSubject(repo, ch), reviewer, map[string]any{
		"run_id": runID, "change_id": ch.ChangeID, "number": ch.Number, "head_oid": ch.HeadOID, "review_id": rv.ID, "patch_set": rv.PatchSet,
		"verdict": verdict, "blocked": out.Blocked, "findings": len(findings), "blocking": len(blocking),
	})
}

func changeSubject(repo ledger.Repo, ch ledger.Change) string {
	return fmt.Sprintf("%s/%s/changes/%d", repo.Org, repo.Name, ch.Number)
}

func reviewPrompt(repo ledger.Repo, ch ledger.Change, plan string, role Role) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are the adversarial reviewer for %s/%s. Review change #%d (%s) for correctness, security and scope creep.\n", repo.Org, repo.Name, ch.Number, ch.Title)
	b.WriteString("The unified diff is in the file $ORE_DIFF_FILE and the checkout is the current directory. The implementer's plan follows.\n")
	b.WriteString("Write your findings as a JSON array to the file $ORE_FINDINGS_FILE; each finding has severity (info|low|medium|high|critical), confidence (0..1), path, message, and optionally category, range {start_line,end_line}, suggested_fix, fingerprint. ")
	b.WriteString("A critical or high finding with confidence >= 0.7 blocks landing. Write [] when you find nothing. Do not edit files.\n")
	if strings.TrimSpace(role.PromptProfile) != "" {
		b.WriteString("\n" + strings.TrimSpace(role.PromptProfile) + "\n")
	}
	if strings.TrimSpace(plan) != "" {
		b.WriteString("\nPlan:\n" + strings.TrimSpace(plan) + "\n")
	}
	return b.String()
}

// parseFindings reads the reviewer's findings file: a JSON array of Finding,
// or an object with a "findings" array. Every finding is validated against
// the spec shape.
func parseFindings(path string) ([]review.Finding, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("the reviewer wrote no findings file ($ORE_FINDINGS_FILE)")
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, errors.New("the findings file is empty (write [] for no findings)")
	}
	var findings []review.Finding
	if err := json.Unmarshal(raw, &findings); err != nil {
		var wrapped struct {
			Findings []review.Finding `json:"findings"`
		}
		if err2 := json.Unmarshal(raw, &wrapped); err2 != nil || wrapped.Findings == nil {
			return nil, fmt.Errorf("not a JSON array of findings: %v", err)
		}
		findings = wrapped.Findings
	}
	for i, f := range findings {
		if err := f.Validate(); err != nil {
			return nil, fmt.Errorf("finding %d: %v", i+1, err)
		}
	}
	if findings == nil {
		findings = []review.Finding{}
	}
	return findings, nil
}

func summarize(all, blocking []review.Finding) string {
	list := blocking
	if len(list) == 0 {
		list = all
	}
	if len(list) == 0 {
		return "no findings"
	}
	var parts []string
	for i, f := range list {
		if i == 5 {
			parts = append(parts, fmt.Sprintf("and %d more", len(list)-5))
			break
		}
		loc := f.Path
		if f.Range != nil && f.Range.StartLine > 0 {
			loc = fmt.Sprintf("%s:%d", f.Path, f.Range.StartLine)
		}
		parts = append(parts, fmt.Sprintf("%s (%.2f) %s: %s", f.Severity, f.Confidence, loc, f.Message))
	}
	return strings.Join(parts, "; ")
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = "    " + lines[i]
	}
	return strings.Join(lines, "\n")
}
