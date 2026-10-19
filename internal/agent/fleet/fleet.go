// SPDX-License-Identifier: AGPL-3.0-only

// Package fleet is the agent fleet (docs/12 §2-5, ADR 0009, ADR 0015): roles
// with adapters, prompt profiles and budgets; the adversarial reviewer that
// runs between verification and landing; budgets enforced before every step
// of the loop; watchers that turn ledger signals into implementer runs with
// loop prevention; evidence-based autonomy level recommendations with
// automatic demotion; and the kill switch. It governs internal/agent through
// the agent.Governor seam and registers as "agent.fleet".
package fleet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/orevcs/ore/internal/agent"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/review"
)

// Service is the fleet: it implements agent.Governor and runs the watcher.
type Service struct {
	DB       *ledger.DB
	ReposDir string
	LogsDir  string
	Loop     *agent.Loop
	Reviews  *review.Service
	Log      *slog.Logger
	// Now is the clock (tests).
	Now func() time.Time
	// Interval is the watcher's tick (default 2 s).
	Interval time.Duration
	// WatcherID names this watcher's ledger cursor (default "fleet").
	WatcherID string
	// RunSync makes the watcher run implementer passes synchronously (tests).
	RunSync bool
	// ReviewTimeout bounds the reviewer adapter (default 10 minutes).
	ReviewTimeout time.Duration
}

// FleetActor is the actor for what the fleet does on its own (level changes,
// watcher-started runs, red-main issues).
var FleetActor = ledger.Actor{Kind: "system", ID: "fleet"}

// Errors the HTTP layer maps to status codes.
var (
	// ErrPaused marks a paused repository or instance (409).
	ErrPaused = errors.New("fleet paused")
	// ErrInvalid marks a bad request (400).
	ErrInvalid = errors.New("invalid")
	// ErrConflict marks a duplicate (409).
	ErrConflict = errors.New("conflict")
)

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// stamp formats t in the ledger's timestamp format.
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) watcherID() string {
	if s.WatcherID != "" {
		return s.WatcherID
	}
	return "fleet"
}

// Settings are the per-repository fleet settings.
type Settings struct {
	Paused               bool       `json:"paused"`
	PauseReason          string     `json:"pause_reason,omitempty"`
	AutoPromote          bool       `json:"auto_promote"`
	AutoDemote           bool       `json:"auto_demote"`
	WatcherRetries       int        `json:"watcher_retries"`
	WatcherWindowMinutes int        `json:"watcher_window_minutes"`
	EvidenceWindowDays   int        `json:"evidence_window_days"`
	Thresholds           Thresholds `json:"thresholds"`
	// LastLevelChangeAt is when the fleet last changed the level automatically;
	// the evidence window restarts there.
	LastLevelChangeAt string `json:"last_level_change_at,omitempty"`
	UpdatedAt         string `json:"updated_at,omitempty"`
}

// DefaultSettings are the settings of a repository without a row.
func DefaultSettings() Settings {
	return Settings{AutoDemote: true, WatcherRetries: 1, WatcherWindowMinutes: 60, EvidenceWindowDays: 30, Thresholds: DefaultThresholds()}
}

// SettingsInput is what PUT settings accepts (pause is a separate call).
type SettingsInput struct {
	AutoPromote          *bool       `json:"auto_promote"`
	AutoDemote           *bool       `json:"auto_demote"`
	WatcherRetries       *int        `json:"watcher_retries"`
	WatcherWindowMinutes *int        `json:"watcher_window_minutes"`
	EvidenceWindowDays   *int        `json:"evidence_window_days"`
	Thresholds           *Thresholds `json:"thresholds"`
}

// SetSettings merges the input over the current settings and emits
// io.ore.fleet.settings.changed.
func (s *Service) SetSettings(ctx context.Context, repo ledger.Repo, in SettingsInput, actor ledger.Actor) (Settings, error) {
	cur, err := s.Settings(ctx, repo)
	if err != nil {
		return cur, err
	}
	if in.AutoPromote != nil {
		cur.AutoPromote = *in.AutoPromote
	}
	if in.AutoDemote != nil {
		cur.AutoDemote = *in.AutoDemote
	}
	if in.WatcherRetries != nil {
		if *in.WatcherRetries < 0 {
			return cur, fmt.Errorf("%w: watcher_retries must be >= 0", ErrInvalid)
		}
		cur.WatcherRetries = *in.WatcherRetries
	}
	if in.WatcherWindowMinutes != nil {
		if *in.WatcherWindowMinutes <= 0 {
			return cur, fmt.Errorf("%w: watcher_window_minutes must be > 0", ErrInvalid)
		}
		cur.WatcherWindowMinutes = *in.WatcherWindowMinutes
	}
	if in.EvidenceWindowDays != nil {
		if *in.EvidenceWindowDays <= 0 {
			return cur, fmt.Errorf("%w: evidence_window_days must be > 0", ErrInvalid)
		}
		cur.EvidenceWindowDays = *in.EvidenceWindowDays
	}
	if in.Thresholds != nil {
		cur.Thresholds = in.Thresholds.withDefaults()
	}
	if err := s.saveSettings(ctx, repo, &cur, "io.ore.fleet.settings.changed", actor, map[string]any{"settings": cur}); err != nil {
		return cur, err
	}
	return cur, nil
}

