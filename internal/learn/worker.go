// SPDX-License-Identifier: AGPL-3.0-only

package learn

import (
	"context"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

// Worker follows the ledger's event stream (the transactional outbox, read
// by sequence number) and re-evaluates the active runs of a repository when
// an event touches it. Progress is detected from the same events every
// pipeline reacts to, so a push, a change or a finished run advances the
// lesson without polling the learner's repository.
type Worker struct {
	S        *Service
	Interval time.Duration
	cursor   int64
	started  bool
}

// relevant events: ref updates, the change lifecycle, finished runs,
// reviews and landings.
func relevant(typ string) bool {
	switch {
	case typ == "io.ore.ref.updated", typ == "io.ore.ref.deleted", typ == "io.ore.pipeline.run.finished", typ == "io.ore.review.submitted", typ == "io.ore.landing.landed":
		return true
	case strings.HasPrefix(typ, "io.ore.change."):
		return true
	}
	return false
}

// Start positions the cursor at the current end of the stream (the worker
// reacts to new events, it does not replay history).
func (w *Worker) Start(ctx context.Context) error {
	var seq int64
	if err := w.S.DB.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&seq); err != nil {
		return err
	}
	w.cursor, w.started = seq, true
	return nil
}

// Run loops until ctx is done.
func (w *Worker) Run(ctx context.Context) {
	if !w.started {
		if err := w.Start(ctx); err != nil {
			w.S.log().Error("learn worker: start", "err", err)
			return
		}
	}
	interval := w.Interval
	if interval <= 0 {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := w.Tick(ctx); err != nil && ctx.Err() == nil {
				w.S.log().Warn("learn worker", "err", err)
			}
		}
	}
}

// Tick processes the events since the cursor. It returns how many runs it
// evaluated; a batch of events for repositories without active runs is no
// work and emits nothing.
func (w *Worker) Tick(ctx context.Context) (int, error) {
	if !w.started {
		if err := w.Start(ctx); err != nil {
			return 0, err
		}
	}
	evaluated := 0
	for {
		evs, err := w.S.DB.Events(ctx, ledger.EventsOptions{AfterSeq: w.cursor, Limit: 500})
		if err != nil {
			return evaluated, err
		}
		if len(evs) == 0 {
			return evaluated, nil
		}
		// one evaluation per repository per batch, at the batch's last seq
		touched := map[string]int64{}
		var order []string
		for _, ev := range evs {
			w.cursor = ev.Seq
			if !relevant(ev.Type) {
				continue
			}
			org, name, ok := repoOfSubject(ev.Subject)
			if !ok {
				continue
			}
			key := org + "/" + name
			if _, seen := touched[key]; !seen {
				order = append(order, key)
			}
			touched[key] = ev.Seq
		}
		for _, key := range order {
			org, name, _ := strings.Cut(key, "/")
			repo, err := w.S.DB.RepoByName(ctx, org, name)
			if err != nil {
				continue
			}
			runs, err := w.S.ActiveRunsForRepo(ctx, repo.ID)
			if err != nil {
				return evaluated, err
			}
			for _, r := range runs {
				if r.LastEventSeq >= touched[key] {
					continue
				}
				out, err := w.S.Check(ctx, r.ID, "event")
				if err != nil {
					w.S.log().Warn("learn worker: check", "run", r.ID, "err", err)
				}
				evaluated++
				_, _ = w.S.DB.Exec(ctx, `UPDATE lesson_runs SET last_event_seq = ? WHERE id = ? AND last_event_seq < ?`, touched[key], r.ID, touched[key])
				if len(out.Advanced) > 0 {
					w.S.log().Info("learn: advanced", "run", r.ID, "lesson", r.LessonID, "steps", out.Advanced, "completed", out.Completed)
				}
			}
		}
		if len(evs) < 500 {
			return evaluated, nil
		}
	}
}

// repoOfSubject reads org/name from a subject such as org/name/refs/heads/x,
// org/name/changes/3 or org/name/runs/prun_1.
func repoOfSubject(subject string) (string, string, bool) {
	parts := strings.SplitN(subject, "/", 3)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[0] == "orgs" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
