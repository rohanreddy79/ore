// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/web"
)

// ServiceName is the key under which the gateway sits in Server.Services.
const ServiceName = "gateway"

func init() {
	registry.Register("connect.gateway", Attach)
}

// Attach builds the gateway from the server's services, attaches it as
// Services["gateway"] and mounts its routes. The rate limit can be tuned
// with ORE_GATEWAY_RATE_LIMIT (writes) and ORE_GATEWAY_RATE_WINDOW (a Go
// duration); the defaults are 20 writes per 10 minutes.
func Attach(ctx context.Context, s *web.Server) error {
	g := &Gateway{DB: s.DB, ReposDir: s.ReposDir, Review: s.Review}
	if v, err := strconv.Atoi(os.Getenv("ORE_GATEWAY_RATE_LIMIT")); err == nil && v > 0 {
		g.RateLimit = v
	}
	if v, err := time.ParseDuration(os.Getenv("ORE_GATEWAY_RATE_WINDOW")); err == nil && v > 0 {
		g.RateWindow = v
	}
	if s.Services == nil {
		s.Services = map[string]any{}
	}
	s.Services[ServiceName] = g
	s.AddRoutes(g.Routes(s))
	return nil
}

// From returns the gateway attached to s, if any.
func From(s *web.Server) (*Gateway, bool) {
	g, ok := s.Services[ServiceName].(*Gateway)
	return g, ok
}
