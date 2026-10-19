// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/agent"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// The watcher (docs/12 §1 "observe", §4 watcher role) tails the ledger and
// turns failure signals into implementer runs: a reverted optimistic landing
// (io.ore.change.reverted, which carries its issue), the failure issues the
// delivery slice opens for failed or rolled-back deployments, and red
// pipelines on the default branch (for which it opens the issue itself). It
// never starts a run for a signal caused by a run it started within the
// repository's window unless the repository allows retries (default 1), and
// it records every decision as a Signal so two watchers never start two runs.

var deploymentIssue = regexp.MustCompile(`^Deployment (\S+) to (\S+) failed health check`)

const redMainPrefix = "Pipeline red on "

// Run tails the ledger until ctx ends.
func (s *Service) Run(ctx context.Context) {
	iv := s.Interval
	if iv == 0 {
		iv = 2 * time.Second
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		if _, err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.log().Warn("fleet watcher", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick processes the events since this watcher's cursor and returns how many
// it looked at.
func (s *Service) Tick(ctx context.Context) (int, error) {
	after, err := s.cursor(ctx)
	if err != nil {
		return 0, err
	}
	evs, err := s.DB.Events(ctx, ledger.EventsOptions{AfterSeq: after, Limit: 500})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ev := range evs {
		if err := s.handle(ctx, ev); err != nil {
			s.log().Warn("fleet watcher: event", "seq", ev.Seq, "type", ev.Type, "err", err)
		}
		after = ev.Seq
		n++
	}
	if n > 0 {
		if err := s.saveCursor(ctx, after); err != nil {
			return n, err
		}
	}
	return n, nil
}

// repoFromSubject resolves "org/name/..." subjects.
func (s *Service) repoFromSubject(ctx context.Context, subject string) (ledger.Repo, bool) {
	parts := strings.SplitN(subject, "/", 3)
	if len(parts) < 2 {
		return ledger.Repo{}, false
	}
	repo, err := s.DB.RepoByName(ctx, parts[0], parts[1])
	if err != nil {
		return ledger.Repo{}, false
	}
	return repo, true
}

func (s *Service) handle(ctx context.Context, ev ledger.Event) error {
	switch {
	case ev.Type == "io.ore.change.reverted":
		repo, ok := s.repoFromSubject(ctx, ev.Subject)
		if !ok {
			return nil
		}
		var d struct {
			ChangeID    string `json:"change_id"`
			IssueNumber int    `json:"issue_number"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		if d.IssueNumber > 0 {
			issue, err := s.DB.Issue(ctx, repo, d.IssueNumber)
			if err == nil {
				cause := s.runForChange(ctx, repo, d.ChangeID)
				if err := s.signal(ctx, repo, ev, issue, cause); err != nil {
					return err
				}
			}
		}
		return s.evaluateAuto(ctx, repo, ev.Type)
	case ev.Type == "io.ore.issue.opened" && ev.OreActor.Kind == "system":
		repo, ok := s.repoFromSubject(ctx, ev.Subject)
		if !ok {
			return nil
		}
		var issue ledger.Issue
		if err := json.Unmarshal(ev.Data, &issue); err != nil || issue.ID == "" {
			return nil
		}
		switch {
		case deploymentIssue.MatchString(issue.Title):
			depID := deploymentIssue.FindStringSubmatch(issue.Title)[1]
			return s.signal(ctx, repo, ev, issue, s.runForDeployment(ctx, repo, depID))
		case strings.HasPrefix(issue.Title, redMainPrefix):
			return s.signal(ctx, repo, ev, issue, s.runForCommit(ctx, repo, commitFromBody(issue.Body)))
		}
		return nil
	case ev.Type == "io.ore.pipeline.run.finished":
		return s.redMain(ctx, ev)
	case ev.Type == "io.ore.deployment.failed" || ev.Type == "io.ore.deployment.rolled_back" || ev.Type == "io.ore.agent.budget.exceeded" || strings.HasPrefix(ev.Type, "io.ore.security."):
		repo, ok := s.repoFromSubject(ctx, ev.Subject)
		if !ok {
			return nil
		}
		return s.evaluateAuto(ctx, repo, ev.Type)
	}
	return nil
}

// redMain opens the signal issue for a red pipeline on the default branch.
// Runs a human asked for are left to that human (they are watching); runs the
// system or an agent triggered (post-merge verification, pushes) become issues.
func (s *Service) redMain(ctx context.Context, ev ledger.Event) error {
	if ev.OreActor.Kind == "human" {
		return nil
	}
	var run ledger.PipelineRun
	if err := json.Unmarshal(ev.Data, &run); err != nil || run.ChangeID != nil || (run.Status != "failed" && run.Status != "error") {
		return nil
	}
	repo, ok := s.repoFromSubject(ctx, ev.Subject)
	if !ok {
		return nil
	}
	tip, err := gitx.RevParse(ctx, s.Loop.ReposDir+"/"+repo.Org+"/"+repo.Name+".git", "refs/heads/"+repo.DefaultBranch)
	if err != nil || tip != run.CommitOID {
		return nil // not the current tip: stale or a branch run
	}
	title := fmt.Sprintf("%s%s: pipeline run %s %s", redMainPrefix, repo.DefaultBranch, run.ID, run.Status)
	var n int
	if err := s.DB.QueryRow(ctx, `SELECT COUNT(1) FROM issues WHERE repo_id = ? AND state = 'open' AND title LIKE ?`, repo.ID, redMainPrefix+repo.DefaultBranch+"%").Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	body := fmt.Sprintf("Pipeline run %s on %s finished %s.\n\nCommit: %s\nRun: %s\n\nFix the default branch: the fleet starts an implementer run on this issue when the repository's autonomy level allows it.\n", run.ID, repo.DefaultBranch, run.Status, run.CommitOID, run.ID)
	_, err = s.DB.CreateIssue(ctx, repo, title, body, FleetActor)
	return err
}

func commitFromBody(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "Commit: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Commit: "))
		}
	}
	return ""
}

// runForChange finds the agent run that opened a change (by Gerrit-style id).
func (s *Service) runForChange(ctx context.Context, repo ledger.Repo, changeID string) *ledger.AgentRun {
	var rowID string
	if err := s.DB.QueryRow(ctx, `SELECT id FROM changes WHERE repo_id = ? AND change_id = ?`, repo.ID, changeID).Scan(&rowID); err != nil {
		return nil
	}
	return s.runWhere(ctx, repo, `change_id = ?`, rowID)
}

func (s *Service) runForDeployment(ctx context.Context, repo ledger.Repo, depID string) *ledger.AgentRun {
	if r := s.runWhere(ctx, repo, `deployment_id = ?`, depID); r != nil {
		return r
	}
	dep, err := s.DB.Deployment(ctx, depID)
	if err != nil {
		return nil
	}
	return s.runForCommit(ctx, repo, dep.CommitOID)
}

// runForCommit finds the agent run whose change was merged as commit.
func (s *Service) runForCommit(ctx context.Context, repo ledger.Repo, commit string) *ledger.AgentRun {
	if commit == "" {
		return nil
	}
	var rowID string
	if err := s.DB.QueryRow(ctx, `SELECT id FROM changes WHERE repo_id = ? AND merge_oid = ?`, repo.ID, commit).Scan(&rowID); err != nil {
		return nil
	}
	return s.runWhere(ctx, repo, `change_id = ?`, rowID)
}

func (s *Service) runWhere(ctx context.Context, repo ledger.Repo, where string, arg any) *ledger.AgentRun {
	var id string
	err := s.DB.QueryRow(ctx, `SELECT id FROM agent_runs WHERE repo_id = ? AND `+where+` ORDER BY created_at DESC LIMIT 1`, repo.ID, arg).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return nil
	}
	run, err := s.DB.AgentRun(ctx, id)
	if err != nil {
		return nil
	}
	return &run
}

// implementer is the principal the watcher's runs act as.
func (s *Service) implementer(ctx context.Context, repo ledger.Repo) ledger.Actor {
	r, err := s.role(ctx, repo, RoleImplementer)
	if err != nil || r.AdapterName == "" {
		return ledger.Actor{Kind: "agent", ID: "claude-code"}
	}
	return ledger.Actor{Kind: "agent", ID: r.AdapterName}
}

// signal decides whether a failure signal becomes an implementer run on its
// issue, records the decision and starts the run.
func (s *Service) signal(ctx context.Context, repo ledger.Repo, ev ledger.Event, issue ledger.Issue, cause *ledger.AgentRun) error {
	sg := Signal{}
	if issue.ID != "" {
		sg.IssueID = &issue.ID
	}
	if cause != nil {
		sg.CauseRunID = &cause.ID
	}
	claimed, err := s.claimSignal(ctx, repo, ev, &sg)
	if err != nil || !claimed {
		return err // another watcher owns this event
	}
	skip := func(reason string) error {
		return s.decideSignal(ctx, repo, &sg, "skipped", reason, map[string]any{"issue_number": issue.Number})
	}
	if issue.State != "open" {
		return skip(fmt.Sprintf("issue #%d is %s", issue.Number, issue.State))
	}
	if started, err := s.startedForIssue(ctx, issue.ID); err != nil {
		return err
	} else if started {
		return skip(fmt.Sprintf("a run was already started for issue #%d", issue.Number))
	}
	repo, err = s.DB.RepoByID(ctx, repo.ID) // the level may have changed since the event
	if err != nil {
		return err
	}
	if repo.AutonomyLevel < 3 {
		return skip(fmt.Sprintf("autonomy level L%d: agents do not land here; issue #%d waits for a human", repo.AutonomyLevel, issue.Number))
	}
	if paused, why, err := s.pausedReason(ctx, repo); err != nil {
		return err
	} else if paused {
		return skip(why)
	}
	st, err := s.Settings(ctx, repo)
	if err != nil {
		return err
	}
	if cause != nil {
		if prev, err := s.signalForRun(ctx, cause.ID); err == nil {
			window := time.Duration(st.WatcherWindowMinutes) * time.Minute
			if age := s.now().Sub(parseStamp(prev.CreatedAt)); age <= window {
				sg.Depth = prev.Depth + 1
				if sg.Depth > st.WatcherRetries {
					return skip(fmt.Sprintf("loop prevention: run %s was started by the watcher %s ago for the same chain of signals and the repository allows %d retry(ies); retry %d refused", cause.ID, age.Round(time.Second), st.WatcherRetries, sg.Depth))
				}
			}
		}
	}
	principal := s.implementer(ctx, repo)
	if err := s.checkBudgets(ctx, repo, principal, "admit", nil); err != nil {
		var be *BudgetError
		if errors.As(err, &be) {
			return skip(err.Error())
		}
		return err
	}
	req := agent.Request{Issue: &issue}
	var run ledger.AgentRun
	if s.RunSync {
		run, err = s.Loop.RunSync(ctx, repo, req, FleetActor)
	} else {
		run, err = s.Loop.Start(ctx, repo, req, FleetActor)
	}
	if run.ID != "" {
		sg.RunID = &run.ID
	}
	if err != nil && run.ID == "" {
		return skip("start: " + err.Error())
	}
	return s.decideSignal(ctx, repo, &sg, "started", "", map[string]any{"issue_number": issue.Number, "run_id": run.ID, "depth": sg.Depth, "cause_run_id": sg.CauseRunID})
}

func (s *Service) evaluateAuto(ctx context.Context, repo ledger.Repo, trigger string) error {
	repo, err := s.DB.RepoByID(ctx, repo.ID)
	if err != nil {
		return err
	}
	_, _, _, err = s.Evaluate(ctx, repo, trigger, FleetActor)
	return err
}
