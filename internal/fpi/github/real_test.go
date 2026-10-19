// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestRealGitHub links a real repository when ORE_GITHUB_TOKEN and
// ORE_GITHUB_TEST_REPO ("owner/name") are set; otherwise it is skipped and
// says so. It reads only: no webhook is created and no status is posted.
func TestRealGitHub(t *testing.T) {
	token, repo := os.Getenv(EnvToken), os.Getenv("ORE_GITHUB_TEST_REPO")
	if token == "" || repo == "" {
		t.Skip("set " + EnvToken + " and ORE_GITHUB_TEST_REPO=owner/name to link a real repository")
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		t.Fatalf("ORE_GITHUB_TEST_REPO must be owner/name, got %q", repo)
	}
	h := newHarness(t, nil, Config{Token: token, API: os.Getenv(EnvAPI)})
	h.createRepo("real", "mirror")
	res := h.link("real", "mirror", owner, name)
	if res.Mirror.Tracking == 0 || len(res.Mirror.Updates) == 0 {
		t.Fatalf("nothing mirrored: %+v", res.Mirror)
	}
	refs := h.refs("real", "mirror")
	if refs["refs/heads/"+res.Link.DefaultBranch] == "" {
		t.Fatalf("default branch %s not mirrored: %v", res.Link.DefaultBranch, refs)
	}
	t.Logf("linked %s: %d refs mirrored, default branch %s", repo, res.Mirror.Tracking, res.Link.DefaultBranch)
	if results, err := h.svc.SyncAll(context.Background()); err != nil || results[0].Status != "ok" {
		t.Fatalf("reconcile: %+v %v", results, err)
	}
}
