// SPDX-License-Identifier: AGPL-3.0-only

package workspace

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// Status prints the ledger's view of the work and what is pending locally.
// A server that cannot be reached leaves the local part.
func (s *State) Status(ctx context.Context, c *Client, out io.Writer) error {
	area := "whole tree"
	if s.Sparse() {
		area = fmt.Sprintf("active area: %s", strings.Join(s.Paths, ", "))
	}
	fmt.Fprintf(out, "%s/%s at %s (%s), base %s\n", s.Org, s.Repo, s.Server, area, short(s.Base))
	r, err := c.Repo(ctx, s.Org, s.Repo)
	online := err == nil
	if err != nil && !Unreachable(err) {
		return err
	}
	if online {
		strategy, _ := StrategyForLevel(r.AutonomyLevel)
		fmt.Fprintf(out, "autonomy L%d %s: `ore land` uses %s\n", r.AutonomyLevel, Levels[min(r.AutonomyLevel, len(Levels)-1)], strategy)
	} else {
		fmt.Fprintf(out, "server unreachable (%v): showing local state only\n", err)
	}
	switch {
	case s.Change == nil:
		fmt.Fprintln(out, "change: none yet (the first save opens one)")
	case s.Change.Number == 0:
		fmt.Fprintf(out, "change: not opened yet, %q is queued offline\n", s.Change.Title)
	case online:
		// patch sets first: reading them records any head the server has
		// not seen yet, so the change's head below is current
		sets, _ := c.PatchSets(ctx, s.Org, s.Repo, s.Change.Number)
		ch, err := c.Change(ctx, s.Org, s.Repo, s.Change.Number)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "change #%d %s: %s (head %s)\n", ch.Number, ch.State, ch.Title, short(ch.HeadOID))
		if len(sets) > 0 {
			fmt.Fprintf(out, "  patch sets: %d\n", len(sets))
			for _, ps := range sets {
				fmt.Fprintf(out, "    %2.0f  %s  %s\n", num(ps["number"]), short(str(ps["head_oid"])), ago(str(ps["created_at"])))
			}
		}
		if runs, err := c.Runs(ctx, s.Org, s.Repo); err == nil {
			shown := 0
			for _, run := range runs {
				if run.ChangeID != nil && *run.ChangeID == ch.ID {
					if shown == 0 {
						fmt.Fprintln(out, "  pipeline runs:")
					}
					fmt.Fprintf(out, "    %s  %-10s on %s  %s\n", run.ID, run.Status, short(run.CommitOID), ago(run.CreatedAt))
					shown++
				}
			}
			if shown == 0 {
				fmt.Fprintln(out, "  pipeline runs: none on this change")
			}
		}
		if deps, err := c.Deployments(ctx, s.Org, s.Repo); err == nil {
			shown := 0
			for _, d := range deps {
				if d.CommitOID == ch.HeadOID || (ch.MergeOID != nil && d.CommitOID == *ch.MergeOID) {
					if shown == 0 {
						fmt.Fprintln(out, "  deployments:")
					}
					fmt.Fprintf(out, "    %s  %-11s %-9s %s  %s\n", d.ID, d.Environment, d.Status, d.URL, ago(d.CreatedAt))
					shown++
				}
			}
			if shown == 0 {
				fmt.Fprintln(out, "  deployments: none of this change")
			}
		}
	default:
		fmt.Fprintf(out, "change #%d: %s\n", s.Change.Number, s.Change.Title)
	}
	edits, err := s.Dirty()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "local: %s", Summary(edits))
	if len(edits) > 0 {
		fmt.Fprint(out, " (unsaved)")
	}
	fmt.Fprintln(out)
	for i, e := range edits {
		if i == 20 {
			fmt.Fprintf(out, "  ... and %d more\n", len(edits)-20)
			break
		}
		fmt.Fprintf(out, "  %-8s %s\n", e.Kind, e.Path)
	}
	if q, err := LoadQueue(s.root); err == nil && len(q.Saves) > 0 {
		fmt.Fprintf(out, "queued offline: %d save(s), %d file(s); sent by the next save or sync\n", len(q.Saves), q.Files())
		for _, ps := range q.Saves {
			fmt.Fprintf(out, "  %q (%s)\n", firstLine(ps.Message), ago(ps.QueuedAt))
		}
	}
	if uc := s.UnresolvedConflicts(); len(uc) > 0 {
		fmt.Fprintf(out, "conflicts to resolve: %s\n", conflictPaths(uc))
	}
	if s.PendingMerge != "" {
		fmt.Fprintf(out, "pending: the merge of %s (%s) is recorded by the next save\n", s.DefaultBranch, short(s.PendingMerge))
	}
	if online {
		refs, err := c.Refs(ctx, s.Org, s.Repo)
		if err == nil {
			tip := refs["refs/heads/"+r.DefaultBranch]
			if tip != "" && tip != s.Base && tip != s.PendingMerge {
				d, err := c.Diff(ctx, s.Org, s.Repo, s.Base, tip, s.Paths)
				if err == nil && d.MergeBase != tip {
					n := len(d.Files)
					if d.MergeBase != "" && d.MergeBase != s.Base { // count upstream's own changes, not ours
						if up, err := c.Diff(ctx, s.Org, s.Repo, d.MergeBase, tip, s.Paths); err == nil {
							n = len(up.Files)
						}
					}
					fmt.Fprintf(out, "upstream: %s has moved to %s (%d file(s) changed in your area); run `ore sync`\n", r.DefaultBranch, short(tip), n)
				}
			}
		}
	}
	return nil
}

func num(v any) float64 { f, _ := v.(float64); return f }

// ago renders an RFC 3339 time relative to now.
func ago(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		if t, err = time.Parse(time.RFC3339, ts); err != nil {
			return ts
		}
	}
	d := Now().Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
