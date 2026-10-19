// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/orevcs/ore/internal/fpi"
	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/web"
)

// Service is the GitHub adapter attached to a server.
type Service struct {
	DB       *ledger.DB
	ReposDir string
	BaseURL  string
	// DataDir holds import downloads and the import signing key (the parent of the logs directory).
	DataDir string
	Log     *slog.Logger
	// Client is nil when no token is configured; linking then fails with a clear error.
	Client         *Client
	SyncInterval   time.Duration
	StatusInterval time.Duration

	server *web.Server
	store  store
	now    func() time.Time

	mu      sync.Mutex
	locks   map[string]*sync.Mutex
	running map[string]bool
	// statusDeferUntil holds the status reporter back after a rate-limit response.
	statusDeferUntil time.Time
}

// New builds the adapter for a server from an explicit configuration.
func New(cfg Config, s *web.Server) *Service {
	svc := &Service{DB: s.DB, ReposDir: s.ReposDir, BaseURL: strings.TrimRight(s.BaseURL, "/"), DataDir: dataDir(s), Log: s.Log, Client: NewClient(cfg),
		SyncInterval: cfg.SyncInterval, StatusInterval: cfg.StatusInterval, server: s, store: store{db: s.DB}, now: cfg.Now, locks: map[string]*sync.Mutex{}}
	if svc.now == nil {
		svc.now = time.Now
	}
	if svc.SyncInterval == 0 {
		svc.SyncInterval = DefaultSyncInterval
	}
	if svc.StatusInterval == 0 {
		svc.StatusInterval = DefaultStatusInterval
	}
	return svc
}

func dataDir(s *web.Server) string {
	if s.LogsDir != "" {
		return filepath.Dir(s.LogsDir)
	}
	if s.ReposDir != "" {
		return filepath.Dir(s.ReposDir)
	}
	return "ore-data"
}

// Install is the registry extension: configuration from the environment,
// routes, the FPI providers, and the workers when a token is configured.
func Install(ctx context.Context, s *web.Server) error {
	cfg, err := ConfigFromEnv()
	if err != nil {
		return err
	}
	svc := New(cfg, s)
	svc.Attach()
	if svc.Client == nil {
		svc.log().Info("github adapter: no " + EnvToken + "; linking is disabled until a token is configured")
		return nil
	}
	if svc.SyncInterval > 0 || svc.StatusInterval > 0 {
		go svc.Run(ctx)
	}
	return nil
}

// Attach mounts the routes, publishes the service and registers the FPI
// providers (native and github) without starting workers.
func (svc *Service) Attach() {
	s := svc.server
	if s.Services == nil {
		s.Services = map[string]any{}
	}
	s.Services["fpi.github"] = svc
	fpi.Register(&fpi.Native{DB: s.DB, ReposDir: s.ReposDir, BaseURL: svc.BaseURL})
	fpi.Register(&Provider{svc: svc})
	s.AddRoutes(svc.Routes)
	// The status reporter starts from now: historical runs are not reported.
	if _, ok, err := svc.store.cursor(context.Background(), statusCursor); err == nil && !ok {
		if seq, err := svc.store.maxEventSeq(context.Background()); err == nil {
			_ = svc.store.setCursor(context.Background(), statusCursor, seq)
		}
	}
}

// Provider returns the FPI provider view of the service.
func (svc *Service) Provider() fpi.Provider { return &Provider{svc: svc} }

func (svc *Service) log() *slog.Logger {
	if svc.Log == nil {
		return slog.Default()
	}
	return svc.Log
}

func (svc *Service) lock(repoID string) *sync.Mutex {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	m, ok := svc.locks[repoID]
	if !ok {
		m = &sync.Mutex{}
		svc.locks[repoID] = m
	}
	return m
}

