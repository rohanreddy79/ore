// SPDX-License-Identifier: AGPL-3.0-only

package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// Levels names the autonomy levels (docs/12).
var Levels = []string{"manual", "assist", "author", "land", "ship", "operate"}

// StrategyForLevel is how a change lands under a repository's autonomy
// level: below L3 the human decides and the server merges at once (queue);
// at L3 the pipeline verifies the head first (land when green); at L4 and
// above the merge happens first and a red verification reverts it.
func StrategyForLevel(level int) (strategy, explanation string) {
	switch {
	case level >= 4:
		return "optimistic", "merge now, verify the merge; a red run reverts it and opens an issue"
	case level == 3:
		return "land_when_green", "the pipeline verifies the change first; it merges when green"
	default:
		return "queue", "merge now; the repository's policy decides"
	}
}

// LandResult is the outcome of a landing request.
type LandResult struct {
	// Outcome is landed, blocked, reverted or queued.
	Outcome string
	Reason  string
	Merge   string
	Run     string
	Issue   int
}

// Land asks the server to land the change under the repository's level and
// reports the outcome.
func (s *State) Land(ctx context.Context, c *Client, out io.Writer) (*LandResult, error) {
	if _, err := s.Flush(ctx, c, out); err != nil {
		return nil, err
	}
	if s.Change == nil || s.Change.Number == 0 {
		return nil, errors.New("nothing to land: save first (ore save -m \"...\")")
	}
	if edits, err := s.Dirty(); err == nil && len(edits) > 0 {
		fmt.Fprintf(out, "note: %s in the working tree are not saved and will not land\n", Summary(edits))
	}
	r, err := c.Repo(ctx, s.Org, s.Repo)
	if err != nil {
		return nil, err
	}
	ch, err := c.Change(ctx, s.Org, s.Repo, s.Change.Number)
	if err != nil {
		return nil, err
	}
	if ch.State == "merged" {
		fmt.Fprintf(out, "change #%d has already landed\n", ch.Number)
		return &LandResult{Outcome: "landed", Merge: deref(ch.MergeOID)}, nil
	}
	strategy, why := StrategyForLevel(r.AutonomyLevel)
	fmt.Fprintf(out, "landing change #%d under L%d %s (%s): %s\n", ch.Number, r.AutonomyLevel, Levels[min(r.AutonomyLevel, len(Levels)-1)], strategy, why)
	body, err := c.Land(ctx, s.Org, s.Repo, ch.Number, strategy)
	if Status(err) == 404 { // a server without review v1: the v0 merge
		body, err = c.Merge(ctx, s.Org, s.Repo, ch.Number)
		if err == nil {
			res := &LandResult{Outcome: "landed", Merge: str(body["merge_commit"])}
			fmt.Fprintf(out, "landed: change #%d merged as %s\n", ch.Number, short(res.Merge))
			return res, nil
		}
	}
	res := &LandResult{}
	var ae *APIError
	switch {
	case err == nil:
		landing, _ := body["landing"].(map[string]any)
		res.Outcome = outcomeOf(str(landing["status"]))
		res.Merge = str(landing["merge_oid"])
		res.Run = str(landing["verification_run_id"])
	case errors.As(err, &ae) && ae.Status == 409:
		res.Reason = str(ae.Body["error"])
		if landing, ok := ae.Body["landing"].(map[string]any); ok {
			res.Outcome = outcomeOf(str(landing["status"]))
			res.Merge = str(landing["merge_oid"])
			res.Run = str(landing["verification_run_id"])
			if n, ok := landing["issue_number"].(float64); ok {
				res.Issue = int(n)
			}
			if e := str(landing["error"]); e != "" {
				res.Reason = e
			}
		} else {
			res.Outcome = "blocked"
		}
		if ap, ok := ae.Body["approvals"].(map[string]any); ok {
			if reason := str(ap["reason"]); reason != "" {
				res.Reason = reason
			}
		}
	default:
		return nil, err
	}
	switch res.Outcome {
	case "landed":
		fmt.Fprintf(out, "landed: change #%d merged as %s", ch.Number, short(res.Merge))
		if res.Run != "" {
			fmt.Fprintf(out, " (verified by run %s)", res.Run)
		}
		fmt.Fprintln(out)
	case "reverted":
		fmt.Fprintf(out, "reverted: change #%d landed as %s but verification failed (run %s); the merge was reverted", ch.Number, short(res.Merge), res.Run)
		if res.Issue > 0 {
			fmt.Fprintf(out, " and issue #%d carries the failure", res.Issue)
		}
		fmt.Fprintln(out)
	case "queued":
		fmt.Fprintf(out, "queued: change #%d is %s; check `ore status`\n", ch.Number, res.Reason)
	default:
		fmt.Fprintf(out, "blocked: %s\n", res.Reason)
	}
	return res, nil
}

func outcomeOf(status string) string {
	switch status {
	case "landed":
		return "landed"
	case "reverted", "revert_failed":
		return "reverted"
	case "requested", "verifying":
		return "queued"
	default:
		return "blocked"
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
