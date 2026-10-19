// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/orevcs/ore/internal/ledger"
)

// uiLedger is the ledger page: newest first, filtered by type (exact or a
// prefix ending in "."), subject prefix, actor kind and org, paged by
// sequence cursors, with a replay cursor for the API and a live tail.
func (s *Server) uiLedger(w http.ResponseWriter, r *http.Request, a ledger.Actor) error {
	q := r.URL.Query()
	f := eventFilter{Type: strings.TrimSpace(q.Get("type")), SubjectPrefix: strings.TrimSpace(q.Get("subject")), ActorKind: q.Get("actor_kind"), Limit: perPage}
	if n, _ := strconv.Atoi(q.Get("limit")); n > 0 && n <= 500 {
		f.Limit = n
	}
	orgName := strings.TrimSpace(q.Get("org"))
	if orgName != "" {
		org, err := s.DB.OrgByName(r.Context(), orgName)
		if err != nil {
			return err
		}
		f.Org = org.ID
	}
	f.Before, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	f.After, _ = strconv.ParseInt(q.Get("after"), 10, 64)
	evs, err := s.eventsPage(r.Context(), f)
	if err != nil {
		return err
	}
	var minSeq, maxSeq int64
	for _, ev := range evs {
		if minSeq == 0 || ev.Seq < minSeq {
			minSeq = ev.Seq
		}
		if ev.Seq > maxSeq {
			maxSeq = ev.Seq
		}
	}
	link := func(key string, val int64) string {
		q2 := url.Values{}
		for k, v := range q {
			if k != "before" && k != "after" && k != "ok" && k != "err" {
				q2[k] = v
			}
		}
		q2.Set(key, strconv.FormatInt(val, 10))
		return "/events?" + q2.Encode()
	}
	data := map[string]any{"Title": "Ledger", "Events": evs, "Filter": f, "Org": orgName, "Latest": s.latestSeq(r.Context()), "Kinds": []string{"human", "agent", "app", "system"}}
	if len(evs) == f.Limit && minSeq > 1 {
		data["Older"] = link("before", minSeq)
	}
	if f.Before > 0 || f.After > 0 {
		data["Newer"] = link("after", maxSeq)
		data["Newest"] = "/events?" + stripCursor(q)
	}
	data["ReplayFrom"] = minSeq - 1
	if minSeq == 0 {
		data["ReplayFrom"] = 0
	}
	stream := url.Values{}
	if f.Type != "" {
		stream.Set("type", f.Type)
	}
	if f.SubjectPrefix != "" {
		stream.Set("subject_prefix", f.SubjectPrefix)
	}
	if f.ActorKind != "" {
		stream.Set("actor_kind", f.ActorKind)
	}
	if f.Org != "" {
		stream.Set("org", f.Org)
	}
	data["StreamQuery"] = stream.Encode()
	data["Live"] = f.Before == 0 && f.After == 0
	s.render(w, r, &a, http.StatusOK, "events", data)
	return nil
}

func stripCursor(q url.Values) string {
	q2 := url.Values{}
	for k, v := range q {
		if k != "before" && k != "after" && k != "ok" && k != "err" {
			q2[k] = v
		}
	}
	return q2.Encode()
}

// subjectHref links an event subject to its page when the subject names one.
func subjectHref(subject string) string {
	parts := strings.Split(subject, "/")
	if len(parts) < 2 {
		return ""
	}
	switch parts[0] {
	case "users":
		return "/identity/users"
	case "orgs":
		if len(parts) >= 4 && parts[2] == "agents" {
			return "/identity/agents?org=" + url.QueryEscape(parts[1])
		}
		return "/identity/users"
	case "tokens":
		return "/identity/tokens"
	}
	base := "/" + parts[0] + "/" + parts[1]
	if len(parts) == 2 {
		return base
	}
	if len(parts) >= 4 {
		switch parts[2] {
		case "issues", "changes", "runs", "deployments", "agent", "stacks":
			return base + "/" + parts[2] + "/" + parts[3]
		case "refs":
			return base + "/changes"
		case "gateway":
			return base + "/gateway"
		case "environments":
			return base + "/environments"
		}
	}
	return base
}
