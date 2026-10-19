// SPDX-License-Identifier: AGPL-3.0-only

package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Scan walks the working tree and returns its manifest: every file not
// ignored, with its mode and SHA-256. A file whose size and modification
// time match the previous manifest keeps its recorded hash, so a scan of a
// large tree costs a directory walk, not a read of every byte.
func Scan(root string, prev map[string]Entry, ig *Ignore) (map[string]Entry, error) {
	cur := map[string]Entry{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if ig != nil && ig.Match(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		e, err := entryFor(p, info, prev[rel])
		if err != nil {
			return err
		}
		if e.Mode == "" {
			return nil // a socket, a device: not a file we record
		}
		cur[rel] = e
		return nil
	})
	return cur, err
}

func entryFor(p string, info fs.FileInfo, old Entry) (Entry, error) {
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(p)
		if err != nil {
			return Entry{}, err
		}
		sum := sha256.Sum256([]byte(target))
		return Entry{SHA256: hex.EncodeToString(sum[:]), Mode: "120000", Size: int64(len(target)), MTime: info.ModTime().UnixNano()}, nil
	case !info.Mode().IsRegular():
		return Entry{}, nil
	}
	mode := "100644"
	if info.Mode()&0o111 != 0 {
		mode = "100755"
	}
	e := Entry{Mode: mode, Size: info.Size(), MTime: info.ModTime().UnixNano()}
	if old.SHA256 != "" && old.Size == e.Size && old.MTime == e.MTime && old.MTime != 0 {
		e.SHA256 = old.SHA256
		return e, nil
	}
	f, err := os.Open(p)
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return Entry{}, err
	}
	e.SHA256 = hex.EncodeToString(h.Sum(nil))
	return e, nil
}

// Edit is one difference between the manifest and the working tree.
type Edit struct {
	Path string
	// Kind is added, changed (content or mode) or deleted.
	Kind string
	Mode string
	// Content is loaded by LoadContent; nil for deletions.
	Content []byte
	SHA256  string
}

// Diff compares the recorded manifest with a scan.
func Diff(manifest, cur map[string]Entry) []Edit {
	var out []Edit
	for p, e := range cur {
		old, ok := manifest[p]
		switch {
		case !ok:
			out = append(out, Edit{Path: p, Kind: "added", Mode: e.Mode, SHA256: e.SHA256})
		case old.SHA256 != e.SHA256 || old.Mode != e.Mode:
			out = append(out, Edit{Path: p, Kind: "changed", Mode: e.Mode, SHA256: e.SHA256})
		}
	}
	for p, old := range manifest {
		if _, ok := cur[p]; !ok {
			out = append(out, Edit{Path: p, Kind: "deleted", Mode: old.Mode})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// LoadContent reads the content of added and changed edits (a symlink's
// content is its target).
func LoadContent(root string, edits []Edit) error {
	for i := range edits {
		if edits[i].Kind == "deleted" {
			continue
		}
		p := filepath.Join(root, filepath.FromSlash(edits[i].Path))
		if edits[i].Mode == "120000" {
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			edits[i].Content = []byte(target)
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		edits[i].Content = b
	}
	return nil
}

// Summary counts edits by kind, for messages.
func Summary(edits []Edit) string {
	var a, c, d int
	for _, e := range edits {
		switch e.Kind {
		case "added":
			a++
		case "changed":
			c++
		case "deleted":
			d++
		}
	}
	var parts []string
	if c > 0 {
		parts = append(parts, fmt.Sprintf("%d changed", c))
	}
	if a > 0 {
		parts = append(parts, fmt.Sprintf("%d added", a))
	}
	if d > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", d))
	}
	if len(parts) == 0 {
		return "no edits"
	}
	return strings.Join(parts, ", ")
}

// WriteFile writes content at rel with mode (100644, 100755 or 120000),
// creating directories; it replaces whatever was there, including a
// symlink or a directory.
func WriteFile(root, rel string, content []byte, mode string) (Entry, error) {
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return Entry{}, err
	}
	if st, err := os.Lstat(p); err == nil && (st.IsDir() || st.Mode()&fs.ModeSymlink != 0 || mode == "120000") {
		if err := os.RemoveAll(p); err != nil {
			return Entry{}, err
		}
	}
	if mode == "120000" {
		if err := os.Symlink(string(content), p); err != nil {
			return Entry{}, err
		}
	} else {
		perm := fs.FileMode(0o644)
		if mode == "100755" {
			perm = 0o755
		}
		if err := os.WriteFile(p, content, perm); err != nil {
			return Entry{}, err
		}
		if err := os.Chmod(p, perm); err != nil { // WriteFile keeps an existing file's mode
			return Entry{}, err
		}
	}
	info, err := os.Lstat(p)
	if err != nil {
		return Entry{}, err
	}
	sum := sha256.Sum256(content)
	return Entry{SHA256: hex.EncodeToString(sum[:]), Mode: mode, Size: int64(len(content)), MTime: info.ModTime().UnixNano()}, nil
}

// RemoveFile deletes rel and the empty directories it leaves behind.
func RemoveFile(root, rel string) error {
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.RemoveAll(p); err != nil {
		return err
	}
	for d := filepath.Dir(p); d != root && strings.HasPrefix(d, root); d = filepath.Dir(d) {
		if os.Remove(d) != nil { // not empty, or gone
			break
		}
	}
	return nil
}

// hashOf is the manifest hash of content.
func hashOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
