// SPDX-License-Identifier: AGPL-3.0-only

package review

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// Land lands a change with a strategy (ADR 0014 §4, docs/12 §3.7):
//
//   - queue: the v0 behaviour, Merge (merge-tree + compare-and-swap).
//   - land_when_green: verify the head with the pipeline runner first and
//     merge only if the run succeeded.
//   - optimistic: merge immediately, verify the merge commit, and if the run
//     fails revert server-side with a compare-and-swap ref update, emit
//     io.ore.change.reverted and open an issue carrying the run log excerpt.
//
// Every strategy first syncs patch sets (a push invalidates approvals),
// refuses a change whose source branch is gone, and evaluates the approval
// policy. The attempt is recorded as a Landing; attempts that do not stick
// return a *LandingError carrying it.
func (s *Service) Land(ctx context.Context, r ledger.Repo, c ledger.Change, req LandRequest, actor ledger.Actor) (Landing, error) {
	pol, err := s.Policy(ctx, r)
	if err != nil {
		return Landing{}, err
	}
	strategy := req.Strategy
	if strategy == "" {
		strategy = pol.DefaultStrategy
	}
	if !validStrategy(strategy) {
		return Landing{}, fmt.Errorf("%w: strategy must be queue|land_when_green|optimistic", ErrBadRequest)
	}
	c, latest, err := s.SyncPatchSets(ctx, r, c)
	if err != nil {
		return Landing{}, err
	}
	rp := s.repoPath(r)
	if c.State == "open" || c.State == StateReverted {
		if _, err := gitx.RevParse(ctx, rp, c.SourceRef); err != nil {
			return Landing{}, fmt.Errorf("%w: %s", ErrSourceGone, c.SourceRef)
		}
	}
	if c.State != "open" {
		return Landing{}, fmt.Errorf("%w: change #%d is %s", ErrNotOpen, c.Number, c.State)
	}
	status, err := s.Approvals(ctx, r, c)
	if err != nil {
		return Landing{}, err
	}
	if !status.Landable {
		return Landing{}, &PolicyError{Status: status}
	}
	l := Landing{PatchSet: latest.Number, Strategy: strategy, Actor: actor}
	if err := s.insertLanding(ctx, r, c, &l); err != nil {
		return l, err
	}
	switch strategy {
	case StrategyLandWhenGreen:
		return s.landWhenGreen(ctx, r, c, &l, actor)
	case StrategyOptimistic:
		return s.landOptimistic(ctx, r, c, &l, actor)
	default:
		mergeOID, err := s.Merge(ctx, r, c, actor)
		if err != nil {
			return s.failLanding(ctx, r, c, &l, err)
		}
		l.MergeOID = &mergeOID
		return l, s.updateLanding(ctx, r, c, &l, LandingLanded, nil)
	}
}

func (s *Service) failLanding(ctx context.Context, r ledger.Repo, c ledger.Change, l *Landing, cause error) (Landing, error) {
	l.Error = cause.Error()
	if err := s.updateLanding(ctx, r, c, l, LandingFailed, nil); err != nil {
		return *l, err
	}
	return *l, &LandingError{Landing: *l, Err: cause}
}

