// SPDX-License-Identifier: AGPL-3.0-only

package learn

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/orevcs/ore/internal/ledger"
)

// Learn at the error (docs/06 §5): an error state in the product maps to
// the lesson that explains it. States are derived from ledger events; the
// catalog names a lesson per state even when that lesson is not shipped
// yet, in which case the answer is a 404 that still names the track and the
// nearest shipped lessons.

// State is one error state.
type State struct {
	State    string `json:"state"`
	Title    string `json:"title"`
	LessonID string `json:"lesson_id"`
	Track    string `json:"track"`
}

// States is the catalog, in display order.
var States = []State{
	{"ref.protected", "Push rejected by the protected-ref policy", "git-and-forge/branches-and-protection", "1-git-and-forge"},
	{"change.conflict", "Merge conflict in a change", "git-and-forge/resolving-a-conflict", "1-git-and-forge"},
	{"pipeline.failed.test", "Pipeline task failed: a test failed", "git-and-forge/a-green-pipeline", "1-git-and-forge"},
	{"pipeline.failed.dependency", "Pipeline task failed: a dependency or build step failed", "pipelines/debugging-a-failed-task", "3-pipelines"},
	{"pipeline.failed.timeout", "Pipeline task failed: timeout", "pipelines/debugging-a-failed-task", "3-pipelines"},
	{"pipeline.failed.cache", "Pipeline task failed: cache", "pipelines/caches-and-artifacts", "3-pipelines"},
	{"pipeline.failed.permissions", "Pipeline task failed: permissions", "pipelines/secrets-and-oidc", "3-pipelines"},
	{"secret.missing", "A pipeline references a secret that is not defined", "pipelines/secrets-and-oidc", "3-pipelines"},
	{"action.ref_rejected", "An action reference was refused (not pinned, not found)", "pipelines/pinning-actions", "3-pipelines"},
	{"deployment.blocked", "Deployment blocked by environment policy", "delivery/environments-and-approvals", "4-delivery"},
	{"agent.write_gated", "An agent's write was gated", "administration/agents-and-budgets", "6-administration"},
	{"budget.exceeded", "An agent exceeded its budget", "administration/agents-and-budgets", "6-administration"},
	{"review.large_change", "Review requested on a very large change", "code-review/writing-reviewable-changes", "2-code-review"},
}

// Explanation is the answer.
type Explanation struct {
	State        State           `json:"state"`
	Lesson       *LessonSummary  `json:"lesson,omitempty"`
	EventID      string          `json:"event_id,omitempty"`
	EventType    string          `json:"event_type,omitempty"`
	Subject      string          `json:"subject,omitempty"`
	Example      json.RawMessage `json:"example,omitempty"`
	Message      string          `json:"message"`
	NearestTrack string          `json:"nearest_track,omitempty"`
	Nearest      []LessonSummary `json:"nearest_lessons,omitempty"`
}

// NoLessonError is the 404: the state is known (or the event unknown) but
// no shipped lesson covers it; it carries the nearest track's lessons.
type NoLessonError struct {
	Explanation Explanation
}

func (e *NoLessonError) Error() string { return e.Explanation.Message }

// LessonSummary is a catalog entry.
type LessonSummary struct {
	ID               string              `json:"id"`
	Version          string              `json:"version"`
	Track            string              `json:"track"`
	Title            string              `json:"title"`
	EstimatedMinutes int                 `json:"estimated_minutes"`
	Artifact         string              `json:"artifact"`
	Mode             string              `json:"mode"`
	Prerequisites    []string            `json:"prerequisites"`
	Languages        []string            `json:"languages"`
	ExamObjectives   []map[string]string `json:"exam_objectives"`
	Steps            int                 `json:"steps"`
	Status           string              `json:"status,omitempty"` // completed | in_progress | available | locked
	RunID            string              `json:"run_id,omitempty"`
}

