// SPDX-License-Identifier: AGPL-3.0-only

package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/registry"
	"github.com/orevcs/ore/internal/review"
	"github.com/orevcs/ore/internal/web"
)

const admin = "admin-token"

// harness is the registry-wired server with fast timings.
type harness struct {
	t    *testing.T
	srv  *httptest.Server
	web  *web.Server
	svc  *Service
	db   *ledger.DB
	org  ledger.Org
	repo ledger.Repo
}

type harnessOptions struct {
	allowPrivate bool
	timeout      time.Duration
	schedule     Schedule
	maxPayload   int
}

// fastSchedule retries quickly: 30 ms, 60 ms, then 100 ms steps within 2 s.
var fastSchedule = Schedule{Steps: []time.Duration{30 * time.Millisecond, 60 * time.Millisecond, 100 * time.Millisecond}, Budget: 2 * time.Second}

func newHarness(t *testing.T, opt harnessOptions) *harness {
	t.Helper()
	if opt.allowPrivate {
		t.Setenv("ORE_WEBHOOKS_ALLOW_PRIVATE", "1")
	}
	dir := t.TempDir()
	db, err := ledger.Open(context.Background(), filepath.Join(dir, "ore.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repos := filepath.Join(dir, "repos")
	s := &web.Server{DB: db, ReposDir: repos, LogsDir: filepath.Join(dir, "logs"), Token: admin, BaseURL: "http://placeholder", Log: slog.Default(),
		Review: &review.Service{DB: db, ReposDir: repos}, Services: map[string]any{}}
	ctx, cancel := context.WithCancel(context.Background())
	if err := registry.Apply(ctx, s); err != nil {
		t.Fatal(err)
	}
	svc, ok := s.Services[ServiceName].(*Service)
	if !ok {
		t.Fatal("webhooks service not attached")
	}
	t.Cleanup(func() { svc.Stop(); cancel() })
	svc.Dispatcher.Poll = 20 * time.Millisecond
	svc.Dispatcher.Schedule = fastSchedule
	if len(opt.schedule.Steps) > 0 {
		svc.Dispatcher.Schedule = opt.schedule
	}
	if opt.timeout > 0 {
		svc.Dispatcher.Client = &http.Client{Timeout: opt.timeout}
	}
	if opt.maxPayload > 0 {
		svc.Dispatcher.MaxPayload = opt.maxPayload
	}
	svc.Exporter.Interval = 30 * time.Millisecond
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	svc.Dispatcher.BaseURL = srv.URL
	h := &harness{t: t, srv: srv, web: s, svc: svc, db: db}
	h.rest("POST", "/api/v0/orgs", map[string]string{"name": "acme"}, &h.org)
	h.rest("POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "app"}, &h.repo)
	return h
}

func (h *harness) rest(method, path string, in, out any) int {
	h.t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, body)
	req.Header.Set("Authorization", "Bearer "+admin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil {
		// start from a zero value: omitted (omitempty) fields must not keep stale values
		if v := reflect.ValueOf(out); v.Kind() == reflect.Pointer {
			v.Elem().Set(reflect.Zero(v.Elem().Type()))
		}
	}
	if out != nil && resp.StatusCode < 400 {
		if err := json.Unmarshal(data, out); err != nil {
			h.t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
	if out != nil && resp.StatusCode >= 400 {
		if m, ok := out.(*map[string]any); ok {
			json.Unmarshal(data, m)
		}
	}
	return resp.StatusCode
}

// issue causes an io.ore.issue.opened event and returns it.
func (h *harness) issue(title string) ledger.Issue {
	h.t.Helper()
	var i ledger.Issue
	if code := h.rest("POST", "/api/v0/repos/acme/app/issues", map[string]string{"title": title}, &i); code != 201 {
		h.t.Fatalf("issue: %d", code)
	}
	return i
}

// emit writes n events straight into the ledger in one transaction.
func (h *harness) emit(n int, typ string) {
	h.t.Helper()
	tx, err := h.db.Begin(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := tx.Emit(h.org.ID, typ, fmt.Sprintf("acme/app/synthetic/%d", i), ledger.System, map[string]any{"i": i}); err != nil {
			h.t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		h.t.Fatal(err)
	}
}

type created struct {
	Endpoint Endpoint
	Secret   Secret
}

func (h *harness) endpoint(body map[string]any) created {
	h.t.Helper()
	var c created
	var errBody map[string]any
	if code := h.rest("POST", "/api/v0/webhooks/endpoints", body, &c); code != 201 {
		h.rest("POST", "/api/v0/webhooks/endpoints", body, &errBody)
		h.t.Fatalf("create endpoint: %d %v", code, errBody)
	}
	return c
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// received is one request seen by a receiver.
type received struct {
	ID, Timestamp, Signature, CEType string
	Body                             []byte
	Headers                          http.Header
	At                               time.Time
}

// receiver is an in-test webhook consumer with a programmable response.
type receiver struct {
	srv  *httptest.Server
	mu   sync.Mutex
	got  []received
	resp func(n int, r *http.Request) (code int, headers http.Header, delay time.Duration)
}

func newReceiver(t *testing.T, resp func(n int, r *http.Request) (int, http.Header, time.Duration)) *receiver {
	t.Helper()
	rc := &receiver{resp: resp}
	rc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rc.mu.Lock()
		n := len(rc.got) + 1
		rc.got = append(rc.got, received{ID: r.Header.Get(HeaderID), Timestamp: r.Header.Get(HeaderTime), Signature: r.Header.Get(HeaderSig), CEType: r.Header.Get("ce-type"), Body: body, Headers: r.Header.Clone(), At: time.Now()})
		rc.mu.Unlock()
		code, headers, delay := 200, http.Header(nil), time.Duration(0)
		if rc.resp != nil {
			code, headers, delay = rc.resp(n, r)
		}
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		for k, v := range headers {
			w.Header()[k] = v
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(rc.srv.Close)
	return rc
}

func (rc *receiver) count() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.got)
}

func (rc *receiver) all() []received {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]received{}, rc.got...)
}

func (rc *receiver) wait(t *testing.T, n int, timeout time.Duration) []received {
	t.Helper()
	waitFor(t, timeout, fmt.Sprintf("%d deliveries (have %d)", n, rc.count()), func() bool { return rc.count() >= n })
	return rc.all()
}

func (rc *receiver) url() string { return rc.srv.URL + "/hook" }

func verifyAll(t *testing.T, secret string, msgs []received) {
	t.Helper()
	for _, m := range msgs {
		if err := Verify(secret, m.ID, m.Timestamp, m.Signature, m.Body, time.Now(), DefaultTolerance); err != nil {
			t.Fatalf("delivery %s: %v (sig %q)", m.ID, err, m.Signature)
		}
	}
}

func decodeBody(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("body %s: %v", b, err)
	}
	return m
}

func TestWebhooksEndToEnd(t *testing.T) {
	h := newHarness(t, harnessOptions{allowPrivate: true})
	rc := newReceiver(t, nil)

	// create an org-wide endpoint for issue events (thin payloads)
	c := h.endpoint(map[string]any{"org": "acme", "url": rc.url(), "types": []string{"io.ore.issue."}, "description": "issues"})
	ep := c.Endpoint
	if ep.Payload != "thin" || !ep.Enabled || ep.Org != "acme" || ep.Repo != "" || len(ep.Secrets) != 1 || ep.Secrets[0].Value != "" || !strings.HasPrefix(c.Secret.Value, SecretPrefix) {
		t.Fatalf("endpoint %+v secret %+v", ep, c.Secret)
	}
	// an endpoint starts at the ledger head: the repository creation before it is not delivered
	i1, i2 := h.issue("first"), h.issue("second")
	msgs := rc.wait(t, 2, 5*time.Second)
	verifyAll(t, c.Secret.Value, msgs)
	if msgs[0].CEType != "io.ore.issue.opened" || msgs[1].CEType != "io.ore.issue.opened" {
		t.Fatalf("types %s %s", msgs[0].CEType, msgs[1].CEType)
	}
	body := decodeBody(t, msgs[0].Body)
	if body["id"] != msgs[0].ID || body["subject"] != "acme/app/issues/1" || body["specversion"] != "1.0" {
		t.Fatalf("envelope %v", body)
	}
	data := body["data"].(map[string]any)
	if data["thin"] != true || !strings.HasPrefix(data["url"].(string), h.srv.URL+"/api/v0/webhooks/events/"+msgs[0].ID+"?") {
		t.Fatalf("thin data %v", data)
	}
	// the signed URL serves the full event without a bearer token
	resp, err := http.Get(data["url"].(string))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("signed url: %v %d", err, resp.StatusCode)
	}
	full, _ := io.ReadAll(resp.Body)
	fm := decodeBody(t, full)
	if fm["data"].(map[string]any)["title"] != "first" || fm["oreactor"].(map[string]any)["kind"] != "human" {
		t.Fatalf("full event %v", fm)
	}
	if resp, _ := http.Get(strings.Replace(data["url"].(string), "sig=", "sig=00", 1)); resp.StatusCode != 401 {
		t.Fatalf("tampered signed url: %d", resp.StatusCode)
	}
	// deliveries and health
	var dels []Delivery
	waitFor(t, 2*time.Second, "succeeded deliveries", func() bool {
		h.rest("GET", "/api/v0/webhooks/endpoints/"+ep.ID+"/deliveries?status=succeeded", nil, &dels)
		return len(dels) == 2
	})
	if dels[0].EventID != msgs[0].ID || dels[0].Attempt != 1 || dels[0].ResponseCode != 200 || dels[0].Kind != KindEvent || len(dels[0].Attempts) != 1 {
		t.Fatalf("delivery %+v", dels[0])
	}
	var hl Health
	h.rest("GET", "/api/v0/webhooks/endpoints/"+ep.ID+"/health", nil, &hl)
	if hl.SuccessRate != 1 || hl.Window != 2 || hl.Counts[StatusSucceeded] != 2 || !hl.Enabled || hl.Cursor < dels[1].EventSeq {
		t.Fatalf("health %+v", hl)
	}
	var one map[string]any
	h.rest("GET", "/api/v0/webhooks/deliveries/"+dels[0].ID, nil, &one)
	if one["thin"] != true || one["event"].(map[string]any)["id"] != msgs[0].ID {
		t.Fatalf("delivery view %v", one)
	}
	_ = i1
	_ = i2

	// a repository-scoped endpoint with full payloads sees only its repository
	rc2 := newReceiver(t, nil)
	var c2 created
	if code := h.rest("POST", "/api/v0/webhooks/repos/acme/app/endpoints", map[string]any{"url": rc2.url(), "payload": "full"}, &c2); code != 201 {
		t.Fatalf("repo endpoint: %d", code)
	}
	if c2.Endpoint.Repo != "acme/app" || c2.Endpoint.RepoID == nil {
		t.Fatalf("repo endpoint %+v", c2.Endpoint)
	}
	h.rest("POST", "/api/v0/repos", map[string]any{"org": "acme", "name": "other"}, nil) // subject acme/other: not for the app endpoint
	h.issue("third")
	m2 := rc2.wait(t, 1, 5*time.Second)
	verifyAll(t, c2.Secret.Value, m2)
	fb := decodeBody(t, m2[0].Body)
	if fb["type"] != "io.ore.issue.opened" || fb["data"].(map[string]any)["title"] != "third" || fb["data"].(map[string]any)["thin"] != nil {
		t.Fatalf("full payload %v", fb)
	}
	time.Sleep(100 * time.Millisecond)
	if rc2.count() != 1 {
		t.Fatalf("repo endpoint received %d", rc2.count())
	}
	rc.wait(t, 3, 5*time.Second)

	// the test button sends a ping synchronously
	var test struct{ Delivery Delivery }
	if code := h.rest("POST", "/api/v0/webhooks/endpoints/"+ep.ID+"/test", nil, &test); code != 200 || test.Delivery.Status != StatusSucceeded || test.Delivery.Kind != KindPing {
		t.Fatalf("test: %d %+v", code, test.Delivery)
	}
	ping := rc.wait(t, 4, 2*time.Second)[3]
	if ping.CEType != EventPing || ping.ID != test.Delivery.EventID {
		t.Fatalf("ping %+v", ping)
	}
	verifyAll(t, c.Secret.Value, []received{ping})

	// list, patch, delete
	var list []Endpoint
	h.rest("GET", "/api/v0/webhooks/endpoints?org=acme", nil, &list)
	if len(list) != 2 || list[0].Secrets[0].Value != "" {
		t.Fatalf("list %+v", list)
	}
	var patched Endpoint
	if code := h.rest("PATCH", "/api/v0/webhooks/endpoints/"+ep.ID, map[string]any{"description": "renamed", "types": []string{"io.ore.change."}}, &patched); code != 200 || patched.Description != "renamed" || patched.Filter.Types[0] != "io.ore.change." {
		t.Fatalf("patch: %d %+v", code, patched)
	}
	h.issue("fourth") // no longer matches
	rc2.wait(t, 2, 5*time.Second)
	time.Sleep(60 * time.Millisecond)
	if rc.count() != 4 {
		t.Fatalf("filter change ignored: %d", rc.count())
	}
	if code := h.rest("DELETE", "/api/v0/webhooks/endpoints/"+ep.ID, nil, nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code := h.rest("GET", "/api/v0/webhooks/endpoints/"+ep.ID, nil, nil); code != 404 {
		t.Fatalf("after delete: %d", code)
	}
	// the slice's own events are on the ledger
	evs, _ := h.db.Events(context.Background(), ledger.EventsOptions{Type: "io.ore.webhook.", Limit: 100})
	var types []string
	for _, e := range evs {
		types = append(types, e.Type)
	}
	if strings.Join(types, ",") != "io.ore.webhook.endpoint.created,io.ore.webhook.endpoint.created,io.ore.webhook.endpoint.updated,io.ore.webhook.endpoint.deleted" {
		t.Fatalf("events %v", types)
	}
}

func TestRetryAfterFiveHundredThenSucceeds(t *testing.T) {
	h := newHarness(t, harnessOptions{allowPrivate: true})
	rc := newReceiver(t, func(n int, r *http.Request) (int, http.Header, time.Duration) {
		if n == 1 {
			return 500, nil, 0
		}
		return 200, nil, 0
	})
	c := h.endpoint(map[string]any{"org": "acme", "url": rc.url(), "types": []string{"io.ore.issue."}})
	h.issue("flaky")
	msgs := rc.wait(t, 2, 5*time.Second)
	if msgs[0].ID != msgs[1].ID {
		t.Fatalf("retry changed webhook-id: %s %s", msgs[0].ID, msgs[1].ID)
	}
	verifyAll(t, c.Secret.Value, msgs)
	var dels []Delivery
	waitFor(t, 2*time.Second, "succeeded", func() bool {
		h.rest("GET", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/deliveries", nil, &dels)
		return len(dels) == 1 && dels[0].Status == StatusSucceeded
	})
	d := dels[0]
	if d.Attempt != 2 || len(d.Attempts) != 2 || d.Attempts[0].Code != 500 || d.Attempts[0].Error != "HTTP 500" || d.Attempts[1].Code != 200 || d.LastError != "" || d.NextAttemptAt != nil {
		t.Fatalf("delivery %+v", d)
	}
	var hl Health
	h.rest("GET", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/health", nil, &hl)
	if hl.SuccessRate != 1 || hl.LastError != "HTTP 500" {
		t.Fatalf("health %+v", hl)
	}
}

func TestRetryAfterHeaderAndExhaustion(t *testing.T) {
	h := newHarness(t, harnessOptions{allowPrivate: true, schedule: Schedule{Steps: []time.Duration{20 * time.Millisecond}, Budget: 300 * time.Millisecond}})
	rc := newReceiver(t, func(n int, r *http.Request) (int, http.Header, time.Duration) {
		return 503, http.Header{"Retry-After": {"1"}}, 0
	})
	c := h.endpoint(map[string]any{"org": "acme", "url": rc.url(), "types": []string{"io.ore.issue."}})
	h.issue("busy")
	rc.wait(t, 1, 5*time.Second)
	var dels []Delivery
	waitFor(t, 2*time.Second, "retry-after scheduled", func() bool {
		h.rest("GET", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/deliveries", nil, &dels)
		return len(dels) == 1 && dels[0].Attempt == 1
	})
	// Retry-After: 1 overrides the 20 ms step, which is beyond the 300 ms budget: exhausted, not retried
	waitFor(t, 2*time.Second, "exhausted", func() bool {
		h.rest("GET", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/deliveries", nil, &dels)
		return dels[0].Status == StatusExhausted
	})
	time.Sleep(50 * time.Millisecond)
	if rc.count() != 1 {
		t.Fatalf("exhausted delivery was retried: %d", rc.count())
	}
}

func TestGoneDisablesEndpoint(t *testing.T) {
	h := newHarness(t, harnessOptions{allowPrivate: true})
	rc := newReceiver(t, func(n int, r *http.Request) (int, http.Header, time.Duration) { return 410, nil, 0 })
	c := h.endpoint(map[string]any{"org": "acme", "url": rc.url(), "types": []string{"io.ore.issue."}})
	h.issue("gone")
	rc.wait(t, 1, 5*time.Second)
	var ep Endpoint
	waitFor(t, 2*time.Second, "disabled", func() bool {
		h.rest("GET", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID, nil, &ep)
		return !ep.Enabled
	})
	if !strings.Contains(ep.DisabledReason, "410") {
		t.Fatalf("reason %q", ep.DisabledReason)
	}
	var dels []Delivery
	h.rest("GET", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/deliveries", nil, &dels)
	if len(dels) != 1 || dels[0].Status != StatusFailed || dels[0].ResponseCode != 410 {
		t.Fatalf("delivery %+v", dels)
	}
	evs, _ := h.db.Events(context.Background(), ledger.EventsOptions{Type: EventEndpointDisabled})
	if len(evs) != 1 {
		t.Fatalf("disabled events %d", len(evs))
	}
	// nothing more is delivered; replay and redeliver refuse; test still works
	h.issue("after")
	time.Sleep(100 * time.Millisecond)
	if rc.count() != 1 {
		t.Fatalf("disabled endpoint received %d", rc.count())
	}
	if code := h.rest("POST", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/replay", map[string]any{"from_seq": 0}, nil); code != 409 {
		t.Fatalf("replay on disabled: %d", code)
	}
	if code := h.rest("POST", "/api/v0/webhooks/deliveries/"+dels[0].ID+"/redeliver", nil, nil); code != 409 {
		t.Fatalf("redeliver on disabled: %d", code)
	}
	// re-enabling resumes from the cursor: the missed event is not replayed automatically
	rc.resp = nil
	var enabled Endpoint
	if code := h.rest("PATCH", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID, map[string]any{"enabled": true}, &enabled); code != 200 || !enabled.Enabled || enabled.DisabledReason != "" {
		t.Fatalf("re-enable: %d %+v", code, enabled)
	}
	// re-enabling resumes from the cursor: the event missed while disabled is delivered, then new ones
	h.issue("resumed")
	msgs := rc.wait(t, 3, 5*time.Second)
	if !strings.Contains(string(msgs[1].Body), "issues/2") || !strings.Contains(string(msgs[2].Body), "issues/3") {
		t.Fatalf("resumed with %s / %s", msgs[1].Body, msgs[2].Body)
	}
}

func TestReplayRange(t *testing.T) {
	h := newHarness(t, harnessOptions{allowPrivate: true})
	rc := newReceiver(t, nil)
	c := h.endpoint(map[string]any{"org": "acme", "url": rc.url(), "types": []string{"io.ore.issue."}})
	h.issue("a")
	h.issue("b")
	h.issue("c")
	first := rc.wait(t, 3, 5*time.Second)
	var dels []Delivery
	h.rest("GET", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/deliveries", nil, &dels)
	// replay (seq of a, seq of c]: b and c again, with their original webhook-ids
	var out struct{ Queued int }
	if code := h.rest("POST", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/replay", map[string]any{"from_seq": dels[0].EventSeq, "to_seq": dels[2].EventSeq}, &out); code != 200 || out.Queued != 2 {
		t.Fatalf("replay: %d %+v", code, out)
	}
	msgs := rc.wait(t, 5, 5*time.Second)
	if msgs[3].ID != first[1].ID || msgs[4].ID != first[2].ID {
		t.Fatalf("replayed ids %s %s want %s %s", msgs[3].ID, msgs[4].ID, first[1].ID, first[2].ID)
	}
	verifyAll(t, c.Secret.Value, msgs)
	h.rest("GET", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/deliveries", nil, &dels)
	kinds := 0
	for _, d := range dels {
		if d.Kind == KindReplay {
			kinds++
		}
	}
	if len(dels) != 5 || kinds != 2 {
		t.Fatalf("deliveries %d replays %d", len(dels), kinds)
	}
	// replay by time: everything since the ledger began, filtered by the endpoint's types
	if code := h.rest("POST", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/replay", map[string]any{"since": "2000-01-01T00:00:00Z"}, &out); code != 200 || out.Queued != 3 {
		t.Fatalf("replay since: %d %+v", code, out)
	}
	rc.wait(t, 8, 5*time.Second)
	if code := h.rest("POST", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/replay", map[string]any{}, nil); code != 400 {
		t.Fatalf("replay without range: %d", code)
	}
	evs, _ := h.db.Events(context.Background(), ledger.EventsOptions{Type: EventReplayRequested})
	if len(evs) != 2 {
		t.Fatalf("replay events %d", len(evs))
	}
}

func TestRotateSecrets(t *testing.T) {
	h := newHarness(t, harnessOptions{allowPrivate: true})
	rc := newReceiver(t, nil)
	c := h.endpoint(map[string]any{"org": "acme", "url": rc.url(), "types": []string{"io.ore.issue."}})
	var rot struct {
		Endpoint Endpoint
		Secret   Secret
	}
	if code := h.rest("POST", "/api/v0/webhooks/endpoints/"+c.Endpoint.ID+"/rotate", nil, &rot); code != 200 || rot.Secret.Value == "" || rot.Secret.Value == c.Secret.Value {
		t.Fatalf("rotate: %d %+v", code, rot)
	}
	if len(rot.Endpoint.Secrets) != 2 || rot.Endpoint.Secrets[0].ExpiresAt == nil || rot.Endpoint.Secrets[1].ExpiresAt != nil || rot.Endpoint.Secrets[0].Value != "" {
		t.Fatalf("secrets after rotation %+v", rot.Endpoint.Secrets)
	}
	exp := parseStamp(*rot.Endpoint.Secrets[0].ExpiresAt)
	if d := time.Until(exp); d < 23*time.Hour || d > 25*time.Hour {
		t.Fatalf("old secret expires in %s", d)
	}
	h.issue("rotated")
	msgs := rc.wait(t, 1, 5*time.Second)
	if len(strings.Fields(msgs[0].Signature)) != 2 {
		t.Fatalf("expected two signatures, got %q", msgs[0].Signature)
	}
	verifyAll(t, c.Secret.Value, msgs)
	verifyAll(t, rot.Secret.Value, msgs)
	// after the grace period only the new secret signs
	ep, _ := h.svc.Store.Endpoint(context.Background(), c.Endpoint.ID)
	if n := len(ep.ActiveSecrets(time.Now().Add(26 * time.Hour))); n != 1 {
		t.Fatalf("active secrets after grace: %d", n)
	}
	evs, _ := h.db.Events(context.Background(), ledger.EventsOptions{Type: EventSecretRotated})
	if len(evs) != 1 {
		t.Fatalf("rotation events %d", len(evs))
	}
}

// fakeS3 is an in-test S3-compatible receiver that verifies SigV4 with the
// known secret and keeps the objects.
type fakeS3 struct {
	srv     *httptest.Server
	mu      sync.Mutex
	objects map[string][]byte
	bad     []string
}

func newFakeS3(t *testing.T, accessKey, secretKey, region string) *fakeS3 {
	t.Helper()
	f := &fakeS3{objects: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		auth := r.Header.Get("Authorization")
		hash := r.Header.Get("x-amz-content-sha256")
		want, signed, scope := SigV4Signature(r, secretKey, region, "s3", hash, r.Header.Get("x-amz-date"))
		expect := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", accessKey, scope, signed, want)
		f.mu.Lock()
		defer f.mu.Unlock()
		if auth != expect || hash != sha256Hex(body) || r.Method != http.MethodPut {
			f.bad = append(f.bad, fmt.Sprintf("%s %s: got %q want %q", r.Method, r.URL.Path, auth, expect))
			w.WriteHeader(403)
			return
		}
		f.objects[r.URL.Path] = body
		w.WriteHeader(200)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeS3) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.objects {
		out = append(out, k)
	}
	return out
}

func TestS3ExportWithSigV4(t *testing.T) {
	t.Setenv("ORE_AUDIT_S3_ACCESS_KEY", "AKIAEXAMPLE")
	t.Setenv("ORE_AUDIT_S3_SECRET_KEY", "verysecret")
	h := newHarness(t, harnessOptions{allowPrivate: true})
	h.svc.Exporter.Batch = 5
	h.svc.Exporter.MaxDelay = time.Hour // only full batches flush in this test
	s3 := newFakeS3(t, "AKIAEXAMPLE", "verysecret", "eu-central-1")
	var ex AuditExport
	if code := h.rest("POST", "/api/v0/webhooks/audit/exports", map[string]any{"kind": "s3", "bucket": "audit", "endpoint": s3.srv.URL, "region": "eu-central-1", "prefix": "ore/", "types": []string{"io.ore.issue."}}, &ex); code != 201 {
		t.Fatalf("create export: %d", code)
	}
	for i := 0; i < 4; i++ {
		h.issue(fmt.Sprint("i", i))
	}
	time.Sleep(120 * time.Millisecond)
	if len(s3.keys()) != 0 {
		t.Fatalf("partial batch flushed early: %v", s3.keys())
	}
	h.issue("i4")
	waitFor(t, 5*time.Second, "s3 object", func() bool { return len(s3.keys()) == 1 })
	key := s3.keys()[0]
	if !strings.HasPrefix(key, "/audit/ore/"+time.Now().UTC().Format("2006/01/02")+"/events-") || !strings.HasSuffix(key, ".ndjson") {
		t.Fatalf("key %s", key)
	}
	lines := strings.Split(strings.TrimSpace(string(s3.objects[key])), "\n")
	if len(lines) != 5 {
		t.Fatalf("lines %d", len(lines))
	}
	for _, l := range lines {
		m := decodeBody(t, []byte(l))
		if m["type"] != "io.ore.issue.opened" || m["data"].(map[string]any)["title"] == nil {
			t.Fatalf("line %s", l)
		}
	}
	if len(s3.bad) != 0 {
		t.Fatalf("bad signatures: %v", s3.bad)
	}
	var st Status
	h.rest("GET", "/api/v0/webhooks/audit", nil, &st)
	if len(st.Exports) != 1 || st.Exports[0].Exported != 5 || st.Exports[0].Lag != 0 || st.Exports[0].LastError != "" {
		t.Fatalf("status %+v", st)
	}
	if code := h.rest("DELETE", "/api/v0/webhooks/audit/exports/"+ex.ID, nil, nil); code != 200 {
		t.Fatalf("delete export: %d", code)
	}
	// a wrong secret is rejected by the receiver and recorded on the export
	t.Setenv("ORE_AUDIT_S3_SECRET_KEY", "wrong")
	h.rest("POST", "/api/v0/webhooks/audit/exports", map[string]any{"kind": "s3", "bucket": "audit", "endpoint": s3.srv.URL, "region": "eu-central-1"}, &ex)
	h.svc.Exporter.Batch = 1
	h.issue("i5")
	waitFor(t, 5*time.Second, "export error", func() bool {
		h.rest("GET", "/api/v0/webhooks/audit", nil, &st)
		return len(st.Exports) == 1 && strings.Contains(st.Exports[0].LastError, "HTTP 403")
	})
}

func jsonDecode(resp *http.Response, v any) error { return json.NewDecoder(resp.Body).Decode(v) }

func queryOf(t *testing.T, link string) url.Values {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}
