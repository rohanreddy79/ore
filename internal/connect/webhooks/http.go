// SPDX-License-Identifier: AGPL-3.0-only

package webhooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/web"
)

// RotationGrace is how long a retiring secret keeps signing after a rotation.
const RotationGrace = 24 * time.Hour

// Service ties the store, the dispatcher and the exporter to the HTTP API.
type Service struct {
	Store      *Store
	Dispatcher *Dispatcher
	Exporter   *Exporter
	Web        *web.Server

	stop func()
	done chan struct{}
}

var errBadRequest = errors.New("bad request")

func bad(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errBadRequest, fmt.Sprintf(format, a...))
}

func status(err error) int {
	switch {
	case errors.Is(err, ledger.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, errBadRequest):
		return http.StatusBadRequest
	case errors.Is(err, ErrDisabled):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// endpointInput is the create/update body.
type endpointInput struct {
	Org         string   `json:"org,omitempty"`
	Repo        string   `json:"repo,omitempty"`
	URL         *string  `json:"url,omitempty"`
	Description *string  `json:"description,omitempty"`
	Types       []string `json:"types,omitempty"`
	Subject     *string  `json:"subject,omitempty"`
	ActorKinds  []string `json:"actor_kinds,omitempty"`
	Expr        *string  `json:"expr,omitempty"`
	Payload     *string  `json:"payload,omitempty"`
	Enabled     *bool    `json:"enabled,omitempty"`
}

// Routes mounts the API under /api/v0/webhooks/.
func (svc *Service) Routes(s *web.Server) func(mux *http.ServeMux) {
	api := func(mux *http.ServeMux, pattern string, fn func(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			actor := s.RequireActor(w, r)
			if actor == nil {
				return
			}
			if err := fn(w, r, *actor); err != nil {
				web.JSONError(w, status(err), err)
			}
		})
	}
	return func(mux *http.ServeMux) {
		api(mux, "POST /api/v0/webhooks/endpoints", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			var in endpointInput
			if err := web.Decode(r, &in); err != nil {
				return bad("%v", err)
			}
			return svc.create(w, r, a, in, "", "")
		})
		api(mux, "POST /api/v0/webhooks/repos/{org}/{name}/endpoints", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			var in endpointInput
			if err := web.Decode(r, &in); err != nil {
				return bad("%v", err)
			}
			return svc.create(w, r, a, in, r.PathValue("org"), r.PathValue("name"))
		})
		api(mux, "GET /api/v0/webhooks/endpoints", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			orgID, repoID := "", ""
			if o := r.URL.Query().Get("org"); o != "" {
				org, err := s.DB.OrgByName(r.Context(), o)
				if err != nil {
					return err
				}
				orgID = org.ID
			}
			if rp := r.URL.Query().Get("repo"); rp != "" {
				org, name, _ := strings.Cut(rp, "/")
				repo, err := s.DB.RepoByName(r.Context(), org, name)
				if err != nil {
					return err
				}
				orgID, repoID = repo.OrgID, repo.ID
			}
			eps, err := svc.Store.Endpoints(r.Context(), orgID, repoID, false)
			if err != nil {
				return err
			}
			out := make([]Endpoint, 0, len(eps))
			for _, e := range eps {
				out = append(out, e.Public())
			}
			return web.JSON(w, http.StatusOK, out)
		})
		api(mux, "GET /api/v0/webhooks/endpoints/{id}", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			ep, err := svc.Store.Endpoint(r.Context(), r.PathValue("id"))
			if err != nil {
				return err
			}
			return web.JSON(w, http.StatusOK, ep.Public())
		})
		api(mux, "PATCH /api/v0/webhooks/endpoints/{id}", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			ep, err := svc.Store.Endpoint(r.Context(), r.PathValue("id"))
			if err != nil {
				return err
			}
			var in endpointInput
			if err := web.Decode(r, &in); err != nil {
				return bad("%v", err)
			}
			if in.Org != "" || in.Repo != "" {
				return bad("org and repo cannot change; create a new endpoint")
			}
			if err := applyInput(&ep, in, r, svc.Dispatcher.AllowPrivate); err != nil {
				return err
			}
			if err := svc.Store.UpdateEndpoint(r.Context(), &ep, EventEndpointUpdated, a, nil); err != nil {
				return err
			}
			if ep.Enabled {
				svc.Dispatcher.Wake(ep.ID)
			}
			return web.JSON(w, http.StatusOK, ep.Public())
		})
		api(mux, "DELETE /api/v0/webhooks/endpoints/{id}", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			ep, err := svc.Store.Endpoint(r.Context(), r.PathValue("id"))
			if err != nil {
				return err
			}
			if err := svc.Store.DeleteEndpoint(r.Context(), ep, a); err != nil {
				return err
			}
			return web.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		})
		api(mux, "POST /api/v0/webhooks/endpoints/{id}/test", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			ep, err := svc.Store.Endpoint(r.Context(), r.PathValue("id"))
			if err != nil {
				return err
			}
			now := stamp(svc.Dispatcher.now())
			del := Delivery{ID: ledger.NewID("whd"), OrgID: ep.OrgID, EndpointID: ep.ID, EventID: ledger.NewID("evt"), Kind: KindPing, Status: StatusPending, CreatedAt: now, UpdatedAt: now, Attempts: []Attempt{}}
			if err := svc.Store.InsertDelivery(r.Context(), &del); err != nil {
				return err
			}
			svc.Dispatcher.Send(r.Context(), &ep, &del)
			del.NextAttemptAt = nil // a ping is not retried by the queue
			if del.Status == StatusPending {
				del.Status = StatusFailed
				svc.Store.UpdateDelivery(r.Context(), &del)
			}
			return web.JSON(w, http.StatusOK, map[string]any{"delivery": del, "endpoint": ep.Public()})
		})
		api(mux, "GET /api/v0/webhooks/endpoints/{id}/deliveries", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			ep, err := svc.Store.Endpoint(r.Context(), r.PathValue("id"))
			if err != nil {
				return err
			}
			q := r.URL.Query()
			after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
			limit, _ := strconv.Atoi(q.Get("limit"))
			if st := q.Get("status"); st != "" && st != StatusPending && st != StatusSucceeded && st != StatusFailed && st != StatusExhausted {
				return bad("status must be pending, succeeded, failed or exhausted")
			}
			list, err := svc.Store.Deliveries(r.Context(), ep.ID, DeliveriesOptions{Status: q.Get("status"), AfterSeq: after, Limit: limit})
			if err != nil {
				return err
			}
			return web.JSON(w, http.StatusOK, list)
		})
		api(mux, "GET /api/v0/webhooks/endpoints/{id}/health", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			ep, err := svc.Store.Endpoint(r.Context(), r.PathValue("id"))
			if err != nil {
				return err
			}
			h, err := svc.Dispatcher.EndpointHealth(r.Context(), ep)
			if err != nil {
				return err
			}
			return web.JSON(w, http.StatusOK, h)
		})
		api(mux, "POST /api/v0/webhooks/endpoints/{id}/replay", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			ep, err := svc.Store.Endpoint(r.Context(), r.PathValue("id"))
			if err != nil {
				return err
			}
			var in struct {
				FromSeq *int64 `json:"from_seq"`
				ToSeq   int64  `json:"to_seq"`
				Since   string `json:"since"`
			}
			if err := web.Decode(r, &in); err != nil {
				return bad("%v", err)
			}
			from := int64(-1)
			switch {
			case in.FromSeq != nil:
				from = *in.FromSeq
			case in.Since != "":
				t, err := time.Parse(time.RFC3339, in.Since)
				if err != nil {
					return bad("since must be RFC 3339")
				}
				seq, err := svc.Store.SeqAt(r.Context(), stamp(t))
				if errors.Is(err, ledger.ErrNotFound) {
					return web.JSON(w, http.StatusOK, map[string]any{"queued": 0, "from_seq": nil, "to_seq": in.ToSeq})
				}
				if err != nil {
					return err
				}
				from = seq - 1
			default:
				return bad("from_seq or since is required")
			}
			if from < 0 {
				return bad("from_seq must be >= 0")
			}
			n, err := svc.Dispatcher.Replay(r.Context(), ep, from, in.ToSeq, a)
			if err != nil {
				return err
			}
			return web.JSON(w, http.StatusOK, map[string]any{"queued": n, "from_seq": from, "to_seq": in.ToSeq})
		})
		api(mux, "POST /api/v0/webhooks/endpoints/{id}/rotate", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			ep, err := svc.Store.Endpoint(r.Context(), r.PathValue("id"))
			if err != nil {
				return err
			}
			sec, retiring := svc.rotate(&ep)
			data := map[string]any{"endpoint_id": ep.ID, "secret_id": sec.ID, "retiring_secret_id": retiring, "grace": RotationGrace.String()}
			if err := svc.Store.UpdateEndpoint(r.Context(), &ep, EventSecretRotated, a, data); err != nil {
				return err
			}
			return web.JSON(w, http.StatusOK, map[string]any{"endpoint": ep.Public(), "secret": sec})
		})
		api(mux, "GET /api/v0/webhooks/deliveries/{id}", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			del, err := svc.Store.Delivery(r.Context(), r.PathValue("id"))
			if err != nil {
				return err
			}
			ep, err := svc.Store.Endpoint(r.Context(), del.EndpointID)
			if err != nil {
				return err
			}
			out := map[string]any{"delivery": del, "endpoint_id": ep.ID}
			if ev, err := svc.Dispatcher.eventFor(r.Context(), ep, del); err == nil {
				body, thin := svc.Dispatcher.Body(ep, ev, svc.Dispatcher.now())
				out["event"], out["payload"], out["thin"] = ev, json.RawMessage(body), thin
			}
			return web.JSON(w, http.StatusOK, out)
		})
		api(mux, "POST /api/v0/webhooks/deliveries/{id}/redeliver", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			orig, err := svc.Store.Delivery(r.Context(), r.PathValue("id"))
			if err != nil {
				return err
			}
			ep, err := svc.Store.Endpoint(r.Context(), orig.EndpointID)
			if err != nil {
				return err
			}
			if !ep.Enabled {
				return ErrDisabled
			}
			now := stamp(svc.Dispatcher.now())
			del := Delivery{ID: ledger.NewID("whd"), OrgID: ep.OrgID, EndpointID: ep.ID, EventSeq: orig.EventSeq, EventID: orig.EventID, Kind: KindRedeliver, Status: StatusPending, CreatedAt: now, UpdatedAt: now, Attempts: []Attempt{}}
			if orig.Kind == KindPing {
				del.Kind = KindPing
			}
			if err := svc.Store.InsertDelivery(r.Context(), &del); err != nil {
				return err
			}
			svc.Dispatcher.Send(r.Context(), &ep, &del)
			return web.JSON(w, http.StatusOK, map[string]any{"delivery": del, "redelivery_of": orig.ID})
		})
		// the signed link carried by thin payloads: no bearer token, the signature is the credential
		mux.HandleFunc("GET /api/v0/webhooks/events/{id}", func(w http.ResponseWriter, r *http.Request) {
			ep, err := svc.Store.Endpoint(r.Context(), r.URL.Query().Get("ep"))
			if err != nil {
				web.JSONError(w, http.StatusUnauthorized, errors.New("bad link"))
				return
			}
			if err := svc.Dispatcher.VerifyURL(ep, r.PathValue("id"), r.URL.Query()); err != nil {
				web.JSONError(w, http.StatusUnauthorized, err)
				return
			}
			ev, err := svc.Store.EventByID(r.Context(), r.PathValue("id"))
			if err != nil {
				web.JSONError(w, status(err), err)
				return
			}
			w.Header().Set("Content-Type", "application/cloudevents+json; charset=utf-8")
			json.NewEncoder(w).Encode(envelope(ev, false, ""))
		})
		// audit export
		api(mux, "GET /api/v0/webhooks/audit", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			st, err := svc.Exporter.Status(r.Context())
			if err != nil {
				return err
			}
			return web.JSON(w, http.StatusOK, st)
		})
		api(mux, "POST /api/v0/webhooks/audit/exports", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			var in struct {
				Kind     string   `json:"kind"`
				Org      string   `json:"org"`
				Types    []string `json:"types"`
				Dir      string   `json:"dir"`
				Bucket   string   `json:"bucket"`
				Endpoint string   `json:"endpoint"`
				Region   string   `json:"region"`
				Prefix   string   `json:"prefix"`
				FromSeq  int64    `json:"from_seq"`
			}
			if err := web.Decode(r, &in); err != nil {
				return bad("%v", err)
			}
			orgID := ""
			if in.Org != "" {
				org, err := s.DB.OrgByName(r.Context(), in.Org)
				if err != nil {
					return err
				}
				orgID = org.ID
			}
			var cfg any
			switch in.Kind {
			case "file":
				if in.Dir == "" {
					return bad("dir is required for a file export")
				}
				cfg = FileConfig{Dir: in.Dir}
			case "s3":
				if in.Bucket == "" {
					return bad("bucket is required for an s3 export")
				}
				cfg = S3Config{Bucket: in.Bucket, Endpoint: in.Endpoint, Region: in.Region, Prefix: in.Prefix}
			default:
				return bad("kind must be file or s3")
			}
			a2, err := svc.CreateExport(r, orgID, in.Kind, cfg, in.Types, in.FromSeq, a)
			if err != nil {
				return err
			}
			return web.JSON(w, http.StatusCreated, a2)
		})
		api(mux, "DELETE /api/v0/webhooks/audit/exports/{id}", func(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
			ex, err := svc.Store.AuditExport(r.Context(), r.PathValue("id"))
			if err != nil {
				return err
			}
			if err := svc.Store.DeleteAuditExport(r.Context(), ex, a); err != nil {
				return err
			}
			return web.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		})
	}
}

