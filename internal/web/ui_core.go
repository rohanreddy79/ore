// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/orevcs/ore/internal/delivery"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/review"
)

// The UI (ui_*.go) is server-rendered Go templates with a little inline
// JavaScript for live updates, no external assets. Every page needs a
// signed-in actor (a redirect to /login otherwise), every form carries a
// double-submit CSRF token, and every write goes through the same services
// or API routes the CLI and the agents use.

const (
	tokenCookie = "ore_token"
	csrfCookie  = "ore_csrf"
	perPage     = 50
)

// uiHandler is a page or form handler: it renders or redirects, or returns
// an error the wrapper turns into an error page (GET) or a flash (POST).
type uiHandler func(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error

// page mounts a UI route behind authentication and, for writes, the CSRF check.
func (s *Server) page(mux *http.ServeMux, pattern string, fn uiHandler) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		actor := s.authenticate(r)
		if actor == nil {
			next := r.URL.RequestURI()
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				next = r.Referer()
				if u, err := url.Parse(next); err == nil {
					next = u.RequestURI()
				}
			}
			http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusSeeOther)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.csrfOK(r) {
			s.errorPage(w, r, actor, http.StatusForbidden, "The form's security token is missing or stale. Reload the page and try again.")
			return
		}
		if err := fn(w, r, *actor); err != nil {
			code := uiStatus(err)
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				s.errorPage(w, r, actor, code, err.Error())
				return
			}
			s.flashBack(w, r, "", err.Error())
		}
	})
}

