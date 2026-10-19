// SPDX-License-Identifier: AGPL-3.0-only

package workspace

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/gitx"
)

// SyncReport says what a sync did.
type SyncReport struct {
	Updated, Deleted, Merged int
	Conflicts                []Conflict
	// Outside lists conflicting paths outside the materialized area (sparse
	// workspaces), which cannot be resolved here.
	Outside []string
	Tip     string
}

// Sync brings the working tree to the tip without losing local edits. With
// an open change it first catches up with saves others made to the change,
// then merges the default branch into the change on the server; a file
// edited locally and changed upstream is a conflict: the upstream version is
// written beside it as <path>.upstream, or merged in place with markers
// when inline is set.
func (s *State) Sync(ctx context.Context, c *Client, inline bool, out io.Writer) (*SyncReport, error) {
	if _, err := s.Flush(ctx, c, out); err != nil {
		return nil, err
	}
	r, err := c.Repo(ctx, s.Org, s.Repo)
	if err != nil {
		return nil, err
	}
	refs, err := c.Refs(ctx, s.Org, s.Repo)
	if err != nil {
		return nil, err
	}
	tip := refs["refs/heads/"+r.DefaultBranch]
	if tip == "" {
		return nil, fmt.Errorf("%s/%s has no %s yet", s.Org, s.Repo, r.DefaultBranch)
	}
	cur, err := Scan(s.root, s.Manifest, s.ignore())
	if err != nil {
		return nil, err
	}
	dirty := map[string]string{}
	for _, e := range Diff(s.Manifest, cur) {
		dirty[e.Path] = e.Kind
	}
	rep := &SyncReport{Tip: tip}
	// conflicts from an earlier sync that are still unresolved stay recorded
	rep.Conflicts = append(rep.Conflicts, s.UnresolvedConflicts()...)
	tracked := r.DefaultBranch
	if s.Change != nil && s.Change.Number > 0 {
		ch, err := c.Change(ctx, s.Org, s.Repo, s.Change.Number)
		if err != nil {
			return nil, err
		}
		switch ch.State {
		case "merged":
			fmt.Fprintf(out, "change #%d has landed; the next save starts a new change\n", ch.Number)
			s.Change = nil
		case "closed":
			fmt.Fprintf(out, "change #%d was closed; returning to %s\n", ch.Number, r.DefaultBranch)
			s.Change = nil
		default:
			tracked = fmt.Sprintf("change #%d", ch.Number)
			if btip := refs[ch.SourceRef]; btip != "" && btip != s.Base {
				if err := s.apply(ctx, c, s.Base, btip, dirty, inline, rep, tracked); err != nil {
					return nil, err
				}
				s.Base = btip
			}
			d, err := c.Diff(ctx, s.Org, s.Repo, s.Base, tip, nil)
			if err != nil {
				return nil, err
			}
			if d.MergeBase != tip { // the tip has commits the change does not
				res, err := c.Commit(ctx, s.Org, s.Repo, CommitRequest{Branch: s.Change.Branch, Base: s.Base, Merge: tip, Message: "Sync with " + r.DefaultBranch})
				var me *MergeConflictError
				switch {
				case errors.As(err, &me):
					if err := s.syncConflicted(ctx, c, tip, d.MergeBase, me, dirty, inline, rep); err != nil {
						return nil, err
					}
					s.PendingMerge = tip
				case err != nil:
					return nil, err
				default:
					if err := s.apply(ctx, c, s.Base, res.Commit, dirty, inline, rep, r.DefaultBranch); err != nil {
						return nil, err
					}
					s.Base = res.Commit
					s.PendingMerge = ""
					// the merge is a patch set of the change: reading the patch
					// sets makes the server record it now rather than when the
					// next save moves the branch again
					_, _ = c.PatchSets(ctx, s.Org, s.Repo, s.Change.Number)
				}
			}
			return s.finishSync(rep, tracked, out)
		}
	}
	if tip != s.Base {
		if err := s.apply(ctx, c, s.Base, tip, dirty, inline, rep, r.DefaultBranch); err != nil {
			return nil, err
		}
		s.Base = tip
	}
	s.PendingMerge = ""
	return s.finishSync(rep, tracked, out)
}

