// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"bytes"
	"strings"
	"testing"
)

func linked(t *testing.T) (*harness, *fakeGitHub, *fakeRepo, LinkResponse) {
	t.Helper()
	gh := newFakeGitHub(t)
	up := gh.addRepo("octo", "app", nil)
	h := newHarness(t, gh, Config{Token: testToken})
	h.createRepo("acme", "app")
	return h, gh, up, h.link("acme", "app", "octo", "app")
}

func TestWebhookBadSignature(t *testing.T) {
	h, _, up, res := linked(t)
	before := len(h.events("io.ore."))
	after := up.commit(t, "main", "Unverified", map[string]string{"x": "1"})
	body := payload(t, "push.json", up, map[string]any{"after": after})
	for name, sig := range map[string]string{"wrong secret": Sign("not-the-secret", body), "missing": "", "garbage": "sha256=00"} {
		code, out := h.deliverRaw("push", "d-"+name, sig, "application/json", body)
		if code != 401 {
			t.Fatalf("%s: expected 401, got %d %v", name, code, out)
		}
	}
	if h.refs("acme", "app")["refs/heads/main"] == after {
		t.Fatal("an unverified push was mirrored")
	}
	if n := len(h.events("io.ore.")); n != before {
		t.Fatalf("events recorded for rejected deliveries: %d -> %d", before, n)
	}
	// The secret still works.
	if code, _ := h.deliver("push", "d-ok", res.Link.Secret, body); code != 200 {
		t.Fatalf("valid delivery: %d", code)
	}
}

func TestWebhookReplayedDeliveryIsIdempotent(t *testing.T) {
	h, _, up, res := linked(t)
	after := up.commit(t, "main", "Once", map[string]string{"x": "1"})
	body := payload(t, "push.json", up, map[string]any{"after": after})
	const id = "7b9e0c22-6a1d-4f3e-9c1b-2f1e0d3a4b5c"
	code, out := h.deliver("push", id, res.Link.Secret, body)
	if code != 200 || !strings.Contains(out["outcomes"].([]any)[0].(map[string]any)["status"].(string), "processed") {
		t.Fatalf("first: %d %v", code, out)
	}
	n := len(h.events("io.ore."))
	code, out = h.deliver("push", id, res.Link.Secret, body)
	if code != 200 || out["outcomes"].([]any)[0].(map[string]any)["status"] != "duplicate" {
		t.Fatalf("replay: %d %v", code, out)
	}
	if m := len(h.events("io.ore.")); m != n {
		t.Fatalf("replay recorded %d new events", m-n)
	}
	if ds := h.events("io.ore.github.delivery.received"); len(ds) != 1 {
		t.Fatalf("delivery.received: %d", len(ds))
	}
	// A different delivery id for the same content is processed again (converges, records nothing new on refs).
	code, out = h.deliver("push", id+"-redelivered", res.Link.Secret, body)
	if code != 200 || out["outcomes"].([]any)[0].(map[string]any)["status"] != "processed" {
		t.Fatalf("redelivery: %d %v", code, out)
	}
	if len(h.events("io.ore.ref.updated")) != 2 { // the initial mirror and the first push
		t.Fatalf("ref.updated: %d", len(h.events("io.ore.ref.updated")))
	}
}

func TestWebhookUnknownRepository404(t *testing.T) {
	h, gh, _, _ := linked(t)
	other := gh.addRepo("someone", "else", nil)
	body := payload(t, "issues_opened.json", other, nil)
	code, out := h.deliver("issues", "d-unknown", "whatever", body)
	if code != 404 || !strings.Contains(out["error"].(string), "someone/else") {
		t.Fatalf("expected 404, got %d %v", code, out)
	}
	if len(h.events("io.ore.issue.")) != 0 {
		t.Fatal("an issue was recorded for an unlinked repository")
	}
}

func TestWebhookTooLarge413(t *testing.T) {
	h, _, up, res := linked(t)
	var b bytes.Buffer
	b.WriteString(`{"ref":"refs/heads/main","repository":{"full_name":"octo/app","name":"app","owner":{"login":"octo"}},"pad":"`)
	b.Write(bytes.Repeat([]byte("x"), MaxWebhookBody))
	b.WriteString(`"}`)
	code, out := h.deliver("push", "d-big", res.Link.Secret, b.Bytes())
	if code != 413 {
		t.Fatalf("expected 413, got %d %v", code, out)
	}
	if len(h.events("io.ore.github.")) != 2 { // linked + initial sync only
		t.Fatalf("events: %d", len(h.events("io.ore.github.")))
	}
	if h.refs("acme", "app")["refs/heads/main"] != up.tip(t, "refs/heads/main") {
		t.Fatal("mirror changed")
	}
}

func TestWebhookWrongContentType415(t *testing.T) {
	h, _, up, res := linked(t)
	body := payload(t, "ping.json", up, nil)
	form := []byte("payload=" + string(body))
	if code, _ := h.deliverRaw("ping", "d-form", Sign(res.Link.Secret, form), "application/x-www-form-urlencoded", form); code != 415 {
		t.Fatalf("expected 415, got %d", code)
	}
}

func TestWebhookPingAndUnknownEvents(t *testing.T) {
	h, _, up, res := linked(t)
	code, out := h.deliver("ping", "d-ping", res.Link.Secret, payload(t, "ping.json", up, nil))
	if code != 200 || out["outcomes"].([]any)[0].(map[string]any)["status"] != "processed" {
		t.Fatalf("ping: %d %v", code, out)
	}
	if ds := h.events("io.ore.github.delivery.received"); len(ds) != 1 || !strings.Contains(string(ds[0].Data), "logically awesome") {
		t.Fatalf("ping delivery: %+v", ds)
	}
	code, out = h.deliver("star", "d-star", res.Link.Secret, payload(t, "star.json", up, nil))
	if code != 200 || out["outcomes"].([]any)[0].(map[string]any)["status"] != "ignored" {
		t.Fatalf("star: %d %v", code, out)
	}
	code, out = h.deliver("issues", "d-labeled", res.Link.Secret, payload(t, "issues_labeled.json", up, nil))
	if code != 200 || out["outcomes"].([]any)[0].(map[string]any)["status"] != "ignored" {
		t.Fatalf("labeled: %d %v", code, out)
	}
	ig := h.events("io.ore.github.event.ignored")
	if len(ig) != 2 || data(t, ig[0])["event"] != "star" || data(t, ig[1])["action"] != "labeled" {
		t.Fatalf("ignored events: %+v", ig)
	}
	// Replaying an ignored delivery is a duplicate, not a second ignored event.
	h.deliver("star", "d-star", res.Link.Secret, payload(t, "star.json", up, nil))
	if len(h.events("io.ore.github.event.ignored")) != 2 {
		t.Fatal("ignored delivery replayed")
	}
	// Installation events carry no repository: acknowledged, nothing to attach.
	code, out = h.deliver("installation", "d-inst", res.Link.Secret, fixture(t, "installation_created.json"))
	if code != 202 || out["status"] != "ignored" {
		t.Fatalf("installation: %d %v", code, out)
	}
	// An App delivery records its installation id on the link.
	h.deliver("push", "d-inst-push", res.Link.Secret, payload(t, "push.json", up, map[string]any{"after": up.tip(t, "refs/heads/main")}))
	var got LinkResponse
	h.api("GET", "/api/v0/repos/acme/app/github/link", nil, &got)
	if got.Link.InstallationID != 44556677 {
		t.Fatalf("installation id not recorded: %+v", got.Link)
	}
}
