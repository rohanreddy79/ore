// SPDX-License-Identifier: AGPL-3.0-only

package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// SaveResult says what a save did.
type SaveResult struct {
	Queued   bool
	Commit   string
	Change   int
	PatchSet int
	Edits    []Edit
}

// Save records the working tree's edits as a commit on the change's hidden
// branch, opening the change on the first save, and updates the manifest.
// When the server is unreachable the save is queued in .ore/pending.json
// and sent by the next save or sync.
func (s *State) Save(ctx context.Context, c *Client, message string, out io.Writer) (*SaveResult, error) {
	if strings.TrimSpace(message) == "" {
		return nil, errors.New("a message is required: ore save -m \"what changed and why\"")
	}
	flushed, ferr := s.Flush(ctx, c, out)
	if ferr != nil && !Unreachable(ferr) {
		return nil, ferr
	}
	if uc := s.UnresolvedConflicts(); len(uc) > 0 {
		return nil, fmt.Errorf("%d conflict(s) from the last sync are unresolved: %s\nresolve each file, delete its %s copy (or remove the markers), then save again", len(uc), conflictPaths(uc), UpstreamSuffix)
	}
	cur, err := Scan(s.root, s.Manifest, s.ignore())
	if err != nil {
		return nil, err
	}
	edits := Diff(s.Manifest, cur)
	edits = s.withResolutions(edits, cur)
	if len(edits) == 0 {
		if flushed > 0 {
			return &SaveResult{Commit: s.Base, Change: s.changeNumber()}, nil
		}
		fmt.Fprintln(out, "nothing to save: the working tree matches the last save")
		return &SaveResult{Commit: s.Base, Change: s.changeNumber()}, nil
	}
	if err := LoadContent(s.root, edits); err != nil {
		return nil, err
	}
	title, body := splitMessage(message)
	newBranch := false
	if s.Change == nil {
		newBranch = true
	} else if s.Change.Number > 0 && ferr == nil {
		ch, err := c.Change(ctx, s.Org, s.Repo, s.Change.Number)
		switch {
		case Unreachable(err):
			ferr = err
		case err != nil:
			return nil, err
		case ch.State == "merged":
			fmt.Fprintf(out, "change #%d has landed; this save starts a new change\n", ch.Number)
			newBranch = true
		case ch.State == "closed":
			return nil, fmt.Errorf("change #%d was closed; run `ore sync` to return to %s (your edits are kept), then save", ch.Number, s.DefaultBranch)
		}
	}
	if newBranch {
		s.Change = &ChangeRef{Branch: BranchName(s.User, title), Title: title}
	}
	req := CommitRequest{Branch: s.Change.Branch, Message: message, Merge: s.PendingMerge, Files: FileEdits(edits)}
	if newBranch || s.Change.Number > 0 {
		req.Base = s.Base // start the new branch here, or insist the change is still where we left it
	}
	if ferr != nil {
		return s.queue(req, edits, cur, out)
	}
	res, err := c.Commit(ctx, s.Org, s.Repo, req)
	var me *MergeConflictError
	switch {
	case Unreachable(err):
		return s.queue(req, edits, cur, out)
	case errors.Is(err, ErrBranchMoved):
		return nil, fmt.Errorf("someone else saved to change #%d since your last sync; run `ore sync` first (your edits are kept)", s.Change.Number)
	case errors.As(err, &me):
		return nil, fmt.Errorf("%s moved and conflicts with this change in %s; run `ore sync` and resolve, then save", s.DefaultBranch, strings.Join(me.Unresolved, ", "))
	case err != nil:
		if newBranch {
			s.Change = nil
		}
		return nil, err
	}
	s.recordSaved(res.Commit, edits, cur)
	result := &SaveResult{Commit: res.Commit, Edits: edits}
	if s.Change.Number == 0 {
		ch, err := c.OpenChange(ctx, s.Org, s.Repo, s.Change.Branch, title, body)
		if err != nil {
			_ = s.Write()
			return nil, fmt.Errorf("saved as %s but the change could not be opened: %w (the next save retries)", short(res.Commit), err)
		}
		s.Change.Number = ch.Number
	}
	if err := s.Write(); err != nil {
		return nil, err
	}
	result.Change = s.Change.Number
	if sets, err := c.PatchSets(ctx, s.Org, s.Repo, s.Change.Number); err == nil {
		result.PatchSet = len(sets)
	}
	if result.PatchSet > 0 {
		fmt.Fprintf(out, "saved change #%d, patch set %d (%s): %s\n", result.Change, result.PatchSet, short(res.Commit), Summary(edits))
	} else {
		fmt.Fprintf(out, "saved change #%d (%s): %s\n", result.Change, short(res.Commit), Summary(edits))
	}
	return result, nil
}

