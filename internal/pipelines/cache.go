// SPDX-License-Identifier: AGPL-3.0-only

package pipelines

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

// CacheStore is the local content-addressed cache under the data directory:
// <Dir>/<repo id>/<trust scope>/<entry>.json plus one tar blob per cached
// path. Keys are looked up exactly, then by restore-key prefix (newest
// first), in the task's own scope and the scopes above it; writes go only to
// the task's own scope, so a fork can never poison what the default branch
// restores (docs/04 A.5).
type CacheStore struct {
	Dir string
}

// CacheEntry describes one saved cache.
type CacheEntry struct {
	Key       string      `json:"key"`
	Scope     string      `json:"scope"`
	RepoID    string      `json:"repo_id"`
	Paths     []string    `json:"paths"`
	Blobs     []CacheBlob `json:"blobs"`
	Size      int64       `json:"size"`
	CreatedAt string      `json:"created_at"`
	Seq       int64       `json:"seq"` // creation time in nanoseconds; orders restore-key matches newest first
	RunID     string      `json:"run_id,omitempty"`
	TaskID    string      `json:"task_id,omitempty"`
	dir       string
}

// CacheBlob is the archive of one cached path.
type CacheBlob struct {
	Path   string `json:"path"`
	File   string `json:"file"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

var validScopes = map[string]bool{TrustDefault: true, TrustProtected: true, TrustInternal: true, TrustFork: true}

func safeSegment(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (c *CacheStore) scopeDir(repoID, scope string) (string, error) {
	if !safeSegment(repoID) {
		return "", fmt.Errorf("cache: bad repository id %q", repoID)
	}
	if !validScopes[scope] {
		return "", fmt.Errorf("cache: unknown trust scope %q", scope)
	}
	return filepath.Join(c.Dir, repoID, scope), nil
}

func entryID(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])[:32]
}

// readScopes lists the scopes a task at scope may read, own scope first.
func readScopes(scope string) []string {
	all := []string{TrustFork, TrustInternal, TrustProtected, TrustDefault}
	var out []string
	for _, s := range all {
		if CanReadScope(scope, s) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return TrustRank(out[i]) > TrustRank(out[j]) })
	return out
}

func (c *CacheStore) loadEntry(path string) (*CacheEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var e CacheEntry
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, err
	}
	e.dir = filepath.Dir(path)
	return &e, nil
}

func (c *CacheStore) entries(repoID, scope string) ([]*CacheEntry, error) {
	dir, err := c.scopeDir(repoID, scope)
	if err != nil {
		return nil, err
	}
	names, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []*CacheEntry
	for _, n := range names {
		e, err := c.loadEntry(n)
		if err != nil {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq > out[j].Seq })
	return out, nil
}

// Lookup finds the entry a task at scope may restore: the exact key in each
// readable scope (own first), then each restore key as a prefix, newest
// first. exact reports whether the primary key matched.
func (c *CacheStore) Lookup(repoID, scope, key string, restoreKeys []string) (entry *CacheEntry, exact bool, err error) {
	if !validScopes[scope] {
		return nil, false, fmt.Errorf("cache: unknown trust scope %q", scope)
	}
	for _, s := range readScopes(scope) {
		dir, err := c.scopeDir(repoID, s)
		if err != nil {
			return nil, false, err
		}
		if e, err := c.loadEntry(filepath.Join(dir, entryID(key)+".json")); err == nil && e.Key == key {
			return e, true, nil
		}
	}
	for _, rk := range restoreKeys {
		rk = strings.TrimSpace(rk)
		if rk == "" {
			continue
		}
		for _, s := range readScopes(scope) {
			all, err := c.entries(repoID, s)
			if err != nil {
				return nil, false, err
			}
			for _, e := range all {
				if strings.HasPrefix(e.Key, rk) {
					return e, false, nil
				}
			}
		}
	}
	return nil, false, nil
}

// Exists reports whether key is already saved in exactly this scope.
func (c *CacheStore) Exists(repoID, scope, key string) bool {
	dir, err := c.scopeDir(repoID, scope)
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, entryID(key)+".json"))
	return err == nil
}

// OpenBlob opens the archive of the i-th cached path.
func (e *CacheEntry) OpenBlob(i int) (io.ReadCloser, error) {
	if i < 0 || i >= len(e.Blobs) {
		return nil, errors.New("cache: no such blob")
	}
	return os.Open(filepath.Join(e.dir, e.Blobs[i].File))
}

// CacheWriter saves one entry: one blob per path, then Commit.
type CacheWriter struct {
	store *CacheStore
	entry CacheEntry
	dir   string
	id    string
	tmp   []string
}

// Begin starts saving key in scope for the task's own scope only.
func (c *CacheStore) Begin(repoID, scope, key string, paths []string, runID, taskID string) (*CacheWriter, error) {
	dir, err := c.scopeDir(repoID, scope)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("cache: empty key")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &CacheWriter{store: c, dir: dir, id: entryID(key), entry: CacheEntry{Key: key, Scope: scope, RepoID: repoID, Paths: paths, RunID: runID, TaskID: taskID}}, nil
}

// Blob returns a writer for the archive of path; close it before Commit.
func (w *CacheWriter) Blob(path string) (io.WriteCloser, error) {
	i := len(w.entry.Blobs)
	name := fmt.Sprintf("%s.%d.tar", w.id, i)
	tmp := filepath.Join(w.dir, name+".tmp")
	f, err := os.Create(tmp)
	if err != nil {
		return nil, err
	}
	w.tmp = append(w.tmp, tmp)
	w.entry.Blobs = append(w.entry.Blobs, CacheBlob{Path: path, File: name})
	return &blobWriter{f: f, h: sha256.New(), w: w, i: i}, nil
}

type blobWriter struct {
	f *os.File
	h interface {
		io.Writer
		Sum([]byte) []byte
	}
	w *CacheWriter
	i int
	n int64
}

func (b *blobWriter) Write(p []byte) (int, error) {
	n, err := b.f.Write(p)
	b.h.Write(p[:n])
	b.n += int64(n)
	return n, err
}

func (b *blobWriter) Close() error {
	if err := b.f.Close(); err != nil {
		return err
	}
	b.w.entry.Blobs[b.i].Size = b.n
	b.w.entry.Blobs[b.i].Digest = "sha256:" + hex.EncodeToString(b.h.Sum(nil))
	b.w.entry.Size += b.n
	return nil
}

// Commit makes the entry visible. Nothing is visible before Commit.
func (w *CacheWriter) Commit() (*CacheEntry, error) {
	for i, tmp := range w.tmp {
		if err := os.Rename(tmp, filepath.Join(w.dir, w.entry.Blobs[i].File)); err != nil {
			w.Abort()
			return nil, err
		}
	}
	w.entry.CreatedAt = ledger.Now()
	w.entry.Seq = time.Now().UnixNano()
	b, _ := json.MarshalIndent(w.entry, "", "  ")
	final := filepath.Join(w.dir, w.id+".json")
	if err := os.WriteFile(final+".tmp", b, 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(final+".tmp", final); err != nil {
		return nil, err
	}
	w.entry.dir = w.dir
	return &w.entry, nil
}

// Abort discards a partial save.
func (w *CacheWriter) Abort() {
	for _, tmp := range w.tmp {
		os.Remove(tmp)
	}
}

// CachePathBase classifies a cache path the way the executor archives it:
// base is "~" (home), "/" (absolute) or "" (workspace-relative) and rel is
// the path under that base. Globs are not supported in cache paths.
func CachePathBase(p string) (base, rel string, err error) {
	p = strings.TrimSpace(p)
	switch {
	case p == "":
		return "", "", errors.New("empty cache path")
	case strings.ContainsAny(p, "*?["):
		return "", "", fmt.Errorf("cache path %q: globs are not supported", p)
	case p == "~" || strings.HasPrefix(p, "~/"):
		return "~", strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"), nil
	case strings.HasPrefix(p, "/"):
		return "/", strings.TrimPrefix(p, "/"), nil
	}
	rel = strings.TrimPrefix(p, "./")
	if rel == ".." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") {
		return "", "", fmt.Errorf("cache path %q escapes the workspace", p)
	}
	return "", rel, nil
}
