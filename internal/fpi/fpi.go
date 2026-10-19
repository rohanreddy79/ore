// SPDX-License-Identifier: AGPL-3.0-only

// Package fpi is the Forge Provider Interface (docs/03 §2.5, spec/fpi): the
// seam through which Ore's loop runs on repositories wherever they are
// hosted. v0.x keeps the interface minimal — repositories, refs, issues as a
// read-through, commit status reporting and inbound hooks — and ships two
// implementations: Native in this file (Ore's own git host, a thin wrapper
// over internal/gitx and the ledger) and the GitHub adapter in ./github.
// Objects, principals, packages and migrations (spec/fpi/README.md) arrive
// with later slices behind the same seam.
package fpi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"sort"
	"sync"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// Repository is a hosted repository as its provider knows it.
type Repository struct {
	Provider      string `json:"provider"`
	ID            string `json:"id"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
	HTMLURL       string `json:"html_url,omitempty"`
	Private       bool   `json:"private"`
}

// FullName returns owner/name.
func (r Repository) FullName() string { return r.Owner + "/" + r.Name }

// Ref is a named pointer to an object.
type Ref struct {
	Name string `json:"name"` // full name, e.g. refs/heads/main
	OID  string `json:"oid"`
}

// Issue is a read-through view of an issue on the provider.
type Issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"` // open | closed
	Author string `json:"author"`
	URL    string `json:"url,omitempty"`
}

// IssueQuery filters a read-through listing.
type IssueQuery struct {
	State string // open | closed | all; default open
	Limit int    // 0 means the provider's default page
}

// Status is a verification result reported against a commit.
type Status struct {
	State       string `json:"state"` // pending | success | failure | error
	Context     string `json:"context"`
	Description string `json:"description,omitempty"`
	TargetURL   string `json:"target_url,omitempty"`
}

// Status states.
const (
	StatusPending = "pending"
	StatusSuccess = "success"
	StatusFailure = "failure"
	StatusError   = "error"
)

// Capabilities says what a provider does natively. Ore emulates what it can
// and reports the rest in the UI.
type Capabilities struct {
	ServerSideMerge bool `json:"server_side_merge"` // Ore updates refs itself (compare-and-swap)
	InboundHooks    bool `json:"inbound_hooks"`     // the provider delivers events to Ore over HTTP
	CommitStatuses  bool `json:"commit_statuses"`   // ReportStatus reaches the provider's own UI
	Issues          bool `json:"issues"`
	PullRequests    bool `json:"pull_requests"`
}

// ErrUnsupported is returned for operations a provider does not implement.
var ErrUnsupported = errors.New("fpi: not supported by this provider")

// Repos resolves repositories.
type Repos interface {
	Repository(ctx context.Context, owner, name string) (Repository, error)
}

// Refs lists refs.
type Refs interface {
	ListRefs(ctx context.Context, repo Repository) ([]Ref, error)
}

// Issues is a read-through for issues; Ore's own issues table is the source
// of truth once an issue is mirrored.
type Issues interface {
	ListIssues(ctx context.Context, repo Repository, q IssueQuery) ([]Issue, error)
}

// Statuses reports verification results against commits.
type Statuses interface {
	ReportStatus(ctx context.Context, repo Repository, sha string, st Status) error
}

// Hooks receives the provider's out-of-band events. The native host has
// none: its receive-pack records ref updates in-band (internal/githttp), so
// its handler answers 404.
type Hooks interface {
	Webhook(w http.ResponseWriter, r *http.Request)
}

// Provider is the v0.x FPI: everything the loop needs from a host.
type Provider interface {
	Name() string
	Capabilities() Capabilities
	Repos
	Refs
	Issues
	Statuses
	Hooks
}

var (
	mu        sync.RWMutex
	providers = map[string]Provider{}
)

// Register makes a provider available by name. Registering the same name
// again replaces the previous provider (extensions may reconfigure).
func Register(p Provider) {
	mu.Lock()
	defer mu.Unlock()
	providers[p.Name()] = p
}

// Lookup returns a registered provider.
func Lookup(name string) (Provider, bool) {
	mu.RLock()
	defer mu.RUnlock()
	p, ok := providers[name]
	return p, ok
}

// Names lists registered providers in sorted order.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(providers))
	for n := range providers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Native is Ore's own git host behind the FPI: bare repositories under
// ReposDir, refs read with gitx, issues from the ledger. Ref updates are
// recorded by githttp (pushes) and the API (server-side commits and merges),
// so this provider only reads.
type Native struct {
	DB       *ledger.DB
	ReposDir string
	BaseURL  string
}

// Name implements Provider.
func (n *Native) Name() string { return "native" }

// Capabilities implements Provider.
func (n *Native) Capabilities() Capabilities {
	return Capabilities{ServerSideMerge: true, Issues: true, PullRequests: true}
}

func (n *Native) repoPath(owner, name string) string {
	return filepath.Join(n.ReposDir, owner, name+".git")
}

// Repository implements Repos: owner is the Ore organization.
func (n *Native) Repository(ctx context.Context, owner, name string) (Repository, error) {
	r, err := n.DB.RepoByName(ctx, owner, name)
	if err != nil {
		return Repository{}, err
	}
	return Repository{Provider: "native", ID: r.ID, Owner: r.Org, Name: r.Name, DefaultBranch: r.DefaultBranch,
		CloneURL: n.BaseURL + "/" + r.Org + "/" + r.Name + ".git", HTMLURL: n.BaseURL + "/" + r.Org + "/" + r.Name, Private: true}, nil
}

// ListRefs implements Refs.
func (n *Native) ListRefs(ctx context.Context, repo Repository) ([]Ref, error) {
	refs, err := gitx.Refs(ctx, n.repoPath(repo.Owner, repo.Name))
	if err != nil {
		return nil, err
	}
	out := make([]Ref, 0, len(refs))
	for name, oid := range refs {
		out = append(out, Ref{Name: name, OID: oid})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListIssues implements Issues from the ledger.
func (n *Native) ListIssues(ctx context.Context, repo Repository, q IssueQuery) ([]Issue, error) {
	r, err := n.DB.RepoByName(ctx, repo.Owner, repo.Name)
	if err != nil {
		return nil, err
	}
	issues, err := n.DB.Issues(ctx, r)
	if err != nil {
		return nil, err
	}
	state := q.State
	if state == "" {
		state = "open"
	}
	var out []Issue
	for _, i := range issues {
		if state != "all" && i.State != state {
			continue
		}
		out = append(out, Issue{Number: i.Number, Title: i.Title, Body: i.Body, State: i.State, Author: "", URL: n.BaseURL + "/" + r.Org + "/" + r.Name})
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	return out, nil
}

// ReportStatus implements Statuses: on the native host a status is a ledger
// event against the commit (the pipeline run itself is already recorded).
func (n *Native) ReportStatus(ctx context.Context, repo Repository, sha string, st Status) error {
	r, err := n.DB.RepoByName(ctx, repo.Owner, repo.Name)
	if err != nil {
		return err
	}
	tx, err := n.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	data := map[string]any{"repo_id": r.ID, "sha": sha, "state": st.State, "context": st.Context, "description": st.Description, "target_url": st.TargetURL}
	if _, err := tx.Emit(r.OrgID, "io.ore.commit.status.reported", r.Org+"/"+r.Name+"/commits/"+sha, ledger.System, data); err != nil {
		return err
	}
	return tx.Commit()
}

// Webhook implements Hooks: the native host has no inbound hooks.
func (n *Native) Webhook(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(map[string]string{"error": "the native provider has no inbound hooks; ref updates are recorded by receive-pack"})
}
