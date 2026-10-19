// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"context"
	"errors"
	"net/http"

	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/web"
)

// Name is the registry name of this extension.
const Name = "agent.fleet"

func init() { registry.Register(Name, Extension) }

// Extension attaches the fleet to the server: it becomes the loop's Governor,
// publishes itself as Services["agent.fleet"], mounts the routes and starts
// the watcher. It is a subpackage of internal/agent because the loop must not
// import web (registry imports web, web imports agent).
func Extension(ctx context.Context, s *web.Server) error {
	if s.DB == nil {
		return errors.New("agent.fleet: server has no database")
	}
	svc := &Service{DB: s.DB, ReposDir: s.ReposDir, LogsDir: s.LogsDir, Loop: s.Loop, Reviews: s.Review, Log: s.Log}
	if s.Loop != nil {
		s.Loop.Governor = svc
		if svc.Reviews == nil {
			svc.Reviews = s.Loop.Review
		}
	}
	if s.Services == nil {
		s.Services = map[string]any{}
	}
	s.Services[Name] = svc
	s.AddRoutes(func(mux *http.ServeMux) { Routes(mux, s, svc) })
	go svc.Run(ctx)
	return nil
}
