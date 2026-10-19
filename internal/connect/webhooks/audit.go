// SPDX-License-Identifier: AGPL-3.0-only

package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

// Exporter streams the ledger to audit exports: newline-delimited
// CloudEvents JSON appended to a daily-rotating file, or PUT to an
// S3-compatible bucket in objects of Batch events or every MaxDelay.
type Exporter struct {
	Store    *Store
	Interval time.Duration // tick (default 10 s)
	Batch    int           // events per S3 object and per file append (default 500)
	MaxDelay time.Duration // S3: flush a partial batch after this (default 60 s)
	Client   *http.Client
	Now      func() time.Time
	Log      *slog.Logger
	// Credentials returns the S3 access key and secret (from the environment).
	Credentials func() (accessKey, secretKey string)

	mu      sync.Mutex
	pending map[string]time.Time // export id -> when a partial S3 batch was first seen
}

// FileConfig is the config of a file export.
type FileConfig struct {
	Dir string `json:"dir"`
}

func (e *Exporter) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Exporter) batch() int {
	if e.Batch > 0 {
		return e.Batch
	}
	return 500
}

func (e *Exporter) maxDelay() time.Duration {
	if e.MaxDelay > 0 {
		return e.MaxDelay
	}
	return time.Minute
}

func (e *Exporter) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

