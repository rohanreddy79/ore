// SPDX-License-Identifier: AGPL-3.0-only

// Package workspace is the workstation side of Ore (ADR 0016 §2): a
// materialized copy of a repository's active area with no git on the
// machine. `ore open` writes the tree and a manifest; `ore save` turns the
// working tree's edits into a commit and a change through the API; `ore
// sync` brings the tree to the tip without losing local edits; `ore land`
// asks for landing under the repository's autonomy level; `ore status`
// shows the ledger's view and what is pending locally. The working copy is
// a cache: the ledger is the source of truth, and edits made offline queue
// in .ore/pending.json until the server answers.
package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Dir is the metadata directory inside a workspace.
const Dir = ".ore"

// StateFile is the workspace description, relative to the root.
const StateFile = ".ore/workspace.json"

// PendingFile holds saves made while the server was unreachable.
const PendingFile = ".ore/pending.json"

// IgnoreFile lists paths `ore save` never records (gitignore syntax subset).
const IgnoreFile = ".oreignore"

// UpstreamSuffix is appended to a conflicting file's upstream version when
// `ore sync` writes both versions side by side.
const UpstreamSuffix = ".upstream"

// ErrNoWorkspace means no enclosing directory holds a workspace.
var ErrNoWorkspace = errors.New("not inside an Ore workspace (run `ore open ORG/REPO` first)")

// State is .ore/workspace.json.
type State struct {
	Version       int    `json:"version"`
	Server        string `json:"server"`
	Org           string `json:"org"`
	Repo          string `json:"repo"`
	DefaultBranch string `json:"default_branch"`
	// Base is the commit whose tree the manifest describes: the tip the tree
	// was opened or synced from, or the last save.
	Base string `json:"base"`
	// Paths are the materialized pathspecs; empty means the whole tree.
	Paths []string `json:"paths"`
	// User names the workstation's user for the change's hidden branch.
	User string `json:"user"`
	// Change is the change the saves go to; nil before the first save.
	Change *ChangeRef `json:"change,omitempty"`
	// PendingMerge is an upstream commit `ore sync` could not merge on the
	// server because of conflicts; the next save records the merge with the
	// resolutions in the working tree.
	PendingMerge string `json:"pending_merge,omitempty"`
	// Conflicts are files `ore sync` left for the user to resolve.
	Conflicts []Conflict `json:"conflicts,omitempty"`
	// Manifest is the tree at Base: path → entry, for every materialized file.
	Manifest map[string]Entry `json:"manifest"`
	OpenedAt string           `json:"opened_at"`
	SyncedAt string           `json:"synced_at,omitempty"`

	root string
}

// ChangeRef is the change the workspace's saves belong to.
type ChangeRef struct {
	// Number is 0 until the change is opened on the server (a first save
	// made offline opens it when the queue flushes).
	Number int `json:"number"`
	// Branch is the hidden source branch, ore/<user>/<slug>-<id>.
	Branch string `json:"branch"`
	Title  string `json:"title"`
}

// Entry is one file in the manifest.
type Entry struct {
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode"` // 100644, 100755 or 120000 (symlink; the hash covers the target)
	Size   int64  `json:"size"`
	// MTime (unix nanoseconds) lets a scan skip hashing an unchanged file.
	MTime int64 `json:"mtime,omitempty"`
}

// Conflict is a file edited locally and changed upstream.
type Conflict struct {
	Path string `json:"path"`
	// Side is the file holding the upstream version (path + .upstream), or ""
	// when the versions were merged inline with markers, or when upstream
	// deleted the file.
	Side string `json:"side,omitempty"`
	// Kind describes it: both-edited, deleted-upstream, deleted-locally.
	Kind string `json:"kind"`
}

// Find walks up from dir to the workspace root and loads its state.
func Find(dir string) (*State, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for d := abs; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, StateFile)); err == nil {
			return Load(d)
		}
		if filepath.Dir(d) == d {
			return nil, ErrNoWorkspace
		}
	}
}

// Load reads the state of the workspace rooted at root.
func Load(root string) (*State, error) {
	b, err := os.ReadFile(filepath.Join(root, StateFile))
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", StateFile, err)
	}
	if s.Manifest == nil {
		s.Manifest = map[string]Entry{}
	}
	s.root = root
	return &s, nil
}

// Root is the workspace directory.
func (s *State) Root() string { return s.root }

// Write writes the state atomically.
func (s *State) Write() error {
	if s.Version == 0 {
		s.Version = 1
	}
	path := filepath.Join(s.root, StateFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Sparse reports whether only part of the tree is materialized.
func (s *State) Sparse() bool { return len(s.Paths) > 0 }

// UnresolvedConflicts lists the recorded conflicts the user has not resolved:
// the side file still exists, or the file still carries markers.
func (s *State) UnresolvedConflicts() []Conflict {
	var out []Conflict
	for _, c := range s.Conflicts {
		if c.Side != "" {
			if _, err := os.Lstat(filepath.Join(s.root, filepath.FromSlash(c.Side))); err == nil {
				out = append(out, c)
			}
			continue
		}
		if c.Kind == "both-edited" && hasMarkers(filepath.Join(s.root, filepath.FromSlash(c.Path))) {
			out = append(out, c)
		}
	}
	return out
}

// Now is the clock (tests may replace it).
var Now = func() time.Time { return time.Now().UTC() }

func sortedPaths(m map[string]Entry) []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