func (s *Service) landWhenGreen(ctx context.Context, r ledger.Repo, c ledger.Change, l *Landing, actor ledger.Actor) (Landing, error) {
	if s.Verifier == nil {
		return s.failLanding(ctx, r, c, l, ErrNoVerifier)
	}
	if err := s.updateLanding(ctx, r, c, l, LandingVerifying, nil); err != nil {
		return *l, err
	}
	run, err := s.Verifier.Run(ctx, r, c.HeadOID, &c.ID, actor)
	if run.ID != "" {
		l.VerificationRunID = &run.ID
	}
	if err != nil {
		return s.failLanding(ctx, r, c, l, fmt.Errorf("%w: %v", ErrVerificationFailed, err))
	}
	if run.Status != "succeeded" {
		return s.failLanding(ctx, r, c, l, fmt.Errorf("%w: run %s finished %s on %s", ErrVerificationFailed, run.ID, run.Status, short(c.HeadOID)))
	}
	// Only the verified head may land: a push during verification restarts the process.
	if head, err := gitx.RevParse(ctx, s.repoPath(r), c.SourceRef); err != nil {
		return s.failLanding(ctx, r, c, l, fmt.Errorf("%w: %s", ErrSourceGone, c.SourceRef))
	} else if head != c.HeadOID {
		_, _, _ = s.SyncPatchSets(ctx, r, c)
		return s.failLanding(ctx, r, c, l, fmt.Errorf("%w: the branch moved to %s during verification of %s; land again", ErrVerificationFailed, short(head), short(c.HeadOID)))
	}
	mergeOID, err := s.Merge(ctx, r, c, actor)
	if err != nil {
		return s.failLanding(ctx, r, c, l, err)
	}
	l.MergeOID = &mergeOID
	return *l, s.updateLanding(ctx, r, c, l, LandingLanded, nil)
}

func (s *Service) landOptimistic(ctx context.Context, r ledger.Repo, c ledger.Change, l *Landing, actor ledger.Actor) (Landing, error) {
	if s.Verifier == nil {
		return s.failLanding(ctx, r, c, l, ErrNoVerifier)
	}
	mergeOID, err := s.Merge(ctx, r, c, actor)
	if err != nil {
		return s.failLanding(ctx, r, c, l, err)
	}
	l.MergeOID = &mergeOID
	if err := s.updateLanding(ctx, r, c, l, LandingVerifying, nil); err != nil {
		return *l, err
	}
	// The merge is on the target now: a client that goes away must not turn
	// into a spurious revert, so verification and the revert run detached
	// from the request's cancellation.
	bg := context.WithoutCancel(ctx)
	run, runErr := s.Verifier.Run(bg, r, mergeOID, &c.ID, actor)
	if run.ID != "" {
		l.VerificationRunID = &run.ID
	}
	if runErr == nil && run.Status == "succeeded" {
		return *l, s.updateLanding(bg, r, c, l, LandingLanded, nil)
	}
	outcome := run.Status
	if runErr != nil {
		outcome = "error (" + runErr.Error() + ")"
	}
	revertOID, err := s.revertMerge(bg, r, c, mergeOID, run.ID, outcome)
	if err != nil {
		l.Error = fmt.Sprintf("verification failed (run %s %s) and the revert of %s failed: %v", run.ID, outcome, short(mergeOID), err)
		cause := fmt.Errorf("%w; revert failed: %v", ErrVerificationFailed, err)
		if uerr := s.updateLanding(bg, r, c, l, LandingRevertFailed, func(tx *ledger.Tx) error {
			_, err := tx.Emit(r.OrgID, "io.ore.change.revert_failed", subject(r, c), ledger.System, map[string]any{"change_id": c.ChangeID, "number": c.Number, "merge_oid": mergeOID, "run_id": run.ID, "error": err.Error()})
			return err
		}); uerr != nil {
			return *l, uerr
		}
		return *l, &LandingError{Landing: *l, Err: cause}
	}
	l.RevertOID = &revertOID
	issue, err := s.DB.CreateIssue(bg, r, fmt.Sprintf("Change #%d reverted: verification failed", c.Number), s.revertIssueBody(r, c, mergeOID, revertOID, run, outcome), ledger.System)
	if err != nil {
		return *l, err
	}
	l.IssueNumber = &issue.Number
	l.Error = fmt.Sprintf("verification failed: run %s %s; reverted as %s; issue #%d opened", run.ID, outcome, short(revertOID), issue.Number)
	if err := s.updateLanding(bg, r, c, l, LandingReverted, func(tx *ledger.Tx) error {
		if _, err := tx.ExecContext(bg, `UPDATE changes SET state = ? WHERE id = ?`, StateReverted, c.ID); err != nil {
			return err
		}
		_, err := tx.Emit(r.OrgID, "io.ore.change.reverted", subject(r, c), ledger.System, map[string]any{
			"change_id": c.ChangeID, "number": c.Number, "target_ref": c.TargetRef, "merge_oid": mergeOID, "revert_oid": revertOID,
			"verification_run_id": run.ID, "verification_status": outcome, "issue_number": issue.Number, "landing_id": l.ID,
		})
		return err
	}); err != nil {
		return *l, err
	}
	return *l, &LandingError{Landing: *l, Err: ErrReverted}
}

