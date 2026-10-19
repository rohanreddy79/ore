// SPDX-License-Identifier: AGPL-3.0-only

package learn

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/orevcs/ore/internal/identity"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/web"
)

func init() { registry.Register("learn", Extension) }

// Extension loads the catalog (embedded plus ORE_LESSONS_DIR), attaches the
// service as s.Services["learn"], mounts the routes and starts the worker.
// Identity, when registered, enforces who may run lessons where. The
// registry applies extensions in name order, so "identity" is already
// attached when "learn" runs.
func Extension(ctx context.Context, s *web.Server) error {
	cat, err := LoadCatalog(os.Getenv("ORE_LESSONS_DIR"))
	if err != nil {
		return fmt.Errorf("learn: %w", err)
	}
	svc := &Service{DB: s.DB, ReposDir: s.ReposDir, Catalog: cat, Review: s.Review, Log: s.Log}
	if id, ok := s.Services["identity"].(*identity.Service); ok && id != nil {
		svc.Identity = id
		svc.OrgsFor = func(ctx context.Context, learner ledger.Actor) ([]string, error) {
			if learner.Kind != "human" {
				return nil, nil
			}
			u, err := id.UserByName(ctx, learner.ID)
			if err != nil {
				return nil, nil
			}
			ms, err := id.Memberships(ctx, u.ID)
			if err != nil {
				return nil, err
			}
			var names []string
			for _, m := range ms {
				names = append(names, m.Org)
			}
			return names, nil
		}
	}
	if s.Services == nil {
		s.Services = map[string]any{}
	}
	s.Services["learn"] = svc
	s.AddRoutes(svc.Routes(s))
	w := &Worker{S: svc, Interval: time.Second}
	if v := os.Getenv("ORE_LEARN_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			w.Interval = d
		}
	}
	if err := w.Start(ctx); err != nil {
		return fmt.Errorf("learn: worker: %w", err)
	}
	go w.Run(ctx)
	return nil
}

// lessons is the catalog annotated with the learner's status.
func (s *Service) lessons(ctx context.Context, a ledger.Actor, lang string) ([]LessonSummary, error) {
	progress, err := s.ProgressFor(ctx, a)
	if err != nil {
		return nil, err
	}
	byLesson := map[string]Progress{}
	for _, p := range progress {
		if prev, ok := byLesson[p.LessonID]; !ok || p.Status == "completed" && prev.Status != "completed" {
			byLesson[p.LessonID] = p
		}
	}
	out := []LessonSummary{}
	for _, l := range s.Catalog.List() {
		sum := s.Summary(l, lang)
		switch p, ok := byLesson[l.Manifest.ID]; {
		case ok && p.Status == "completed":
			sum.Status, sum.RunID = "completed", p.RunID
		case ok:
			sum.Status, sum.RunID = "in_progress", p.RunID
		default:
			sum.Status = "available"
			for _, pre := range l.Manifest.Prerequisites {
				if pp, ok := byLesson[pre]; !ok || pp.Status != "completed" {
					sum.Status = "locked"
				}
			}
		}
		out = append(out, sum)
	}
	return out, nil
}
