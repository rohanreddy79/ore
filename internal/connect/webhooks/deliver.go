// SPDX-License-Identifier: AGPL-3.0-only

package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/orevcs/ore/internal/ledger"
)

// Dispatcher runs one queue per enabled endpoint: a goroutine that reads
// the ledger from the endpoint's cursor, queues deliveries, sends the due
// ones in order and sleeps until the next retry. One slow receiver only
// slows its own queue.
type Dispatcher struct {
	Store *Store
	// Client sends deliveries; its Timeout is the per-attempt limit (default 30 s).
	Client *http.Client
	// BaseURL roots the signed URLs in thin payloads.
	BaseURL      string
	Schedule     Schedule
	MaxPayload   int           // full payloads above this fall back to thin (default 1 MiB)
	AllowPrivate bool          // allow endpoints on loopback/private networks (ORE_WEBHOOKS_ALLOW_PRIVATE)
	Poll         time.Duration // ledger scan interval (default 2 s)
	Batch        int           // events scanned per step (default 200)
	URLTTL       time.Duration // validity of signed thin URLs (default 24 h)
	Now          func() time.Time
	Log          *slog.Logger

	mu      sync.Mutex
	ctx     context.Context
	workers map[string]*worker
	wg      sync.WaitGroup
	rmu     sync.Mutex
	rnd     *rand.Rand
}

type worker struct{ wake chan struct{} }

func (d *Dispatcher) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Dispatcher) poll() time.Duration {
	if d.Poll > 0 {
		return d.Poll
	}
	return 2 * time.Second
}

func (d *Dispatcher) batch() int {
	if d.Batch > 0 {
		return d.Batch
	}
	return 200
}

func (d *Dispatcher) maxPayload() int {
	if d.MaxPayload > 0 {
		return d.MaxPayload
	}
	return 1 << 20
}

func (d *Dispatcher) urlTTL() time.Duration {
	if d.URLTTL > 0 {
		return d.URLTTL
	}
	return 24 * time.Hour
}

func (d *Dispatcher) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	d.Client = &http.Client{Timeout: 30 * time.Second}
	return d.Client
}

func (d *Dispatcher) schedule() Schedule {
	if len(d.Schedule.Steps) > 0 {
		return d.Schedule
	}
	return DefaultSchedule
}

func (d *Dispatcher) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

