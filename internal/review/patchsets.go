// SPDX-License-Identifier: AGPL-3.0-only

package review

import (
	"context"
	"errors"
	"fmt"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// recordPatchSet appends a patch set (base, head) to c unless the latest patch
// set already has that head. It also backfills patch set 1 from the change's
// recorded base/head for changes opened before review v1. base "" means
// "compute the merge base against the target tip". It returns the latest
// patch set and whether a new one was recorded.
func (s *Service) recordPatchSet(ctx context.Context, r ledger.Repo, c ledger.Change, base, head string, actor ledger.Actor) (PatchSet, bool, error) {
	rp := s.repoPath(r)
	if base == "" {
		base = c.BaseOID
		if tip, err := gitx.RevParse(ctx, rp, c.TargetRef); err == nil {
			if mb, err := gitx.MergeBase(ctx, rp, tip, head); err == nil {
				base = mb
			}
		}
	}
	existing, err := s.loadPatchSets(ctx, c)
	if err != nil {
		return PatchSet{}, false, err
	}
	var toInsert []PatchSet
	if len(existing) == 0 {
		toInsert = append(toInsert, PatchSet{Number: 1, BaseOID: c.BaseOID, HeadOID: c.HeadOID, PushedBy: s.pushedBy(ctx, r, c.SourceRef, c.HeadOID, actor)})
	}
	latest := PatchSet{}
	if len(existing) > 0 {
		latest = existing[len(existing)-1]
	} else {
		latest = toInsert[0]
	}
	if latest.HeadOID != head {
		toInsert = append(toInsert, PatchSet{Number: latest.Number + 1, BaseOID: base, HeadOID: head, PushedBy: s.pushedBy(ctx, r, c.SourceRef, head, actor)})
	}
	if len(toInsert) == 0 {
		return latest, false, nil
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return PatchSet{}, false, err
	}
	defer tx.Rollback()
	// Re-check under the transaction: a concurrent request may have recorded it.
	if err := tx.LockRow("changes", c.ID); err != nil {
		return PatchSet{}, false, err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(number), 0) FROM patch_sets WHERE change_id = ?`, c.ID).Scan(&n); err != nil {
		return PatchSet{}, false, err
	}
	if n != len(existing) {
		tx.Rollback()
		return s.recordPatchSet(ctx, r, c, base, head, actor)
	}
	for i := range toInsert {
		if err := insertPatchSetTx(ctx, tx, r, c, &toInsert[i]); err != nil {
			return PatchSet{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return PatchSet{}, false, err
	}
	return toInsert[len(toInsert)-1], true, nil
}

// SyncPatchSets reconciles a change's patch sets with its source branch: it
// backfills patch set 1, and when the branch moved (a push the server saw
// only as a ref update) it records a new patch set and updates the change's
// head. A reverted change whose branch moved is reopened. It returns the
// refreshed change and its latest patch set. A missing source branch is not
// an error here (merged changes delete theirs); landing checks for it.
func (s *Service) SyncPatchSets(ctx context.Context, r ledger.Repo, c ledger.Change) (ledger.Change, PatchSet, error) {
	// Callers often hold a stale copy of the change; the ledger's row is the truth.
	c, err := s.DB.Change(ctx, r, c.Number)
	if err != nil {
		return c, PatchSet{}, err
	}
	// Only backfill from the change's recorded head; never append it as a new
	// patch set. The row's head lags the branch until a sync updates it, and a
	// concurrent sync may already have recorded the branch's head, in which
	// case appending the lagging head would record it again (and the branch's
	// head once more below).
	existing, err := s.loadPatchSets(ctx, c)
	if err != nil {
		return c, PatchSet{}, err
	}
	var latest PatchSet
	if len(existing) == 0 {
		latest, _, err = s.recordPatchSet(ctx, r, c, c.BaseOID, c.HeadOID, ledger.System)
		if err != nil {
			return c, latest, err
		}
	} else {
		latest = existing[len(existing)-1]
	}
	if c.State != "open" && c.State != StateReverted {
		return c, latest, nil
	}
	rp := s.repoPath(r)
	head, err := gitx.RevParse(ctx, rp, c.SourceRef)
	if err != nil {
		return c, latest, nil
	}
	if head == latest.HeadOID && head == c.HeadOID {
		return c, latest, nil
	}
	if head != latest.HeadOID {
		latest, _, err = s.recordPatchSet(ctx, r, c, "", head, ledger.System)
		if err != nil {
			return c, latest, err
		}
	}
	if head != c.HeadOID {
		pusher := s.pushedBy(ctx, r, c.SourceRef, head, ledger.System)
		if err := s.DB.UpdateChangeHead(ctx, r, c, head, pusher); err != nil {
			return c, latest, err
		}
		if c.State == StateReverted {
			if err := s.setChangeState(ctx, r, c, "open", "io.ore.change.reopened", pusher, map[string]any{"reason": "new patch set after revert", "head_oid": head}); err != nil {
				return c, latest, err
			}
		}
		c, err = s.DB.Change(ctx, r, c.Number)
		if err != nil {
			return c, latest, err
		}
	}
	return c, latest, nil
}

// PatchSets returns a change's patch sets, oldest first, after syncing with
// the source branch.
func (s *Service) PatchSets(ctx context.Context, r ledger.Repo, c ledger.Change) ([]PatchSet, error) {
	if _, _, err := s.SyncPatchSets(ctx, r, c); err != nil {
		return nil, err
	}
	return s.loadPatchSets(ctx, c)
}

// PatchSet returns one patch set by number (0 = latest).
func (s *Service) PatchSet(ctx context.Context, r ledger.Repo, c ledger.Change, number int) (PatchSet, error) {
	sets, err := s.PatchSets(ctx, r, c)
	if err != nil {
		return PatchSet{}, err
	}
	if number == 0 && len(sets) > 0 {
		return sets[len(sets)-1], nil
	}
	for _, p := range sets {
		if p.Number == number {
			return p, nil
		}
	}
	return PatchSet{}, fmt.Errorf("%w: patch set %d", ledger.ErrNotFound, number)
}

// Interdiff computes what changed in the patch between two patch sets. When
// the bases are equal it is the plain diff between the heads. When the change
// was rebased, the old patch is first replayed onto the new base with a
// three-way merge so the interdiff shows only the patch's own evolution; if
// that replay conflicts it falls back to a diff of the two diffs.
func (s *Service) Interdiff(ctx context.Context, r ledger.Repo, c ledger.Change, from, to int, withRangeDiff bool) (Interdiff, error) {
	sets, err := s.PatchSets(ctx, r, c)
	if err != nil {
		return Interdiff{}, err
	}
	find := func(n int) (PatchSet, error) {
		if n == 0 && len(sets) > 0 {
			return sets[len(sets)-1], nil
		}
		for _, p := range sets {
			if p.Number == n {
				return p, nil
			}
		}
		return PatchSet{}, fmt.Errorf("%w: patch set %d", ledger.ErrNotFound, n)
	}
	a, err := find(from)
	if err != nil {
		return Interdiff{}, err
	}
	b, err := find(to)
	if err != nil {
		return Interdiff{}, err
	}
	rp := s.repoPath(r)
	id := Interdiff{From: a, To: b, Rebased: a.BaseOID != b.BaseOID}
	if withRangeDiff {
		if rd, err := rangeDiff(ctx, rp, a.BaseOID, a.HeadOID, b.BaseOID, b.HeadOID); err == nil {
			id.RangeDiff = rd
		} else {
			id.Note = "range-diff unavailable: " + err.Error()
		}
	}
	if a.HeadOID == b.HeadOID {
		id.Identical = true
		return id, nil
	}
	left := a.HeadOID
	if id.Rebased {
		tree, _, err := mergeTreeOnto(ctx, rp, a.BaseOID, b.BaseOID, a.HeadOID)
		switch {
		case errors.Is(err, gitx.ErrConflict):
			da, err1 := gitx.Diff(ctx, rp, a.BaseOID, a.HeadOID)
			db, err2 := gitx.Diff(ctx, rp, b.BaseOID, b.HeadOID)
			if err1 != nil || err2 != nil {
				return id, errors.Join(err1, err2)
			}
			dd, err := diffTexts(ctx, rp, da, db)
			if err != nil {
				return id, err
			}
			id.Diff, id.Note = dd, "the old patch set does not apply cleanly onto the new base; showing a diff of the two diffs"
			id.Identical = dd == ""
			return id, nil
		case err != nil:
			return id, err
		}
		left = tree
	}
	diff, err := gitx.Diff(ctx, rp, left, b.HeadOID)
	if err != nil {
		return id, err
	}
	id.Diff = diff
	id.Identical = diff == ""
	return id, nil
}
