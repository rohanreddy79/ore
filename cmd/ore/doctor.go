// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// check is one line of the doctor report.
type check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | fail
	Detail string `json:"detail"`
}

// report is what `ore doctor` prints.
type report struct {
	Version string  `json:"version"`
	Checks  []check `json:"checks"`
}

// Failed reports whether a required check failed.
func (r report) Failed() bool {
	for _, c := range r.Checks {
		if c.Status == "fail" {
			return true
		}
	}
	return false
}

func (r *report) add(name, status, detail string) {
	r.Checks = append(r.Checks, check{name, status, detail})
}

// doctorEnv lets tests simulate a host without docker or a busy port.
type doctorEnv struct {
	lookPath func(string) (string, error)
	listen   func(addr string) (net.Listener, error)
}

var errDoctorFailed = errors.New("doctor: a required check failed")

// runDoctor implements `ore doctor`.
func runDoctor(args []string, w io.Writer) error {
	cfg, err := loadConfig(parseFlags(args))
	if err != nil {
		return err
	}
	r := doctor(context.Background(), cfg, doctorEnv{})
	fmt.Fprintf(w, "ore %s doctor\n", r.Version)
	for _, c := range r.Checks {
		fmt.Fprintf(w, "  %-5s %-10s %s\n", c.Status, c.Name, c.Detail)
	}
	if r.Failed() {
		fmt.Fprintln(w, "result: FAIL (fix the failed checks above)")
		return errDoctorFailed
	}
	fmt.Fprintln(w, "result: ok")
	return nil
}

