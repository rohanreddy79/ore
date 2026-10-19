// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Environment variables read by ConfigFromEnv.
const (
	EnvToken          = "ORE_GITHUB_TOKEN"           // personal access token or GitHub App installation token
	EnvTokenFile      = "ORE_GITHUB_TOKEN_FILE"      // alternative: a file re-read on every request (rotating installation tokens)
	EnvAPI            = "ORE_GITHUB_API"             // API base URL; default https://api.github.com (tests point it at a fake)
	EnvSyncInterval   = "ORE_GITHUB_SYNC_INTERVAL"   // reconciliation interval; default 5m; "off" disables the worker
	EnvStatusInterval = "ORE_GITHUB_STATUS_INTERVAL" // how often pipeline results are posted as statuses; default 5s
)

// Defaults.
const (
	DefaultAPI            = "https://api.github.com"
	DefaultSyncInterval   = 5 * time.Minute
	DefaultStatusInterval = 5 * time.Second
	// StatusContext is the commit status context Ore posts under.
	StatusContext = "ore/pipeline"
)

// Config configures the adapter.
type Config struct {
	Token     string
	TokenFile string
	API       string
	// SyncInterval is the reconciliation cadence; 0 means DefaultSyncInterval, < 0 disables the worker.
	SyncInterval time.Duration
	// StatusInterval is the status reporter cadence; 0 means DefaultStatusInterval.
	StatusInterval time.Duration
	// Now is the clock (tests inject one).
	Now func() time.Time
}

// ConfigFromEnv reads the configuration from the environment.
func ConfigFromEnv() (Config, error) {
	cfg := Config{Token: strings.TrimSpace(os.Getenv(EnvToken)), TokenFile: os.Getenv(EnvTokenFile), API: strings.TrimRight(os.Getenv(EnvAPI), "/")}
	d, err := parseInterval(os.Getenv(EnvSyncInterval), DefaultSyncInterval)
	if err != nil {
		return cfg, fmt.Errorf("%s: %w", EnvSyncInterval, err)
	}
	cfg.SyncInterval = d
	d, err = parseInterval(os.Getenv(EnvStatusInterval), DefaultStatusInterval)
	if err != nil {
		return cfg, fmt.Errorf("%s: %w", EnvStatusInterval, err)
	}
	cfg.StatusInterval = d
	return cfg, nil
}

func parseInterval(s string, def time.Duration) (time.Duration, error) {
	s = strings.TrimSpace(s)
	switch s {
	case "":
		return def, nil
	case "off", "0", "false", "none":
		return -1, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return -1, nil
	}
	return d, nil
}

// Configured reports whether a token source is set.
func (c Config) Configured() bool { return c.Token != "" || c.TokenFile != "" }
