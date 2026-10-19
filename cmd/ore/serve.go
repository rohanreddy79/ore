// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/orevcs/ore/internal/agent"
	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/githttp"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/review"
	"github.com/orevcs/ore/internal/web"
)

// server is one `ore serve` process: the ledger, the web server with its
// extensions, request logging and metrics, the backup worker, and a graceful
// shutdown that drains running pipelines and agent runs.
type server struct {
	cfg     config
	log     *slog.Logger
	db      *ledger.DB
	web     *web.Server
	http    *http.Server
	ln      net.Listener
	metrics *metrics
	token   string

	cancelBackground context.CancelFunc
	done             chan struct{}
}

// newServer opens everything and starts listening; serve() then blocks.
func newServer(ctx context.Context, cfg config, logOut io.Writer) (*server, error) {
	if err := os.MkdirAll(cfg.Data, 0o755); err != nil {
		return nil, err
	}
	logger := newLogger(cfg, logOut)
	token, err := loadOrCreateToken(cfg.Token, filepath.Join(cfg.Data, "token"))
	if err != nil {
		return nil, err
	}
	db, err := ledger.Open(ctx, cfg.dbTarget())
	if err != nil {
		return nil, err
	}
	backend, err := gitx.HTTPBackendPath(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	reposDir, logsDir := filepath.Join(cfg.Data, "repos"), filepath.Join(cfg.Data, "logs")
	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		db.Close()
		return nil, err
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		host, port, _ := net.SplitHostPort(cfg.Addr)
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = cfg.PublicHost
		}
		baseURL = "http://" + net.JoinHostPort(host, port)
	}
	ws := &web.Server{
		DB: db, ReposDir: reposDir, LogsDir: logsDir, Token: token, BaseURL: baseURL, Log: logger,
		Git:      &githttp.Handler{DB: db, ReposDir: reposDir, Backend: backend, Log: logger},
		Runner:   &pipelines.Runner{DB: db, ReposDir: reposDir, LogsDir: logsDir, Executor: cfg.Executor},
		Deployer: &delivery.Deployer{DB: db, ReposDir: reposDir, Executor: deployExecutor(cfg.Executor), PublicHost: cfg.PublicHost},
		Review:   &review.Service{DB: db, ReposDir: reposDir},
	}
	ws.Loop = &agent.Loop{DB: db, ReposDir: reposDir, LogsDir: logsDir, Runner: ws.Runner, Deployer: ws.Deployer, Review: ws.Review, AdapterCmd: cfg.AgentCmd, AdapterName: cfg.AgentName}
	ws.Services = map[string]any{}
	m := newMetrics()
	ws.AddRoutes(func(mux *http.ServeMux) { mux.Handle("GET /metrics", m.handler(db)) })

	// runs a crash left behind are marked interrupted before anything new starts
	if n := markInterrupted(ctx, db, logger, "server restarted"); n > 0 {
		logger.Warn("recovered interrupted runs from a previous process", "count", n)
	}

	bgCtx, cancel := context.WithCancel(context.Background())
	if err := registry.Apply(bgCtx, ws); err != nil {
		cancel()
		db.Close()
		return nil, fmt.Errorf("extensions: %w", err)
	}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		cancel()
		db.Close()
		return nil, err
	}
	s := &server{cfg: cfg, log: logger, db: db, web: ws, ln: ln, metrics: m, token: token, cancelBackground: cancel, done: make(chan struct{})}
	s.http = &http.Server{Addr: cfg.Addr, Handler: s.middleware(ws.Handler()), ReadHeaderTimeout: 10 * time.Second}
	if cfg.BackupTo != "" {
		go s.backupWorker(bgCtx)
	}
	return s, nil
}

// middleware adds request ids, structured request logs and metrics.
func (s *server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 128 {
			id = ledger.NewID("req")
		}
		w.Header().Set("X-Request-ID", id)
		sw := &statusWriter{ResponseWriter: w}
		s.metrics.begin()
		next.ServeHTTP(sw, r)
		d := time.Since(start)
		if sw.code == 0 {
			sw.code = http.StatusOK
		}
		s.metrics.end(r.Method, sw.code, d)
		level := slog.LevelInfo
		if r.URL.Path == "/healthz" || r.URL.Path == "/metrics" {
			level = slog.LevelDebug
		} else if sw.code >= 500 {
			level = slog.LevelError
		}
		s.log.Log(r.Context(), level, "request", "request_id", id, "method", r.Method, "path", r.URL.Path, "status", sw.code, "bytes", sw.bytes, "duration_ms", d.Milliseconds(), "remote", remoteIP(r))
	})
}

func remoteIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// serve blocks until shutdown is called.
func (s *server) serve() error {
	defer close(s.done)
	if err := s.http.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// addr is the bound address.
func (s *server) addr() string { return s.ln.Addr().String() }

// shutdown stops accepting requests, waits up to the drain timeout for
// running pipelines and agent runs, marks the rest interrupted on the ledger,
// stops background workers and closes the database.
func (s *server) shutdown(ctx context.Context) error {
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := s.http.Shutdown(hctx)
	s.drain(ctx)
	s.cancelBackground()
	<-s.done
	if cerr := s.db.Close(); err == nil {
		err = cerr
	}
	return err
}

// drain waits for in-flight work, then marks what is left interrupted.
func (s *server) drain(ctx context.Context) {
	deadline := time.Now().Add(s.cfg.DrainTimeout)
	for {
		n := countRunning(ctx, s.db)
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			s.log.Warn("drain timeout; marking runs interrupted", "running", n)
			markInterrupted(ctx, s.db, s.log, "server shutdown")
			return
		}
		s.log.Info("draining running work before exit", "running", n, "deadline", deadline.Format(time.RFC3339))
		select {
		case <-ctx.Done():
			markInterrupted(context.Background(), s.db, s.log, "server shutdown")
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func countRunning(ctx context.Context, db *ledger.DB) int {
	var a, b int
	_ = db.QueryRow(ctx, `SELECT COUNT(1) FROM pipeline_runs WHERE status IN ('running','queued')`).Scan(&a)
	_ = db.QueryRow(ctx, `SELECT COUNT(1) FROM agent_runs WHERE status = 'running'`).Scan(&b)
	return a + b
}

// markInterrupted ends running pipeline and agent runs on the ledger with the
// status "interrupted" and the given reason, returning how many it touched.
func markInterrupted(ctx context.Context, db *ledger.DB, log *slog.Logger, reason string) int {
	n := 0
	rows, err := db.Query(ctx, `SELECT id FROM pipeline_runs WHERE status IN ('running','queued')`)
	if err == nil {
		var ids []string
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			run, err := db.PipelineRun(ctx, id)
			if err != nil {
				continue
			}
			repo, err := db.RepoByID(ctx, run.RepoID)
			if err != nil {
				continue
			}
			if err := db.UpdatePipelineRun(ctx, repo, &run, "interrupted", "", ledger.System); err != nil {
				log.Warn("mark pipeline run interrupted", "id", id, "err", err)
				continue
			}
			n++
		}
	}
	rows, err = db.Query(ctx, `SELECT id FROM agent_runs WHERE status = 'running'`)
	if err == nil {
		var ids []string
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			run, err := db.AgentRun(ctx, id)
			if err != nil {
				continue
			}
			repo, err := db.RepoByID(ctx, run.RepoID)
			if err != nil {
				continue
			}
			run.Status, run.Error = "interrupted", reason
			if err := db.UpdateAgentRun(ctx, repo, &run, ledger.System); err != nil {
				log.Warn("mark agent run interrupted", "id", id, "err", err)
				continue
			}
			n++
		}
	}
	return n
}

// banner is what `ore serve` prints on start.
func (s *server) banner() string {
	return fmt.Sprintf("ore %s serving on %s\n  data:     %s\n  database: %s (%s)\n  executor: %s (pipelines) / %s (deployments)\n  agent:    %s\n  features: %s\n  logs:     %s at %s; /metrics for Prometheus\n  ui:       %s/login?token=%s\n  api:      %s/api/v0  (Authorization: Bearer %s)\n  git:      git clone %s/<org>/<repo>.git  (user: anything, password: the token)\n",
		version, s.addr(), s.cfg.Data, s.db.Target(), s.db.Dialect(), s.cfg.Executor, deployExecutor(s.cfg.Executor), s.cfg.AgentName, strings.Join(registry.Names(), ", "), s.cfg.LogFormat, s.cfg.LogLevel, s.web.BaseURL, s.token, s.web.BaseURL, s.token, s.web.BaseURL)
}

func runServe(args []string) error {
	cfg, err := loadConfig(parseFlags(args))
	if err != nil {
		return err
	}
	ctx := context.Background()
	s, err := newServer(ctx, cfg, os.Stderr)
	if err != nil {
		return err
	}
	if !cfg.Quiet {
		fmt.Fprint(os.Stderr, s.banner())
	}
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		sig := <-stop
		s.log.Info("shutting down", "signal", sig.String(), "drain_timeout", cfg.DrainTimeout.String())
		ctx, cancel := context.WithTimeout(context.Background(), cfg.DrainTimeout+15*time.Second)
		defer cancel()
		if err := s.shutdown(ctx); err != nil {
			s.log.Error("shutdown", "err", err)
		}
	}()
	return s.serve()
}

func deployExecutor(pipelineExecutor string) string {
	if pipelineExecutor == "docker" {
		return "docker"
	}
	return "none"
}
