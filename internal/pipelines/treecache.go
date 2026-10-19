// SPDX-License-Identifier: AGPL-3.0-only

package pipelines

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/orevcs/ore/internal/gitx"
)

// Materialized trees are cached per (repository, commit, sparse paths) and
// cloned for each run: extracting every blob of a 100k-file tree costs
// seconds of CPU on every run, while a copy-on-write clone of the cached tree
// (APFS clonefile, btrfs/xfs reflink) costs milliseconds and a plain copy is
// still cheaper than decompressing every object again. The cache keeps the
// last few trees per repository and evicts the rest in the background.
// ORE_TREE_CACHE=0 disables it; ORE_TREE_CACHE_KEEP sets the trees kept per
// repository (default 4).
type treeCache struct {
	mu       sync.Mutex
	building map[string]chan struct{}
}

var trees = &treeCache{building: map[string]chan struct{}{}}

// treeCacheKeep is how many trees are kept per repository.
func treeCacheKeep() int {
	if v, err := strconv.Atoi(os.Getenv("ORE_TREE_CACHE_KEEP")); err == nil && v > 0 {
		return v
	}
	return 4
}

// materialize produces the tree of commit oid (limited to sparse paths when
// non-empty) in dest, which must exist and be empty. It reports how the tree
// was produced, for the run log.
func (rn *Runner) materialize(ctx context.Context, repoPath, repoID, oid string, sparse []string, dest string) (how string, err error) {
	if os.Getenv("ORE_TREE_CACHE") == "0" {
		return "extracted", gitx.ArchivePaths(ctx, repoPath, oid, dest, sparse)
	}
	sum := sha256.Sum256([]byte(strings.Join(sparse, "\x00")))
	key := oid + "-" + hex.EncodeToString(sum[:6])
	cacheDir := filepath.Join(rn.dataDir(), "cache", "trees", repoID)
	src := filepath.Join(cacheDir, key)
	how = "extracted into the tree cache"
	hit, err := trees.ensure(ctx, src, func(tmp string) error {
		// Build from the nearest cached tree when there is one: clone it and
		// apply the diff, so a run on a fresh commit costs the changed files,
		// not the whole tree.
		if prev, prevOID := trees.nearest(cacheDir, key); prev != "" {
			if err := buildIncremental(ctx, repoPath, prev, prevOID, oid, sparse, tmp); err == nil {
				how = "built from the previous tree and the diff"
				return nil
			}
			os.RemoveAll(tmp)
			os.MkdirAll(tmp, 0o755)
		}
		return gitx.ArchivePaths(ctx, repoPath, oid, tmp, sparse)
	})
	if err != nil {
		// The cache directory is unusable (read-only data dir, full disk):
		// fall back to a direct extraction so the run still happens.
		return "extracted (tree cache unavailable: " + err.Error() + ")", gitx.ArchivePaths(ctx, repoPath, oid, dest, sparse)
	}
	if hit {
		how = "cloned from the tree cache"
	}
	now := time.Now()
	_ = os.Chtimes(src, now, now) // most recently used
	if err := cloneTree(ctx, src, dest); err != nil {
		return how, err
	}
	go trees.evict(cacheDir, treeCacheKeep())
	return how, nil
}

// nearest returns the most recently used cached tree built with the same
// sparse paths as key (the suffix after the commit) and its commit, or "".
func (c *treeCache) nearest(cacheDir, key string) (path, oid string) {
	suffix := key[strings.LastIndexByte(key, '-'):]
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return "", ""
	}
	var best time.Time
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasSuffix(name, suffix) || strings.HasPrefix(name, ".tmp-") || name == key {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().After(best) {
			best, path, oid = info.ModTime(), filepath.Join(cacheDir, name), strings.TrimSuffix(name, suffix)
		}
	}
	return path, oid
}

