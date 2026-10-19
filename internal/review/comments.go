// SPDX-License-Identifier: AGPL-3.0-only

package review

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/orevcs/ore/internal/ledger"
)

// laneFor resolves the lane for a request: derived from the actor, lowered on
// request; a non-human actor can never claim the human lane.
func laneFor(actor ledger.Actor, requested string) (string, error) {
	lane := LaneFor(actor)
	if requested == "" {
		return lane, nil
	}
	switch requested {
	case LaneHuman, LaneBot, LaneAgent:
	default:
		return "", fmt.Errorf("%w: lane must be human|bot|agent", ErrBadRequest)
	}
	if requested == LaneHuman && actor.Kind != "human" {
		return "", fmt.Errorf("%w: a %s actor cannot post in the human lane", ErrBadRequest, actor.Kind)
	}
	return requested, nil
}

// PostComment anchors a comment to (patch set, path, lines). Comments are
// allowed on any change state (a merged change may receive a late comment);
// the lane is recorded.
func (s *Service) PostComment(ctx context.Context, r ledger.Repo, c ledger.Change, req CommentRequest, actor ledger.Actor) (Comment, error) {
	if strings.TrimSpace(req.Body) == "" {
		return Comment{}, fmt.Errorf("%w: body is required", ErrBadRequest)
	}
	lane, err := laneFor(actor, req.Lane)
	if err != nil {
		return Comment{}, err
	}
	c, latest, err := s.SyncPatchSets(ctx, r, c)
	if err != nil {
		return Comment{}, err
	}
	ps := latest.Number
	if req.PatchSet != 0 {
		if req.PatchSet < 1 || req.PatchSet > latest.Number {
			return Comment{}, fmt.Errorf("%w: patch set %d does not exist (latest is %d)", ErrBadRequest, req.PatchSet, latest.Number)
		}
		ps = req.PatchSet
	}
	cm := Comment{PatchSet: ps, Path: req.Path, Body: req.Body, Author: actor, Lane: lane}
	if req.StartLine != 0 || req.EndLine != 0 {
		if req.Path == "" {
			return Comment{}, fmt.Errorf("%w: a line comment needs a path", ErrBadRequest)
		}
		start, end := req.StartLine, req.EndLine
		if end == 0 {
			end = start
		}
		if start < 1 || end < start {
			return Comment{}, fmt.Errorf("%w: bad line range %d-%d", ErrBadRequest, start, end)
		}
		cm.StartLine, cm.EndLine = &start, &end
	}
	if err := s.insertComment(ctx, r, c, &cm); err != nil {
		return cm, err
	}
	list := []Comment{cm}
	s.anchorComments(ctx, r, c, list, latest)
	return list[0], nil
}

// Comments lists a change's comments with each one re-anchored to the latest
// patch set, or marked outdated when its lines no longer exist.
func (s *Service) Comments(ctx context.Context, r ledger.Repo, c ledger.Change) ([]Comment, error) {
	c, latest, err := s.SyncPatchSets(ctx, r, c)
	if err != nil {
		return nil, err
	}
	list, err := s.loadComments(ctx, c)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []Comment{}
	}
	s.anchorComments(ctx, r, c, list, latest)
	return list, nil
}

// ResolveComment marks a comment resolved (or unresolved).
func (s *Service) ResolveComment(ctx context.Context, r ledger.Repo, c ledger.Change, id string, resolved bool, actor ledger.Actor) (Comment, error) {
	cm, err := s.loadComment(ctx, c, id)
	if err != nil {
		return cm, err
	}
	if err := s.markResolved(ctx, r, c, cm, resolved, actor); err != nil {
		return cm, err
	}
	cm.Resolved = resolved
	return cm, nil
}

// anchorComments fills Anchor/Outdated against the latest patch set. Line
// mapping goes through the zero-context diff between the comment's patch set
// head and the latest head, per path; the result is cached per (head, path).
func (s *Service) anchorComments(ctx context.Context, r ledger.Repo, c ledger.Change, list []Comment, latest PatchSet) {
	sets, err := s.loadPatchSets(ctx, c)
	if err != nil {
		return
	}
	heads := map[int]string{}
	for _, p := range sets {
		heads[p.Number] = p.HeadOID
	}
	rp := s.repoPath(r)
	type key struct{ head, path string }
	cache := map[key][]hunk{}
	exists := map[string]bool{}
	for i := range list {
		cm := &list[i]
		if cm.StartLine == nil || cm.Path == "" {
			continue // change-level comment: never outdated
		}
		if cm.PatchSet == latest.Number {
			cm.Anchor = &Anchor{PatchSet: latest.Number, StartLine: *cm.StartLine, EndLine: *cm.EndLine}
			continue
		}
		from, ok := heads[cm.PatchSet]
		if !ok {
			cm.Outdated = true
			continue
		}
		if ex, seen := exists[cm.Path]; !seen {
			ex = pathExists(ctx, rp, latest.HeadOID, cm.Path)
			exists[cm.Path] = ex
		}
		if !exists[cm.Path] {
			cm.Outdated = true
			continue
		}
		k := key{from, cm.Path}
		hunks, seen := cache[k]
		if !seen {
			diff, err := diffNoContext(ctx, rp, from, latest.HeadOID, cm.Path)
			if err != nil {
				cm.Outdated = true
				continue
			}
			hunks = parseHunks(diff)
			cache[k] = hunks
		}
		start, ok1 := mapLine(hunks, *cm.StartLine)
		end, ok2 := mapLine(hunks, *cm.EndLine)
		if !ok1 || !ok2 {
			cm.Outdated = true
			continue
		}
		cm.Anchor = &Anchor{PatchSet: latest.Number, StartLine: start, EndLine: end}
	}
}

// hunk is one @@ -a,b +c,d @@ header of a zero-context diff.
type hunk struct{ oldStart, oldLen, newStart, newLen int }

// parseHunks extracts hunk headers from a unified diff.
func parseHunks(diff string) []hunk {
	var out []hunk
	for _, line := range strings.Split(diff, "\n") {
		if !strings.HasPrefix(line, "@@ ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		os, ol := parseRange(fields[1])
		ns, nl := parseRange(fields[2])
		out = append(out, hunk{os, ol, ns, nl})
	}
	return out
}

func parseRange(s string) (start, length int) {
	s = strings.TrimLeft(s, "-+")
	length = 1
	if i := strings.IndexByte(s, ','); i >= 0 {
		length, _ = strconv.Atoi(s[i+1:])
		s = s[:i]
	}
	start, _ = strconv.Atoi(s)
	return start, length
}

// mapLine maps a 1-based line of the old side through the hunks to the new
// side. It reports false when the line was deleted or modified.
func mapLine(hunks []hunk, line int) (int, bool) {
	delta := 0
	for _, h := range hunks {
		if h.oldLen == 0 {
			// pure insertion after old line h.oldStart
			if line <= h.oldStart {
				return line + delta, true
			}
			delta += h.newLen
			continue
		}
		if line < h.oldStart {
			return line + delta, true
		}
		if line < h.oldStart+h.oldLen {
			return 0, false
		}
		delta += h.newLen - h.oldLen
	}
	return line + delta, true
}
