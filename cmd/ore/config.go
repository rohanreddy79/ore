// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/ledger/pg"
)

// config is the effective server configuration: flags win over environment
// variables, which win over defaults. `ore config` prints it with secrets
// redacted.
type config struct {
	Data       string `json:"data"`
	Addr       string `json:"addr"`
	DB         string `json:"db"`
	Executor   string `json:"executor"`
	AgentCmd   string `json:"agent_cmd"`
	AgentName  string `json:"agent_name"`
	PublicHost string `json:"public_host"`
	BaseURL    string `json:"base_url"`
	Token      string `json:"token"`
	LogFormat  string `json:"log_format"`
	LogLevel   string `json:"log_level"`

	BackupTo       string        `json:"backup_to"`
	BackupInterval time.Duration `json:"backup_interval"`
	BackupKeep     int           `json:"backup_keep"`
	DrainTimeout   time.Duration `json:"drain_timeout"`
	Quiet          bool          `json:"quiet"`
}

// loadConfig reads flags and environment.
func loadConfig(fl *flags) (config, error) {
	c := config{
		Data:       fl.str("data", envOr("ORE_DATA", "ore-data")),
		Addr:       fl.str("addr", envOr("ORE_ADDR", ":7777")),
		DB:         fl.str("db", os.Getenv("ORE_DB_URL")),
		Executor:   fl.str("executor", envOr("ORE_EXECUTOR", "docker")),
		AgentCmd:   fl.str("agent-cmd", os.Getenv("ORE_AGENT_CMD")),
		AgentName:  fl.str("agent-name", envOr("ORE_AGENT_NAME", "claude-code")),
		PublicHost: fl.str("public-host", envOr("ORE_PUBLIC_HOST", "localhost")),
		BaseURL:    fl.str("base-url", os.Getenv("ORE_BASE_URL")),
		Token:      fl.str("token", os.Getenv("ORE_TOKEN")),
		LogFormat:  fl.str("log-format", envOr("ORE_LOG_FORMAT", "text")),
		LogLevel:   fl.str("log-level", envOr("ORE_LOG_LEVEL", "info")),
		BackupTo:   fl.str("backup-to", os.Getenv("ORE_BACKUP_TO")),
		BackupKeep: fl.int("backup-keep", envInt("ORE_BACKUP_KEEP", 7)),
		Quiet:      fl.bool("quiet"),
	}
	var err error
	if c.BackupInterval, err = parseDuration(fl.str("backup-interval", envOr("ORE_BACKUP_INTERVAL", "24h"))); err != nil {
		return c, fmt.Errorf("backup interval: %w", err)
	}
	if c.DrainTimeout, err = parseDuration(fl.str("drain-timeout", envOr("ORE_DRAIN_TIMEOUT", "30s"))); err != nil {
		return c, fmt.Errorf("drain timeout: %w", err)
	}
	switch c.LogFormat {
	case "text", "json":
	default:
		return c, fmt.Errorf("--log-format must be text or json, got %q", c.LogFormat)
	}
	if _, err := parseLevel(c.LogLevel); err != nil {
		return c, err
	}
	switch c.Executor {
	case "docker", "host", "none":
	default:
		return c, fmt.Errorf("--executor must be docker, host or none, got %q", c.Executor)
	}
	return c, nil
}

// dbTarget is what ledger.Open receives: the configured URL or the SQLite file in the data directory.
func (c config) dbTarget() string {
	if c.DB != "" {
		return c.DB
	}
	return filepath.Join(c.Data, "ore.db")
}

// dbDialect names the database kind without opening it.
func (c config) dbDialect() string {
	if pg.IsURL(c.dbTarget()) {
		return "postgres"
	}
	return "sqlite"
}

// redacted returns the configuration for display, with secrets hidden.
func (c config) redacted() map[string]any {
	out := map[string]any{}
	b, _ := json.Marshal(c)
	_ = json.Unmarshal(b, &out)
	out["db"] = pg.Redact(c.dbTarget())
	out["db_dialect"] = c.dbDialect()
	if c.Token != "" {
		out["token"] = "REDACTED"
	} else {
		out["token"] = "(generated on first start, stored in " + filepath.Join(c.Data, "token") + ")"
	}
	out["backup_interval"] = c.BackupInterval.String()
	out["drain_timeout"] = c.DrainTimeout.String()
	out["version"] = version
	out["sources"] = map[string]string{"flags": "--data --addr --db --executor --agent-cmd --agent-name --public-host --base-url --token --log-format --log-level --backup-to --backup-interval --backup-keep --drain-timeout", "env": "ORE_DATA ORE_ADDR ORE_DB_URL ORE_EXECUTOR ORE_AGENT_CMD ORE_AGENT_NAME ORE_PUBLIC_HOST ORE_BASE_URL ORE_TOKEN ORE_LOG_FORMAT ORE_LOG_LEVEL ORE_BACKUP_TO ORE_BACKUP_INTERVAL ORE_BACKUP_KEEP ORE_DRAIN_TIMEOUT"}
	return out
}

// runConfig prints the effective configuration.
func runConfig(args []string, w io.Writer) error {
	cfg, err := loadConfig(parseFlags(args))
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(cfg.redacted())
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("--log-level must be debug, info, warn or error, got %q", s)
}

// newLogger builds the process logger from --log-format and --log-level.
func newLogger(c config, w io.Writer) *slog.Logger {
	level, _ := parseLevel(c.LogLevel)
	opts := &slog.HandlerOptions{Level: level}
	if c.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

func parseDuration(s string) (time.Duration, error) {
	if s == "" || s == "0" {
		return 0, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return time.ParseDuration(s)
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