// Run ticks until ctx ends.
func (e *Exporter) Run(ctx context.Context) {
	iv := e.Interval
	if iv <= 0 {
		iv = 10 * time.Second
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		e.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick exports what every enabled export is due to write.
func (e *Exporter) Tick(ctx context.Context) {
	exports, err := e.Store.AuditExports(ctx, true)
	if err != nil {
		e.log().Error("audit: list exports", "err", err)
		return
	}
	for i := range exports {
		a := &exports[i]
		var err error
		switch a.Kind {
		case "file":
			err = e.exportFile(ctx, a)
		case "s3":
			err = e.exportS3(ctx, a)
		default:
			err = fmt.Errorf("unknown export kind %q", a.Kind)
		}
		if err != nil && ctx.Err() == nil {
			a.LastError = err.Error()
			e.log().Error("audit: export", "id", a.ID, "kind", a.Kind, "err", err)
			e.Store.UpdateAuditProgress(ctx, a)
		}
	}
}

func matchTypes(prefixes []string, typ string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, p := range prefixes {
		if typ == p || strings.HasPrefix(typ, p) {
			return true
		}
	}
	return false
}

// collect reads matching events after the export's cursor, up to Batch of
// them or ten pages, and reports the last sequence number read and whether
// the ledger head was reached. The cursor only moves when the caller writes.
func (e *Exporter) collect(ctx context.Context, a *AuditExport) (matched []ledger.Event, last int64, atHead bool, err error) {
	head, err := e.Store.HeadSeq(ctx)
	if err != nil {
		return nil, a.Cursor, true, err
	}
	last = a.Cursor
	if head <= last {
		return nil, last, true, nil
	}
	for pages := 0; pages < 10; pages++ {
		evs, err := e.Store.EventsRange(ctx, a.OrgID, last, head, e.batch())
		if err != nil {
			return nil, a.Cursor, false, err
		}
		if len(evs) == 0 {
			return matched, last, true, nil
		}
		for _, ev := range evs {
			if matchTypes(a.FilterTypes, ev.Type) {
				matched = append(matched, ev)
			}
		}
		last = evs[len(evs)-1].Seq
		if len(matched) >= e.batch() || len(evs) < e.batch() {
			return matched, last, last >= head, nil
		}
	}
	return matched, last, last >= head, nil
}

func ndjson(events []ledger.Event) []byte {
	var buf bytes.Buffer
	for _, ev := range events {
		b, _ := json.Marshal(envelope(ev, false, ""))
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// FileName is the daily file an export writes at t.
func FileName(dir string, t time.Time) string {
	return filepath.Join(dir, "events-"+t.UTC().Format("2006-01-02")+".ndjson")
}

func (e *Exporter) advance(ctx context.Context, a *AuditExport, last int64, written int) error {
	a.Cursor, a.Exported, a.LastError = last, a.Exported+int64(written), ""
	if written > 0 {
		s := stamp(e.now())
		a.LastExportAt = &s
	}
	return e.Store.UpdateAuditProgress(ctx, a)
}

func (e *Exporter) exportFile(ctx context.Context, a *AuditExport) error {
	var cfg FileConfig
	if err := json.Unmarshal(a.Config, &cfg); err != nil || cfg.Dir == "" {
		return fmt.Errorf("file export %s: config.dir is required", a.ID)
	}
	for {
		matched, last, atHead, err := e.collect(ctx, a)
		if err != nil || last == a.Cursor {
			return err
		}
		if len(matched) > 0 {
			if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(FileName(cfg.Dir, e.now()), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			if _, err := f.Write(ndjson(matched)); err != nil {
				f.Close()
				return err
			}
			if err := f.Sync(); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
		if err := e.advance(ctx, a, last, len(matched)); err != nil {
			return err
		}
		if atHead {
			return nil
		}
	}
}

func (e *Exporter) exportS3(ctx context.Context, a *AuditExport) error {
	var cfg S3Config
	if err := json.Unmarshal(a.Config, &cfg); err != nil || cfg.Bucket == "" {
		return fmt.Errorf("s3 export %s: config.bucket is required", a.ID)
	}
	for {
		matched, last, atHead, err := e.collect(ctx, a)
		if err != nil || last == a.Cursor {
			return err
		}
		if len(matched) == 0 {
			// nothing to write in this stretch: move the cursor on
			if err := e.advance(ctx, a, last, 0); err != nil {
				return err
			}
			if atHead {
				return nil
			}
			continue
		}
		if len(matched) < e.batch() && atHead {
			// a partial batch: hold it until MaxDelay has passed since it was first seen
			e.mu.Lock()
			if e.pending == nil {
				e.pending = map[string]time.Time{}
			}
			first, ok := e.pending[a.ID]
			if !ok {
				first = e.now()
				e.pending[a.ID] = first
			}
			e.mu.Unlock()
			if e.now().Sub(first) < e.maxDelay() {
				return nil
			}
		}
		now := e.now()
		key := strings.Trim(cfg.Prefix, "/")
		if key != "" {
			key += "/"
		}
		key += fmt.Sprintf("%s/events-%d-%d.ndjson", now.UTC().Format("2006/01/02"), matched[0].Seq, matched[len(matched)-1].Seq)
		ak, sk := "", ""
		if e.Credentials != nil {
			ak, sk = e.Credentials()
		}
		client := e.Client
		if client == nil {
			client = &http.Client{Timeout: 60 * time.Second}
		}
		if err := S3Put(ctx, client, cfg, ak, sk, key, ndjson(matched), "application/x-ndjson", now); err != nil {
			return err
		}
		e.mu.Lock()
		delete(e.pending, a.ID)
		e.mu.Unlock()
		if err := e.advance(ctx, a, last, len(matched)); err != nil {
			return err
		}
		if atHead {
			return nil
		}
	}
}

// Status reports every export with its lag behind the ledger head.
type Status struct {
	HeadSeq int64          `json:"head_seq"`
	Exports []ExportStatus `json:"exports"`
}

// ExportStatus is one export with its lag.
type ExportStatus struct {
	AuditExport
	Lag int64 `json:"lag"`
}

// Status computes the audit status.
func (e *Exporter) Status(ctx context.Context) (Status, error) {
	head, err := e.Store.HeadSeq(ctx)
	if err != nil {
		return Status{}, err
	}
	exports, err := e.Store.AuditExports(ctx, false)
	if err != nil {
		return Status{}, err
	}
	st := Status{HeadSeq: head, Exports: []ExportStatus{}}
	for _, a := range exports {
		st.Exports = append(st.Exports, ExportStatus{AuditExport: a, Lag: head - a.Cursor})
	}
	return st, nil
}