// buildIncremental produces commit oid's tree in tmp (which exists and is
// empty) from the cached tree of prevOID: clone, delete what the diff
// removed, extract what it added or changed. Paths that changed kind (file
// to directory or back) are removed before extraction.
func buildIncremental(ctx context.Context, repoPath, prev, prevOID, oid string, sparse []string, tmp string) error {
	args := []string{"diff-tree", "-r", "--name-status", "--no-renames", "-z", prevOID, oid}
	if len(sparse) > 0 {
		args = append(append(args, "--"), sparse...)
	}
	out, err := gitx.Run(ctx, repoPath, nil, args...)
	if err != nil {
		return err
	}
	var removed, changed []string
	fields := strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		status, path := fields[i], fields[i+1]
		if path == "" || strings.Contains(path, "..") {
			return fmt.Errorf("unexpected diff entry %q", path)
		}
		if strings.HasPrefix(status, "D") {
			removed = append(removed, path)
		} else {
			changed = append(changed, path)
		}
	}
	if err := cloneTree(ctx, prev, tmp); err != nil {
		return err
	}
	for _, p := range append(removed, changed...) {
		if err := os.RemoveAll(filepath.Join(tmp, filepath.FromSlash(p))); err != nil {
			return err
		}
	}
	if len(changed) == 0 {
		return nil
	}
	return gitx.ArchivePaths(ctx, repoPath, oid, tmp, changed)
}

// ensure makes src exist, building it at most once across concurrent callers
// (others wait for the builder); it reports whether src already existed.
func (c *treeCache) ensure(ctx context.Context, src string, build func(tmp string) error) (bool, error) {
	for {
		if st, err := os.Stat(src); err == nil && st.IsDir() {
			return true, nil
		}
		c.mu.Lock()
		if done, ok := c.building[src]; ok {
			c.mu.Unlock()
			select {
			case <-done:
				continue // built (or failed): re-check
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		done := make(chan struct{})
		c.building[src] = done
		c.mu.Unlock()
		err := c.build(src, build)
		c.mu.Lock()
		delete(c.building, src)
		c.mu.Unlock()
		close(done)
		return false, err
	}
}

func (c *treeCache) build(src string, build func(tmp string) error) error {
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(src), ".tmp-")
	if err != nil {
		return err
	}
	if err := build(tmp); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, src); err != nil {
		os.RemoveAll(tmp)
		if st, serr := os.Stat(src); serr == nil && st.IsDir() {
			return nil // another process won the race
		}
		return err
	}
	return nil
}

// evict removes trees beyond keep, oldest first, skipping anything used in
// the last two minutes (a run may still be cloning it).
func (c *treeCache) evict(cacheDir string, keep int) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return
	}
	type tree struct {
		path string
		mod  time.Time
	}
	var ts []tree
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".tmp-") {
			continue
		}
		if info, err := e.Info(); err == nil {
			ts = append(ts, tree{filepath.Join(cacheDir, e.Name()), info.ModTime()})
		}
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].mod.After(ts[j].mod) })
	cutoff := time.Now().Add(-2 * time.Minute)
	for _, t := range ts[min(keep, len(ts)):] {
		if t.mod.Before(cutoff) {
			os.RemoveAll(t.path)
		}
	}
}

// cloneTree copies src's contents into dest (an existing empty directory),
// copy-on-write where the filesystem supports it: one directory clone on
// APFS, per-file reflinks on btrfs and xfs, a plain copy elsewhere.
func cloneTree(ctx context.Context, src, dest string) error {
	if os.Remove(dest) == nil { // clonefile needs the target absent
		if err := cloneDir(src, dest); err == nil {
			return nil
		}
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
	}
	var args []string
	switch runtime.GOOS {
	case "darwin":
		args = []string{"-Rc", src + "/.", dest}
	case "linux":
		args = []string{"-a", "--reflink=auto", src + "/.", dest}
	default:
		args = []string{"-R", src + "/.", dest}
	}
	cmd := exec.CommandContext(ctx, "cp", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("clone tree: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
