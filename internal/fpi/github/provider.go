// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"net/http"

	"github.com/orevcs/ore/internal/fpi"
)

// Provider is the GitHub adapter behind the FPI: reads go to GitHub's REST
// API, statuses are posted there, and the webhook handler is the Service's.
type Provider struct {
	svc *Service
}

// Name implements fpi.Provider.
func (p *Provider) Name() string { return "github" }

// Capabilities implements fpi.Provider: GitHub owns refs and merges; Ore
// mirrors, receives hooks and posts statuses.
func (p *Provider) Capabilities() fpi.Capabilities {
	return fpi.Capabilities{InboundHooks: true, CommitStatuses: true, Issues: true, PullRequests: true}
}

func (p *Provider) client() (*Client, error) {
	if p.svc.Client == nil {
		return nil, ErrNoToken
	}
	return p.svc.Client, nil
}

// Repository implements fpi.Repos.
func (p *Provider) Repository(ctx context.Context, owner, name string) (fpi.Repository, error) {
	c, err := p.client()
	if err != nil {
		return fpi.Repository{}, err
	}
	return c.Repo(ctx, owner, name)
}

// ListRefs implements fpi.Refs.
func (p *Provider) ListRefs(ctx context.Context, repo fpi.Repository) ([]fpi.Ref, error) {
	c, err := p.client()
	if err != nil {
		return nil, err
	}
	return c.ListRefs(ctx, repo.Owner, repo.Name)
}

// ListIssues implements fpi.Issues.
func (p *Provider) ListIssues(ctx context.Context, repo fpi.Repository, q fpi.IssueQuery) ([]fpi.Issue, error) {
	c, err := p.client()
	if err != nil {
		return nil, err
	}
	return c.ListIssues(ctx, repo.Owner, repo.Name, q)
}

// ReportStatus implements fpi.Statuses.
func (p *Provider) ReportStatus(ctx context.Context, repo fpi.Repository, sha string, st fpi.Status) error {
	c, err := p.client()
	if err != nil {
		return err
	}
	return c.CreateStatus(ctx, repo.Owner, repo.Name, sha, st)
}

// Webhook implements fpi.Hooks.
func (p *Provider) Webhook(w http.ResponseWriter, r *http.Request) { p.svc.Webhook(w, r) }
