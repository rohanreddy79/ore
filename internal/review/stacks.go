// SPDX-License-Identifier: AGPL-3.0-only

package review

import (
	"context"
	"errors"
	"fmt"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// OpenStack opens (or adopts) one change per branch, bottom first: the first
// branch targets the stack's target, every other branch targets the branch
// below it. Branches must build on each other (each contains the previous).
func (s *Service) OpenStack(ctx context.Context, r ledger.Repo, req StackRequest, actor ledger.Actor) (Stack, error) {
	if len(req.Branches) == 0 {
		return Stack{}, fmt.Errorf("%w: branches are required (bottom first)", ErrBadRequest)
	}
	rp := s.repoPath(r)
	target := "refs/heads/" + r.DefaultBranch
	if req.TargetRef != "" {
		target = fullRef(req.TargetRef)
	}
	if _, err := gitx.RevParse(ctx, rp, target); err != nil {
		return Stack{}, fmt.Errorf("%w: target %s not found", ErrBadRequest, target)
	}
	heads := make([]string, len(req.Branches))
	for i, b := range req.Branches {
		head, err := gitx.RevParse(ctx, rp, fullRef(b))
		if err != nil {
			return Stack{}, fmt.Errorf("%w: branch %s not found", ErrBadRequest, b)
		}
		heads[i] = head
		if i > 0 && !gitx.IsAncestor(ctx, rp, heads[i-1], head) {
			return Stack{}, fmt.Errorf("%w: %s does not build on %s (a stack is linear, bottom first)", ErrBadRequest, b, req.Branches[i-1])
		}
	}
	st := Stack{TargetRef: target, CreatedBy: actor, ChangeIDs: []string{}}
	var numbers []int
	for i, b := range req.Branches {
		t := target
		if i > 0 {
			t = fullRef(req.Branches[i-1])
		}
		ch, err := s.Open(ctx, r, OpenRequest{SourceRef: b, TargetRef: t}, actor)
		if errors.Is(err, ErrNothingToMerge) {
			return Stack{}, fmt.Errorf("%w: %s has no commits beyond %s", ErrBadRequest, b, t)
		}
		if err != nil {
			return Stack{}, err
		}
		if ch.TargetRef != t {
			tip, _ := gitx.RevParse(ctx, rp, t)
			base, err := gitx.MergeBase(ctx, rp, tip, ch.HeadOID)
			if err != nil {
				return Stack{}, err
			}
			if err := s.retargetChange(ctx, r, ch, t, base, actor); err != nil {
				return Stack{}, err
			}
		}
		st.ChangeIDs = append(st.ChangeIDs, ch.ID)
		numbers = append(numbers, ch.Number)
	}
	if err := s.insertStack(ctx, r, &st, numbers); err != nil {
		return st, err
	}
	return s.Stack(ctx, r, st.ID)
}

// Stack returns a stack with its changes in order.
func (s *Service) Stack(ctx context.Context, r ledger.Repo, id string) (Stack, error) {
	st, err := s.loadStack(ctx, r, id)
	if err != nil {
		return st, err
	}
	st.Changes = []ledger.Change{}
	for _, cid := range st.ChangeIDs {
		c, err := s.changeByID(ctx, r, cid)
		if err != nil {
			return st, err
		}
		st.Changes = append(st.Changes, c)
		if st.FailedChangeID != nil && *st.FailedChangeID == cid {
			st.FailedChangeNumber = c.Number
		}
	}
	return st, nil
}

// LandStack lands a stack bottom-up. Each change above the first is rebased
// server-side onto the target's new tip (its commits replayed with
// merge-tree and commit-tree, no working copy), its branch moved with
// compare-and-swap, a new patch set recorded, and then landed with the
// requested strategy. The first conflict or refusal aborts: lower changes
// stay merged, the failing and upper changes stay open, and the stack
// records which change failed and why. Landing again resumes from there.
func (s *Service) LandStack(ctx context.Context, r ledger.Repo, st Stack, req LandRequest, actor ledger.Actor) (Stack, []Landing, error) {
	switch st.State {
	case "landed":
		return st, nil, fmt.Errorf("%w: stack %s already landed", ErrNotOpen, st.ID)
	case "landing":
		return st, nil, fmt.Errorf("%w: stack %s is landing", ErrNotOpen, st.ID)
	}
	st.FailedChangeID, st.Error = nil, ""
	if err := s.updateStack(ctx, r, &st, "landing", "io.ore.stack.landing", actor, map[string]any{"strategy": req.Strategy}); err != nil {
		return st, nil, err
	}
	rp := s.repoPath(r)
	committer := gitx.Author{Name: "Ore", Email: "ore@" + r.Org + ".invalid"}
	landings := []Landing{}
	abort := func(c ledger.Change, cause error) (Stack, []Landing, error) {
		st.FailedChangeID = &c.ID
		st.Error = fmt.Sprintf("change #%d: %v", c.Number, cause)
		merged, open := []int{}, []int{}
		for _, cid := range st.ChangeIDs {
			if x, err := s.changeByID(ctx, r, cid); err == nil {
				if x.State == "merged" {
					merged = append(merged, x.Number)
				} else {
					open = append(open, x.Number)
				}
			}
		}
		_ = s.updateStack(ctx, r, &st, "aborted", "io.ore.stack.aborted", actor, map[string]any{"failed_change_number": c.Number, "merged": merged, "not_landed": open})
		full, err := s.Stack(ctx, r, st.ID)
		if err != nil {
			full = st
		}
		return full, landings, &StackError{Stack: full, Landings: landings, Err: cause}
	}
	for i, cid := range st.ChangeIDs {
		c, err := s.changeByID(ctx, r, cid)
		if err != nil {
			return abort(ledger.Change{ID: cid}, err)
		}
		if c.State == "merged" || c.State == "closed" {
			continue
		}
		if c.State != "open" {
			return abort(c, fmt.Errorf("%w: change #%d is %s", ErrNotOpen, c.Number, c.State))
		}
		if c, _, err = s.SyncPatchSets(ctx, r, c); err != nil {
			return abort(c, err)
		}
		head, err := gitx.RevParse(ctx, rp, c.SourceRef)
		if err != nil {
			return abort(c, fmt.Errorf("%w: %s", ErrSourceGone, c.SourceRef))
		}
		if i > 0 {
			tip, err := gitx.RevParse(ctx, rp, st.TargetRef)
			if err != nil {
				return abort(c, err)
			}
			commits, err := commitsToReplay(ctx, rp, tip, head)
			if err != nil {
				return abort(c, err)
			}
			res, err := rebaseCommits(ctx, rp, commits, tip, committer)
			if errors.Is(err, gitx.ErrConflict) {
				return abort(c, fmt.Errorf("rebasing %s onto %s: %w", short(res.Conflict), short(tip), &ConflictError{Paths: res.Conflicts}))
			}
			if err != nil {
				return abort(c, err)
			}
			if res.NewHead != head {
				if err := gitx.UpdateRef(ctx, rp, c.SourceRef, res.NewHead, head); err != nil {
					return abort(c, fmt.Errorf("%w: %s", ErrStackMoved, c.SourceRef))
				}
				_ = s.DB.RecordRefUpdate(ctx, r, c.SourceRef, head, res.NewHead, "rebase", actor)
			}
			if c.TargetRef != st.TargetRef {
				if err := s.retargetChange(ctx, r, c, st.TargetRef, tip, actor); err != nil {
					return abort(c, err)
				}
			}
			if res.NewHead != head {
				if err := s.emitChange(ctx, r, c, "io.ore.change.rebased", actor, map[string]any{"old_head": head, "new_head": res.NewHead, "onto": tip, "replayed": len(res.Replayed)}); err != nil {
					return abort(c, err)
				}
				if _, _, err := s.recordPatchSet(ctx, r, c, tip, res.NewHead, actor); err != nil {
					return abort(c, err)
				}
				if err := s.DB.UpdateChangeHead(ctx, r, c, res.NewHead, actor); err != nil {
					return abort(c, err)
				}
			}
			if c, err = s.DB.Change(ctx, r, c.Number); err != nil {
				return abort(c, err)
			}
			if res.NewHead == tip {
				// Everything in this change is already on the target: nothing to land.
				if err := s.DB.CloseChange(ctx, r, c, "empty after rebase onto "+short(tip), actor); err != nil {
					return abort(c, err)
				}
				continue
			}
		}
		l, err := s.Land(ctx, r, c, req, actor)
		if l.ID != "" {
			landings = append(landings, l)
		}
		if err != nil {
			return abort(c, err)
		}
	}
	if err := s.updateStack(ctx, r, &st, "landed", "io.ore.stack.landed", actor, map[string]any{"landings": len(landings)}); err != nil {
		return st, landings, err
	}
	full, err := s.Stack(ctx, r, st.ID)
	return full, landings, err
}

func (s *Service) emitChange(ctx context.Context, r ledger.Repo, c ledger.Change, typ string, actor ledger.Actor, data map[string]any) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	data["change_id"] = c.ChangeID
	data["number"] = c.Number
	if _, err := tx.Emit(r.OrgID, typ, subject(r, c), actor, data); err != nil {
		return err
	}
	return tx.Commit()
}
