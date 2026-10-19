// SPDX-License-Identifier: AGPL-3.0-only

package review

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/orevcs/ore/internal/gitx"
)

// Server-side git operations review v1 needs beyond internal/gitx: a
// three-way merge with an explicit merge base (rebase-aware interdiffs,
// cherry-picks, reverts), a rebase by replaying commits with commit-tree,
// zero-context diffs for line mapping, and range-diff. No working copy.

// mergeTreeOnto merges theirs into ours using mergeBase as the common
// ancestor and returns the resulting tree. On conflict it returns the
// conflicting paths and gitx.ErrConflict.
func mergeTreeOnto(ctx context.Context, rp, mergeBase, ours, theirs string) (tree string, conflicts []string, err error) {
	out, runErr := gitx.Run(ctx, rp, nil, "merge-tree", "--write-tree", "--name-only", "--no-messages", "--merge-base="+mergeBase, ours, theirs)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) && ee.ExitCode() == 1 && len(lines) > 0 && lines[0] != "" {
			var paths []string
			for _, l := range lines[1:] {
				if l != "" {
					paths = append(paths, l)
				}
			}
			return lines[0], paths, gitx.ErrConflict
		}
		return "", nil, runErr
	}
	return lines[0], nil, nil
}

// commitInfo is the metadata needed to replay a commit.
type commitInfo struct {
	Parents []string
	Tree    string
	Author  gitx.Author
	Date    string
	Message string
}

func readCommit(ctx context.Context, rp, oid string) (commitInfo, error) {
	out, err := gitx.Run(ctx, rp, nil, "log", "-1", "--format=%P%x1f%T%x1f%an%x1f%ae%x1f%aI%x1f%B", oid)
	if err != nil {
		return commitInfo{}, err
	}
	f := strings.SplitN(out, "\x1f", 6)
	if len(f) != 6 {
		return commitInfo{}, fmt.Errorf("unexpected log output for %s", oid)
	}
	ci := commitInfo{Tree: f[1], Author: gitx.Author{Name: f[2], Email: f[3]}, Date: f[4], Message: f[5]}
	if p := strings.TrimSpace(f[0]); p != "" {
		ci.Parents = strings.Fields(p)
	}
	return ci, nil
}

// commitTreeAs creates a commit preserving the original author and date; the
// committer is Ore.
func commitTreeAs(ctx context.Context, rp, tree string, parents []string, message string, author gitx.Author, date string, committer gitx.Author) (string, error) {
	args := []string{"commit-tree", tree}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	env := []string{
		"GIT_AUTHOR_NAME=" + author.Name, "GIT_AUTHOR_EMAIL=" + author.Email,
		"GIT_COMMITTER_NAME=" + committer.Name, "GIT_COMMITTER_EMAIL=" + committer.Email,
	}
	if date != "" {
		env = append(env, "GIT_AUTHOR_DATE="+date)
	}
	out, err := gitx.RunStdin(ctx, rp, env, []byte(message), args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// commitsToReplay lists the non-merge commits of head that are not already in
// upstream by patch id (what `git rebase` would replay), oldest first.
func commitsToReplay(ctx context.Context, rp, upstream, head string) ([]string, error) {
	out, err := gitx.Run(ctx, rp, nil, "rev-list", "--reverse", "--no-merges", "--cherry-pick", "--right-only", upstream+"..."+head)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// rebaseResult reports a server-side rebase.
type rebaseResult struct {
	NewHead   string
	Replayed  []string
	Conflict  string   // the commit that conflicted, if any
	Conflicts []string // conflicting paths
}

// rebaseCommits replays commits (oldest first) onto onto, each as a three-way
// merge against its first parent (merge-tree) followed by commit-tree. It
// stops at the first conflict and reports it; nothing is written to any ref.
func rebaseCommits(ctx context.Context, rp string, commits []string, onto string, committer gitx.Author) (rebaseResult, error) {
	res := rebaseResult{NewHead: onto}
	for _, c := range commits {
		ci, err := readCommit(ctx, rp, c)
		if err != nil {
			return res, err
		}
		if len(ci.Parents) != 1 {
			return res, fmt.Errorf("cannot replay %s: %d parents (stacks replay linear history)", c[:8], len(ci.Parents))
		}
		tree, conflicts, err := mergeTreeOnto(ctx, rp, ci.Parents[0], res.NewHead, c)
		if errors.Is(err, gitx.ErrConflict) {
			res.Conflict, res.Conflicts = c, conflicts
			return res, err
		}
		if err != nil {
			return res, err
		}
		if onto, err := readCommit(ctx, rp, res.NewHead); err == nil && onto.Tree == tree {
			continue // the patch is already present; skip the empty commit
		}
		newOID, err := commitTreeAs(ctx, rp, tree, []string{res.NewHead}, ci.Message, ci.Author, ci.Date, committer)
		if err != nil {
			return res, err
		}
		res.NewHead = newOID
		res.Replayed = append(res.Replayed, newOID)
	}
	return res, nil
}

// diffNoContext returns the zero-context unified diff of one path between two
// revisions, which is what the line mapper needs.
func diffNoContext(ctx context.Context, rp, a, b, path string) (string, error) {
	return gitx.Run(ctx, rp, nil, "diff", "-U0", "--no-color", "--no-renames", a, b, "--", path)
}

// pathExists reports whether path is a blob at rev.
func pathExists(ctx context.Context, rp, rev, path string) bool {
	_, err := gitx.Run(ctx, rp, nil, "cat-file", "-e", rev+":"+path)
	return err == nil
}

// rangeDiff runs git range-diff over two patch-set ranges.
func rangeDiff(ctx context.Context, rp, base1, head1, base2, head2 string) (string, error) {
	return gitx.Run(ctx, rp, nil, "range-diff", "--no-color", base1+".."+head1, base2+".."+head2)
}

// diffTexts diffs two texts (a diff of diffs) without a working copy, using
// git diff --no-index over stdin-fed blobs.
func diffTexts(ctx context.Context, rp, a, b string) (string, error) {
	ba, err := gitx.RunStdin(ctx, rp, nil, []byte(a), "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	bb, err := gitx.RunStdin(ctx, rp, nil, []byte(b), "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	out, err := gitx.Run(ctx, rp, nil, "diff", "--no-color", strings.TrimSpace(ba), strings.TrimSpace(bb))
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return out, nil
		}
		return "", err
	}
	return out, nil
}

func short(oid string) string {
	if len(oid) > 8 {
		return oid[:8]
	}
	return oid
}
