// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/orevcs/ore/internal/ledger"
)

func TestDeployRecordsLedgerWithoutExecutor(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := ledger.Open(ctx, filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	org, _ := db.CreateOrg(ctx, "acme", ledger.System)
	repo, _ := db.CreateRepo(ctx, org, "app", "main", 2, 8080, ledger.System)
	d := &Deployer{DB: db, ReposDir: dir, Executor: "none"}
	first, err := d.Deploy(ctx, repo, "aaaa", "production", ledger.System, nil)
	if err != nil || first.Status != "succeeded" || first.PreviousID != nil {
		t.Fatalf("first: %v %+v", err, first)
	}
	second, err := d.Deploy(ctx, repo, "bbbb", "production", ledger.System, nil)
	if err != nil || second.PreviousID == nil || *second.PreviousID != first.ID {
		t.Fatalf("second: %v %+v", err, second)
	}
	rb, err := d.Rollback(ctx, repo, "production", ledger.System)
	if err != nil || rb.CommitOID != "aaaa" || rb.RollbackOf == nil || *rb.RollbackOf != second.ID {
		t.Fatalf("rollback: %v %+v", err, rb)
	}
	evs, _ := db.Events(ctx, ledger.EventsOptions{Type: "io.ore.deployment."})
	var types []string
	for _, e := range evs {
		types = append(types, e.Type)
	}
	want := 3*3 + 1 // requested, started, succeeded ×3 plus rolled_back
	if len(evs) != want {
		t.Fatalf("expected %d deployment events, got %d: %v", want, len(evs), types)
	}
}
