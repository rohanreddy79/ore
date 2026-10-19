// SPDX-License-Identifier: AGPL-3.0-only

package identity

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/web"
)

func extensionServer(t *testing.T) *web.Server {
	t.Helper()
	dir := t.TempDir()
	db, err := ledger.Open(context.Background(), filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &web.Server{DB: db, ReposDir: filepath.Join(dir, "repos"), Token: "tok", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestExtensionSSHAddress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	t.Setenv("ORE_SSH_ADDR", "off")
	s := extensionServer(t)
	if err := Extension(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Services["identity.sshd"]; ok {
		t.Fatal("sshd started although ORE_SSH_ADDR=off")
	}
	if s.Authenticate == nil || s.Services["identity"] == nil {
		t.Fatal("hook or service not attached")
	}
	if _, err := s.Services["identity"].(*Service).UserByName(ctx, "admin"); err != nil {
		t.Fatalf("bootstrap owner: %v", err)
	}

	// an explicit address that cannot bind is an error, not a silent fallback
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	t.Setenv("ORE_SSH_ADDR", busy.Addr().String())
	if err := Extension(ctx, extensionServer(t)); err == nil {
		t.Fatal("busy explicit address accepted")
	}

	// a free explicit address binds and is exposed as a service
	t.Setenv("ORE_SSH_ADDR", "127.0.0.1:0")
	s = extensionServer(t)
	if err := Extension(ctx, s); err != nil {
		t.Fatal(err)
	}
	if s.Services["identity.sshd"] == nil {
		t.Fatal("sshd not attached")
	}
}
