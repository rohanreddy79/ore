// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/orevcs/ore/internal/connect/gateway"
	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/web"
)

// ServiceName is the key under which the MCP server sits in Server.Services.
const ServiceName = "mcp"

func init() {
	registry.Register("connect.mcp", Attach)
}

// ReadOnlyFromEnv reports whether ORE_MCP_READ_ONLY asks for read-only mode.
func ReadOnlyFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ORE_MCP_READ_ONLY"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Attach mounts /mcp. It needs the gateway (registered as connect.gateway,
// which the registry applies first by name order).
func Attach(ctx context.Context, s *web.Server) error {
	gw, ok := gateway.From(s)
	if !ok {
		return errors.New("connect.mcp: the safe-outputs gateway (connect.gateway) is not attached")
	}
	m := &Server{Web: s, Gateway: gw, ReadOnly: ReadOnlyFromEnv(), Version: os.Getenv("ORE_VERSION")}
	s.Services[ServiceName] = m
	s.AddRoutes(func(mux *http.ServeMux) {
		mux.Handle("/mcp", m)
	})
	return nil
}