func (d *Dispatcher) random() *rand.Rand {
	d.rmu.Lock()
	defer d.rmu.Unlock()
	if d.rnd == nil {
		d.rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return d.rnd
}

// Run starts the workers for every enabled endpoint, rescans for new or
// re-enabled endpoints every poll interval, and returns once ctx ends and
// every worker has stopped.
func (d *Dispatcher) Run(ctx context.Context) {
	d.mu.Lock()
	d.ctx = ctx
	if d.workers == nil {
		d.workers = map[string]*worker{}
	}
	d.mu.Unlock()
	t := time.NewTicker(d.poll())
	defer t.Stop()
	for {
		d.tick(ctx)
		select {
		case <-ctx.Done():
			d.wg.Wait()
			return
		case <-t.C:
		}
	}
}

func (d *Dispatcher) tick(ctx context.Context) {
	eps, err := d.Store.Endpoints(ctx, "", "", true)
	if err != nil {
		if ctx.Err() == nil {
			d.log().Error("webhooks: list endpoints", "err", err)
		}
		return
	}
	for _, ep := range eps {
		d.Wake(ep.ID)
	}
}

// Wake nudges an endpoint's queue (after a replay, a change of filters or an
// enable), starting its worker if the dispatcher is running and the queue
// has none.
func (d *Dispatcher) Wake(endpointID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx == nil || d.ctx.Err() != nil {
		return
	}
	w, ok := d.workers[endpointID]
	if !ok {
		w = &worker{wake: make(chan struct{}, 1)}
		d.workers[endpointID] = w
		d.wg.Add(1)
		go d.runWorker(d.ctx, endpointID, w)
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (d *Dispatcher) runWorker(ctx context.Context, id string, w *worker) {
	defer d.wg.Done()
	defer func() {
		d.mu.Lock()
		if d.workers[id] == w {
			delete(d.workers, id)
		}
		d.mu.Unlock()
	}()
	for {
		ep, err := d.Store.Endpoint(ctx, id)
		if err != nil || !ep.Enabled {
			return
		}
		if ep, err = d.enqueue(ctx, ep); err != nil && ctx.Err() == nil {
			d.log().Error("webhooks: enqueue", "endpoint", id, "err", err)
		}
		d.drain(ctx, &ep)
		if ctx.Err() != nil {
			return
		}
		wait := d.poll()
		if next, ok := d.Store.NextDue(ctx, id); ok {
			if until := next.Sub(d.now()); until < wait {
				wait = until
			}
		}
		if wait <= 0 {
			continue
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-w.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// enqueue scans the ledger after the endpoint's cursor and queues a
// delivery for every matching event, moving the cursor to the head so
// non-matching stretches are never rescanned.
func (d *Dispatcher) enqueue(ctx context.Context, ep Endpoint) (Endpoint, error) {
	for {
		head, err := d.Store.HeadSeq(ctx)
		if err != nil {
			return ep, err
		}
		if head <= ep.Cursor {
			return ep, nil
		}
		evs, err := d.Store.EventsRange(ctx, ep.OrgID, ep.Cursor, head, d.batch())
		if err != nil {
			return ep, err
		}
		var matched []ledger.Event
		for _, ev := range evs {
			if ep.Accepts(ev) {
				matched = append(matched, ev)
			}
		}
		cursor := head
		if len(evs) == d.batch() {
			cursor = evs[len(evs)-1].Seq
		}
		if _, err := d.Store.InsertDeliveries(ctx, ep, matched, KindEvent, cursor, d.now()); err != nil {
			return ep, err
		}
		ep.Cursor = cursor
		if len(evs) < d.batch() {
			return ep, nil
		}
	}
}

// drain sends the endpoint's due deliveries in order until none is due,
// the endpoint is disabled or ctx ends.
func (d *Dispatcher) drain(ctx context.Context, ep *Endpoint) {
	for ctx.Err() == nil {
		due, err := d.Store.DueDeliveries(ctx, ep.ID, d.now(), 50)
		if err != nil || len(due) == 0 {
			return
		}
		for i := range due {
			if ctx.Err() != nil {
				return
			}
			// the endpoint may have been disabled or changed since it was loaded
			cur, err := d.Store.Endpoint(ctx, ep.ID)
			if err != nil || !cur.Enabled {
				ep.Enabled = false
				return
			}
			cur.Cursor = ep.Cursor
			*ep = cur
			if err := d.Send(ctx, ep, &due[i]); err != nil && ctx.Err() == nil {
				d.log().Warn("webhooks: delivery attempt failed", "delivery", due[i].ID, "endpoint", ep.ID, "err", err)
			}
			if !ep.Enabled {
				return
			}
		}
	}
}

// eventFor loads the event behind a delivery; pings are synthesized.
func (d *Dispatcher) eventFor(ctx context.Context, ep Endpoint, del Delivery) (ledger.Event, error) {
	if del.Kind == KindPing {
		return PingEvent(ep, del.EventID, del.CreatedAt), nil
	}
	return d.Store.EventByID(ctx, del.EventID)
}

// PingEvent is the synthetic io.ore.webhook.ping event a test delivery sends.
func PingEvent(ep Endpoint, id, at string) ledger.Event {
	data, _ := json.Marshal(map[string]any{"endpoint_id": ep.ID, "url": ep.URL, "message": "ping from Ore"})
	return ledger.Event{SpecVersion: "1.0", ID: id, Source: "ore://" + ep.OrgID, Type: EventPing, Subject: "webhooks/" + ep.ID, Time: at,
		DataContentType: "application/json", OreActor: ledger.System, OreOrg: ep.OrgID, Data: data}
}

// Send performs one attempt of a delivery now and persists the outcome. It
// is what the workers, the test button and redelivery use.
func (d *Dispatcher) Send(ctx context.Context, ep *Endpoint, del *Delivery) error {
	ev, err := d.eventFor(ctx, *ep, *del)
	if err != nil {
		return d.finish(ctx, ep, del, 0, 0, fmt.Errorf("event %s: %w", del.EventID, err), nil, true)
	}
	now := d.now()
	body, _ := d.Body(*ep, ev, now)
	ts := now
	secrets := ep.ActiveSecrets(now)
	values := make([]string, 0, len(secrets))
	for _, s := range secrets {
		values = append(values, s.Value)
	}
	sig, err := SignAll(values, ev.ID, ts, body)
	if err != nil {
		return d.finish(ctx, ep, del, 0, 0, err, nil, true)
	}
	if err := CheckAddress(ctx, ep.URL, d.AllowPrivate); err != nil {
		return d.finish(ctx, ep, del, 0, 0, fmt.Errorf("refused: %w", err), nil, true)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL, bytes.NewReader(body))
	if err != nil {
		return d.finish(ctx, ep, del, 0, 0, err, nil, true)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ore-webhooks/0")
	req.Header.Set(HeaderID, ev.ID)
	req.Header.Set(HeaderTime, strconv.FormatInt(ts.Unix(), 10))
	req.Header.Set(HeaderSig, sig)
	req.Header.Set("ce-type", ev.Type)
	start := time.Now()
	resp, err := d.client().Do(req)
	latency := time.Since(start)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			err = fmt.Errorf("timeout after %s", d.client().Timeout)
		}
		return d.finish(ctx, ep, del, 0, latency, err, nil, false)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return d.finish(ctx, ep, del, resp.StatusCode, latency, nil, resp.Header, false)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// finish records an attempt's outcome: success on 2xx, disable on 410,
// permanent failure when permanent, otherwise a retry per the schedule and
// Retry-After, or exhaustion once the budget is spent.
func (d *Dispatcher) finish(ctx context.Context, ep *Endpoint, del *Delivery, code int, latency time.Duration, attemptErr error, h http.Header, permanent bool) error {
	now := d.now()
	del.Attempt++
	if del.FirstAttemptAt == nil {
		s := stamp(now)
		del.FirstAttemptAt = &s
	}
	del.ResponseCode, del.LatencyMs, del.UpdatedAt = code, latency.Milliseconds(), stamp(now)
	att := Attempt{N: del.Attempt, At: stamp(now), Code: code, Latency: latency.Milliseconds()}
	if attemptErr != nil {
		att.Error = attemptErr.Error()
	} else if code < 200 || code >= 300 {
		att.Error = fmt.Sprintf("HTTP %d", code)
	}
	del.Attempts = append(del.Attempts, att)
	if len(del.Attempts) > 50 {
		del.Attempts = del.Attempts[len(del.Attempts)-50:]
	}
	del.LastError = att.Error
	del.NextAttemptAt = nil
	switch {
	case attemptErr == nil && code >= 200 && code < 300:
		del.Status = StatusSucceeded
	case code == http.StatusGone:
		del.Status = StatusFailed
		del.LastError = "HTTP 410 Gone: endpoint disabled"
		if err := d.disable(ctx, ep, "receiver answered 410 Gone", del); err != nil {
			d.log().Error("webhooks: disable endpoint", "endpoint", ep.ID, "err", err)
		}
	case permanent:
		del.Status = StatusFailed
	default:
		first := parseStamp(*del.FirstAttemptAt)
		var ra time.Duration
		if h != nil {
			ra = retryAfter(h, now)
		}
		if next, ok := d.schedule().Next(del.Attempt, first, now, ra, d.random()); ok {
			del.Status = StatusPending
			s := stamp(next)
			del.NextAttemptAt = &s
		} else {
			del.Status = StatusExhausted
		}
	}
	if err := d.Store.UpdateDelivery(ctx, del); err != nil {
		return err
	}
	if attemptErr != nil {
		return attemptErr
	}
	return nil
}

func (d *Dispatcher) disable(ctx context.Context, ep *Endpoint, reason string, del *Delivery) error {
	ep.Enabled, ep.DisabledReason = false, reason
	return d.Store.UpdateEndpoint(ctx, ep, EventEndpointDisabled, ledger.System, map[string]any{"endpoint_id": ep.ID, "url": ep.URL, "reason": reason, "delivery_id": del.ID, "event_id": del.EventID})
}

// Body renders the payload for an endpoint: full when asked for and within
// MaxPayload, otherwise thin (the envelope with a signed URL to the event).
// Pings are always full. The boolean reports whether the body is thin.
func (d *Dispatcher) Body(ep Endpoint, ev ledger.Event, now time.Time) ([]byte, bool) {
	if ep.Payload == "full" || ev.Type == EventPing {
		b, _ := json.Marshal(envelope(ev, false, ""))
		if ev.Type == EventPing || len(b) <= d.maxPayload() {
			return b, false
		}
	}
	b, _ := json.Marshal(envelope(ev, true, d.SignedURL(ep, ev.ID, now.Add(d.urlTTL()))))
	return b, true
}

// SignedURL is the self-authenticating link to the full event carried by thin payloads.
func (d *Dispatcher) SignedURL(ep Endpoint, eventID string, exp time.Time) string {
	secrets := ep.ActiveSecrets(d.now())
	secret := ""
	if len(secrets) > 0 {
		secret = secrets[0].Value
	}
	q := url.Values{"ep": {ep.ID}, "exp": {strconv.FormatInt(exp.Unix(), 10)}, "sig": {urlSignature(secret, eventID, ep.ID, exp.Unix())}}
	return strings.TrimRight(d.BaseURL, "/") + "/api/v0/webhooks/events/" + eventID + "?" + q.Encode()
}

func urlSignature(secret, eventID, endpointID string, exp int64) string {
	key, err := secretBytes(secret)
	if err != nil {
		key = []byte(secret)
	}
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "%s|%s|%d", eventID, endpointID, exp)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyURL checks a signed event URL's query against the endpoint's active secrets.
func (d *Dispatcher) VerifyURL(ep Endpoint, eventID string, q url.Values) error {
	exp, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	if err != nil || time.Unix(exp, 0).Before(d.now()) {
		return errors.New("link expired")
	}
	for _, s := range ep.ActiveSecrets(d.now()) {
		if subtle.ConstantTimeCompare([]byte(urlSignature(s.Value, eventID, ep.ID, exp)), []byte(q.Get("sig"))) == 1 {
			return nil
		}
	}
	return errors.New("bad signature")
}

// Replay queues deliveries for the endpoint's matching events in
// (fromSeq, toSeq] and wakes its queue. The endpoint must be enabled.
func (d *Dispatcher) Replay(ctx context.Context, ep Endpoint, fromSeq, toSeq int64, actor ledger.Actor) (int, error) {
	if !ep.Enabled {
		return 0, ErrDisabled
	}
	if toSeq <= 0 {
		head, err := d.Store.HeadSeq(ctx)
		if err != nil {
			return 0, err
		}
		toSeq = head
	}
	total := 0
	cursor := fromSeq
	for {
		evs, err := d.Store.EventsRange(ctx, ep.OrgID, cursor, toSeq, d.batch())
		if err != nil {
			return total, err
		}
		if len(evs) == 0 {
			break
		}
		var matched []ledger.Event
		for _, ev := range evs {
			if ep.Accepts(ev) {
				matched = append(matched, ev)
			}
		}
		n, err := d.Store.InsertDeliveries(ctx, ep, matched, KindReplay, 0, d.now())
		if err != nil {
			return total, err
		}
		total += n
		cursor = evs[len(evs)-1].Seq
		if len(evs) < d.batch() {
			break
		}
	}
	tx, err := d.Store.DB.Begin(ctx)
	if err != nil {
		return total, err
	}
	defer tx.Rollback()
	if _, err := tx.Emit(ep.OrgID, EventReplayRequested, "webhooks/"+ep.ID, actor, map[string]any{"endpoint_id": ep.ID, "from_seq": fromSeq, "to_seq": toSeq, "queued": total}); err != nil {
		return total, err
	}
	if err := tx.Commit(); err != nil {
		return total, err
	}
	d.Wake(ep.ID)
	return total, nil
}

// ErrDisabled is returned for operations that need an enabled endpoint.
var ErrDisabled = errors.New("endpoint is disabled")

// Health summarizes an endpoint's recent deliveries.
type Health struct {
	Enabled        bool           `json:"enabled"`
	DisabledReason string         `json:"disabled_reason,omitempty"`
	Counts         map[string]int `json:"counts"`
	Window         int            `json:"window"` // deliveries considered
	SuccessRate    float64        `json:"success_rate"`
	P50Ms          int64          `json:"p50_ms"`
	P95Ms          int64          `json:"p95_ms"`
	LastError      string         `json:"last_error,omitempty"`
	LastErrorAt    string         `json:"last_error_at,omitempty"`
	LastSuccessAt  string         `json:"last_success_at,omitempty"`
	NextAttemptAt  string         `json:"next_attempt_at,omitempty"`
	Cursor         int64          `json:"cursor"`
	HeadSeq        int64          `json:"head_seq"`
}

// EndpointHealth computes the health over the last 100 attempted deliveries.
func (d *Dispatcher) EndpointHealth(ctx context.Context, ep Endpoint) (Health, error) {
	h := Health{Enabled: ep.Enabled, DisabledReason: ep.DisabledReason, Cursor: ep.Cursor}
	var err error
	if h.Counts, err = d.Store.CountDeliveries(ctx, ep.ID); err != nil {
		return h, err
	}
	h.HeadSeq, _ = d.Store.HeadSeq(ctx)
	recent, err := d.Store.RecentDeliveries(ctx, ep.ID, 100)
	if err != nil {
		return h, err
	}
	h.Window = len(recent)
	var lat []int64
	ok := 0
	for _, del := range recent {
		if del.Status == StatusSucceeded {
			ok++
			if h.LastSuccessAt == "" || del.UpdatedAt > h.LastSuccessAt {
				h.LastSuccessAt = del.UpdatedAt
			}
		}
		for _, a := range del.Attempts {
			if a.Error != "" && (h.LastErrorAt == "" || a.At > h.LastErrorAt) {
				h.LastError, h.LastErrorAt = a.Error, a.At
			}
			if a.Code > 0 || a.Error == "" {
				lat = append(lat, a.Latency)
			}
		}
	}
	if len(recent) > 0 {
		h.SuccessRate = float64(ok) / float64(len(recent))
	}
	if len(lat) > 0 {
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		h.P50Ms = lat[(len(lat)-1)*50/100]
		h.P95Ms = lat[(len(lat)-1)*95/100]
	}
	if next, ok := d.Store.NextDue(ctx, ep.ID); ok {
		h.NextAttemptAt = stamp(next)
	}
	return h, nil
}

// CheckAddress validates an endpoint URL: http or https with a host, and,
// unless allowPrivate, not loopback, private, link-local or unspecified
// (checked on literal IPs and on every address the name resolves to;
// names that do not resolve are accepted and fail at delivery time).
func CheckAddress(ctx context.Context, raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("url must be http(s)://host[:port]/path")
	}
	if allowPrivate {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("%s is a private address (set ORE_WEBHOOKS_ALLOW_PRIVATE=1 to allow)", host)
	}
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return fmt.Errorf("%s is a private address (set ORE_WEBHOOKS_ALLOW_PRIVATE=1 to allow)", host)
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		if isPrivateIP(a.IP) {
			return fmt.Errorf("%s resolves to the private address %s (set ORE_WEBHOOKS_ALLOW_PRIVATE=1 to allow)", host, a.IP)
		}
	}
	return nil
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast()
}
