// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Health is the result of probing a deployment's health path.
type Health struct {
	URL            string `json:"url"`
	Healthy        bool   `json:"healthy"`
	Skipped        bool   `json:"skipped,omitempty"` // target none: nothing to probe
	Attempts       int    `json:"attempts"`
	LastStatus     int    `json:"last_status,omitempty"`
	LastError      string `json:"last_error,omitempty"`
	ElapsedMS      int64  `json:"elapsed_ms"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// probe polls url until it answers 2xx/3xx or the timeout elapses.
func probe(ctx context.Context, url string, timeout, interval time.Duration) Health {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	h := Health{URL: url, TimeoutSeconds: int(timeout / time.Second)}
	start := time.Now()
	deadline := start.Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for {
		h.Attempts++
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			h.LastError = err.Error()
			break
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			h.LastStatus, h.LastError = resp.StatusCode, ""
			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				h.Healthy = true
				break
			}
			h.LastError = fmt.Sprintf("status %d", resp.StatusCode)
		} else {
			h.LastError = err.Error()
		}
		if ctx.Err() != nil || !time.Now().Add(interval).Before(deadline) {
			break
		}
		time.Sleep(interval)
	}
	h.ElapsedMS = time.Since(start).Milliseconds()
	return h
}

// String summarizes the probe for logs and issues.
func (h Health) String() string {
	if h.Skipped {
		return "health check skipped (target none)"
	}
	if h.Healthy {
		return fmt.Sprintf("healthy after %d attempt(s), %d ms", h.Attempts, h.ElapsedMS)
	}
	return fmt.Sprintf("unhealthy after %d attempt(s) in %d ms (timeout %ds): %s", h.Attempts, h.ElapsedMS, h.TimeoutSeconds, h.LastError)
}
