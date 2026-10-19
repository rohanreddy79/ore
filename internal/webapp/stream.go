// SPDX-License-Identifier: AGPL-3.0-only

package webapp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/web"
)

// Poll intervals: the ledger is SQLite without notifications in v0, so the
// streams poll; the intervals are short enough to feel live and cheap enough
// to leave open for hours.
var (
	eventPoll     = 400 * time.Millisecond
	logPoll       = 250 * time.Millisecond
	heartbeatGap  = 15 * time.Second
	logBatchLines = 500
)

type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSE(w http.ResponseWriter) (*sseWriter, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &sseWriter{w: w, f: f}, true
}

func (s *sseWriter) event(id, name string, data []byte) {
	if id != "" {
		fmt.Fprintf(s.w, "id: %s\n", id)
	}
	if name != "" {
		fmt.Fprintf(s.w, "event: %s\n", name)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fmt.Fprintf(s.w, "data: %s\n", line)
	}
	io.WriteString(s.w, "\n")
	s.f.Flush()
}

func (s *sseWriter) comment(text string) {
	fmt.Fprintf(s.w, ": %s\n\n", text)
	s.f.Flush()
}

// lastEventID reads the resume point from Last-Event-ID or ?after=.
func lastEventID(r *http.Request) int64 {
	if h := r.Header.Get("Last-Event-ID"); h != "" {
		if n, err := strconv.ParseInt(h, 10, 64); err == nil {
			return n
		}
	}
	n, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	return n
}

// streamEvents tails the ledger as server-sent events. Each message is one
// CloudEvent (the same JSON as GET /api/v0/events) with the sequence number as
// the SSE id, so a reconnecting client resumes without gaps or duplicates.
// Filters: type (exact or prefix ending in "."), subject (exact) and
// subject_prefix (an org/repo path).
func streamEvents(s *web.Server, w http.ResponseWriter, r *http.Request) error {
	sse, ok := newSSE(w)
	if !ok {
		return fmt.Errorf("streaming unsupported")
	}
	q := r.URL.Query()
	after := lastEventID(r)
	if after == 0 && q.Get("after") == "" {
		// Default to "from now": the client loads history over REST.
		if evs, err := s.DB.Events(r.Context(), ledger.EventsOptions{Limit: 1000}); err == nil && len(evs) > 0 {
			after = evs[len(evs)-1].Seq
			for after < evs[len(evs)-1].Seq {
				after = evs[len(evs)-1].Seq
			}
			// Events() is capped at 1000 rows in sequence order; walk to the end.
			for len(evs) == 1000 {
				evs, err = s.DB.Events(r.Context(), ledger.EventsOptions{AfterSeq: after, Limit: 1000})
				if err != nil || len(evs) == 0 {
					break
				}
				after = evs[len(evs)-1].Seq
			}
		}
	}
	prefix := q.Get("subject_prefix")
	opt := ledger.EventsOptions{Type: q.Get("type"), Subject: q.Get("subject"), Limit: 500}
	sse.comment("ore ledger stream")
	sse.event("", "ready", []byte(fmt.Sprintf(`{"after":%d}`, after)))
	ticker := time.NewTicker(eventPoll)
	defer ticker.Stop()
	lastBeat := time.Now()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		opt.AfterSeq = after
		evs, err := s.DB.Events(ctx, opt)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			sse.event("", "error", []byte(`{"error":"ledger read failed"}`))
			continue
		}
		for _, ev := range evs {
			after = ev.Seq
			if prefix != "" && !strings.HasPrefix(ev.Subject, prefix) {
				continue
			}
			b, _ := json.Marshal(ev)
			sse.event(strconv.FormatInt(ev.Seq, 10), "", b)
			lastBeat = time.Now()
		}
		if time.Since(lastBeat) > heartbeatGap {
			sse.comment("ping")
			lastBeat = time.Now()
		}
	}
}

// streamLog tails a run's log file. Messages are batches of lines
// (event "lines", data = JSON array of strings, id = the number of the last
// line sent, 1-based), followed by "status" events whenever the run's status
// changes and a final "done" event once the run finished and the file is
// drained. ?after=N (or Last-Event-ID) skips the first N lines.
func streamLog(w http.ResponseWriter, r *http.Request, logPath string, status func(ctx context.Context) (string, bool)) error {
	sse, ok := newSSE(w)
	if !ok {
		return fmt.Errorf("streaming unsupported")
	}
	ctx := r.Context()
	skip := lastEventID(r)
	var sent int64
	var offset int64
	var partial string
	lastStatus := ""
	lastBeat := time.Now()
	emitStatus := func() (bool, bool) {
		st, finished := status(ctx)
		if st != lastStatus {
			lastStatus = st
			sse.event("", "status", []byte(fmt.Sprintf(`{"status":%q}`, st)))
		}
		return finished, true
	}
	readMore := func() (int, error) {
		if logPath == "" {
			return 0, nil
		}
		f, err := os.Open(logPath)
		if err != nil {
			if os.IsNotExist(err) {
				return 0, nil
			}
			return 0, err
		}
		defer f.Close()
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return 0, err
		}
		rd := bufio.NewReaderSize(f, 64<<10)
		var batch []string
		n := 0
		flush := func() {
			if len(batch) == 0 {
				return
			}
			b, _ := json.Marshal(batch)
			sse.event(strconv.FormatInt(sent, 10), "lines", b)
			batch = batch[:0]
			lastBeat = time.Now()
		}
		for {
			chunk, err := rd.ReadString('\n')
			offset += int64(len(chunk))
			if err != nil {
				// A trailing partial line waits for its newline.
				if len(chunk) > 0 {
					offset -= int64(len(chunk))
				}
				break
			}
			line := partial + strings.TrimRight(chunk, "\r\n")
			partial = ""
			n++
			sent++
			if sent <= skip {
				continue
			}
			batch = append(batch, line)
			if len(batch) >= logBatchLines {
				flush()
			}
		}
		flush()
		return n, nil
	}
	finished, _ := emitStatus()
	if _, err := readMore(); err != nil {
		return err
	}
	ticker := time.NewTicker(logPoll)
	defer ticker.Stop()
	idle := 0
	for {
		if finished {
			// Drain once more, then close.
			if n, _ := readMore(); n == 0 {
				idle++
			}
			if idle >= 2 {
				sse.event("", "done", []byte(fmt.Sprintf(`{"status":%q,"lines":%d}`, lastStatus, sent)))
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		if _, err := readMore(); err != nil {
			return err
		}
		finished, _ = emitStatus()
		if time.Since(lastBeat) > heartbeatGap {
			sse.comment("ping")
			lastBeat = time.Now()
		}
	}
}