// Pause stops the fleet for one repository (repo != nil) or the whole
// instance: new runs are refused and in-flight runs stop at their next step.
func (s *Service) Pause(ctx context.Context, repo *ledger.Repo, reason string, actor ledger.Actor) error {
	if repo == nil {
		return s.setInstancePaused(ctx, true, reason, actor)
	}
	cur, err := s.Settings(ctx, *repo)
	if err != nil {
		return err
	}
	cur.Paused, cur.PauseReason = true, reason
	return s.saveSettings(ctx, *repo, &cur, "io.ore.fleet.paused", actor, map[string]any{"reason": reason, "scope": "repository"})
}

// Resume lifts a pause.
func (s *Service) Resume(ctx context.Context, repo *ledger.Repo, actor ledger.Actor) error {
	if repo == nil {
		return s.setInstancePaused(ctx, false, "", actor)
	}
	cur, err := s.Settings(ctx, *repo)
	if err != nil {
		return err
	}
	cur.Paused, cur.PauseReason = false, ""
	return s.saveSettings(ctx, *repo, &cur, "io.ore.fleet.resumed", actor, map[string]any{"scope": "repository"})
}

// pausedReason reports whether runs are paused for the repository, and why.
func (s *Service) pausedReason(ctx context.Context, repo ledger.Repo) (bool, string, error) {
	paused, reason, err := s.InstancePaused(ctx)
	if err != nil {
		return false, "", err
	}
	if paused {
		return true, "instance paused: " + orDefault(reason, "kill switch"), nil
	}
	st, err := s.Settings(ctx, repo)
	if err != nil {
		return false, "", err
	}
	if st.Paused {
		return true, fmt.Sprintf("repository %s/%s paused: %s", repo.Org, repo.Name, orDefault(st.PauseReason, "kill switch")), nil
	}
	return false, "", nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// ---- agent.Governor ----

// Admit refuses a new run when the fleet is paused or the run budget is spent.
func (s *Service) Admit(ctx context.Context, repo ledger.Repo, principal ledger.Actor) error {
	if paused, why, err := s.pausedReason(ctx, repo); err != nil {
		return err
	} else if paused {
		return fmt.Errorf("%w: %s", ErrPaused, why)
	}
	return s.checkBudgets(ctx, repo, principal, "admit", nil)
}

// Gate stops a run at its next step when the fleet was paused or a budget is
// exhausted; a budget stop emits io.ore.agent.budget.exceeded.
func (s *Service) Gate(ctx context.Context, repo ledger.Repo, run *ledger.AgentRun, principal ledger.Actor, step string) error {
	if paused, why, err := s.pausedReason(ctx, repo); err != nil {
		return err
	} else if paused {
		return fmt.Errorf("%w: %s", ErrPaused, why)
	}
	return s.checkBudgets(ctx, repo, principal, step, run)
}

// Role returns the repository's role configuration for the loop.
func (s *Service) Role(ctx context.Context, repo ledger.Repo, name string) (agent.RoleSpec, bool) {
	r, err := s.role(ctx, repo, name)
	if err != nil || !r.Enabled || r.Default {
		return agent.RoleSpec{}, false
	}
	return agent.RoleSpec{Name: r.Name, AdapterCmd: r.AdapterCmd, AdapterName: r.AdapterName, PromptProfile: r.PromptProfile}, true
}

// Status is the fleet's view of one repository.
type Status struct {
	Repo          string         `json:"repo"`
	Level         int            `json:"autonomy_level"`
	InstancePause bool           `json:"instance_paused"`
	Settings      Settings       `json:"settings"`
	Roles         []Role         `json:"roles"`
	Budgets       []BudgetStatus `json:"budgets"`
}

// RepoStatus assembles the fleet status of a repository.
func (s *Service) RepoStatus(ctx context.Context, repo ledger.Repo) (Status, error) {
	st := Status{Repo: repo.Org + "/" + repo.Name, Level: repo.AutonomyLevel}
	var err error
	if st.InstancePause, _, err = s.InstancePaused(ctx); err != nil {
		return st, err
	}
	if st.Settings, err = s.Settings(ctx, repo); err != nil {
		return st, err
	}
	if st.Roles, err = s.Roles(ctx, repo); err != nil {
		return st, err
	}
	if st.Budgets, err = s.Budgets(ctx, repo); err != nil {
		return st, err
	}
	return st, nil
}
