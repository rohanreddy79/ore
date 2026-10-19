// SPDX-License-Identifier: AGPL-3.0-only

package fpi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

func TestNativeProvider(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := ledger.Open(ctx, filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	org, _ := db.CreateOrg(ctx, "acme", ledger.System)
	repo, _ := db.CreateRepo(ctx, org, "app", "main", 2, 8080, ledger.System)
	rp := filepath.Join(dir, "repos", "acme", "app.git")
	if err := gitx.InitBare(ctx, rp, "main"); err != nil {
		t.Fatal(err)
	}
	oid, _ := gitx.CommitFiles(ctx, rp, "", []gitx.FileEdit{{Path: "README", Content: []byte("x")}}, "init", gitx.Author{Name: "t", Email: "t@t"})
	gitx.UpdateRef(ctx, rp, "refs/heads/main", oid, gitx.ZeroOID)
	db.CreateIssue(ctx, repo, "open one", "", ledger.System)
	closed, _ := db.CreateIssue(ctx, repo, "closed one", "", ledger.System)
	db.CloseIssue(ctx, repo, closed, "done", ledger.System)

	n := &Native{DB: db, ReposDir: filepath.Join(dir, "repos"), BaseURL: "http://ore.test"}
	Register(n)
	if p, ok := Lookup("native"); !ok || p.Name() != "native" || !p.Capabilities().ServerSideMerge || p.Capabilities().InboundHooks {
		t.Fatalf("lookup: %v %+v", ok, p)
	}
	r, err := n.Repository(ctx, "acme", "app")
	if err != nil || r.FullName() != "acme/app" || r.CloneURL != "http://ore.test/acme/app.git" || r.DefaultBranch != "main" {
		t.Fatalf("repository: %+v %v", r, err)
	}
	if _, err := n.Repository(ctx, "acme", "nope"); err != ledger.ErrNotFound {
		t.Fatalf("missing: %v", err)
	}
	refs, err := n.ListRefs(ctx, r)
	if err != nil || len(refs) != 1 || refs[0].Name != "refs/heads/main" || refs[0].OID != oid {
		t.Fatalf("refs: %+v %v", refs, err)
	}
	open, _ := n.ListIssues(ctx, r, IssueQuery{})
	all, _ := n.ListIssues(ctx, r, IssueQuery{State: "all", Limit: 1})
	if len(open) != 1 || open[0].Title != "open one" || len(all) != 1 {
		t.Fatalf("issues: %+v %+v", open, all)
	}
	if err := n.ReportStatus(ctx, r, oid, Status{State: StatusSuccess, Context: "ore/pipeline", TargetURL: "http://ore.test/acme/app/runs/x"}); err != nil {
		t.Fatal(err)
	}
	evs, _ := db.Events(ctx, ledger.EventsOptions{Type: "io.ore.commit.status.reported"})
	if len(evs) != 1 || evs[0].Subject != "acme/app/commits/"+oid {
		t.Fatalf("status event: %+v", evs)
	}
	rec := httptest.NewRecorder()
	n.Webhook(rec, httptest.NewRequest(http.MethodPost, "/hooks/native", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("webhook: %d", rec.Code)
	}
	if names := Names(); len(names) != 1 || names[0] != "native" {
		t.Fatalf("names: %v", names)
	}
}