// doctor runs every check and returns the report.
func doctor(ctx context.Context, cfg config, env doctorEnv) report {
	if env.lookPath == nil {
		env.lookPath = exec.LookPath
	}
	if env.listen == nil {
		env.listen = func(addr string) (net.Listener, error) { return net.Listen("tcp", addr) }
	}
	r := report{Version: version}

	// git and git http-backend: required
	if p, err := env.lookPath("git"); err != nil {
		r.add("git", "fail", "git is not on PATH; Ore serves repositories with git")
	} else {
		out, _ := exec.CommandContext(ctx, p, "--version").Output()
		r.add("git", "ok", strings.TrimSpace(string(out)))
		if _, err := gitx.HTTPBackendPath(ctx); err != nil {
			r.add("git-http", "fail", "git http-backend not found: "+err.Error())
		} else {
			r.add("git-http", "ok", "git http-backend available")
		}
	}

	// docker: optional; without it pipelines run on the host and deployments are recorded only
	if _, err := env.lookPath("docker"); err != nil {
		if cfg.Executor == "docker" {
			r.add("docker", "warn", "docker is not on PATH: start with --executor host (pipelines on the host, deployments recorded only)")
		} else {
			r.add("docker", "ok", "not needed with --executor "+cfg.Executor)
		}
	} else {
		dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		out, err := exec.CommandContext(dctx, "docker", "version", "--format", "{{.Server.Version}}").Output()
		cancel()
		if err != nil {
			r.add("docker", "warn", "docker CLI present but the engine does not answer: pipelines fall back to --executor host, deployments are recorded only")
		} else {
			r.add("docker", "ok", "engine "+strings.TrimSpace(string(out)))
		}
	}

	// Accept backlog: every connection past the kernel's listen queue is
	// reset or dropped before the server ever sees it. Linux defaults to 4096
	// since 5.4 and macOS to 128; a burst of a few hundred fresh clients (a
	// load balancer draining, a fleet of agents starting) needs more.
	if n, ok := listenBacklog(); ok {
		if n < 1024 {
			r.add("backlog", "warn", fmt.Sprintf("kernel accept backlog is %d; set net.core.somaxconn=4096 (sysctl, or docker run --sysctl) so connection bursts are queued, not reset", n))
		} else {
			r.add("backlog", "ok", fmt.Sprintf("kernel accept backlog %d", n))
		}
	}

	// data directory: must be creatable and writable; the token must be private
	if st, err := os.Stat(cfg.Data); err != nil {
		if err := os.MkdirAll(cfg.Data, 0o755); err != nil {
			r.add("data", "fail", fmt.Sprintf("%s cannot be created: %v", cfg.Data, err))
		} else {
			r.add("data", "ok", cfg.Data+" created")
		}
	} else if !st.IsDir() {
		r.add("data", "fail", cfg.Data+" is not a directory")
	} else if f, err := os.CreateTemp(cfg.Data, ".doctor-*"); err != nil {
		r.add("data", "fail", fmt.Sprintf("%s is not writable: %v", cfg.Data, err))
	} else {
		f.Close()
		os.Remove(f.Name())
		r.add("data", "ok", cfg.Data+" is writable")
	}
	if st, err := os.Stat(filepath.Join(cfg.Data, "token")); err == nil {
		if st.Mode().Perm()&0o077 != 0 {
			r.add("token", "warn", fmt.Sprintf("%s is mode %o; chmod 600 it", filepath.Join(cfg.Data, "token"), st.Mode().Perm()))
		} else {
			r.add("token", "ok", "admin token file is private (0600)")
		}
	} else {
		r.add("token", "ok", "no token file yet; `ore serve` generates one")
	}
	if entries, err := os.ReadDir(filepath.Join(cfg.Data, "repos")); err == nil {
		n := 0
		for _, e := range entries {
			if e.IsDir() {
				if subs, err := os.ReadDir(filepath.Join(cfg.Data, "repos", e.Name())); err == nil {
					n += len(subs)
				}
			}
		}
		r.add("repos", "ok", fmt.Sprintf("%d repositories under %s", n, filepath.Join(cfg.Data, "repos")))
	}

	// database connectivity and migration state
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	db, err := ledger.OpenWith(dctx, cfg.dbTarget(), ledger.Options{NoMigrate: true})
	cancel()
	if err != nil {
		r.add("database", "fail", err.Error())
	} else {
		ms, _ := db.Migrations(ctx)
		pending := 0
		for _, m := range ms {
			if m.AppliedAt == "" {
				pending++
			}
		}
		switch {
		case pending == len(ms):
			r.add("database", "ok", fmt.Sprintf("%s (%s) reachable; new database, %d migrations will apply on first start", db.Target(), db.Dialect(), pending))
		case pending > 0:
			r.add("database", "warn", fmt.Sprintf("%s (%s) reachable; %d migrations pending: run `ore migrate apply`", db.Target(), db.Dialect(), pending))
		default:
			counts, _ := db.Counts(ctx)
			r.add("database", "ok", fmt.Sprintf("%s (%s) reachable; %d migrations applied; %d repositories, %d events", db.Target(), db.Dialect(), len(ms), counts["repos"], counts["events"]))
		}
		db.Close()
	}

	// port
	if ln, err := env.listen(cfg.Addr); err != nil {
		detail := fmt.Sprintf("%s is in use", cfg.Addr)
		host, port, _ := net.SplitHostPort(cfg.Addr)
		if host == "" {
			host = "127.0.0.1"
		}
		hc := &http.Client{Timeout: 2 * time.Second}
		if resp, herr := hc.Get("http://" + net.JoinHostPort(host, port) + "/healthz"); herr == nil {
			resp.Body.Close()
			detail += " by a server that answers /healthz (another ore?)"
		}
		r.add("port", "warn", detail)
	} else {
		ln.Close()
		r.add("port", "ok", cfg.Addr+" is free")
	}

	// disk space under the data directory
	if free, total, err := diskFree(cfg.Data); err == nil {
		gib := float64(free) / (1 << 30)
		detail := fmt.Sprintf("%.1f GiB free of %.1f GiB at %s", gib, float64(total)/(1<<30), cfg.Data)
		switch {
		case free < 100<<20:
			r.add("disk", "fail", detail+": under 100 MiB")
		case free < 1<<30:
			r.add("disk", "warn", detail+": under 1 GiB")
		default:
			r.add("disk", "ok", detail)
		}
	}
	return r
}

func diskFree(path string) (free, total uint64, err error) {
	for p := path; ; p = filepath.Dir(p) {
		var st syscall.Statfs_t
		if err = syscall.Statfs(p, &st); err == nil {
			return uint64(st.Bavail) * uint64(st.Bsize), uint64(st.Blocks) * uint64(st.Bsize), nil
		}
		if p == filepath.Dir(p) {
			return 0, 0, err
		}
	}
}

// listenBacklog reads the kernel's maximum accept queue length.
func listenBacklog() (int, bool) {
	switch runtime.GOOS {
	case "linux":
		b, err := os.ReadFile("/proc/sys/net/core/somaxconn")
		if err != nil {
			return 0, false
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(b)))
		return n, err == nil
	case "darwin", "freebsd":
		out, err := exec.Command("sysctl", "-n", "kern.ipc.somaxconn").Output()
		if err != nil {
			return 0, false
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(out)))
		return n, err == nil
	}
	return 0, false
}
