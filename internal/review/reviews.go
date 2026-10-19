// SPDX-License-Identifier: AGPL-3.0-only

package review

import (
	"context"
	"fmt"

	"github.com/orevcs/ore/internal/ledger"
)

// SubmitReview records a verdict with structured findings on a patch set
// (default: the latest). Approvals from the human lane count toward the
// policy; agent and bot approvals count only when the repository allows it.
func (s *Service) SubmitReview(ctx context.Context, r ledger.Repo, c ledger.Change, req ReviewRequest, actor ledger.Actor) (Review, error) {
	switch req.Verdict {
	case "comment", "approve", "request_changes":
	case "":
		req.Verdict = "comment"
	default:
		return Review{}, fmt.Errorf("%w: verdict must be comment|approve|request_changes", ErrBadRequest)
	}
	for _, f := range req.Findings {
		if err := f.Validate(); err != nil {
			return Review{}, err
		}
	}
	lane, err := laneFor(actor, req.Lane)
	if err != nil {
		return Review{}, err
	}
	c, latest, err := s.SyncPatchSets(ctx, r, c)
	if err != nil {
		return Review{}, err
	}
	ps := latest.Number
	if req.PatchSet != 0 {
		if req.PatchSet < 1 || req.PatchSet > latest.Number {
			return Review{}, fmt.Errorf("%w: patch set %d does not exist (latest is %d)", ErrBadRequest, req.PatchSet, latest.Number)
		}
		ps = req.PatchSet
	}
	rv := Review{PatchSet: ps, Reviewer: actor, Lane: lane, Verdict: req.Verdict, Findings: req.Findings, Body: req.Body}
	if rv.Findings == nil {
		rv.Findings = []Finding{}
	}
	if err := s.insertReview(ctx, r, c, &rv); err != nil {
		return rv, err
	}
	pol, err := s.Policy(ctx, r)
	if err != nil {
		return rv, err
	}
	rv.Counts = rv.PatchSet == latest.Number && rv.Verdict != "comment" && laneCounts(rv.Lane, pol)
	return rv, nil
}

func laneCounts(lane string, pol Policy) bool {
	return lane == LaneHuman || pol.AllowAgentApprovals
}

// Reviews lists a change's reviews with Counts evaluated against the latest
// patch set and the current policy.
func (s *Service) Reviews(ctx context.Context, r ledger.Repo, c ledger.Change) ([]Review, error) {
	c, latest, err := s.SyncPatchSets(ctx, r, c)
	if err != nil {
		return nil, err
	}
	pol, err := s.Policy(ctx, r)
	if err != nil {
		return nil, err
	}
	list, err := s.loadReviews(ctx, c)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []Review{}
	}
	for i := range list {
		list[i].Counts = list[i].PatchSet == latest.Number && list[i].Verdict != "comment" && laneCounts(list[i].Lane, pol)
	}
	return list, nil
}

// Approvals evaluates the repository's policy against the latest patch set:
// the latest non-comment verdict per reviewer counts; a push (new patch set)
// resets everything.
func (s *Service) Approvals(ctx context.Context, r ledger.Repo, c ledger.Change) (ApprovalStatus, error) {
	c, latest, err := s.SyncPatchSets(ctx, r, c)
	if err != nil {
		return ApprovalStatus{}, err
	}
	pol, err := s.Policy(ctx, r)
	if err != nil {
		return ApprovalStatus{}, err
	}
	list, err := s.loadReviews(ctx, c)
	if err != nil {
		return ApprovalStatus{}, err
	}
	st := ApprovalStatus{PatchSet: latest.Number, Required: pol.RequiredApprovals, Approvers: []ledger.Actor{}}
	type k struct{ kind, id string }
	last := map[k]Review{}
	var order []k
	for _, rv := range list {
		if rv.PatchSet != latest.Number || rv.Verdict == "comment" {
			continue
		}
		key := k{rv.Reviewer.Kind, rv.Reviewer.ID}
		if _, seen := last[key]; !seen {
			order = append(order, key)
		}
		last[key] = rv
	}
	for _, key := range order {
		rv := last[key]
		counts := laneCounts(rv.Lane, pol)
		switch {
		case rv.Verdict == "approve" && counts:
			st.Approvals++
			st.Approvers = append(st.Approvers, rv.Reviewer)
		case rv.Verdict == "approve":
			st.Ignored = append(st.Ignored, IgnoredApproval{Reviewer: rv.Reviewer, Reason: rv.Lane + " lane approvals do not count (repository policy)"})
		case rv.Verdict == "request_changes" && counts:
			st.ChangesRequestedBy = append(st.ChangesRequestedBy, rv.Reviewer)
		}
	}
	switch {
	case len(st.ChangesRequestedBy) > 0:
		st.Reason = fmt.Sprintf("changes requested by %s/%s on patch set %d", st.ChangesRequestedBy[0].Kind, st.ChangesRequestedBy[0].ID, latest.Number)
	case st.Approvals < st.Required:
		st.Reason = fmt.Sprintf("needs %d approval(s) from the human lane on patch set %d, has %d", st.Required, latest.Number, st.Approvals)
	default:
		st.Landable = true
	}
	return st, nil
}