func (s *State) finishSync(rep *SyncReport, tracked string, out io.Writer) (*SyncReport, error) {
	s.Conflicts = dedupeConflicts(rep.Conflicts)
	rep.Conflicts = s.Conflicts
	s.SyncedAt = Now().Format(time.RFC3339)
	if err := s.Write(); err != nil {
		return nil, err
	}
	parts := []string{}
	if rep.Updated > 0 {
		parts = append(parts, fmt.Sprintf("%d updated", rep.Updated))
	}
	if rep.Deleted > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", rep.Deleted))
	}
	if rep.Merged > 0 {
		parts = append(parts, fmt.Sprintf("%d merged", rep.Merged))
	}
	if len(parts) == 0 {
		parts = append(parts, "already up to date")
	}
	fmt.Fprintf(out, "synced with %s at %s: %s\n", tracked, short(s.Base), strings.Join(parts, ", "))
	if len(rep.Conflicts) > 0 {
		fmt.Fprintf(out, "%d conflict(s) to resolve before the next save:\n", len(rep.Conflicts))
		for _, cf := range rep.Conflicts {
			switch {
			case cf.Side != "":
				fmt.Fprintf(out, "  %s  (%s; the upstream version is in %s: merge by hand, then delete it)\n", cf.Path, cf.Kind, cf.Side)
			case cf.Kind == "deleted-upstream":
				fmt.Fprintf(out, "  %s  (deleted upstream, edited here: keep it or delete it, then save)\n", cf.Path)
			default:
				fmt.Fprintf(out, "  %s  (%s; merged with markers: edit, then save)\n", cf.Path, cf.Kind)
			}
		}
	}
	if len(rep.Outside) > 0 {
		fmt.Fprintf(out, "%d conflict(s) outside the materialized area cannot be resolved here: %s (open the repository with --all to resolve them)\n", len(rep.Outside), strings.Join(rep.Outside, ", "))
	}
	return rep, nil
}