// withResolutions adds the paths of a pending merge's conflicts to the
// edits, whether or not they differ from the manifest: keeping one's own
// version is a resolution the server must be told about.
func (s *State) withResolutions(edits []Edit, cur map[string]Entry) []Edit {
	if s.PendingMerge == "" || len(s.Conflicts) == 0 {
		return edits
	}
	have := map[string]bool{}
	for _, e := range edits {
		have[e.Path] = true
	}
	for _, c := range s.Conflicts {
		if have[c.Path] {
			continue
		}
		if e, ok := cur[c.Path]; ok {
			edits = append(edits, Edit{Path: c.Path, Kind: "changed", Mode: e.Mode, SHA256: e.SHA256})
		} else {
			edits = append(edits, Edit{Path: c.Path, Kind: "deleted"})
		}
	}
	return edits
}

// queue stores the save for later and moves the manifest forward as if it
// had been sent, so later edits diff against it.
func (s *State) queue(req CommitRequest, edits []Edit, cur map[string]Entry, out io.Writer) (*SaveResult, error) {
	q, err := LoadQueue(s.root)
	if err != nil {
		return nil, err
	}
	ps := PendingSave{Message: req.Message, Branch: req.Branch, Merge: req.Merge, Files: req.Files, QueuedAt: Now().Format(time.RFC3339)}
	if len(q.Saves) == 0 && s.Change.Number == 0 {
		ps.Base = s.Base // the new branch starts where the tree was opened
	}
	q.Saves = append(q.Saves, ps)
	if err := q.Save(s.root); err != nil {
		return nil, err
	}
	s.recordSaved("", edits, cur)
	if err := s.Write(); err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "server unreachable: save queued (%s, %d queued in all); it is sent by the next save or sync\n", Summary(edits), len(q.Saves))
	return &SaveResult{Queued: true, Edits: edits}, nil
}

// recordSaved moves the manifest to the saved state.
func (s *State) recordSaved(commit string, edits []Edit, cur map[string]Entry) {
	for _, e := range edits {
		if e.Kind == "deleted" {
			delete(s.Manifest, e.Path)
		} else {
			s.Manifest[e.Path] = cur[e.Path]
		}
	}
	if commit != "" {
		s.Base = commit
	}
	s.PendingMerge = ""
	s.Conflicts = nil
}

