// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

// Live updates are server-sent events: the ledger stream tails the events
// table by polling it every StreamInterval (500 ms), the log streams tail a
// run's log file as it grows. Browsers reconnect on their own and send
// Last-Event-ID, which both streams honor (a sequence number, a byte offset).

const (
	defaultStreamInterval = 500 * time.Millisecond
	streamHeartbeat       = 15 * time.Second
	// maxLogLine bounds one log line in pages and streams; longer lines are cut with a marker.
	maxLogLine = 4096
	// maxLogPage bounds the log text rendered into a page (the stream continues from there).
	maxLogPage = 2 << 20
)

func (s *Server) streamInterval() time.Duration {
	if s.StreamInterval > 0 {
		return s.StreamInterval
	}
	return defaultStreamInterval
}

func (s *Server) mountStreams(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v0/events/stream", s.streamEvents)
	mux.HandleFunc("GET /api/v0/repos/{org}/{name}/runs/{id}/log/stream", func(w http.ResponseWriter, r *http.Request) {
		if s.authenticate(r) == nil {
			jsonErr(w, http.StatusUnauthorized, errors.New("missing or invalid token"))
			return
		}
		run, err := s.DB.PipelineRun(r.Context(), r.PathValue("id"))
		if err != nil {
			jsonErr(w, http.StatusNotFound, err)
			return
		}
		s.streamLog(w, r, func() (string, string, bool) {
			run, err := s.DB.PipelineRun(r.Context(), run.ID)
			if err != nil {
				return "", "", true
			}
			return run.LogPath, run.Status, run.Status != "queued" && run.Status != "running"
		})
	})
	mux.HandleFunc("GET /api/v0/repos/{org}/{name}/agent/runs/{id}/log/stream", func(w http.ResponseWriter, r *http.Request) {
		if s.authenticate(r) == nil {
			jsonErr(w, http.StatusUnauthorized, errors.New("missing or invalid token"))
			return
		}
		run, err := s.DB.AgentRun(r.Context(), r.PathValue("id"))
		if err != nil {
			jsonErr(w, http.StatusNotFound, err)
			return
		}
		s.streamLog(w, r, func() (string, string, bool) {
			run, err := s.DB.AgentRun(r.Context(), run.ID)
			if err != nil {
				return "", "", true
			}
			return run.LogPath, run.Status, run.Status != "running"
		})
	})
}

// sse prepares a server-sent-events response.
func sse(w http.ResponseWriter) (http.Flusher, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, ": connected\n\n")
	f.Flush()
	return f, true
}

func lastEventID(r *http.Request, param string) int64 {
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	n, _ := strconv.ParseInt(r.URL.Query().Get(param), 10, 64)
	return n
}

// streamEvents tails the ledger: ?after=&type=&subject=&subject_prefix=&actor_kind=&org=.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	if s.authenticate(r) == nil {
		jsonErr(w, http.StatusUnauthorized, errors.New("missing or invalid token"))
		return
	}
	q := r.URL.Query()
	after := lastEventID(r, "after")
	if after == 0 && q.Get("after") == "" && q.Get("from_start") == "" {
		after = s.latestSeq(r.Context()) // tail from now unless the client asked for history
	}
	prefix, kind := q.Get("subject_prefix"), q.Get("actor_kind")
	opt := ledger.EventsOptions{Type: q.Get("type"), Subject: q.Get("subject"), OrgID: q.Get("org"), Limit: 200}
	f, ok := sse(w)
	if !ok {
		return
	}
	ticker := time.NewTicker(s.streamInterval())
	defer ticker.Stop()
	last := time.Now()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
		opt.AfterSeq = after
		evs, err := s.DB.Events(r.Context(), opt)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", strings.ReplaceAll(err.Error(), "\n", " "))
			f.Flush()
			return
		}
		sent := false
		for _, ev := range evs {
			after = ev.Seq
			if prefix != "" && !strings.HasPrefix(ev.Subject, prefix) {
				continue
			}
			if kind != "" && ev.OreActor.Kind != kind {
				continue
			}
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.Seq, b)
			sent = true
		}
		if sent {
			f.Flush()
			last = time.Now()
		} else if time.Since(last) > streamHeartbeat {
			io.WriteString(w, ": ping\n\n")
			f.Flush()
			last = time.Now()
		}
	}
}

