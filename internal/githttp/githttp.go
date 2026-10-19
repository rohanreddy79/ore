// SPDX-License-Identifier: AGPL-3.0-only

// Package githttp serves git's smart HTTP protocol for Ore's repositories
// behind Ore's authentication and policy, and records every ref update on
// the ledger by snapshotting refs around each receive-pack.
//
// It drives `git upload-pack` and `git receive-pack` in --stateless-rpc mode
// directly, streaming the request body to stdin and stdout to the response,
// instead of going through net/http/cgi: the CGI handler rejects chunked
// request bodies, and git uses chunked encoding for any push larger than
// http.postBuffer (1 MiB), so CGI cannot serve real repositories.
package githttp

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// Auth resolves credentials to an actor; nil means unauthenticated.
type Auth func(r *http.Request) *ledger.Actor

// Handler serves /{org}/{repo}.git/... .
type Handler struct {
	DB       *ledger.DB
	ReposDir string
	// Backend is kept for compatibility with older wiring; it is no longer used.
	Backend string
	Auth    Auth
	Log     *slog.Logger
	// Timeout bounds one transport exchange (default 1 hour: large pushes).
	Timeout time.Duration

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// RepoPath returns the on-disk bare repository path.
func RepoPath(reposDir, org, name string) string {
	return filepath.Join(reposDir, org, name+".git")
}

func (h *Handler) lock(key string) *sync.Mutex {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.locks == nil {
		h.locks = map[string]*sync.Mutex{}
	}
	m, ok := h.locks[key]
	if !ok {
		m = &sync.Mutex{}
		h.locks[key] = m
	}
	return m
}

func (h *Handler) logger() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

// ServeHTTP routes a git request. Expected path: /{org}/{repo}.git/{rest}.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(parts) < 3 || !strings.HasSuffix(parts[1], ".git") {
		http.NotFound(w, r)
		return
	}
	org, name, rest := parts[0], strings.TrimSuffix(parts[1], ".git"), parts[2]
	repo, err := h.DB.RepoByName(r.Context(), org, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	actor := h.Auth(r)
	if actor == nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="ore"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	// The organization's git transport mode (ADR 0016 §3) is read after
	// authentication, so an anonymous probe learns nothing from it.
	st, err := h.DB.OrgSettings(r.Context(), ledger.Org{ID: repo.OrgID, Name: repo.Org})
	if err != nil {
		http.Error(w, "organization settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	service := r.URL.Query().Get("service")
	write := rest == "git-receive-pack" || (rest == "info/refs" && service == "git-receive-pack")
	if msg := TransportRefusal(st, repo.Name, write); msg != "" {
		h.refuse(w, r, rest, service, st.GitTransport, msg, *actor)
		return
	}
	repoPath := RepoPath(h.ReposDir, org, name)
	env := []string{
		"GIT_PROTOCOL=" + r.Header.Get("Git-Protocol"),
		"REMOTE_USER=" + actor.ID,
		"ORE_ACTOR_KIND=" + actor.Kind,
		"ORE_ACTOR_ID=" + actor.ID,
		"ORE_PROTECTED_REFS=refs/heads/" + repo.DefaultBranch,
		"ORE_REPO_ID=" + repo.ID,
	}
	switch {
	case rest == "info/refs" && r.Method == http.MethodGet:
		if service != "git-upload-pack" && service != "git-receive-pack" {
			http.Error(w, "smart HTTP only: add ?service=git-upload-pack", http.StatusForbidden)
			return
		}
		h.advertise(w, r, repoPath, service, env)
	case rest == "git-upload-pack" && r.Method == http.MethodPost:
		h.rpc(w, r, repoPath, "upload-pack", env)
	case rest == "git-receive-pack" && r.Method == http.MethodPost:
		// A push: serialize per repository, snapshot refs, serve, snapshot again, record.
		m := h.lock(repo.ID)
		m.Lock()
		defer m.Unlock()
		ctx := context.Background()
		before, err := gitx.Refs(ctx, repoPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		h.rpc(w, r, repoPath, "receive-pack", env)
		after, err := gitx.Refs(ctx, repoPath)
		if err != nil {
			h.logger().Error("refs after push", "err", err)
			return
		}
		for _, u := range gitx.DiffRefs(before, after) {
			if err := h.DB.RecordRefUpdate(ctx, repo, u.Ref, u.Old, u.New, "push", *actor); err != nil {
				h.logger().Error("record ref update", "err", err)
			}
		}
	default:
		http.NotFound(w, r)
	}
}

func pktLine(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

// TransportRefusal is what a git user reads when the organization's git
// transport mode does not allow the request (ADR 0016 §3), or "" when it is
// allowed: the whole bridge is off, or pushes are off in read-only mode. The
// message names the Ore client command that replaces the git one, then the
// reason the owner recorded. Smart HTTP sends it as an ERR packet and SSH
// prints it on stderr, so both transports show the same text.
func TransportRefusal(st ledger.OrgSettings, repo string, write bool) string {
	var msg string
	switch {
	case st.GitTransport == ledger.GitTransportOff:
		msg = fmt.Sprintf("the git transport is off for %s; use the Ore client: ore open %s/%s", st.Org, st.Org, repo)
	case st.GitTransport == ledger.GitTransportReadOnly && write:
		msg = fmt.Sprintf("git pushes are off for %s; save your work with the Ore client: ore open %s/%s, then ore save", st.Org, st.Org, repo)
	default:
		return ""
	}
	if st.GitTransportReason != "" {
		msg += " (" + st.GitTransportReason + ")"
	}
	return msg
}

// refuse answers a request the organization's git transport mode refuses.
// The ref advertisement (GET info/refs) is answered the way git expects a
// smart server to speak: 200, the service header, then an ERR packet, which
// git prints as "fatal: remote error: <message>" over protocol v0 and v2 (an
// error status would show the user nothing but the number). The RPC endpoints, which a client only
// reaches after an advertisement, answer 403 with the message as text.
func (h *Handler) refuse(w http.ResponseWriter, r *http.Request, rest, service, mode, msg string, actor ledger.Actor) {
	h.logger().Info("git transport refused", "mode", mode, "actor", actor.Kind+"/"+actor.ID, "path", r.URL.Path, "reason", msg)
	w.Header().Set("X-Ore-Git-Transport", mode)
	w.Header().Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
	if rest == "info/refs" && r.Method == http.MethodGet && (service == "git-upload-pack" || service == "git-receive-pack") {
		w.Header().Set("Content-Type", "application/x-"+service+"-advertisement")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, pktLine("# service="+service+"\n"))
		io.WriteString(w, "0000")
		io.WriteString(w, pktLine("ERR "+msg+"\n"))
		io.WriteString(w, "0000")
		return
	}
	http.Error(w, "ore: "+msg, http.StatusForbidden)
}

// advertise answers GET info/refs for the smart protocol.
func (h *Handler) advertise(w http.ResponseWriter, r *http.Request, repoPath, service string, env []string) {
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", strings.TrimPrefix(service, "git-"), "--stateless-rpc", "--advertise-refs", repoPath)
	cmd.Env = append(os.Environ(), env...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		h.logger().Error("git advertise", "service", service, "err", err, "stderr", strings.TrimSpace(stderr.String()))
		http.Error(w, "git "+service+" failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-"+service+"-advertisement")
	w.Header().Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
	// Protocol v2 clients expect the capability list without the service header.
	if strings.HasPrefix(r.Header.Get("Git-Protocol"), "version=2") {
		w.Write(out)
		return
	}
	io.WriteString(w, pktLine("# service="+service+"\n"))
	io.WriteString(w, "0000")
	w.Write(out)
}

// rpc answers POST git-upload-pack / git-receive-pack, streaming both ways.
func (h *Handler) rpc(w http.ResponseWriter, r *http.Request, repoPath, service string, env []string) {
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout())
	defer cancel()
	var body io.Reader = r.Body
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, "bad gzip body", http.StatusBadRequest)
			return
		}
		defer gz.Close()
		body = gz
	}
	cmd := exec.CommandContext(ctx, "git", service, "--stateless-rpc", repoPath)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = body
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := cmd.Start(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-git-"+service+"-result")
	w.Header().Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, rerr := stdout.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				cancel()
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		// The client already received git's own error in the sideband; log for operators.
		h.logger().Warn("git "+service, "err", err, "stderr", strings.TrimSpace(stderr.String()))
	}
}

func (h *Handler) timeout() time.Duration {
	if h.Timeout > 0 {
		return h.Timeout
	}
	return time.Hour
}
