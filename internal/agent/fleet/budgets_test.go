// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"errors"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/agent"
	"github.com/orevcs/ore/internal/ledger"
)

func TestExceededRules(t *testing.T) {
	b := Budget{RunsPerDay: 2, CIMinutesPerDay: 10, OpenChanges: 1, WritesPerHour: 5}
	if got := exceeded(b, Usage{Runs: 1}, 1); len(got) != 0 {
		t.Fatalf("second run of two should pass: %v", got)
	}
	if got := exceeded(b, Usage{Runs: 2}, 1); len(got) != 1 || got[0].Budget != "runs_per_day" {
		t.Fatalf("third run should fail: %v", got)
	}
	if got := exceeded(b, Usage{CIMinutes: 10}, 0); len(got) != 1 || got[0].Budget != "ci_minutes_per_day" {
		t.Fatalf("ci minutes: %v", got)
	}
	if got := exceeded(b, Usage{OpenChanges: 1}, 0); len(got) != 1 || got[0].Budget != "open_changes" {
		t.Fatalf("open changes: %v", got)
	}
	if got := exceeded(b, Usage{WritesLastHour: 5}, 0); len(got) != 1 || got[0].Budget != "writes_per_hour" {
		t.Fatalf("writes: %v", got)
	}
	if got := exceeded(Budget{}, Usage{Runs: 99, CIMinutes: 99, OpenChanges: 99, WritesLastHour: 99}, 1); len(got) != 0 {
		t.Fatalf("zero means unlimited: %v", got)
	}
	e := &BudgetError{Scope: "principal", Principal: "claude-code", Budget: "runs_per_day", Used: 3, Limit: 2}
	if !strings.Contains(e.Error(), "runs_per_day 3/2 for agent claude-code") {
		t.Fatalf("message: %s", e.Error())
	}
}

func TestUsageComesFromTheLedger(t *testing.T) {
	e := newEnv(t, 2)
	// one run at L2 opens a change and stops; its pipeline run was by the agent
	run, err := e.run(agent.Request{Prompt: "fix the greeting"})
	if err != nil || run.Status != "blocked" {
		t.Fatalf("L2 run: %v %+v", err, run)
	}
	u, err := e.svc.usage(e.ctx, e.repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if u.Runs != 1 || u.OpenChanges != 1 || u.CIMinutes <= 0 || u.WritesLastHour < 2 {
		t.Fatalf("usage: %+v", u)
	}
	per, _ := e.svc.usage(e.ctx, e.repo, "demo-script")
	if per.Runs != 1 || per.OpenChanges != 1 {
		t.Fatalf("per-principal usage: %+v", per)
	}
	other, _ := e.svc.usage(e.ctx, e.repo, "someone-else")
	if other.Runs != 0 || other.OpenChanges != 0 || other.WritesLastHour != 0 {
		t.Fatalf("other principal should be idle: %+v", other)
	}
	list, err := e.svc.Budgets(e.ctx, e.repo)
	if err != nil || len(list) != 2 || list[0].Scope != "repository" || list[1].Principal != "demo-script" || list[1].Source != "none" {
		t.Fatalf("budgets: %v %+v", err, list)
	}
	// identity's agent budgets are honoured when the fleet has no row
	if _, err := e.db.Exec(e.ctx, `INSERT INTO users(id, name, email, role, status, created_at) VALUES('usr_1','rohan','r@x','owner','active','now')`); err == nil {
		_, _ = e.db.Exec(e.ctx, `INSERT INTO agents(id, org_id, name, owner_user_id, budgets, status, created_at) VALUES('agt_1', ?, 'demo-script', 'usr_1', '{"runs_per_day": 7}', 'active', 'now')`, e.org.ID)
		b, src := e.svc.budgetFor(e.ctx, e.repo, map[string]Budget{}, "demo-script")
		if src != "identity" || b.RunsPerDay != 7 {
			t.Fatalf("identity budget: %s %+v", src, b)
		}
	}
	if _, err := e.svc.SetBudget(e.ctx, e.repo, "", Budget{RunsPerDay: -1}, e.human); !errors.Is(err, ErrInvalid) {
		t.Fatal("negative budget accepted")
	}
}

func TestBudgetExhaustedMidRun(t *testing.T) {
	e := newEnv(t, 2)
	first, err := e.run(agent.Request{Prompt: "first"})
	e.mustBlocked(first, err, "human must land")
	// one open agent change exists; a budget of one open change stops the next run at verify, after it changed and pushed
	if _, err := e.svc.SetBudget(e.ctx, e.repo, "", Budget{OpenChanges: 1}, e.human); err != nil {
		t.Fatal(err)
	}
	run, err := e.run(agent.Request{Prompt: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "blocked" || run.Step != "verify" || !strings.Contains(run.Error, "budget exceeded: open_changes 1/1") {
		t.Fatalf("expected a budget stop at verify, got %s %q at %s", run.Status, run.Error, run.Step)
	}
	evs := e.events("io.ore.agent.budget.exceeded")
	if len(evs) != 1 || !strings.Contains(string(evs[0].Data), `"step":"verify"`) || !strings.Contains(string(evs[0].Data), run.ID) {
		t.Fatalf("budget event: %+v", evs)
	}
	if ch, _ := e.db.Changes(e.ctx, e.repo); len(ch) != 1 {
		t.Fatalf("the blocked run must not open a change: %d", len(ch))
	}
	// the run budget refuses admission before anything is recorded
	e.svc.SetBudget(e.ctx, e.repo, "", Budget{RunsPerDay: 2}, e.human)
	_, err = e.run(agent.Request{Prompt: "third"})
	var be *BudgetError
	if !errors.As(err, &be) || !errors.Is(err, agent.ErrRefused) || be.Budget != "runs_per_day" {
		t.Fatalf("expected refusal by runs_per_day, got %v", err)
	}
	if len(e.runs()) != 2 {
		t.Fatalf("refused run must not be recorded: %d", len(e.runs()))
	}
	// a per-principal budget applies to that principal only
	e.svc.SetBudget(e.ctx, e.repo, "", Budget{}, e.human)
	e.svc.SetBudget(e.ctx, e.repo, "demo-script", Budget{RunsPerDay: 1}, e.human)
	if _, err := e.run(agent.Request{Prompt: "fourth"}); !errors.As(err, &be) || be.Scope != "principal" || be.Principal != "demo-script" {
		t.Fatalf("principal budget: %v", err)
	}
	e.loop.AdapterName = "other-agent"
	if _, err := e.run(agent.Request{Prompt: "fifth"}); err != nil {
		t.Fatalf("another principal is not bound by demo-script's budget: %v", err)
	}
	if list, _ := e.svc.Budgets(e.ctx, e.repo); len(list) != 3 {
		t.Fatalf("budgets should list both principals: %+v", list)
	}
}

func (e *env) mustBlocked(run ledger.AgentRun, err error, want string) ledger.AgentRun {
	e.t.Helper()
	if err != nil {
		e.t.Fatalf("run: %v", err)
	}
	if run.Status != "blocked" || !strings.Contains(run.Error, want) {
		e.t.Fatalf("expected blocked with %q, got %s %q\n%s", want, run.Status, run.Error, e.runLog(run))
	}
	return run
}
