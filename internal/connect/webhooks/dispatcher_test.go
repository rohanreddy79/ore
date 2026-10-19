// SPDX-License-Identifier: AGPL-3.0-only

package webhooks

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

func TestEnqueueAdvancesCursorAndDeduplicates(t *testing.T) {
	f := newAuditFixture(t)
	d := &Dispatcher{Store: f.store, BaseURL: "http://ore.test"}
	ep := Endpoint{ID: ledger.NewID("whep"), OrgID: f.org.ID, URL: "http://93.184.216.34/h", Payload: "thin", Enabled: true, Filter: Filter{Types: []string{"io.ore.issue."}},
		Secrets: []Secret{{ID: "whs_1", Value: NewSecret(), CreatedAt: ledger.Now()}}, CreatedBy: ledger.System, CreatedAt: ledger.Now(), UpdatedAt: ledger.Now()}
	if err := f.store.InsertEndpoint(context.Background(), &ep); err != nil {
		t.Fatal(err)
	}
	f.emit(t, 5, "io.ore.other.x")
	ep2, err := d.enqueue(context.Background(), ep)
	if err != nil {
		t.Fatal(err)
	}
	head, _ := f.store.HeadSeq(context.Background())
	if ep2.Cursor != head {
		t.Fatalf("cursor %d head %d", ep2.Cursor, head)
	}
	if n, _ := f.store.CountDeliveries(context.Background(), ep.ID); len(n) != 0 {
		t.Fatalf("deliveries for non-matching events: %v", n)
	}
	stored, _ := f.store.Endpoint(context.Background(), ep.ID)
	if stored.Cursor != head {
		t.Fatalf("cursor not persisted: %d", stored.Cursor)
	}
	f.emit(t, 2, "io.ore.issue.opened")
	ep3, _ := d.enqueue(context.Background(), ep2)
	due, _ := f.store.DueDeliveries(context.Background(), ep.ID, time.Now(), 10)
	if len(due) != 2 || due[0].Status != StatusPending || due[0].EventSeq >= due[1].EventSeq {
		t.Fatalf("due %+v", due)
	}
	// enqueueing again (or from a stale cursor) never duplicates a delivery
	d.enqueue(context.Background(), ep3)
	d.enqueue(context.Background(), ep2)
	due, _ = f.store.DueDeliveries(context.Background(), ep.ID, time.Now(), 10)
	if len(due) != 2 {
		t.Fatalf("duplicated: %d", len(due))
	}
	// paging: more events than one batch are drained in one call
	d.Batch = 3
	f.emit(t, 7, "io.ore.issue.opened")
	ep4, _ := d.enqueue(context.Background(), ep3)
	head, _ = f.store.HeadSeq(context.Background())
	due, _ = f.store.DueDeliveries(context.Background(), ep.ID, time.Now(), 100)
	if ep4.Cursor != head || len(due) != 9 {
		t.Fatalf("paged enqueue: cursor %d head %d due %d", ep4.Cursor, head, len(due))
	}
	// bodies: thin by default with a signed link, full on request, ping always full
	ev, _ := f.store.EventByID(context.Background(), due[0].EventID)
	body, thin := d.Body(ep, ev, time.Now())
	if !thin || !strings.Contains(string(body), `"url":"http://ore.test/api/v0/webhooks/events/`+ev.ID+`?`) {
		t.Fatalf("thin body %s", body)
	}
	ep.Payload = "full"
	if body, thin = d.Body(ep, ev, time.Now()); thin || !strings.Contains(string(body), `"data":{"i":0}`) {
		t.Fatalf("full body %s", body)
	}
	d.MaxPayload = 10
	if _, thin = d.Body(ep, ev, time.Now()); !thin {
		t.Fatal("oversized full body not thinned")
	}
	if _, thin = d.Body(ep, PingEvent(ep, "evt_p", ledger.Now()), time.Now()); thin {
		t.Fatal("ping must be full")
	}
	// signed links verify with the endpoint's secret and reject expiry or tampering
	link := d.SignedURL(ep, ev.ID, time.Now().Add(time.Hour))
	q := queryOf(t, link)
	if err := d.VerifyURL(ep, ev.ID, q); err != nil {
		t.Fatalf("verify link: %v", err)
	}
	if err := d.VerifyURL(ep, "evt_other", q); err == nil {
		t.Fatal("link for another event accepted")
	}
	q.Set("exp", "1")
	if err := d.VerifyURL(ep, ev.ID, q); err == nil {
		t.Fatal("expired link accepted")
	}
}
