// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

// metrics is the in-process request accounting behind /metrics; pipeline,
// deployment and agent figures are read from the ledger at scrape time so
// they are exact and survive restarts.
type metrics struct {
	mu       sync.Mutex
	requests map[string]int64 // "METHOD code"
	durSum   float64
	durCount int64
	inFlight int64
	started  time.Time
}

func newMetrics() *metrics {
	return &metrics{requests: map[string]int64{}, started: time.Now()}
}

func (m *metrics) begin() {
	m.mu.Lock()
	m.inFlight++
	m.mu.Unlock()
}

func (m *metrics) end(method string, code int, d time.Duration) {
	m.mu.Lock()
	m.inFlight--
	m.requests[method+" "+strconv.Itoa(code)]++
	m.durSum += d.Seconds()
	m.durCount++
	m.mu.Unlock()
}

// handler serves Prometheus text format.
func (m *metrics) handler(db *ledger.DB) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		m.mu.Lock()
		keys := make([]string, 0, len(m.requests))
		for k := range m.requests {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(w, "# HELP ore_build_info Ore version.\n# TYPE ore_build_info gauge\nore_build_info{version=%q} 1\n", version)
		fmt.Fprintf(w, "# HELP ore_uptime_seconds Seconds since the server started.\n# TYPE ore_uptime_seconds gauge\nore_uptime_seconds %d\n", int(time.Since(m.started).Seconds()))
		fmt.Fprintln(w, "# HELP ore_http_requests_total HTTP requests by method and status code.\n# TYPE ore_http_requests_total counter")
		for _, k := range keys {
			var method, code string
			fmt.Sscanf(k, "%s %s", &method, &code)
			fmt.Fprintf(w, "ore_http_requests_total{method=%q,code=%q} %d\n", method, code, m.requests[k])
		}
		fmt.Fprintf(w, "# HELP ore_http_request_duration_seconds Request duration.\n# TYPE ore_http_request_duration_seconds summary\nore_http_request_duration_seconds_sum %g\nore_http_request_duration_seconds_count %d\n", m.durSum, m.durCount)
		fmt.Fprintf(w, "# HELP ore_http_requests_in_flight Requests being served.\n# TYPE ore_http_requests_in_flight gauge\nore_http_requests_in_flight %d\n", m.inFlight)
		m.mu.Unlock()
		if db == nil {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		for _, t := range []struct{ name, table, help string }{
			{"ore_pipeline_runs_total", "pipeline_runs", "Pipeline runs by status."},
			{"ore_deployments_total", "deployments", "Deployments by status."},
			{"ore_agent_runs_total", "agent_runs", "Agent runs by status."},
		} {
			fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", t.name, t.help, t.name)
			rows, err := db.Query(ctx, `SELECT status, COUNT(1) FROM `+t.table+` GROUP BY status ORDER BY status`)
			if err != nil {
				continue
			}
			for rows.Next() {
				var status string
				var n int64
				if rows.Scan(&status, &n) == nil {
					fmt.Fprintf(w, "%s{status=%q} %d\n", t.name, status, n)
				}
			}
			rows.Close()
		}
		var events int64
		if db.QueryRow(ctx, `SELECT COUNT(1) FROM events`).Scan(&events) == nil {
			fmt.Fprintf(w, "# HELP ore_ledger_events_total Events on the ledger.\n# TYPE ore_ledger_events_total counter\nore_ledger_events_total %d\n", events)
		}
		st := db.Stats()
		fmt.Fprintf(w, "# HELP ore_db_open_connections Open database connections.\n# TYPE ore_db_open_connections gauge\nore_db_open_connections %d\n", st.OpenConnections)
	})
}

// statusWriter captures the status code and size for logging and metrics.
type statusWriter struct {
	http.ResponseWriter
	code  int
	bytes int64
}

func (s *statusWriter) WriteHeader(code int) {
	if s.code == 0 {
		s.code = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

// Flush lets streaming handlers (SSE, logs) keep working through the wrapper.
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		if s.code == 0 {
			s.code = http.StatusOK
		}
		f.Flush()
	}
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
