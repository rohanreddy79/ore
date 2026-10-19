// SPDX-License-Identifier: AGPL-3.0-only

package webhooks

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/web"
)

// ServiceName is the key under which the service sits in Server.Services.
const ServiceName = "webhooks"

func init() {
	registry.Register("connect.webhooks", Attach)
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Attach builds the service from the environment, mounts the API and starts
// the dispatcher and the exporter for the life of ctx.
//
//	ORE_WEBHOOKS_ALLOW_PRIVATE=1   allow endpoints on loopback/private networks
//	ORE_WEBHOOKS_TIMEOUT=30s       per-attempt timeout
//	ORE_WEBHOOKS_POLL=2s           ledger scan interval
//	ORE_AUDIT_DIR=/path            file export of every event (daily files)
//	ORE_AUDIT_S3_BUCKET, ORE_AUDIT_S3_ENDPOINT, ORE_AUDIT_S3_REGION, ORE_AUDIT_S3_PREFIX,
//	ORE_AUDIT_S3_ACCESS_KEY, ORE_AUDIT_S3_SECRET_KEY   S3 export (credentials stay in the environment)
//	ORE_AUDIT_BATCH=500, ORE_AUDIT_INTERVAL=10s, ORE_AUDIT_MAX_DELAY=60s
func Attach(ctx context.Context, s *web.Server) error {
	svc := New(s)
	if s.Services == nil {
		s.Services = map[string]any{}
	}
	s.Services[ServiceName] = svc
	s.AddRoutes(svc.Routes(s))
	if err := svc.ensureEnvExports(ctx); err != nil {
		return err
	}
	svc.Start(ctx)
	return nil
}

// New builds a service with settings from the environment (not started).
func New(s *web.Server) *Service {
	st := &Store{DB: s.DB}
	d := &Dispatcher{Store: st, BaseURL: s.BaseURL, AllowPrivate: envBool("ORE_WEBHOOKS_ALLOW_PRIVATE"), Log: s.Log, Client: &http.Client{Timeout: 30 * time.Second}}
	if v, err := time.ParseDuration(os.Getenv("ORE_WEBHOOKS_TIMEOUT")); err == nil && v > 0 {
		d.Client.Timeout = v
	}
	if v, err := time.ParseDuration(os.Getenv("ORE_WEBHOOKS_POLL")); err == nil && v > 0 {
		d.Poll = v
	}
	ex := &Exporter{Store: st, Log: s.Log, Credentials: func() (string, string) {
		return os.Getenv("ORE_AUDIT_S3_ACCESS_KEY"), os.Getenv("ORE_AUDIT_S3_SECRET_KEY")
	}}
	if v, err := strconv.Atoi(os.Getenv("ORE_AUDIT_BATCH")); err == nil && v > 0 {
		ex.Batch = v
	}
	if v, err := time.ParseDuration(os.Getenv("ORE_AUDIT_INTERVAL")); err == nil && v > 0 {
		ex.Interval = v
	}
	if v, err := time.ParseDuration(os.Getenv("ORE_AUDIT_MAX_DELAY")); err == nil && v > 0 {
		ex.MaxDelay = v
	}
	return &Service{Store: st, Dispatcher: d, Exporter: ex, Web: s}
}

// Start runs the dispatcher and the exporter until ctx ends or Stop is called.
func (svc *Service) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{}, 2)
	svc.stop, svc.done = cancel, done
	go func() { svc.Dispatcher.Run(ctx); done <- struct{}{} }()
	go func() { svc.Exporter.Run(ctx); done <- struct{}{} }()
}

// Stop ends the workers and waits for them (tests call it before closing the database).
func (svc *Service) Stop() {
	if svc.stop == nil {
		return
	}
	svc.stop()
	<-svc.done
	<-svc.done
	svc.stop = nil
}

// ensureEnvExports creates the exports the environment asks for, once.
func (svc *Service) ensureEnvExports(ctx context.Context) error {
	existing, err := svc.Store.AuditExports(ctx, false)
	if err != nil {
		return err
	}
	has := func(kind, key string) bool {
		for _, a := range existing {
			if a.Kind != kind {
				continue
			}
			var m map[string]string
			json.Unmarshal(a.Config, &m)
			if m["dir"] == key || m["bucket"] == key {
				return true
			}
		}
		return false
	}
	now := ledger.Now()
	if dir := os.Getenv("ORE_AUDIT_DIR"); dir != "" && !has("file", dir) {
		cfg, _ := json.Marshal(FileConfig{Dir: dir})
		a := AuditExport{ID: ledger.NewID("aexp"), Kind: "file", Config: cfg, FilterTypes: []string{}, Enabled: true, CreatedAt: now, UpdatedAt: now}
		if err := svc.Store.InsertAuditExport(ctx, &a, ledger.System); err != nil {
			return err
		}
	}
	if bucket := os.Getenv("ORE_AUDIT_S3_BUCKET"); bucket != "" && !has("s3", bucket) {
		cfg, _ := json.Marshal(S3Config{Bucket: bucket, Endpoint: os.Getenv("ORE_AUDIT_S3_ENDPOINT"), Region: os.Getenv("ORE_AUDIT_S3_REGION"), Prefix: os.Getenv("ORE_AUDIT_S3_PREFIX")})
		a := AuditExport{ID: ledger.NewID("aexp"), Kind: "s3", Config: cfg, FilterTypes: []string{}, Enabled: true, CreatedAt: now, UpdatedAt: now}
		if err := svc.Store.InsertAuditExport(ctx, &a, ledger.System); err != nil {
			return err
		}
	}
	return nil
}
