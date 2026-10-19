// SPDX-License-Identifier: AGPL-3.0-only

// Package environments wires the delivery slice into the server: environments
// with policy and approvals, policy-gated deployment requests, remote targets,
// health-gated rollouts with automatic rollback, preview TTLs and the DORA
// view. It lives beside package delivery because web imports delivery, so the
// registration (which needs web.Server) cannot sit in delivery itself.
package environments

import (
	"context"
	"errors"
	"net/http"

	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/web"
)

// Name is the registry name of this extension.
const Name = "delivery.environments"

func init() {
	registry.Register(Name, Extension)
}

// Extension attaches the environments service to the server's Deployer, mounts
// the routes and starts the background reaper.
func Extension(ctx context.Context, s *web.Server) error {
	if s.DB == nil {
		return errors.New("delivery.environments: server has no database")
	}
	svc := &delivery.Service{DB: s.DB, ReposDir: s.ReposDir, Deployer: s.Deployer, Log: s.Log, Secrets: lazySecrets{s}}
	if s.Deployer != nil {
		s.Deployer.Environments = svc
	}
	if s.Services == nil {
		s.Services = map[string]any{}
	}
	s.Services[Name] = svc
	s.AddRoutes(func(mux *http.ServeMux) { Routes(mux, s, svc) })
	go svc.Run(ctx)
	return nil
}

// lazySecrets resolves secret: key references through the secrets store
// another slice registers as Services["secrets"], looked up at call time so
// registration order does not matter.
type lazySecrets struct{ s *web.Server }

func (l lazySecrets) Secret(ctx context.Context, orgID, name string) (string, error) {
	if src, ok := l.s.Services["secrets"].(delivery.SecretSource); ok {
		return src.Secret(ctx, orgID, name)
	}
	return "", errors.New("no secrets store is registered (Services[\"secrets\"])")
}
