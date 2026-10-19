// SPDX-License-Identifier: AGPL-3.0-only

package identity

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/orevcs/ore/internal/identity/sshd"
	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/web"
)

// DefaultSSHAddr is where git over SSH listens unless ORE_SSH_ADDR says otherwise.
const DefaultSSHAddr = ":2222"

// HostKeyFile is the host key's name inside the data directory.
const HostKeyFile = "ssh_host_ed25519_key"

func init() { registry.Register("identity", Extension) }

// Extension wires identity into the server: the bootstrap owner, the
// Authenticate hook, s.Services["identity"], the REST routes, and the git SSH
// listener (ORE_SSH_ADDR, default :2222, "off" to disable; when the default
// port is busy a free one is used and logged, an explicit address must bind).
func Extension(ctx context.Context, s *web.Server) error {
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	svc := New(s.DB, s.Token)
	svc.Log = log
	if _, err := svc.EnsureBootstrap(ctx); err != nil {
		return fmt.Errorf("identity: bootstrap: %w", err)
	}
	if s.Services == nil {
		s.Services = map[string]any{}
	}
	s.Services["identity"] = svc
	s.Authenticate = svc.Authenticate
	s.AddRoutes(svc.Routes)

	addr, explicit := os.LookupEnv("ORE_SSH_ADDR")
	if !explicit || addr == "" {
		addr, explicit = DefaultSSHAddr, false
	}
	if addr == "off" {
		return nil
	}
	srv := &sshd.Server{DB: s.DB, ReposDir: s.ReposDir, HostKeyPath: filepath.Join(filepath.Dir(s.ReposDir), HostKeyFile), Auth: svc, Log: log}
	ln, err := srv.Listen(addr)
	if err != nil {
		if explicit {
			return fmt.Errorf("identity: ssh listen %s: %w", addr, err)
		}
		log.Warn("identity: default ssh address is busy; using a free port", "addr", addr, "err", err)
		if ln, err = srv.Listen(":0"); err != nil {
			return fmt.Errorf("identity: ssh listen: %w", err)
		}
	}
	s.Services["identity.sshd"] = srv
	log.Info("identity: git over ssh", "addr", ln.Addr().String(), "host_key", srv.HostKeyFingerprint(), "example", "git clone ssh://git@localhost:"+portOf(ln.Addr().String())+"/<org>/<repo>.git")
	go func() {
		if err := srv.Serve(ctx, ln); err != nil {
			log.Error("identity: ssh server", "err", err)
		}
	}()
	return nil
}

func portOf(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return addr
}
