// SPDX-License-Identifier: AGPL-3.0-only

// Package review implements Changes: the unit of review with a stable
// change-id (ADR 0008), opened from a branch and landed by a server-side merge
// with a compare-and-swap ref update (ADR 0014). No working copy is involved.
package review

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// Service opens, inspects and lands changes.
type Service struct {
	DB       *ledger.DB
	ReposDir string
}

// ErrNothingToMerge means the source is already contained in the target.
var ErrNothingToMerge = errors.New("nothing to merge: source is already in target")

// ConflictError lists conflicting paths from a server-side merge.
type ConflictError struct{ Paths []string }

func (e *ConflictError) Error() string {
	return "merge conflict in: " + strings.Join(e.Paths, ", ")
}

func (s *Service) repoPath(r ledger.Repo) string {
	return filepath.Join(s.ReposDir, r.Org, r.Name+".git")
}

// NewChangeID returns a Gerrit-style change id (I + 40 hex), used when the
// commits carry no Jujutsu change-id header or Change-Id trailer.
func NewChangeID() string {
	var b [20]byte
	rand.Read(b[:])
	return "I" + hex.EncodeToString(b[:])
}

// OpenRequest describes a change to open.
type OpenRequest struct {
	SourceRef  string `json:"source_ref"` // branch name or full ref
	TargetRef  string `json:"target_ref"` // defaults to the repository's default branch
	Title      string `json:"title"`
	Body       string `json:"body"`
	Provenance string `json:"provenance"` // JSON; "{}" when unknown
}

func fullRef(name string) string {
	if strings.HasPrefix(name, "refs/") {
		return name
	}
	return "refs/heads/" + name
}

// Open creates a change from a branch, or records a new patch set if an open
// change for that branch already exists.
func (s *Service) Open(ctx context.Context, r ledger.Repo, req OpenRequest, actor ledger.Actor) (ledger.Change, error) {
	rp := s.repoPath(r)
	src, tgt := fullRef(req.SourceRef), fullRef(req.TargetRef)
	if req.TargetRef == "" {
		tgt = "refs/heads/" + r.DefaultBranch
	}
	head, err := gitx.RevParse(ctx, rp, src)
	if err != nil {
		return ledger.Change{}, fmt.Errorf("source %s: %w", src, err)
	}
	target, err := gitx.RevParse(ctx, rp, tgt)
	if err != nil {
		return ledger.Change{}, fmt.Errorf("target %s: %w", tgt, err)
	}
	base, err := gitx.MergeBase(ctx, rp, target, head)
	if err != nil {
		return ledger.Change{}, err
	}
	if base == head {
		return ledger.Change{}, ErrNothingToMerge
	}
	if existing, err := s.DB.OpenChangeBySourceRef(ctx, r, src); err == nil {
		if existing.HeadOID != head {
			if err := s.DB.UpdateChangeHead(ctx, r, existing, head, actor); err != nil {
				return existing, err
			}
			existing.HeadOID = head
		}
		return existing, nil
	}
	title := req.Title
	if title == "" {
		if log, err := gitx.Log(ctx, rp, head, 1); err == nil && len(log) == 1 {
			title = log[0].Subject
		} else {
			title = req.SourceRef
		}
	}
	c := ledger.Change{ChangeID: NewChangeID(), Title: title, Body: req.Body, SourceRef: src, TargetRef: tgt, BaseOID: base, HeadOID: head, Provenance: req.Provenance}
	if err := s.DB.InsertChange(ctx, r, &c, actor); err != nil {
		return c, err
	}
	return c, nil
}

// Diff returns the change's diff against its merge base.
func (s *Service) Diff(ctx context.Context, r ledger.Repo, c ledger.Change) (string, error) {
	return gitx.Diff(ctx, s.repoPath(r), c.BaseOID, c.HeadOID)
}

// DiffStat returns the change's stat line.
func (s *Service) DiffStat(ctx context.Context, r ledger.Repo, c ledger.Change) (string, error) {
	return gitx.DiffStat(ctx, s.repoPath(r), c.BaseOID, c.HeadOID)
}

// Merge lands the change server-side: three-way merge of the head into the
// current target tip with git merge-tree, a merge commit, and a compare-and-
// swap update of the target ref. Retries once if the target moved underneath.
func (s *Service) Merge(ctx context.Context, r ledger.Repo, c ledger.Change, actor ledger.Actor) (string, error) {
	if c.State != "open" {
		return "", fmt.Errorf("change #%d is %s", c.Number, c.State)
	}
	rp := s.repoPath(r)
	head, err := gitx.RevParse(ctx, rp, c.SourceRef)
	if err != nil {
		return "", fmt.Errorf("source %s: %w", c.SourceRef, err)
	}
	if head != c.HeadOID {
		if err := s.DB.UpdateChangeHead(ctx, r, c, head, actor); err != nil {
			return "", err
		}
		c.HeadOID = head
	}
	author := gitx.Author{Name: "Ore", Email: "ore@" + r.Org + ".invalid"}
	if actor.Kind == "human" {
		author = gitx.Author{Name: actor.ID, Email: actor.ID + "@" + r.Org + ".invalid"}
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		old, err := gitx.RevParse(ctx, rp, c.TargetRef)
		if err != nil {
			return "", fmt.Errorf("target %s: %w", c.TargetRef, err)
		}
		if gitx.IsAncestor(ctx, rp, head, old) {
			return "", ErrNothingToMerge
		}
		tree, conflicts, err := gitx.MergeTree(ctx, rp, old, head)
		if errors.Is(err, gitx.ErrConflict) {
			return "", &ConflictError{Paths: strings.Split(strings.TrimSpace(conflicts), "\n")}
		}
		if err != nil {
			return "", err
		}
		msg := fmt.Sprintf("Merge change #%d: %s\n\nChange-Id: %s\nOre-Actor: %s/%s\n", c.Number, c.Title, c.ChangeID, actor.Kind, actor.ID)
		mergeOID, err := gitx.CommitTree(ctx, rp, tree, []string{old, head}, msg, author)
		if err != nil {
			return "", err
		}
		if err := gitx.UpdateRef(ctx, rp, c.TargetRef, mergeOID, old); err != nil {
			lastErr = err
			continue // the target moved; recompute against the new tip
		}
		if err := s.DB.MarkMerged(ctx, r, c, mergeOID, old, actor); err != nil {
			return mergeOID, err
		}
		return mergeOID, nil
	}
	return "", fmt.Errorf("merge lost the race three times: %w", lastErr)
}