// Routes mounts the adapter's endpoints.
func (svc *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /hooks/github", svc.Webhook)
	mux.HandleFunc("GET /api/v0/github/links", svc.api(svc.handleList))
	mux.HandleFunc("GET /api/v0/repos/{org}/{name}/github/link", svc.api(svc.handleGet))
	mux.HandleFunc("POST /api/v0/repos/{org}/{name}/github/link", svc.api(svc.handleLink))
	mux.HandleFunc("DELETE /api/v0/repos/{org}/{name}/github/link", svc.api(svc.handleUnlink))
	mux.HandleFunc("POST /api/v0/repos/{org}/{name}/github/sync", svc.api(svc.handleSync))
	svc.importRoutes(mux)
}

var errBadRequest = errors.New("bad request")

func (svc *Service) api(fn func(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := svc.server.RequireActor(w, r)
		if actor == nil {
			return
		}
		if err := fn(w, r, *actor); err != nil {
			var apiErr *APIError
			var rl *RateLimitError
			code := http.StatusInternalServerError
			switch {
			case errors.Is(err, ledger.ErrNotFound):
				code = http.StatusNotFound
			case errors.Is(err, errBadRequest):
				code = http.StatusBadRequest
			case errors.Is(err, errAlreadyLinked):
				code = http.StatusConflict
			case errors.Is(err, ErrNoToken):
				code = http.StatusServiceUnavailable
			case errors.As(err, &rl):
				code = http.StatusServiceUnavailable
				w.Header().Set("Retry-After", fmt.Sprint(int(rl.RetryAfter.Seconds())))
			case errors.As(err, &apiErr):
				code = http.StatusBadGateway
				if apiErr.Status == http.StatusNotFound {
					code = http.StatusNotFound
				}
			}
			web.JSONError(w, code, err)
		}
	}
}

func (svc *Service) repo(r *http.Request) (ledger.Repo, error) {
	return svc.DB.RepoByName(r.Context(), r.PathValue("org"), r.PathValue("name"))
}

// LinkRequest is the body of POST /api/v0/repos/{org}/{name}/github/link.
type LinkRequest struct {
	Owner          string `json:"owner"`
	Name           string `json:"name"`
	Repository     string `json:"repository,omitempty"` // "owner/name", alternative to owner+name
	Secret         string `json:"secret,omitempty"`     // webhook secret; generated when empty
	InstallationID int64  `json:"installation_id,omitempty"`
}

// LinkResponse is returned by link and get.
type LinkResponse struct {
	Link       Link         `json:"link"`
	WebhookURL string       `json:"webhook_url"`
	Mirror     MirrorResult `json:"mirror"`
}

func newSecret() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func (svc *Service) webhookURL() string { return svc.BaseURL + "/hooks/github" }

