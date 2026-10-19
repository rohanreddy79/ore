// SPDX-License-Identifier: AGPL-3.0-only

package webapp

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/web"
)

// cookieName is the cookie the server-rendered pages already honour; the app
// reuses it so both UIs share one sign-in.
const cookieName = "ore_token"

// sessionGet answers who the cookie (or bearer token) resolves to.
func sessionGet(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error {
	return web.JSON(w, 200, map[string]any{"actor": actor})
}

// sessionCreate validates a token pasted into the sign-in form by asking the
// server's own authenticator (the identity extension when present), then sets
// the cookie. The token never appears in a URL.
func sessionCreate(s *web.Server, w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if err := web.Decode(r, &in); err != nil || strings.TrimSpace(in.Token) == "" {
		web.JSONError(w, http.StatusBadRequest, errors.New("token is required"))
		return
	}
	probe := r.Clone(r.Context())
	probe.Header = http.Header{}
	probe.Header.Set("Authorization", "Bearer "+strings.TrimSpace(in.Token))
	rec := discard{}
	actor := s.RequireActor(rec, probe)
	if actor == nil {
		web.JSONError(w, http.StatusUnauthorized, errors.New("that token is not valid"))
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: strings.TrimSpace(in.Token), Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil, MaxAge: int((30 * 24 * time.Hour).Seconds()),
	})
	web.JSON(w, 200, map[string]any{"actor": actor})
}

// sessionDelete clears the cookie.
func sessionDelete(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

// discard is a ResponseWriter for probing the authenticator without writing
// its 401 to the real response.
type discard struct{}

func (discard) Header() http.Header         { return http.Header{} }
func (discard) Write(b []byte) (int, error) { return len(b), nil }
func (discard) WriteHeader(int)             {}