// CreateExport stores an audit export starting after fromSeq.
func (svc *Service) CreateExport(r *http.Request, orgID, kind string, cfg any, types []string, fromSeq int64, actor ledger.Actor) (AuditExport, error) {
	raw, _ := json.Marshal(cfg)
	now := ledger.Now()
	a := AuditExport{ID: ledger.NewID("aexp"), OrgID: orgID, Kind: kind, Config: raw, FilterTypes: nonNil(types), Cursor: fromSeq, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := svc.Store.InsertAuditExport(r.Context(), &a, actor); err != nil {
		return a, err
	}
	return a, nil
}

func (svc *Service) create(w http.ResponseWriter, r *http.Request, a ledger.Actor, in endpointInput, orgName, repoName string) error {
	if repoName == "" && in.Repo != "" {
		orgName, repoName, _ = strings.Cut(in.Repo, "/")
	}
	if orgName == "" {
		orgName = in.Org
	}
	if orgName == "" {
		return bad("org (name) or repo (org/name) is required")
	}
	ep := Endpoint{ID: ledger.NewID("whep"), Payload: "thin", Enabled: true, CreatedBy: a, Secrets: []Secret{}}
	if repoName != "" {
		repo, err := svc.Web.DB.RepoByName(r.Context(), orgName, repoName)
		if err != nil {
			return err
		}
		ep.OrgID, ep.Org, ep.RepoID, ep.Repo = repo.OrgID, repo.Org, &repo.ID, repo.Org+"/"+repo.Name
	} else {
		org, err := svc.Web.DB.OrgByName(r.Context(), orgName)
		if err != nil {
			return err
		}
		ep.OrgID, ep.Org = org.ID, org.Name
	}
	if in.URL == nil || *in.URL == "" {
		return bad("url is required")
	}
	if err := applyInput(&ep, in, r, svc.Dispatcher.AllowPrivate); err != nil {
		return err
	}
	head, err := svc.Store.HeadSeq(r.Context())
	if err != nil {
		return err
	}
	ep.Cursor = head // an endpoint receives what happens after it was created
	sec, _ := svc.rotate(&ep)
	ep.CreatedAt, ep.UpdatedAt = ledger.Now(), ledger.Now()
	if err := svc.Store.InsertEndpoint(r.Context(), &ep); err != nil {
		return err
	}
	svc.Dispatcher.Wake(ep.ID)
	return web.JSON(w, http.StatusCreated, map[string]any{"endpoint": ep.Public(), "secret": sec})
}

// applyInput validates and applies the mutable fields of an endpoint.
func applyInput(ep *Endpoint, in endpointInput, r *http.Request, allowPrivate bool) error {
	if in.URL != nil {
		if err := CheckAddress(r.Context(), *in.URL, allowPrivate); err != nil {
			return bad("url: %v", err)
		}
		ep.URL = *in.URL
	}
	if in.Description != nil {
		ep.Description = *in.Description
	}
	if in.Types != nil {
		ep.Filter.Types = in.Types
	}
	if in.Subject != nil {
		ep.Filter.Subject = *in.Subject
	}
	if in.ActorKinds != nil {
		ep.Filter.ActorKinds = in.ActorKinds
	}
	if in.Expr != nil {
		ep.Filter.Expr = *in.Expr
	}
	if err := ep.Filter.Validate(); err != nil {
		return bad("filter: %v", err)
	}
	if in.Payload != nil {
		if *in.Payload != "thin" && *in.Payload != "full" {
			return bad("payload must be thin or full")
		}
		ep.Payload = *in.Payload
	}
	if in.Enabled != nil {
		ep.Enabled = *in.Enabled
		if ep.Enabled {
			ep.DisabledReason = ""
		} else if ep.DisabledReason == "" {
			ep.DisabledReason = "disabled by " + "request"
		}
	}
	return nil
}

// rotate adds a new secret and gives the current one RotationGrace to retire;
// secrets past their expiry are dropped. Returns the new secret and the
// retiring secret's id.
func (svc *Service) rotate(ep *Endpoint) (Secret, string) {
	now := svc.Dispatcher.now()
	var kept []Secret
	retiring := ""
	for _, s := range ep.Secrets {
		if s.ExpiresAt != nil && !parseStamp(*s.ExpiresAt).After(now) {
			continue
		}
		if s.ExpiresAt == nil {
			exp := stamp(now.Add(RotationGrace))
			s.ExpiresAt = &exp
			retiring = s.ID
		}
		kept = append(kept, s)
	}
	sec := Secret{ID: ledger.NewID("whs"), Value: NewSecret(), CreatedAt: stamp(now)}
	ep.Secrets = append(kept, sec)
	return sec, retiring
}