// streamLog tails a log file. state reports the file path, the run status
// and whether the run is finished; the stream waits for a file that does not
// exist yet, sends each new line as an event whose id is the byte offset
// after it, announces status changes, and ends with a "done" event once the
// run is finished and the file has stopped growing.
func (s *Server) streamLog(w http.ResponseWriter, r *http.Request, state func() (path, status string, done bool)) {
	offset := lastEventID(r, "offset")
	f, ok := sse(w)
	if !ok {
		return
	}
	ticker := time.NewTicker(s.streamInterval())
	defer ticker.Stop()
	lastStatus, lastSent := "", time.Now()
	for {
		path, status, done := state()
		if status != lastStatus {
			fmt.Fprintf(w, "event: status\ndata: %s\n\n", status)
			f.Flush()
			lastStatus, lastSent = status, time.Now()
		}
		lines, next, err := readLogFrom(path, offset, done)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", strings.ReplaceAll(err.Error(), "\n", " "))
			f.Flush()
			return
		}
		for _, l := range lines {
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", next, l)
		}
		if len(lines) > 0 {
			offset = next
			f.Flush()
			lastSent = time.Now()
		} else if done {
			fmt.Fprintf(w, "event: done\ndata: %s\n\n", status)
			f.Flush()
			return
		} else if time.Since(lastSent) > streamHeartbeat {
			io.WriteString(w, ": ping\n\n")
			f.Flush()
			lastSent = time.Now()
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// readLogFrom returns the complete lines after offset and the offset after
// them; a trailing partial line is left for the next call unless final.
func readLogFrom(path string, offset int64, final bool) ([]string, int64, error) {
	if path == "" {
		return nil, offset, os.ErrNotExist
	}
	fh, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer fh.Close()
	if _, err := fh.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	buf, err := io.ReadAll(io.LimitReader(fh, maxLogPage))
	if err != nil || len(buf) == 0 {
		return nil, offset, err
	}
	end := bytes.LastIndexByte(buf, '\n')
	if end < 0 {
		if !final {
			return nil, offset, nil
		}
		end = len(buf) - 1
	} else if final && end < len(buf)-1 {
		end = len(buf) - 1
	}
	chunk := buf[:end+1]
	var lines []string
	for _, l := range bytes.Split(bytes.TrimSuffix(chunk, []byte("\n")), []byte("\n")) {
		lines = append(lines, truncateLine(strings.TrimRight(string(l), "\r")))
	}
	return lines, offset + int64(len(chunk)), nil
}

// truncateLine cuts a line to maxLogLine bytes with a marker.
func truncateLine(l string) string {
	if len(l) <= maxLogLine {
		return l
	}
	cut := maxLogLine
	for cut > 0 && cut < len(l) && (l[cut]&0xC0) == 0x80 { // do not split a UTF-8 sequence
		cut--
	}
	return l[:cut] + fmt.Sprintf(" … [line truncated: %d more bytes]", len(l)-cut)
}

// loadLog reads a log for a page: lines truncated, size bounded, and the
// byte offset the live stream should continue from.
func loadLog(path string) (text string, offset int64, truncated bool) {
	if path == "" {
		return "", 0, false
	}
	fh, err := os.Open(path)
	if err != nil {
		return "", 0, false
	}
	defer fh.Close()
	buf, _ := io.ReadAll(io.LimitReader(fh, maxLogPage+1))
	if len(buf) > maxLogPage {
		buf = buf[:maxLogPage]
		if i := bytes.LastIndexByte(buf, '\n'); i > 0 {
			buf = buf[:i+1]
		}
		truncated = true
	}
	offset = int64(len(buf))
	if end := bytes.LastIndexByte(buf, '\n'); end >= 0 && end < len(buf)-1 {
		buf = buf[:end+1] // a partial last line is left to the stream
		offset = int64(len(buf))
	}
	var b strings.Builder
	for _, l := range bytes.Split(bytes.TrimSuffix(buf, []byte("\n")), []byte("\n")) {
		if len(buf) == 0 {
			break
		}
		b.WriteString(truncateLine(strings.TrimRight(string(l), "\r")))
		b.WriteByte('\n')
	}
	return b.String(), offset, truncated
}