// revertMerge creates a revert of mergeOID (relative to its first parent) on
// top of the target's current tip with a three-way merge, and moves the
// target with compare-and-swap, retrying if the tip moved. If later commits
// conflict with the revert, a ConflictError is returned.
func (s *Service) revertMerge(ctx context.Context, r ledger.Repo, c ledger.Change, mergeOID, runID, outcome string) (string, error) {
	rp := s.repoPath(r)
	mi, err := readCommit(ctx, rp, mergeOID)
	if err != nil {
		return "", err
	}
	if len(mi.Parents) == 0 {
		return "", fmt.Errorf("cannot revert root commit %s", short(mergeOID))
	}
	parent := mi.Parents[0]
	author := gitx.Author{Name: "Ore", Email: "ore@" + r.Org + ".invalid"}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		tip, err := gitx.RevParse(ctx, rp, c.TargetRef)
		if err != nil {
			return "", err
		}
		tree, conflicts, err := mergeTreeOnto(ctx, rp, mergeOID, tip, parent)
		if errors.Is(err, gitx.ErrConflict) {
			return "", &ConflictError{Paths: conflicts}
		}
		if err != nil {
			return "", err
		}
		msg := fmt.Sprintf("Revert \"Merge change #%d: %s\"\n\nThis reverts commit %s: verification run %s finished %s.\n\nChange-Id: %s\nOre-Reverts: %s\nOre-Actor: system/ore\n",
			c.Number, c.Title, mergeOID, runID, outcome, c.ChangeID, mergeOID)
		revert, err := gitx.CommitTree(ctx, rp, tree, []string{tip}, msg, author)
		if err != nil {
			return "", err
		}
		if err := gitx.UpdateRef(ctx, rp, c.TargetRef, revert, tip); err != nil {
			lastErr = err
			continue
		}
		_ = s.DB.RecordRefUpdate(ctx, r, c.TargetRef, tip, revert, "revert", ledger.System)
		return revert, nil
	}
	return "", fmt.Errorf("revert lost the race three times: %w", lastErr)
}

func (s *Service) revertIssueBody(r ledger.Repo, c ledger.Change, mergeOID, revertOID string, run ledger.PipelineRun, outcome string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Change #%d %q landed optimistically as %s on %s. Verification run %s finished %s, so Ore reverted the merge as %s; %s now points at the revert.\n\n",
		c.Number, c.Title, short(mergeOID), c.TargetRef, run.ID, outcome, short(revertOID), c.TargetRef)
	fmt.Fprintf(&b, "Run log excerpt (%s):\n\n```\n%s\n```\n\n", run.ID, logExcerpt(run.LogPath, 40, 6000))
	fmt.Fprintf(&b, "Fix the change on `%s`, rebased onto `%s` (its original commits are already in that history through the reverted merge, so a fix on top of them would land as no change), and land it again; the next push reopens change #%d.\n\nChange-Id: %s\nOre-Merge: %s\nOre-Revert: %s\nOre-Run: %s\n",
		c.SourceRef, c.TargetRef, c.Number, c.ChangeID, mergeOID, revertOID, run.ID)
	return b.String()
}

// logExcerpt returns the last n lines of a log, capped at maxBytes.
func logExcerpt(path string, n, maxBytes int) string {
	if path == "" {
		return "(no log)"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "(log unavailable: " + err.Error() + ")"
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := strings.Join(lines, "\n")
	if len(out) > maxBytes {
		out = out[len(out)-maxBytes:]
	}
	return out
}
