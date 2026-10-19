// SPDX-License-Identifier: AGPL-3.0-only

package webhooks

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestReceiverNeverAnswers(t *testing.T) {
	h := newHarness(t, harnessOptions{allowPrivate: true, timeout: 150 * time.Millisecond})
	rc := newReceiver(t, func(n int, r *http.Request) (int, http.Header, time.Duration) {
		if n == 1 {
			return 200, nil, 2 * time.Second // never answers within the timeout
		}
		return 200, nil, 0
	})
	c := h.endpoint(map[string]any{"org": "acme", "url": rc.url(), "types": []string{"io.ore.issue."}})
	h.issue("slow")
	var dels []Delivery
	waitFor(t, 3*time.Second, "timeout recorded", func() bool {
		h.rest("GET", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/deliveries", nil, &dels)
		return len(dels) == 1 && dels[0].Attempt >= 1
	})
	if a := dels[0].Attempts[0]; !strings.Contains(a.Error, "timeout after 150ms") || a.Code != 0 || a.Latency < 100 {
		t.Fatalf("first attempt %+v", a)
	}
	// the retry succeeds with the same webhook-id
	msgs := rc.wait(t, 2, 5*time.Second)
	if msgs[0].ID != msgs[1].ID {
		t.Fatalf("ids differ: %s %s", msgs[0].ID, msgs[1].ID)
	}
	waitFor(t, 3*time.Second, "succeeded", func() bool {
		h.rest("GET", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/deliveries", nil, &dels)
		return dels[0].Status == StatusSucceeded
	})
	if dels[0].Attempt != 2 || dels[0].LastError != "" {
		t.Fatalf("delivery %+v", dels[0])
	}
	var hl Health
	h.rest("GET", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/health", nil, &hl)
	if !strings.Contains(hl.LastError, "timeout") || hl.SuccessRate != 1 {
		t.Fatalf("health %+v", hl)
	}
}

func TestPayloadOverSizeLimitUsesThin(t *testing.T) {
	h := newHarness(t, harnessOptions{allowPrivate: true, maxPayload: 700})
	rc := newReceiver(t, nil)
	c := h.endpoint(map[string]any{"org": "acme", "url": rc.url(), "types": []string{"io.ore.issue."}, "payload": "full"})
	h.rest("POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": "big", "body": strings.Repeat("x", 2000)}, nil)
	h.rest("POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": "small"}, nil)
	msgs := rc.wait(t, 2, 5*time.Second)
	verifyAll(t, c.Secret.Value, msgs)
	big, small := decodeBody(t, msgs[0].Body), decodeBody(t, msgs[1].Body)
	if big["data"].(map[string]any)["thin"] != true || len(msgs[0].Body) > 700 {
		t.Fatalf("big payload was not thinned: %d bytes %v", len(msgs[0].Body), big["data"])
	}
	if small["data"].(map[string]any)["title"] != "small" {
		t.Fatalf("small payload not full: %v", small["data"])
	}
	// the thin link resolves to the full 2000-byte body
	resp, err := http.Get(big["data"].(map[string]any)["url"].(string))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("thin link: %v", err)
	}
	var full map[string]any
	if err := decodeJSON(resp, &full); err != nil || len(full["data"].(map[string]any)["body"].(string)) != 2000 {
		t.Fatalf("full body: %v %v", err, full["data"])
	}
	var dels []Delivery
	h.rest("GET", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/deliveries", nil, &dels)
	var view map[string]any
	h.rest("GET", "/api/v0/webhooks/deliveries/"+dels[0].ID, nil, &view)
	if view["thin"] != true {
		t.Fatalf("delivery view %v", view["thin"])
	}
}

func decodeJSON(resp *http.Response, v any) error {
	defer resp.Body.Close()
	return jsonDecode(resp, v)
}

func TestPrivateURLRefusedUnlessAllowed(t *testing.T) {
	h := newHarness(t, harnessOptions{allowPrivate: false})
	for _, u := range []string{"http://127.0.0.1:1/hook", "http://localhost:8080/hook", "http://[::1]/hook", "http://10.1.2.3/hook", "http://169.254.169.254/latest/meta-data", "http://app.localhost/hook"} {
		var errBody map[string]any
		if code := h.rest("POST", "/api/v0/webhooks/endpoints", map[string]any{"org": "acme", "url": u}, &errBody); code != 400 || !strings.Contains(fmt.Sprint(errBody["error"]), "private address") {
			t.Fatalf("%s: %d %v", u, code, errBody)
		}
	}
	for _, u := range []string{"ftp://example.com/hook", "http:///hook", "not a url"} {
		if code := h.rest("POST", "/api/v0/webhooks/endpoints", map[string]any{"org": "acme", "url": u}, nil); code != 400 {
			t.Fatalf("%s: %d", u, code)
		}
	}
	// a public literal address is accepted without resolving anything
	if code := h.rest("POST", "/api/v0/webhooks/endpoints", map[string]any{"org": "acme", "url": "http://93.184.216.34/hook"}, nil); code != 201 {
		t.Fatalf("public address: %d", code)
	}
	// with ORE_WEBHOOKS_ALLOW_PRIVATE the same addresses are accepted
	h.svc.Dispatcher.AllowPrivate = true
	if code := h.rest("POST", "/api/v0/webhooks/endpoints", map[string]any{"org": "acme", "url": "http://127.0.0.1:1/hook"}, nil); code != 201 {
		t.Fatalf("allowed private: %d", code)
	}
	// and the check also runs at delivery time
	if err := CheckAddress(context.Background(), "http://127.0.0.1:9/x", false); err == nil {
		t.Fatal("delivery-time check accepted loopback")
	}
}

func TestDuplicateWebhookIDOnRedeliver(t *testing.T) {
	h := newHarness(t, harnessOptions{allowPrivate: true})
	rc := newReceiver(t, nil)
	c := h.endpoint(map[string]any{"org": "acme", "url": rc.url(), "types": []string{"io.ore.issue."}})
	h.issue("once")
	first := rc.wait(t, 1, 5*time.Second)
	var dels []Delivery
	waitFor(t, 2*time.Second, "delivery", func() bool {
		h.rest("GET", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/deliveries", nil, &dels)
		return len(dels) == 1 && dels[0].Status == StatusSucceeded
	})
	var out struct {
		Delivery     Delivery
		RedeliveryOf string `json:"redelivery_of"`
	}
	if code := h.rest("POST", "/api/v0/webhooks/deliveries/"+dels[0].ID+"/redeliver", nil, &out); code != 200 || out.Delivery.Kind != KindRedeliver || out.Delivery.Status != StatusSucceeded || out.RedeliveryOf != dels[0].ID {
		t.Fatalf("redeliver: %d %+v", code, out)
	}
	msgs := rc.wait(t, 2, 2*time.Second)
	if msgs[1].ID != first[0].ID || msgs[1].Timestamp < first[0].Timestamp {
		t.Fatalf("redelivery id %s, original %s", msgs[1].ID, first[0].ID)
	}
	verifyAll(t, c.Secret.Value, msgs)
	if string(msgs[1].Body) != string(first[0].Body) {
		// the thin link's expiry moves with time; everything else is identical
		a, b := decodeBody(t, first[0].Body), decodeBody(t, msgs[1].Body)
		if a["id"] != b["id"] || a["type"] != b["type"] {
			t.Fatalf("redelivered body differs: %s vs %s", first[0].Body, msgs[1].Body)
		}
	}
	h.rest("GET", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/deliveries", nil, &dels)
	if len(dels) != 2 || dels[0].EventID != dels[1].EventID || dels[1].Kind != KindRedeliver {
		t.Fatalf("deliveries %+v", dels)
	}
	if code := h.rest("POST", "/api/v0/webhooks/deliveries/whd_nope/redeliver", nil, nil); code != 404 {
		t.Fatalf("unknown delivery: %d", code)
	}
}

func TestInvalidFilterExpression(t *testing.T) {
	h := newHarness(t, harnessOptions{allowPrivate: true})
	rc := newReceiver(t, nil)
	cases := map[string]map[string]any{
		"unterminated":    {"expr": `type == "io.ore`},
		"dangling":        {"expr": `type ==`},
		"unknown field":   {"expr": `repository == "x"`},
		"unknown func":    {"expr": `regex(type, "x")`},
		"bad glob":        {"subject": "["},
		"bad actor":       {"actor_kinds": []string{"robot"}},
		"bad payload":     {"payload": "huge"},
		"bad status list": {"types": []string{" "}},
	}
	for name, extra := range cases {
		body := map[string]any{"org": "acme", "url": rc.url()}
		for k, v := range extra {
			body[k] = v
		}
		var errBody map[string]any
		if code := h.rest("POST", "/api/v0/webhooks/endpoints", body, &errBody); code != 400 {
			t.Fatalf("%s: %d %v", name, code, errBody)
		}
	}
	c := h.endpoint(map[string]any{"org": "acme", "url": rc.url(), "expr": `type == "io.ore.issue.opened"`})
	var errBody map[string]any
	if code := h.rest("PATCH", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID, map[string]any{"expr": `type in`}, &errBody); code != 400 || !strings.Contains(fmt.Sprint(errBody["error"]), "expr") {
		t.Fatalf("patch bad expr: %d %v", code, errBody)
	}
	var ep Endpoint
	h.rest("GET", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID, nil, &ep)
	if ep.Filter.Expr != `type == "io.ore.issue.opened"` {
		t.Fatalf("bad patch was applied: %q", ep.Filter.Expr)
	}
	// unknown JSON fields are rejected too
	if code := h.rest("POST", "/api/v0/webhooks/endpoints", map[string]any{"org": "acme", "url": rc.url(), "secret": "mine"}, nil); code != 400 {
		t.Fatalf("unknown field: %d", code)
	}
}

func TestReplayOnDisabledEndpoint(t *testing.T) {
	h := newHarness(t, harnessOptions{allowPrivate: true})
	rc := newReceiver(t, nil)
	c := h.endpoint(map[string]any{"org": "acme", "url": rc.url(), "types": []string{"io.ore.issue."}})
	h.issue("x")
	rc.wait(t, 1, 5*time.Second)
	var ep Endpoint
	if code := h.rest("PATCH", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID, map[string]any{"enabled": false}, &ep); code != 200 || ep.Enabled || ep.DisabledReason == "" {
		t.Fatalf("disable: %d %+v", code, ep)
	}
	var errBody map[string]any
	if code := h.rest("POST", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/replay", map[string]any{"from_seq": 0}, &errBody); code != 409 || !strings.Contains(fmt.Sprint(errBody["error"]), "disabled") {
		t.Fatalf("replay: %d %v", code, errBody)
	}
	if code := h.rest("POST", "/api/v0/webhooks/endpoints/whep_nope/replay", map[string]any{"from_seq": 0}, nil); code != 404 {
		t.Fatalf("unknown endpoint: %d", code)
	}
	time.Sleep(50 * time.Millisecond)
	if rc.count() != 1 {
		t.Fatalf("disabled endpoint got %d", rc.count())
	}
}

func TestWorkerRestartResumesFromCursors(t *testing.T) {
	h := newHarness(t, harnessOptions{allowPrivate: true})
	rc := newReceiver(t, nil)
	c := h.endpoint(map[string]any{"org": "acme", "url": rc.url(), "types": []string{"io.ore.issue."}})
	for i := 0; i < 3; i++ {
		h.issue(fmt.Sprint("before ", i))
	}
	first := rc.wait(t, 3, 5*time.Second)
	var dels []Delivery
	waitFor(t, 2*time.Second, "acknowledged", func() bool {
		h.rest("GET", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/deliveries?status=succeeded", nil, &dels)
		return len(dels) == 3
	})
	h.svc.Stop() // the workers are gone; the API keeps running
	h.issue("while down 1")
	h.issue("while down 2")
	time.Sleep(60 * time.Millisecond)
	if rc.count() != 3 {
		t.Fatalf("delivered while stopped: %d", rc.count())
	}
	// a new process: new dispatcher on the same database
	svc2 := New(h.web)
	svc2.Dispatcher.Poll, svc2.Dispatcher.Schedule, svc2.Dispatcher.BaseURL, svc2.Dispatcher.AllowPrivate = 20*time.Millisecond, fastSchedule, h.srv.URL, true
	ctx, cancel := context.WithCancel(context.Background())
	svc2.Start(ctx)
	t.Cleanup(func() { svc2.Stop(); cancel() })
	msgs := rc.wait(t, 5, 5*time.Second)
	time.Sleep(100 * time.Millisecond)
	if rc.count() != 5 {
		t.Fatalf("re-sent acknowledged deliveries: %d", rc.count())
	}
	seen := map[string]bool{}
	for _, m := range msgs {
		if seen[m.ID] {
			t.Fatalf("duplicate %s", m.ID)
		}
		seen[m.ID] = true
	}
	for i := range first {
		if msgs[i].ID != first[i].ID {
			t.Fatalf("order changed")
		}
	}
	if !strings.Contains(string(msgs[3].Body), "issues/4") || !strings.Contains(string(msgs[4].Body), "issues/5") {
		t.Fatalf("resumed with %s / %s", msgs[3].Body, msgs[4].Body)
	}
	h.rest("GET", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/deliveries", nil, &dels)
	if len(dels) != 5 {
		t.Fatalf("deliveries %d", len(dels))
	}
}

func TestThousandEventsThreeEndpointsOneSlow(t *testing.T) {
	h := newHarness(t, harnessOptions{allowPrivate: true})
	fast1, fast2 := newReceiver(t, nil), newReceiver(t, nil)
	slow := newReceiver(t, func(n int, r *http.Request) (int, http.Header, time.Duration) { return 200, nil, 25 * time.Millisecond })
	var eps []created
	for _, rc := range []*receiver{fast1, slow, fast2} {
		eps = append(eps, h.endpoint(map[string]any{"org": "acme", "url": rc.url(), "types": []string{"io.ore.load."}}))
	}
	start := time.Now()
	h.emit(1000, "io.ore.load.test")
	waitFor(t, 60*time.Second, "fast receivers", func() bool { return fast1.count() >= 1000 && fast2.count() >= 1000 })
	elapsed := time.Since(start)
	slowCount := slow.count()
	t.Logf("fast endpoints finished 1000 deliveries in %s; the slow endpoint had received %d", elapsed, slowCount)
	if slowCount >= 1000 {
		t.Fatalf("the slow endpoint (25 ms per request) cannot have finished: %d", slowCount)
	}
	if slowCount == 0 {
		t.Fatal("the slow endpoint received nothing")
	}
	// order is preserved per endpoint and nothing was duplicated
	for _, rc := range []*receiver{fast1, fast2} {
		msgs := rc.all()
		seen := map[string]bool{}
		var lastSeq float64
		for _, m := range msgs {
			if seen[m.ID] {
				t.Fatalf("duplicate %s", m.ID)
			}
			seen[m.ID] = true
			seq := decodeBody(t, m.Body)["oreseq"].(float64)
			if seq <= lastSeq {
				t.Fatalf("out of order: %v after %v", seq, lastSeq)
			}
			lastSeq = seq
		}
	}
	var hl Health
	h.rest("GET", "/api/v0/webhooks/endpoints/"+eps[0].Endpoint.ID+"/health", nil, &hl)
	if hl.Counts[StatusSucceeded] != 1000 || hl.SuccessRate != 1 {
		t.Fatalf("health %+v", hl)
	}
	h.rest("GET", "/api/v0/webhooks/endpoints/"+eps[1].Endpoint.ID+"/health", nil, &hl)
	if hl.Counts[StatusPending] == 0 || hl.P50Ms < 20 {
		t.Fatalf("slow health %+v", hl)
	}
}
