// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/web"
)

// MaxWebhookBody is the largest delivery accepted (GitHub caps payloads at 25 MB; Ore needs far less).
const MaxWebhookBody = 1 << 20

// Headers GitHub sets on deliveries.
const (
	HeaderEvent     = "X-GitHub-Event"
	HeaderDelivery  = "X-GitHub-Delivery"
	HeaderSignature = "X-Hub-Signature-256"
)

// Sign computes the X-Hub-Signature-256 value for a body.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// VerifySignature checks a delivery's X-Hub-Signature-256 against the secret
// in constant time. Missing or malformed signatures fail.
func VerifySignature(secret string, body []byte, signature string) bool {
	if secret == "" || !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	want, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil || len(want) != sha256.Size {
		return false
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return subtle.ConstantTimeCompare(m.Sum(nil), want) == 1
}

// DeliveryOutcome is the per-link result of a delivery.
type DeliveryOutcome struct {
	Repo    string `json:"repo"`
	Status  string `json:"status"` // processed | duplicate | ignored | error
	Event   string `json:"event"`
	Action  string `json:"action,omitempty"`
	Detail  any    `json:"detail,omitempty"`
	Updates int    `json:"updates,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Webhook receives GitHub deliveries (POST /hooks/github). The link is found
// from repository.full_name, the signature is verified against that link's
// secret, replayed delivery ids are acknowledged without side effects, and
// the event is applied synchronously so GitHub's delivery log shows the
// outcome. Processing uses a background context so a dropped connection
// does not abort a fetch midway.
func (svc *Service) Webhook(w http.ResponseWriter, r *http.Request) {
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
		web.JSONError(w, http.StatusUnsupportedMediaType, fmt.Errorf("content type %q: configure the webhook with content type application/json", ct))
		return
	}
	if r.ContentLength > MaxWebhookBody {
		web.JSONError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("payload of %d bytes exceeds %d", r.ContentLength, MaxWebhookBody))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxWebhookBody))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			web.JSONError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("payload exceeds %d bytes", MaxWebhookBody))
			return
		}
		web.JSONError(w, http.StatusBadRequest, err)
		return
	}
	event := r.Header.Get(HeaderEvent)
	delivery := r.Header.Get(HeaderDelivery)
	if event == "" {
		web.JSONError(w, http.StatusBadRequest, errors.New("missing "+HeaderEvent))
		return
	}
	env, err := parseEnvelope(body)
	if err != nil {
		web.JSONError(w, http.StatusBadRequest, err)
		return
	}
	owner, name := env.owner(), env.name()
	if owner == "" || name == "" {
		// Installation-level events carry no repository; there is nothing to attach them to.
		web.JSON(w, http.StatusAccepted, map[string]string{"status": "ignored", "reason": "no repository in payload", "event": event})
		return
	}
	links, err := svc.store.linksByFullName(r.Context(), owner, name)
	if err != nil {
		web.JSONError(w, http.StatusInternalServerError, err)
		return
	}
	if len(links) == 0 {
		web.JSONError(w, http.StatusNotFound, fmt.Errorf("no Ore repository is linked to %s/%s", owner, name))
		return
	}
	sig := r.Header.Get(HeaderSignature)
	var verified []Link
	for _, l := range links {
		if VerifySignature(l.Secret, body, sig) {
			verified = append(verified, l)
		}
	}
	if len(verified) == 0 {
		svc.log().Warn("github: webhook signature rejected", "repo", owner+"/"+name, "delivery", delivery, "event", event)
		web.JSONError(w, http.StatusUnauthorized, errors.New("invalid or missing "+HeaderSignature))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	code := http.StatusOK
	var outcomes []DeliveryOutcome
	for _, l := range verified {
		o := svc.handleDelivery(ctx, l, event, delivery, env, body)
		if o.Status == "error" {
			code = http.StatusInternalServerError
		}
		outcomes = append(outcomes, o)
	}
	web.JSON(w, code, map[string]any{"delivery": delivery, "event": event, "outcomes": outcomes})
}

func actorFor(login string) ledger.Actor {
	if login == "" {
		return ledger.Actor{Kind: "app", ID: "github"}
	}
	return ledger.Actor{Kind: "human", ID: "github:" + login}
}

func (svc *Service) handleDelivery(ctx context.Context, link Link, event, delivery string, env envelope, body []byte) DeliveryOutcome {
	repo, err := svc.DB.RepoByID(ctx, link.RepoID)
	if err != nil {
		return DeliveryOutcome{Repo: link.RepoID, Status: "error", Event: event, Error: err.Error()}
	}
	out := DeliveryOutcome{Repo: repo.Org + "/" + repo.Name, Event: event, Action: env.Action}
	m := svc.lock(repo.ID)
	m.Lock()
	defer m.Unlock()
	if delivery != "" {
		if seen, err := svc.store.deliverySeen(ctx, repo.ID, delivery); err != nil {
			out.Status, out.Error = "error", err.Error()
			return out
		} else if seen {
			out.Status = "duplicate"
			return out
		}
	}
	if env.Installation != nil && env.Installation.ID != 0 && link.InstallationID != env.Installation.ID {
		_ = svc.store.setInstallation(ctx, link, env.Installation.ID)
	}
	actor := actorFor(env.Sender.Login)
	ev, err := Translate(event, body)
	if err != nil {
		out.Status, out.Error = "error", err.Error()
		return out
	}
	var extra map[string]any
	switch e := ev.(type) {
	case PingEvent:
		out.Status, out.Detail = "processed", e
		extra = map[string]any{"ping": e}
	case PushEvent:
		res, err := svc.applyPush(ctx, link, repo, e, actor, delivery)
		if err != nil {
			out.Status, out.Error = "error", err.Error()
			return out
		}
		out.Status, out.Updates, out.Detail = "processed", len(res.Updates), res.Updates
	case IssueEvent:
		detail, err := svc.applyIssue(ctx, repo, e, actor)
		if err != nil {
			out.Status, out.Error = "error", err.Error()
			return out
		}
		out.Status, out.Detail = "processed", detail
	case PullRequestEvent:
		detail, err := svc.applyPullRequest(ctx, link, repo, e, actor)
		if err != nil {
			out.Status, out.Error = "error", err.Error()
			return out
		}
		out.Status, out.Detail = "processed", detail
	case IgnoredEvent:
		out.Status, out.Detail = "ignored", e
		extra = map[string]any{"reason": e.Reason}
	default:
		out.Status, out.Error = "error", fmt.Sprintf("unexpected translation %T", ev)
		return out
	}
	if err := svc.store.recordDelivery(ctx, repo, delivery, event, env.Action, out.Status, actor, extra); err != nil {
		out.Status, out.Error = "error", err.Error()
	}
	return out
}