// Summary builds the catalog entry of a lesson.
func (s *Service) Summary(l *Lesson, lang string) LessonSummary {
	m := l.Manifest
	prereq := m.Prerequisites
	if prereq == nil {
		prereq = []string{}
	}
	obj := m.ExamObjectives
	if obj == nil {
		obj = []map[string]string{}
	}
	return LessonSummary{ID: m.ID, Version: m.Version, Track: m.Track, Title: l.Title(lang), EstimatedMinutes: m.EstimatedMinutes, Artifact: m.Artifact, Mode: m.Mode, Prerequisites: prereq, Languages: l.Languages(), ExamObjectives: obj, Steps: len(m.Steps)}
}

func stateByName(name string) (State, bool) {
	for _, st := range States {
		if st.State == name {
			return st, true
		}
	}
	return State{}, false
}

// ExplainState answers for a state name (for error surfaces that have no
// ledger event yet, such as the pre-receive hook's refusal).
func (s *Service) ExplainState(name, lang string) (Explanation, error) {
	st, ok := stateByName(name)
	if !ok {
		return Explanation{}, s.noLesson(Explanation{State: State{State: name, Title: "Unknown state"}, Message: fmt.Sprintf("no lesson explains state %q", name)}, "", lang)
	}
	return s.explain(st, Explanation{}, lang)
}

func (s *Service) explain(st State, ex Explanation, lang string) (Explanation, error) {
	ex.State = st
	if l, ok := s.Catalog.Lesson(st.LessonID); ok {
		sum := s.Summary(l, lang)
		ex.Lesson = &sum
		ex.Message = fmt.Sprintf("%s: the lesson %q (%s) explains it", st.Title, sum.Title, st.LessonID)
		return ex, nil
	}
	ex.Message = fmt.Sprintf("%s: the lesson that explains it (%s, track %s) is not shipped yet", st.Title, st.LessonID, st.Track)
	return Explanation{}, s.noLesson(ex, st.Track, lang)
}

// noLesson fills the nearest track (the shipped track closest by number to
// track, or the first shipped track) and returns the 404.
func (s *Service) noLesson(ex Explanation, track, lang string) error {
	tracks := s.Catalog.Tracks()
	if len(tracks) > 0 {
		nearest := tracks[0]
		if track != "" {
			want := trackNumber(track)
			best := -1
			for _, t := range tracks {
				d := trackNumber(t) - want
				if d < 0 {
					d = -d
				}
				if best < 0 || d < best {
					best, nearest = d, t
				}
			}
		}
		ex.NearestTrack = nearest
		for _, l := range s.Catalog.List() {
			if l.Manifest.Track == nearest {
				ex.Nearest = append(ex.Nearest, s.Summary(l, lang))
			}
		}
	}
	return &NoLessonError{Explanation: ex}
}

