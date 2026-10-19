// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/pipelines"
)

// The workstation client (`ore open`, `ore sync`; ADR 0016 §2) materializes
// a repository without git on the workstation. It needs two reads the UI
// never needed: a streamed tar of a tree (sparse by default) and the list of
// paths that differ between two commits, with modes.

// archive streams `GET .../archive?ref=&path=...&all=1` as a tar. With no
// explicit path and no all=1 the repository's active area is sent: the
// `paths:` the pipeline file declares at that commit (plus the file), or
// the whole tree when it declares none. The response says what it carries
// in X-Ore-Commit (the resolved commit) and X-Ore-Archive-Paths (a JSON
// array; empty for the whole tree); a failure after the first byte is
// reported in the X-Ore-Archive-Error trailer, since the status is gone.
func (s *Server) archive(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repo(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	ref := q.Get("ref")
	if ref == "" {
		ref = "refs/heads/" + repo.DefaultBranch
	}
	rp := s.repoPath(repo)
	oid, err := gitx.ResolveRef(r.Context(), rp, ref)
	if err != nil {
		return gitErr(err)
	}
	paths := []string{}
	for _, p := range q["path"] {
		p = strings.Trim(p, "/")
		if !validTreePath(p) {
			return fmt.Errorf("%w: bad path %q", errBadRequest, p)
		}
		paths = append(paths, p)
	}
	if len(paths) == 0 && q.Get("all") == "" {
		if sparse := pipelines.SparsePaths(r.Context(), rp, oid); sparse != nil {
			paths = sparse
		}
	}
	pathsJSON, _ := json.Marshal(paths)
	h := w.Header()
	h.Set("Content-Type", "application/x-tar")
	h.Set("X-Ore-Commit", oid)
	h.Set("X-Ore-Archive-Paths", string(pathsJSON))
	h.Set("Trailer", "X-Ore-Archive-Error")
	cw := &countingWriter{w: w}
	if err := gitx.ArchiveTo(r.Context(), rp, oid, paths, cw); err != nil {
		if strings.Contains(err.Error(), "did not match any files") {
			err = fmt.Errorf("%w: %v", ledger.ErrNotFound, err)
		}
		if cw.n == 0 { // nothing sent yet: a proper status is still possible
			h.Del("Content-Type")
			h.Del("X-Ore-Commit")
			h.Del("X-Ore-Archive-Paths")
			h.Del("Trailer")
			return gitErr(err)
		}
		h.Set("X-Ore-Archive-Error", err.Error())
		if s.Log != nil {
			s.Log.Error("archive failed mid-stream", "repo", repo.Org+"/"+repo.Name, "error", err.Error())
		}
		return nil
	}
	h.Set("X-Ore-Archive-Error", "")
	return nil
}

// diff answers `GET .../diff?from=&to=&path=...` with the paths that differ
// between two commits (added, modified, deleted, type-changed; modes and
// blob ids on both sides) and their merge base: `ore sync` brings a
// materialized tree from one commit to the other with it, and decides from
// the merge base whether the tip is already contained.
func (s *Server) diff(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	repo, err := s.repo(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	if q.Get("from") == "" || q.Get("to") == "" {
		return fmt.Errorf("%w: from and to are required", errBadRequest)
	}
	rp := s.repoPath(repo)
	from, err := gitx.ResolveRef(r.Context(), rp, q.Get("from"))
	if err != nil {
		return gitErr(err)
	}
	to, err := gitx.ResolveRef(r.Context(), rp, q.Get("to"))
	if err != nil {
		return gitErr(err)
	}
	var paths []string
	for _, p := range q["path"] {
		p = strings.Trim(p, "/")
		if !validTreePath(p) {
			return fmt.Errorf("%w: bad path %q", errBadRequest, p)
		}
		paths = append(paths, p)
	}
	type result struct {
		From      string           `json:"from"`
		To        string           `json:"to"`
		MergeBase string           `json:"merge_base,omitempty"`
		Files     []gitx.DiffEntry `json:"files"`
	}
	// Two commits never change: the answer is immutable per (from, to, paths).
	v, err := readCache.get("diff|"+repo.ID+"|"+from+"|"+to+"|"+strings.Join(paths, "\x00"), func() (any, int, error) {
		entries, err := gitx.DiffTree(r.Context(), rp, from, to, paths)
		if err != nil {
			return nil, 0, err
		}
		if entries == nil {
			entries = []gitx.DiffEntry{}
		}
		res := result{From: from, To: to, Files: entries}
		if from != to {
			if mb, err := gitx.MergeBase(r.Context(), rp, from, to); err == nil {
				res.MergeBase = mb
			}
		} else {
			res.MergeBase = from
		}
		size := 128
		for _, e := range entries {
			size += len(e.Path) + 120
		}
		return res, size, nil
	})
	if err != nil {
		return gitErr(err)
	}
	return respond(w, nil, v)
}

// validTreePath accepts a relative path inside a tree: no empty segments, no
// "." or "..", and never something git could read as an option.
func validTreePath(p string) bool {
	if p == "" || strings.HasPrefix(p, "-") || strings.ContainsRune(p, 0) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

type countingWriter struct {
	w http.ResponseWriter
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// errBranchMoved is answered with 409 and the branch's current tip when a
// commit's `base` no longer matches it (`ore save` learns that someone else
// saved to the change and syncs first).
var errBranchMoved = errors.New("branch moved")