// Link links an Ore repository to owner/name and performs the initial mirror.
func (svc *Service) Link(ctx context.Context, repo ledger.Repo, req LinkRequest, actor ledger.Actor) (LinkResponse, error) {
	if req.Repository != "" && (req.Owner == "" || req.Name == "") {
		req.Owner, req.Name, _ = strings.Cut(req.Repository, "/")
	}
	req.Owner, req.Name = strings.TrimSpace(req.Owner), strings.TrimSuffix(strings.TrimSpace(req.Name), ".git")
	if req.Owner == "" || req.Name == "" || strings.ContainsAny(req.Owner+req.Name, "/ \t\n") {
		return LinkResponse{}, fmt.Errorf("%w: owner and name (or repository \"owner/name\") are required", errBadRequest)
	}
	if svc.Client == nil {
		return LinkResponse{}, ErrNoToken
	}
	if _, err := svc.store.linkByRepo(ctx, repo.ID); err == nil {
		return LinkResponse{}, errAlreadyLinked
	}
	gh, err := svc.Client.Repo(ctx, req.Owner, req.Name)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return LinkResponse{}, fmt.Errorf("github: %s/%s not found (or the token cannot see it): %w", req.Owner, req.Name, ledger.ErrNotFound)
		}
		return LinkResponse{}, err
	}
	if gh.CloneURL == "" {
		return LinkResponse{}, fmt.Errorf("github: %s/%s: the API returned no clone_url", req.Owner, req.Name)
	}
	secret := req.Secret
	if secret == "" {
		secret = newSecret()
	}
	link := Link{Owner: gh.Owner, Name: gh.Name, GitHubID: atoi64(gh.ID), Secret: secret, InstallationID: req.InstallationID, CloneURL: gh.CloneURL, HTMLURL: gh.HTMLURL, DefaultBranch: gh.DefaultBranch}
	newDefault := ""
	if gh.DefaultBranch != "" && gh.DefaultBranch != repo.DefaultBranch {
		newDefault = gh.DefaultBranch
	}
	m := svc.lock(repo.ID)
	m.Lock()
	defer m.Unlock()
	if err := svc.store.insertLink(ctx, repo, &link, newDefault, actor); err != nil {
		return LinkResponse{}, err
	}
	if newDefault != "" {
		repo.DefaultBranch = newDefault
		if _, err := gitx.Run(ctx, svc.repoPath(repo), nil, "symbolic-ref", "HEAD", "refs/heads/"+newDefault); err != nil {
			svc.log().Warn("github: set HEAD", "repo", repo.Org+"/"+repo.Name, "err", err)
		}
	}
	res, err := svc.mirror(ctx, link, repo, actor, nil)
	if err != nil {
		_ = svc.store.updateSync(ctx, link, "failed", err.Error(), svc.now(), nil)
		return LinkResponse{Link: link, WebhookURL: svc.webhookURL(), Mirror: res}, fmt.Errorf("linked, but the initial mirror failed: %w", err)
	}
	_ = svc.store.updateSync(ctx, link, "ok", "", svc.now(), nil)
	_ = svc.store.emit(ctx, repo, "io.ore.github.sync.completed", repo.Org+"/"+repo.Name+"/github", actor, map[string]any{"initial": true, "updates": res.Updates, "tracking": res.Tracking})
	link, _ = svc.store.linkByRepo(ctx, repo.ID)
	return LinkResponse{Link: link, WebhookURL: svc.webhookURL(), Mirror: res}, nil
}

func atoi64(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

func (svc *Service) handleLink(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error {
	repo, err := svc.repo(r)
	if err != nil {
		return err
	}
	var req LinkRequest
	if err := web.Decode(r, &req); err != nil {
		return fmt.Errorf("%w: %v", errBadRequest, err)
	}
	res, err := svc.Link(r.Context(), repo, req, actor)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusCreated, res)
}

func (svc *Service) handleGet(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error {
	repo, err := svc.repo(r)
	if err != nil {
		return err
	}
	link, err := svc.store.linkByRepo(r.Context(), repo.ID)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, LinkResponse{Link: link, WebhookURL: svc.webhookURL()})
}

func (svc *Service) handleList(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error {
	links, err := svc.store.allLinks(r.Context())
	if err != nil {
		return err
	}
	out := make([]Link, 0, len(links))
	for _, l := range links {
		out = append(out, l.Redacted())
	}
	return web.JSON(w, http.StatusOK, out)
}

func (svc *Service) handleUnlink(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error {
	repo, err := svc.repo(r)
	if err != nil {
		return err
	}
	link, err := svc.store.linkByRepo(r.Context(), repo.ID)
	if err != nil {
		return err
	}
	if err := svc.store.deleteLink(r.Context(), repo, link, actor); err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]string{"status": "unlinked"})
}

func (svc *Service) handleSync(w http.ResponseWriter, r *http.Request, actor ledger.Actor) error {
	repo, err := svc.repo(r)
	if err != nil {
		return err
	}
	link, err := svc.store.linkByRepo(r.Context(), repo.ID)
	if err != nil {
		return err
	}
	res, err := svc.SyncLink(r.Context(), link)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, res)
}
