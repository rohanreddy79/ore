// SPDX-License-Identifier: AGPL-3.0-only

// Package registry lets feature packages wire themselves into the server
// without editing cmd/ore or internal/web: each package registers an
// Extension from an init() in its own register.go, and `ore serve` applies
// every registered extension before building the HTTP handler.
package registry

import (
	"context"
	"sort"
	"sync"

	"github.com/orevcs/ore/internal/web"
)

// Extension configures a feature on the server: add routes with
// s.AddRoutes, replace authentication with s.Authenticate, start background
// workers with ctx, or attach services to s.
type Extension func(ctx context.Context, s *web.Server) error

var (
	mu   sync.Mutex
	exts = map[string]Extension{}
)

// Register adds a named extension. Names must be unique; registering twice panics.
func Register(name string, ext Extension) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := exts[name]; dup {
		panic("registry: duplicate extension " + name)
	}
	exts[name] = ext
}

// Names returns registered extension names in sorted order.
func Names() []string {
	mu.Lock()
	defer mu.Unlock()
	names := make([]string, 0, len(exts))
	for n := range exts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Apply runs every extension in name order.
func Apply(ctx context.Context, s *web.Server) error {
	for _, n := range Names() {
		mu.Lock()
		ext := exts[n]
		mu.Unlock()
		if err := ext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}