func trackNumber(t string) int {
	n := 0
	for _, c := range t {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// Explain maps a ledger event to the lesson that explains the state it
// records.
func (s *Service) Explain(ctx context.Context, eventID, lang string) (Explanation, error) {
	ev, err := s.eventByID(ctx, eventID)
	if err != nil {
		return Explanation{}, err
	}
	ex := Explanation{EventID: ev.ID, EventType: ev.Type, Subject: ev.Subject, Example: ev.Data}
	state, ok := s.classify(ctx, ev)
	if !ok {
		ex.State = State{State: "unknown", Title: "No error state for this event"}
		ex.Message = fmt.Sprintf("no lesson explains events of type %s", ev.Type)
		return Explanation{}, s.noLesson(ex, "", lang)
	}
	return s.explain(state, ex, lang)
}

func (s *Service) eventByID(ctx context.Context, id string) (ledger.Event, error) {
	var ev ledger.Event
	var data string
	err := s.DB.QueryRow(ctx, `SELECT seq, id, org_id, type, subject, time, actor_kind, actor_id, data FROM events WHERE id = ?`, id).
		Scan(&ev.Seq, &ev.ID, &ev.OreOrg, &ev.Type, &ev.Subject, &ev.Time, &ev.OreActor.Kind, &ev.OreActor.ID, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return ev, fmt.Errorf("%w: event %s", ErrNotFound, id)
	}
	if err != nil {
		return ev, err
	}
	ev.Data = json.RawMessage(data)
	return ev, nil
}

// classify derives the error state from an event, reading the pipeline run
// behind a failed run event to tell test, timeout, secret and action states apart.
func (s *Service) classify(ctx context.Context, ev ledger.Event) (State, bool) {
	var data map[string]any
	_ = json.Unmarshal(ev.Data, &data)
	str := func(k string) string {
		v, _ := data[k].(string)
		return v
	}
	name := ""
	switch {
	case ev.Type == "io.ore.pipeline.run.finished":
		if str("status") != "failed" && str("status") != "error" {
			return State{}, false
		}
		name = s.classifyRun(ctx, str("id"), str("tasks"), str("log_path"))
	case ev.Type == "io.ore.deployment.refused", ev.Type == "io.ore.deployment.approval.refused":
		name = "deployment.blocked"
	case ev.Type == "io.ore.agent.session.write.gated":
		name = "agent.write_gated"
		if strings.Contains(strings.ToLower(string(ev.Data)), "budget") {
			name = "budget.exceeded"
		}
	case ev.Type == "io.ore.change.revert_failed":
		name = "change.conflict"
	case strings.HasPrefix(ev.Type, "io.ore.landing.") || strings.HasPrefix(ev.Type, "io.ore.change.") || strings.HasPrefix(ev.Type, "io.ore.stack."):
		if strings.Contains(strings.ToLower(string(ev.Data)), "conflict") {
			name = "change.conflict"
		}
	case ev.Type == "io.ore.change.review.requested":
		if n, ok := data["lines"].(float64); ok && n >= 3000 {
			name = "review.large_change"
		}
	}
	if name == "" {
		return State{}, false
	}
	return stateByName(name)
}

func (s *Service) classifyRun(ctx context.Context, runID, tasks, logPath string) string {
	if runID != "" {
		if run, err := s.DB.PipelineRun(ctx, runID); err == nil {
			tasks, logPath = run.Tasks, run.LogPath
		}
	}
	logText := ""
	if logPath != "" {
		if b, err := os.ReadFile(logPath); err == nil {
			if len(b) > 65536 {
				b = b[:65536]
			}
			logText = string(b)
		}
	}
	lower := strings.ToLower(logText)
	switch {
	case strings.Contains(lower, "secrets not defined"):
		return "secret.missing"
	case strings.Contains(lower, "uses.ref-not-found") || strings.Contains(lower, "does not exist in"):
		return "action.ref_rejected"
	}
	var list []map[string]any
	_ = json.Unmarshal([]byte(tasks), &list)
	var failed []map[string]any
	for _, t := range list {
		if st, _ := t["status"].(string); st == "failed" {
			failed = append(failed, t)
		}
	}
	for _, t := range failed {
		if r, _ := t["reason"].(string); r == "timeout" {
			return "pipeline.failed.timeout"
		}
	}
	for _, t := range failed {
		id := strings.ToLower(fmt.Sprint(t["id"]) + " " + fmt.Sprint(t["name"]))
		switch {
		case strings.Contains(id, "cache"):
			return "pipeline.failed.cache"
		case strings.Contains(id, "test") || strings.Contains(id, "lint") || strings.Contains(id, "check"):
			return "pipeline.failed.test"
		}
	}
	switch {
	case strings.Contains(lower, "permission denied") || strings.Contains(lower, "403") || strings.Contains(lower, "unauthorized"):
		return "pipeline.failed.permissions"
	case strings.Contains(lower, "fail") && (strings.Contains(lower, "--- fail") || strings.Contains(lower, "tests failed") || strings.Contains(lower, "assert")):
		return "pipeline.failed.test"
	case len(failed) > 0:
		return "pipeline.failed.dependency"
	}
	return "pipeline.failed.test"
}

// SortedStates lists the states for the API.
func SortedStates() []State {
	out := append([]State{}, States...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].State < out[j].State })
	return out
}