// Flush sends the queued saves in order. It stops at the first failure and
// keeps the rest; it returns how many were sent.
func (s *State) Flush(ctx context.Context, c *Client, out io.Writer) (int, error) {
	q, err := LoadQueue(s.root)
	if err != nil || len(q.Saves) == 0 {
		return 0, err
	}
	if s.Change != nil && s.Change.Number > 0 {
		ch, err := c.Change(ctx, s.Org, s.Repo, s.Change.Number)
		if err != nil {
			return 0, err
		}
		switch ch.State {
		case "merged":
			branch := BranchName(s.User, firstLine(q.Saves[0].Message))
			fmt.Fprintf(out, "change #%d has landed; the queued saves start a new change\n", ch.Number)
			for i := range q.Saves {
				q.Saves[i].Branch = branch
			}
			q.Saves[0].Base = s.Base
			s.Change = &ChangeRef{Branch: branch, Title: firstLine(q.Saves[0].Message)}
		case "closed":
			return 0, fmt.Errorf("change #%d was closed while %d save(s) were queued; run `ore sync` to return to %s, then save", ch.Number, len(q.Saves), s.DefaultBranch)
		}
	}
	n := 0
	for len(q.Saves) > 0 {
		e := q.Saves[0]
		res, err := c.Commit(ctx, s.Org, s.Repo, CommitRequest{Branch: e.Branch, Message: e.Message, Base: e.Base, Merge: e.Merge, Files: e.Files})
		if err != nil {
			_ = q.Save(s.root)
			if Unreachable(err) {
				return n, err
			}
			return n, fmt.Errorf("queued save %q could not be sent: %w", firstLine(e.Message), err)
		}
		s.Base = res.Commit
		q.Saves = q.Saves[1:]
		if err := q.Save(s.root); err != nil {
			return n, err
		}
		n++
		if s.Change == nil {
			s.Change = &ChangeRef{Branch: e.Branch, Title: firstLine(e.Message)}
		}
		if s.Change.Number == 0 {
			title, body := splitMessage(e.Message)
			ch, err := c.OpenChange(ctx, s.Org, s.Repo, s.Change.Branch, title, body)
			if err != nil {
				_ = s.Write()
				return n, fmt.Errorf("queued save sent as %s but the change could not be opened: %w", short(res.Commit), err)
			}
			s.Change.Number = ch.Number
		}
		if err := s.Write(); err != nil {
			return n, err
		}
		fmt.Fprintf(out, "sent queued save %q as %s (change #%d)\n", firstLine(e.Message), short(res.Commit), s.Change.Number)
	}
	return n, nil
}

func (s *State) changeNumber() int {
	if s.Change == nil {
		return 0
	}
	return s.Change.Number
}

// ignore loads .oreignore plus the side files of recorded conflicts.
func (s *State) ignore() *Ignore {
	ig := LoadIgnore(s.root)
	for _, c := range s.Conflicts {
		if c.Side != "" {
			ig.Add(c.Side)
		}
	}
	return ig
}

func conflictPaths(cs []Conflict) string {
	var ps []string
	for _, c := range cs {
		ps = append(ps, c.Path)
	}
	return strings.Join(ps, ", ")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func splitMessage(m string) (title, body string) {
	m = strings.TrimSpace(m)
	if i := strings.IndexByte(m, '\n'); i >= 0 {
		return strings.TrimSpace(m[:i]), strings.TrimSpace(m[i+1:])
	}
	return m, ""
}

var slugClean = regexp.MustCompile(`[^a-z0-9]+`)

// Slug turns a message into a branch-safe word list.
func Slug(title string) string {
	s := strings.Trim(slugClean.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	if s == "" {
		return "change"
	}
	return s
}

// BranchName is the hidden branch of a change: ore/<user>/<slug>-<id>. The
// id keeps two changes with the same title apart; the user never sees it.
func BranchName(user, title string) string {
	var b [3]byte
	rand.Read(b[:])
	return "ore/" + CleanName(user) + "/" + Slug(title) + "-" + hex.EncodeToString(b[:])
}

// Dirty returns the working tree's edits against the manifest.
func (s *State) Dirty() ([]Edit, error) {
	cur, err := Scan(s.root, s.Manifest, s.ignore())
	if err != nil {
		return nil, err
	}
	return Diff(s.Manifest, cur), nil
}

// pathExists reports whether rel exists in the working tree.
func (s *State) pathExists(rel string) bool {
	_, err := os.Lstat(filepath.Join(s.root, filepath.FromSlash(rel)))
	return err == nil
}
