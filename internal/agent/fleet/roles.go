// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"context"
	"fmt"
	"sort"

	"github.com/orevcs/ore/internal/ledger"
)

// Role names (docs/12 §4). Each is an app with capabilities, a budget and a
// prompt profile; any model can back any role.
const (
	RoleImplementer       = "implementer"
	RoleReviewer          = "reviewer"
	RoleWatcher           = "watcher"
	RoleDependencyUpdater = "dependency-updater"
)

// RoleNames lists the roles in display order.
var RoleNames = []string{RoleImplementer, RoleReviewer, RoleWatcher, RoleDependencyUpdater}

// Role is a per-repository role configuration: the adapter command run with
// sh -c in a workspace, how its provenance is labelled, a prompt profile
// prepended to the task, and budgets. The implementer defaults to the
// server's adapter; the reviewer is off until a command is configured.
type Role struct {
	ID            string `json:"id,omitempty"`
	OrgID         string `json:"org_id,omitempty"`
	RepoID        string `json:"repo_id,omitempty"`
	Name          string `json:"name"`
	AdapterCmd    string `json:"adapter_cmd"`
	AdapterName   string `json:"adapter_name"`
	PromptProfile string `json:"prompt_profile"`
	Budgets       Budget `json:"budgets"`
	Enabled       bool   `json:"enabled"`
	// Default is true when the repository has no row and the role is the
	// server-wide default.
	Default   bool   `json:"default"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// RoleInput is what PUT accepts.
type RoleInput struct {
	AdapterCmd    *string `json:"adapter_cmd"`
	AdapterName   *string `json:"adapter_name"`
	PromptProfile *string `json:"prompt_profile"`
	Budgets       *Budget `json:"budgets"`
	Enabled       *bool   `json:"enabled"`
}

func validRole(name string) bool {
	for _, n := range RoleNames {
		if n == name {
			return true
		}
	}
	return false
}

func (s *Service) defaultRole(name string) Role {
	r := Role{Name: name, Default: true}
	switch name {
	case RoleImplementer:
		r.Enabled = true
		if s.Loop != nil {
			r.AdapterCmd, r.AdapterName = s.Loop.AdapterCmd, s.Loop.AdapterName
		}
		if r.AdapterName == "" {
			r.AdapterName = "claude-code"
		}
	case RoleWatcher:
		r.Enabled = true
		r.AdapterName = "fleet-watcher"
	case RoleReviewer:
		r.AdapterName = "reviewer"
	case RoleDependencyUpdater:
		r.AdapterName = "dependency-updater"
	}
	return r
}

func (s *Service) role(ctx context.Context, repo ledger.Repo, name string) (Role, error) {
	if !validRole(name) {
		return Role{}, fmt.Errorf("%w: unknown role %q", ErrInvalid, name)
	}
	rows, err := s.loadRoles(ctx, repo)
	if err != nil {
		return Role{}, err
	}
	if r, ok := rows[name]; ok {
		return r, nil
	}
	return s.defaultRole(name), nil
}

// Roles lists the four roles of a repository, defaults filled in.
func (s *Service) Roles(ctx context.Context, repo ledger.Repo) ([]Role, error) {
	rows, err := s.loadRoles(ctx, repo)
	if err != nil {
		return nil, err
	}
	out := make([]Role, 0, len(RoleNames))
	for _, n := range RoleNames {
		if r, ok := rows[n]; ok {
			out = append(out, r)
		} else {
			out = append(out, s.defaultRole(n))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return indexOf(out[i].Name) < indexOf(out[j].Name) })
	return out, nil
}

func indexOf(name string) int {
	for i, n := range RoleNames {
		if n == name {
			return i
		}
	}
	return len(RoleNames)
}

// SetRole configures a role for a repository and emits io.ore.fleet.role.updated.
func (s *Service) SetRole(ctx context.Context, repo ledger.Repo, name string, in RoleInput, actor ledger.Actor) (Role, error) {
	cur, err := s.role(ctx, repo, name)
	if err != nil {
		return cur, err
	}
	if cur.Default {
		cur = Role{Name: name, Enabled: cur.Enabled, AdapterName: cur.AdapterName, AdapterCmd: cur.AdapterCmd}
	}
	if in.AdapterCmd != nil {
		cur.AdapterCmd = *in.AdapterCmd
	}
	if in.AdapterName != nil {
		cur.AdapterName = *in.AdapterName
	}
	if in.PromptProfile != nil {
		cur.PromptProfile = *in.PromptProfile
	}
	if in.Budgets != nil {
		if in.Budgets.CIMinutesPerDay < 0 || in.Budgets.RunsPerDay < 0 || in.Budgets.OpenChanges < 0 || in.Budgets.WritesPerHour < 0 {
			return cur, fmt.Errorf("%w: budgets must be >= 0", ErrInvalid)
		}
		cur.Budgets = *in.Budgets
	}
	if in.Enabled != nil {
		cur.Enabled = *in.Enabled
	} else if in.AdapterCmd != nil && *in.AdapterCmd != "" && name == RoleReviewer {
		cur.Enabled = true // configuring a reviewer command turns the reviewer on
	}
	if cur.AdapterName == "" {
		cur.AdapterName = name
	}
	cur.Default = false
	if err := s.upsertRole(ctx, repo, &cur, actor); err != nil {
		return cur, err
	}
	// a role's own budget is the principal budget of its adapter name
	if !cur.Budgets.IsZero() && cur.AdapterName != "" {
		if _, err := s.SetBudget(ctx, repo, cur.AdapterName, cur.Budgets, actor); err != nil {
			return cur, err
		}
	}
	return cur, nil
}
