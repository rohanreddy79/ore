// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"testing"

	"github.com/orevcs/ore/internal/gitx"
)

func TestPushDeletedBranch(t *testing.T) {
	h, _, up, res := linked(t)
	sha := up.commit(t, "feature/retry", "Work", map[string]string{"uploader.go": "package uploader\n"})
	h.deliver("push", "d-create", res.Link.Secret, payload(t, "push.json", up, map[string]any{"ref": "refs/heads/feature/retry", "before": gitx.ZeroOID, "after": sha, "created": true}))
	if h.refs("acme", "app")["refs/heads/feature/retry"] != sha {
		t.Fatal("branch not mirrored")
	}
	// Ore-only branches survive mirroring: an agent branch exists only here.
	rp := h.svc.repoPath(h.must("acme", "app"))
	if err := gitx.UpdateRef(context.Background(), rp, "refs/heads/ore/agent/issue-1-abc", sha, gitx.ZeroOID); err != nil {
		t.Fatal(err)
	}
	up.deleteRef(t, "refs/heads/feature/retry")
	code, out := h.deliver("push", "d-delete", res.Link.Secret, payload(t, "push_deleted.json", up, map[string]any{"before": sha}))
	if code != 200 {
		t.Fatalf("delete webhook: %d %v", code, out)
	}
	refs := h.refs("acme", "app")
	if _, ok := refs["refs/heads/feature/retry"]; ok {
		t.Fatal("deleted branch still present")
	}
	if refs["refs/heads/ore/agent/issue-1-abc"] != sha {
		t.Fatal("the Ore-only branch was pruned")
	}
	del := h.events("io.ore.ref.deleted")
	if len(del) != 1 {
		t.Fatalf("ref.deleted events: %d", len(del))
	}
	if d := data(t, del[0]); d["ref"] != "refs/heads/feature/retry" || d["old"] != sha || d["new"] != gitx.ZeroOID || d["via"] != "github" {
		t.Fatalf("ref.deleted data: %v", d)
	}
	if m := h.events("io.ore.github.push.mirrored"); len(m) != 2 || data(t, m[1])["deleted"] != true {
		t.Fatalf("push.mirrored: %+v", m)
	}
}

func TestPushForced(t *testing.T) {
	h, _, up, res := linked(t)
	base := up.tip(t, "refs/heads/main")
	first := up.commit(t, "feature/retry", "First attempt", map[string]string{"uploader.go": "v1\n"})
	h.deliver("push", "d-1", res.Link.Secret, payload(t, "push.json", up, map[string]any{"ref": "refs/heads/feature/retry", "before": gitx.ZeroOID, "after": first, "created": true}))
	// Rewrite the branch from main: the new tip does not descend from the old one.
	rewritten, err := gitx.CommitFiles(context.Background(), up.path, base, []gitx.FileEdit{{Path: "uploader.go", Content: []byte("v2 rewritten\n")}}, "Rewritten\n", testAuthor)
	if err != nil {
		t.Fatal(err)
	}
	up.setRef(t, "refs/heads/feature/retry", rewritten)
	code, out := h.deliver("push", "d-force", res.Link.Secret, payload(t, "push_forced.json", up, map[string]any{"before": first, "after": rewritten}))
	if code != 200 {
		t.Fatalf("force push webhook: %d %v", code, out)
	}
	if h.refs("acme", "app")["refs/heads/feature/retry"] != rewritten {
		t.Fatal("force push not mirrored")
	}
	rp := h.svc.repoPath(h.must("acme", "app"))
	if gitx.IsAncestor(context.Background(), rp, first, rewritten) {
		t.Fatal("test setup: old tip is an ancestor of the new one")
	}
	upd := h.events("io.ore.ref.updated")
	last := data(t, upd[len(upd)-1])
	if last["ref"] != "refs/heads/feature/retry" || last["old"] != first || last["new"] != rewritten {
		t.Fatalf("ref.updated: %v", last)
	}
	m := h.events("io.ore.github.push.mirrored")
	d := data(t, m[len(m)-1])
	if d["forced"] != true {
		t.Fatalf("push.mirrored not marked forced: %v", d)
	}
	ups := d["updates"].([]any)
	if len(ups) != 1 || ups[0].(map[string]any)["forced"] != true || ups[0].(map[string]any)["old"] != first {
		t.Fatalf("updates: %v", ups)
	}
	// The old commit is still an object (gc.auto is off), so a change that pointed at it keeps its diff.
	if _, err := gitx.Run(context.Background(), rp, nil, "cat-file", "-e", first); err != nil {
		t.Fatal("old tip lost")
	}
}