func dedupeConflicts(cs []Conflict) []Conflict {
	seen := map[string]bool{}
	var out []Conflict
	for _, c := range cs {
		if !seen[c.Path] {
			seen[c.Path] = true
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// blob is a file fetched from a commit.
type blob struct {
	content []byte
	mode    string
}

// fetchBlobs reads files at ref through archive requests in batches.
func (s *State) fetchBlobs(ctx context.Context, c *Client, ref string, paths []string) (map[string]blob, error) {
	out := map[string]blob{}
	for len(paths) > 0 {
		n := min(len(paths), 200)
		batch := paths[:n]
		paths = paths[n:]
		resp, _, _, err := c.Archive(ctx, s.Org, s.Repo, ref, batch, false)
		if err != nil {
			return nil, err
		}
		tr := tar.NewReader(resp.Body)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				resp.Body.Close()
				return nil, fmt.Errorf("archive: %w", err)
			}
			name := strings.TrimPrefix(path.Clean("/"+h.Name), "/")
			switch h.Typeflag {
			case tar.TypeReg:
				b, err := io.ReadAll(tr)
				if err != nil {
					resp.Body.Close()
					return nil, err
				}
				mode := "100644"
				if h.Mode&0o111 != 0 {
					mode = "100755"
				}
				out[name] = blob{content: b, mode: mode}
			case tar.TypeSymlink:
				out[name] = blob{content: []byte(h.Linkname), mode: "120000"}
			}
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err := ArchiveError(resp); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// apply brings the tree from commit `from` to commit `to` within the
// materialized paths; files edited locally and changed between the two are
// conflicts. The manifest follows `to`.
func (s *State) apply(ctx context.Context, c *Client, from, to string, dirty map[string]string, inline bool, rep *SyncReport, what string) error {
	d, err := c.Diff(ctx, s.Org, s.Repo, from, to, s.Paths)
	if err != nil {
		return err
	}
	var need, baseNeed []string
	for _, e := range d.Files {
		if e.Status != "D" {
			need = append(need, e.Path)
		}
		if _, isDirty := dirty[e.Path]; isDirty && inline && e.Status != "A" {
			baseNeed = append(baseNeed, e.Path)
		}
	}
	blobs, err := s.fetchBlobs(ctx, c, to, need)
	if err != nil {
		return err
	}
	var bases map[string]blob
	if len(baseNeed) > 0 {
		if bases, err = s.fetchBlobs(ctx, c, from, baseNeed); err != nil {
			return err
		}
	}
	for _, e := range d.Files {
		kind, isDirty := dirty[e.Path]
		up := blobs[e.Path]
		if !isDirty {
			if e.Status == "D" {
				if err := RemoveFile(s.root, e.Path); err != nil {
					return err
				}
				delete(s.Manifest, e.Path)
				rep.Deleted++
				continue
			}
			ent, err := WriteFile(s.root, e.Path, up.content, up.mode)
			if err != nil {
				return err
			}
			s.Manifest[e.Path] = ent
			rep.Updated++
			continue
		}
		var base []byte
		if b, ok := bases[e.Path]; ok {
			base = b.content
		}
		if err := s.conflict(e, kind, up, base, inline, rep, what); err != nil {
			return err
		}
	}
	return nil
}

// conflict handles one path edited locally (kind: added, changed, deleted)
// and changed upstream (e); up is the upstream version, base the common one
// when known. The manifest takes the upstream entry so the local file (or
// its absence) is the edit the next save records.
func (s *State) conflict(e gitx.DiffEntry, kind string, up blob, base []byte, inline bool, rep *SyncReport, what string) error {
	p := e.Path
	switch {
	case e.Status == "D" && kind == "deleted":
		delete(s.Manifest, p) // both deleted it: agreed
		return nil
	case e.Status == "D":
		delete(s.Manifest, p) // upstream deleted it; the local file stays as an addition
		rep.Conflicts = append(rep.Conflicts, Conflict{Path: p, Kind: "deleted-upstream"})
		return nil
	case kind == "deleted":
		side := p + UpstreamSuffix
		if _, err := WriteFile(s.root, side, up.content, up.mode); err != nil {
			return err
		}
		s.Manifest[p] = entryOf(up)
		rep.Conflicts = append(rep.Conflicts, Conflict{Path: p, Side: side, Kind: "deleted-locally"})
		return nil
	}
	s.Manifest[p] = entryOf(up)
	if inline && up.mode != "120000" {
		local, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(p)))
		if err == nil {
			merged, n := Merge3(base, local, up.content, "local", what)
			switch {
			case n == 0:
				if _, err := WriteFile(s.root, p, merged, up.mode); err != nil {
					return err
				}
				rep.Merged++
				return nil
			case n > 0:
				if _, err := WriteFile(s.root, p, merged, up.mode); err != nil {
					return err
				}
				rep.Conflicts = append(rep.Conflicts, Conflict{Path: p, Kind: "both-edited"})
				return nil
			}
		}
	}
	side := p + UpstreamSuffix
	if _, err := WriteFile(s.root, side, up.content, up.mode); err != nil {
		return err
	}
	rep.Conflicts = append(rep.Conflicts, Conflict{Path: p, Side: side, Kind: "both-edited"})
	return nil
}

func entryOf(b blob) Entry {
	return Entry{SHA256: hashOf(b.content), Mode: b.mode, Size: int64(len(b.content))}
}

// syncConflicted handles a default branch the server could not merge into
// the change: upstream's changes since the merge base are applied to the
// tree where they do not collide with the change or with local edits; the
// rest become conflicts; the merge itself is recorded by the next save.
func (s *State) syncConflicted(ctx context.Context, c *Client, tip, mergeBase string, me *MergeConflictError, dirty map[string]string, inline bool, rep *SyncReport) error {
	theirs, err := c.Diff(ctx, s.Org, s.Repo, mergeBase, tip, s.Paths)
	if err != nil {
		return err
	}
	ours, err := c.Diff(ctx, s.Org, s.Repo, mergeBase, s.Base, s.Paths)
	if err != nil {
		return err
	}
	oursSet := map[string]bool{}
	for _, e := range ours.Files {
		oursSet[e.Path] = true
	}
	conflicted := map[string]bool{}
	for _, p := range me.Conflicts {
		conflicted[p] = true
	}
	inArea := map[string]bool{}
	var need, baseNeed []string
	for _, e := range theirs.Files {
		inArea[e.Path] = true
		if e.Status != "D" {
			need = append(need, e.Path)
		}
		_, isDirty := dirty[e.Path]
		if (isDirty || oursSet[e.Path]) && e.Status != "A" {
			baseNeed = append(baseNeed, e.Path)
		}
	}
	for _, p := range me.Conflicts {
		if !inArea[p] {
			rep.Outside = append(rep.Outside, p)
		}
	}
	blobs, err := s.fetchBlobs(ctx, c, tip, need)
	if err != nil {
		return err
	}
	bases, err := s.fetchBlobs(ctx, c, mergeBase, baseNeed)
	if err != nil {
		return err
	}
	for _, e := range theirs.Files {
		p := e.Path
		kind, isDirty := dirty[p]
		up := blobs[p]
		base := bases[p].content
		switch {
		case !isDirty && !oursSet[p]:
			// untouched by the change: take upstream's version and record it,
			// so the save does not resend what the server merge yields anyway
			if e.Status == "D" {
				if err := RemoveFile(s.root, p); err != nil {
					return err
				}
				delete(s.Manifest, p)
				rep.Deleted++
			} else {
				ent, err := WriteFile(s.root, p, up.content, up.mode)
				if err != nil {
					return err
				}
				s.Manifest[p] = ent
				rep.Updated++
			}
		case isDirty || conflicted[p]:
			if !isDirty {
				kind = "changed" // the change's saved edit stands in for a local one
				if !s.pathExists(p) {
					kind = "deleted"
				}
			}
			before := s.Manifest[p]
			if err := s.conflict(e, kind, up, base, inline, rep, s.DefaultBranch); err != nil {
				return err
			}
			// the resolution is sent with the merge: keep the manifest on the
			// change's version so the local file counts as an edit
			if _, ok := s.Manifest[p]; ok && before.SHA256 != "" {
				s.Manifest[p] = before
			}
		default:
			// changed on both sides and merged cleanly by the server: do the
			// same merge here so the tree matches what the save will record
			local, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(p)))
			if err != nil || e.Status == "D" || up.mode == "120000" {
				if err := s.conflict(e, "changed", up, base, false, rep, s.DefaultBranch); err != nil {
					return err
				}
				continue
			}
			merged, n := Merge3(base, local, up.content, "local", s.DefaultBranch)
			if n != 0 {
				before := s.Manifest[p]
				if err := s.conflict(e, "changed", up, base, inline, rep, s.DefaultBranch); err != nil {
					return err
				}
				s.Manifest[p] = before
				continue
			}
			if _, err := WriteFile(s.root, p, merged, up.mode); err != nil {
				return err
			}
			rep.Merged++
		}
	}
	return nil
}
