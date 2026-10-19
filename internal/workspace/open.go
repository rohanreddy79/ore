// SPDX-License-Identifier: AGPL-3.0-only

package workspace

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Open materializes org/repo's active area (or the whole tree with all)
// into dir, which must be absent or empty, and writes the workspace state.
func Open(ctx context.Context, c *Client, org, repo, dir string, all bool, user string, out io.Writer) (*State, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if entries, err := os.ReadDir(abs); err == nil && len(entries) > 0 {
		return nil, fmt.Errorf("%s is not empty", dir)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	r, err := c.Repo(ctx, org, repo)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	resp, commit, paths, err := c.Archive(ctx, org, repo, "", nil, all)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if commit == "" {
		return nil, fmt.Errorf("%s/%s is empty: nothing to open yet", org, repo)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	st := &State{Version: 1, Server: c.Base, Org: org, Repo: repo, DefaultBranch: r.DefaultBranch, Base: commit, Paths: paths, User: user,
		Manifest: map[string]Entry{}, OpenedAt: Now().Format(time.RFC3339), root: abs}
	if st.Paths == nil {
		st.Paths = []string{}
	}
	n, err := Extract(resp.Body, abs, st.Manifest)
	if err != nil {
		return nil, err
	}
	if err := ArchiveError(resp); err != nil {
		return nil, err
	}
	if err := st.Write(); err != nil {
		return nil, err
	}
	area := "the whole tree"
	if len(paths) > 0 {
		area = fmt.Sprintf("the active area: %s", strings.Join(paths, ", "))
	}
	fmt.Fprintf(out, "opened %s/%s at %s: %d files, %s, in %s (%s)\n", org, repo, short(commit), n, area, dir, time.Since(start).Round(time.Millisecond))
	return st, nil
}

// Extract writes a tar stream into root and records every file in manifest;
// it returns the number of files written. Entries that would escape root
// are refused.
func Extract(r io.Reader, root string, manifest map[string]Entry) (int, error) {
	tr := tar.NewReader(r)
	n := 0
	buf := make([]byte, 256<<10)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, fmt.Errorf("archive: %w", err)
		}
		name := strings.TrimPrefix(path.Clean("/"+h.Name), "/")
		if name == "" || name == "." {
			continue
		}
		dest := filepath.Join(root, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return n, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return n, err
			}
			mode, perm := "100644", fs.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode, perm = "100755", 0o755
			}
			f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
			if err != nil {
				return n, err
			}
			hash := sha256.New()
			size, err := io.CopyBuffer(io.MultiWriter(f, hash), tr, buf)
			cerr := f.Close()
			if err != nil {
				return n, err
			}
			if cerr != nil {
				return n, cerr
			}
			info, err := os.Lstat(dest)
			if err != nil {
				return n, err
			}
			manifest[name] = Entry{SHA256: hex.EncodeToString(hash.Sum(nil)), Mode: mode, Size: size, MTime: info.ModTime().UnixNano()}
			n++
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return n, err
			}
			os.Remove(dest)
			if err := os.Symlink(h.Linkname, dest); err != nil {
				return n, err
			}
			info, err := os.Lstat(dest)
			if err != nil {
				return n, err
			}
			manifest[name] = Entry{SHA256: hashOf([]byte(h.Linkname)), Mode: "120000", Size: int64(len(h.Linkname)), MTime: info.ModTime().UnixNano()}
			n++
		default:
			// pax global headers (git archive stamps the commit), hard links
			// and devices are not part of a source tree
		}
	}
}

// short abbreviates an object id.
func short(oid string) string {
	if len(oid) > 8 {
		return oid[:8]
	}
	return oid
}
