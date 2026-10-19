// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// TrackingPrefix is the namespace in Ore's bare repository that holds
// GitHub's refs as last fetched: refs/github/heads/*, refs/github/tags/*
// and refs/github/pull/N/head. Diffing it before and after a fetch yields
// exactly what moved on GitHub — updates, deletions and force pushes — and
// branches that exist only in Ore (the agent's ore/agent/* branches, for
// instance) are never touched.
const TrackingPrefix = "refs/github/"

// RefChange is one applied ref update.
type RefChange struct {
	Ref    string `json:"ref"`
	Old    string `json:"old"`
	New    string `json:"new"`
	Forced bool   `json:"forced"` // old is not an ancestor of new (and neither is zero)
}

// MirrorResult summarizes a mirror pass.
type MirrorResult struct {
	Updates   []RefChange `json:"updates"`
	Tracking  int         `json:"tracking"` // refs under refs/github/ after the fetch
	PullHeads map[int]string
}

func (svc *Service) repoPath(repo ledger.Repo) string {
	return filepath.Join(svc.ReposDir, repo.Org, repo.Name+".git")
}

func refsUnder(all map[string]string, prefix string) map[string]string {
	out := map[string]string{}
	for k, v := range all {
		if strings.HasPrefix(k, prefix) {
			out[k] = v
		}
	}
	return out
}

// localRef maps a tracking ref to the ref Ore serves, or "" for refs that
// stay in the tracking namespace only (pull request heads).
func localRef(tracking string) string {
	rest := strings.TrimPrefix(tracking, TrackingPrefix)
	if strings.HasPrefix(rest, "heads/") || strings.HasPrefix(rest, "tags/") {
		return "refs/" + rest
	}
	return ""
}

// mirror fetches GitHub's branches and tags (and the heads of the given
// pull requests) into the tracking namespace, applies every difference to
// Ore's refs with compare-and-swap, and records each applied update on the
// ledger with via "github". The caller holds the repository lock.
func (svc *Service) mirror(ctx context.Context, link Link, repo ledger.Repo, actor ledger.Actor, pulls []int) (MirrorResult, error) {
	var res MirrorResult
	if svc.Client == nil {
		return res, ErrNoToken
	}
	rp := svc.repoPath(repo)
	env, err := svc.Client.gitEnv(link.CloneURL)
	if err != nil {
		return res, err
	}
	before, err := gitx.Refs(ctx, rp)
	if err != nil {
		return res, err
	}
	if _, err := gitx.Run(ctx, rp, env, "fetch", "--quiet", "--prune", "--no-tags", "--no-write-fetch-head", link.CloneURL,
		"+refs/heads/*:"+TrackingPrefix+"heads/*", "+refs/tags/*:"+TrackingPrefix+"tags/*"); err != nil {
		return res, fmt.Errorf("mirror fetch from %s: %w", redactURL(link.CloneURL), err)
	}
	if len(pulls) > 0 {
		args := []string{"fetch", "--quiet", "--no-tags", "--no-write-fetch-head", link.CloneURL}
		for _, n := range pulls {
			args = append(args, fmt.Sprintf("+refs/pull/%d/head:%spull/%d/head", n, TrackingPrefix, n))
		}
		if _, err := gitx.Run(ctx, rp, env, args...); err != nil {
			// One missing pull ref fails the batch; fetch them one at a time so the others still land.
			for _, n := range pulls {
				if _, err := gitx.Run(ctx, rp, env, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", link.CloneURL, fmt.Sprintf("+refs/pull/%d/head:%spull/%d/head", n, TrackingPrefix, n)); err != nil {
					svc.log().Warn("github: fetch pull head", "repo", repo.Org+"/"+repo.Name, "pr", n, "err", err)
				}
			}
		}
	}
	after, err := gitx.Refs(ctx, rp)
	if err != nil {
		return res, err
	}
	trackedAfter := refsUnder(after, TrackingPrefix)
	res.Tracking = len(trackedAfter)
	res.PullHeads = map[int]string{}
	for ref, oid := range trackedAfter {
		if rest, ok := strings.CutPrefix(ref, TrackingPrefix+"pull/"); ok {
			if n, err := strconv.Atoi(strings.TrimSuffix(rest, "/head")); err == nil {
				res.PullHeads[n] = oid
			}
		}
	}
	diffs := gitx.DiffRefs(refsUnder(before, TrackingPrefix), trackedAfter)
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Ref < diffs[j].Ref })
	for _, d := range diffs {
		target := localRef(d.Ref)
		if target == "" {
			continue
		}
		cur, exists := before[target]
		if !exists {
			cur = gitx.ZeroOID
		}
		if d.New == gitx.ZeroOID {
			if !exists {
				continue
			}
			if err := gitx.DeleteRef(ctx, rp, target, cur); err != nil {
				svc.log().Warn("github: delete ref", "ref", target, "err", err)
				continue
			}
			if err := svc.DB.RecordRefUpdate(ctx, repo, target, cur, gitx.ZeroOID, "github", actor); err != nil {
				return res, err
			}
			res.Updates = append(res.Updates, RefChange{Ref: target, Old: cur, New: gitx.ZeroOID})
			continue
		}
		if cur == d.New {
			continue // Ore already had it (the repository was seeded with the same commit)
		}
		if err := gitx.UpdateRef(ctx, rp, target, d.New, cur); err != nil {
			svc.log().Warn("github: update ref", "ref", target, "err", err)
			continue
		}
		forced := cur != gitx.ZeroOID && !gitx.IsAncestor(ctx, rp, cur, d.New)
		if err := svc.DB.RecordRefUpdate(ctx, repo, target, cur, d.New, "github", actor); err != nil {
			return res, err
		}
		res.Updates = append(res.Updates, RefChange{Ref: target, Old: cur, New: d.New, Forced: forced})
	}
	return res, nil
}

// isMirrored reports whether a commit is reachable from any GitHub ref, i.e.
// whether GitHub knows it and a status can be posted against it.
func (svc *Service) isMirrored(ctx context.Context, repo ledger.Repo, sha string) bool {
	out, err := gitx.Run(ctx, svc.repoPath(repo), nil, "for-each-ref", "--count=1", "--contains", sha, TrackingPrefix)
	return err == nil && strings.TrimSpace(out) != ""
}

func redactURL(u string) string {
	if i := strings.Index(u, "@"); i >= 0 {
		if j := strings.Index(u, "://"); j >= 0 && j < i {
			return u[:j+3] + "***" + u[i:]
		}
	}
	return u
}