// uiStatus maps service errors to HTTP statuses for error pages.
func uiStatus(err error) int {
	var pe *review.PolicyError
	var le *review.LandingError
	var ce *review.ConflictError
	var de *delivery.ConflictError
	var fe *delivery.FailedError
	var re *delivery.RefusedError
	var te *delivery.TransitionError
	switch {
	case errors.Is(err, ledger.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, errBadRequest), errors.Is(err, review.ErrBadRequest), errors.Is(err, delivery.ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, delivery.ErrForbidden), errors.As(err, &pe), errors.As(err, &re):
		return http.StatusForbidden
	case errors.As(err, &le), errors.As(err, &ce), errors.As(err, &de), errors.As(err, &fe), errors.As(err, &te),
		errors.Is(err, review.ErrNothingToMerge), errors.Is(err, review.ErrNotOpen), errors.Is(err, review.ErrSourceGone),
		errors.Is(err, review.ErrVerificationFailed), errors.Is(err, review.ErrReverted), errors.Is(err, review.ErrNoVerifier):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// ---- CSRF: a double-submit token in a cookie and a hidden form field ----

func (s *Server) csrfToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) == 32 {
		return c.Value
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	tok := hex.EncodeToString(b[:])
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
	return tok
}

func (s *Server) csrfOK(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil || len(c.Value) != 32 {
		return false
	}
	got := r.FormValue("csrf")
	return len(got) == 32 && subtle.ConstantTimeCompare([]byte(got), []byte(c.Value)) == 1
}

// ---- rendering ----

// render executes a template with the common keys filled in.
func (s *Server) render(w http.ResponseWriter, r *http.Request, actor *ledger.Actor, code int, name string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["CSRF"] = s.csrfToken(w, r)
	data["Actor"] = actor
	data["Path"] = r.URL.Path
	data["BaseURL"] = s.BaseURL
	data["Features"] = s.featureNames()
	q := r.URL.Query()
	data["Flash"] = q.Get("ok")
	data["FlashErr"] = q.Get("err")
	if _, ok := data["Title"]; !ok {
		data["Title"] = "Ore"
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		if s.Log != nil {
			s.Log.Error("ui: template", "name", name, "err", err)
		}
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.WriteHeader(code)
	w.Write(buf.Bytes())
}

// errorPage renders a titled error with the layout (never a panic, never raw text).
func (s *Server) errorPage(w http.ResponseWriter, r *http.Request, actor *ledger.Actor, code int, msg string) {
	title := http.StatusText(code)
	if code == http.StatusNotFound {
		title = "Not found"
	}
	s.render(w, r, actor, code, "error", map[string]any{"Title": title, "Code": code, "Message": msg})
}

// flashBack redirects to the referer (or the index) with a message.
func (s *Server) flashBack(w http.ResponseWriter, r *http.Request, ok, errMsg string) {
	back := r.Referer()
	if u, err := url.Parse(back); err == nil && u.Path != "" {
		back = u.Path
	} else {
		back = "/"
	}
	s.redirectFlash(w, r, back, ok, errMsg)
}

// redirectFlash redirects to a local path carrying a flash in the query.
func (s *Server) redirectFlash(w http.ResponseWriter, r *http.Request, path, ok, errMsg string) {
	u, err := url.Parse(path)
	if err != nil || strings.HasPrefix(path, "//") {
		u = &url.URL{Path: "/"}
	}
	q := u.Query()
	q.Del("ok")
	q.Del("err")
	if ok != "" {
		q.Set("ok", ok)
	}
	if errMsg != "" {
		q.Set("err", errMsg)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

// localPath keeps redirects on this host.
func localPath(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return "/"
	}
	return p
}

func (s *Server) featureNames() []string {
	names := make([]string, 0, len(s.Services))
	for n := range s.Services {
		names = append(names, n)
	}
	return names
}

// has reports whether an extension attached a service under name.
func (s *Server) has(name string) bool { _, ok := s.Services[name]; return ok }

// ---- login ----

func (s *Server) mountAuth(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		next := localPath(r.URL.Query().Get("next"))
		if tok := r.URL.Query().Get("token"); tok != "" { // the v0 flow: /login?token=...
			if s.authenticate(r) == nil {
				s.render(w, r, nil, http.StatusUnauthorized, "login", map[string]any{"Title": "Sign in", "Next": next, "Error": "That token is not valid."})
				return
			}
			s.setToken(w, tok)
			http.Redirect(w, r, next, http.StatusFound)
			return
		}
		actor := s.authenticate(r)
		s.render(w, r, actor, http.StatusOK, "login", map[string]any{"Title": "Sign in", "Next": next})
	})
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		if !s.csrfOK(r) {
			s.errorPage(w, r, nil, http.StatusForbidden, "The form's security token is missing or stale. Reload the page and try again.")
			return
		}
		next := localPath(r.FormValue("next"))
		tok := strings.TrimSpace(r.FormValue("token"))
		probe := r.Clone(r.Context())
		probe.Header = http.Header{"Authorization": {"Bearer " + tok}}
		if tok == "" || s.authenticate(probe) == nil {
			s.render(w, r, nil, http.StatusUnauthorized, "login", map[string]any{"Title": "Sign in", "Next": next, "Error": "That token is not valid."})
			return
		}
		s.setToken(w, tok)
		http.Redirect(w, r, next, http.StatusSeeOther)
	})
	mux.HandleFunc("POST /logout", func(w http.ResponseWriter, r *http.Request) {
		if !s.csrfOK(r) {
			s.errorPage(w, r, nil, http.StatusForbidden, "The form's security token is missing or stale.")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: tokenCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

func (s *Server) setToken(w http.ResponseWriter, tok string) {
	http.SetCookie(w, &http.Cookie{Name: tokenCookie, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

// ---- in-process dispatch to the API ----

// recorder captures an internal API response.
type recorder struct {
	hdr  http.Header
	code int
	body bytes.Buffer
}

func (rec *recorder) Header() http.Header { return rec.hdr }
func (rec *recorder) Write(b []byte) (int, error) {
	if rec.code == 0 {
		rec.code = http.StatusOK
	}
	return rec.body.Write(b)
}
func (rec *recorder) WriteHeader(code int) { rec.code = code }

// apiResult is what an internal call returned.
type apiResult struct {
	Code int
	Body []byte
}

// Err returns the API's error message when the call failed.
func (a apiResult) Err() error {
	if a.Code < 400 {
		return nil
	}
	var e struct{ Error string }
	if json.Unmarshal(a.Body, &e) == nil && e.Error != "" {
		return fmt.Errorf("%s", e.Error)
	}
	return fmt.Errorf("HTTP %d", a.Code)
}

// OK reports a 2xx answer.
func (a apiResult) OK() bool { return a.Code >= 200 && a.Code < 300 }

// Decode unmarshals a successful body.
func (a apiResult) Decode(v any) error {
	if err := a.Err(); err != nil {
		return err
	}
	return json.Unmarshal(a.Body, v)
}

// api calls one of the server's own API routes on behalf of the request's
// principal (the credential headers are forwarded), without a network hop.
// Slices that import package web cannot be called directly from here, so
// their routes are the contract.
func (s *Server) api(r *http.Request, method, path string, body any) apiResult {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return apiResult{Code: http.StatusInternalServerError, Body: []byte(`{"error":"` + err.Error() + `"}`)}
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(r.Context(), method, path, rd)
	if err != nil {
		return apiResult{Code: http.StatusInternalServerError, Body: []byte(`{"error":"` + err.Error() + `"}`)}
	}
	if h := r.Header.Get("Authorization"); h != "" {
		req.Header.Set("Authorization", h)
	}
	if h := r.Header.Get("Cookie"); h != "" {
		req.Header.Set("Cookie", h)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.root == nil {
		return apiResult{Code: http.StatusServiceUnavailable, Body: []byte(`{"error":"router not built"}`)}
	}
	rec := &recorder{hdr: http.Header{}}
	s.root.ServeHTTP(rec, req)
	if rec.code == 0 {
		rec.code = http.StatusOK
	}
	return apiResult{Code: rec.code, Body: rec.body.Bytes()}
}

// available reports whether a route exists (an extension mounted it): a
// GET that answers 404 with the mux's plain-text body means no route.
func (s *Server) available(r *http.Request, path string) bool {
	res := s.api(r, http.MethodGet, path, nil)
	return res.Code != http.StatusNotFound || json.Valid(res.Body)
}

// ---- pagination ----

// pager describes one page of a list.
type pager struct {
	Page, Per, Total, Pages int
	From, To                int
	Prev, Next              string
	HasPrev, HasNext        bool
}

// paginate slices [0,total) by the request's page and per parameters.
func paginate(r *http.Request, total int) (pager, int, int) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	per, _ := strconv.Atoi(q.Get("per"))
	if per < 1 || per > 500 {
		per = perPage
	}
	pages := (total + per - 1) / per
	if pages == 0 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	lo := (page - 1) * per
	hi := lo + per
	if hi > total {
		hi = total
	}
	p := pager{Page: page, Per: per, Total: total, Pages: pages, From: lo + 1, To: hi}
	if total == 0 {
		p.From = 0
	}
	link := func(n int) string {
		q2 := url.Values{}
		for k, v := range q {
			q2[k] = v
		}
		q2.Set("page", strconv.Itoa(n))
		return r.URL.Path + "?" + q2.Encode()
	}
	if page > 1 {
		p.HasPrev, p.Prev = true, link(page-1)
	}
	if page < pages {
		p.HasNext, p.Next = true, link(page+1)
	}
	return p, lo, hi
}

// ---- small helpers shared by the pages ----

func (s *Server) repoFor(r *http.Request) (ledger.Repo, error) {
	return s.DB.RepoByName(r.Context(), r.PathValue("org"), r.PathValue("name"))
}

func repoPath(repo ledger.Repo) string { return "/" + repo.Org + "/" + repo.Name }

// delivery returns the environments service when its extension is attached.
func (s *Server) delivery() *delivery.Service {
	if s.Deployer != nil && s.Deployer.Environments != nil {
		return s.Deployer.Environments
	}
	if svc, ok := s.Services["delivery.environments"].(*delivery.Service); ok {
		return svc
	}
	return nil
}

// eventsBefore reads events newest first with filters, the ledger page's query.
type eventFilter struct {
	Type, Subject, SubjectPrefix, ActorKind, Org string
	Before, After                                int64
	Limit                                        int
}

func (s *Server) eventsPage(ctx context.Context, f eventFilter) ([]ledger.Event, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = perPage
	}
	q := `SELECT seq, id, org_id, type, subject, time, actor_kind, actor_id, data FROM events WHERE 1=1`
	var args []any
	if f.Before > 0 {
		q += ` AND seq < ?`
		args = append(args, f.Before)
	}
	if f.After > 0 {
		q += ` AND seq > ?`
		args = append(args, f.After)
	}
	if f.Type != "" {
		if strings.HasSuffix(f.Type, ".") {
			q += ` AND type LIKE ?`
			args = append(args, f.Type+"%")
		} else {
			q += ` AND type = ?`
			args = append(args, f.Type)
		}
	}
	if f.Subject != "" {
		q += ` AND subject = ?`
		args = append(args, f.Subject)
	}
	if f.SubjectPrefix != "" {
		q += ` AND subject LIKE ? ESCAPE '\'`
		args = append(args, likePrefix(f.SubjectPrefix))
	}
	if f.ActorKind != "" {
		q += ` AND actor_kind = ?`
		args = append(args, f.ActorKind)
	}
	if f.Org != "" {
		q += ` AND org_id = ?`
		args = append(args, f.Org)
	}
	order := " DESC"
	if f.After > 0 && f.Before == 0 {
		order = " ASC"
	}
	q += ` ORDER BY seq` + order + ` LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ledger.Event
	for rows.Next() {
		var ev ledger.Event
		var data string
		if err := rows.Scan(&ev.Seq, &ev.ID, &ev.OreOrg, &ev.Type, &ev.Subject, &ev.Time, &ev.OreActor.Kind, &ev.OreActor.ID, &data); err != nil {
			return nil, err
		}
		ev.SpecVersion, ev.DataContentType, ev.Source = "1.0", "application/json", "ore://"+ev.OreOrg
		ev.Data = json.RawMessage(data)
		out = append(out, ev)
	}
	if order == " ASC" { // newest first on the page
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, rows.Err()
}

func likePrefix(p string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(p) + "%"
}

// latestSeq returns the ledger's head sequence number.
func (s *Server) latestSeq(ctx context.Context) int64 {
	var n int64
	s.DB.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&n)
	return n
}

// tableExists reports whether another slice's table is present (its migration ran).
func (s *Server) tableExists(ctx context.Context, name string) bool {
	var n int
	s.DB.QueryRow(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	return n > 0
}
