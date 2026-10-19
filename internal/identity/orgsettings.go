// SPDX-License-Identifier: AGPL-3.0-only

package identity

import (
	"errors"
	"net/http"

	"github.com/orevcs/ore/internal/ledger"
	"github.com/orevcs/ore/internal/web"
)

// Organization settings (ADR 0016 §3), mounted from Routes at
// /api/v0/orgs/{org}/settings: members (and the organization's agents) read
// them, the organization's owners and administrators change them. The one
// setting so far is git_transport (on, read-only, off), which the git bridge
// consults on every request; every change is an
// io.ore.organization.settings.changed event written with the row.

func (s *Service) getOrgSettings(w http.ResponseWriter, r *http.Request, p *Principal) error {
	org, err := s.DB.OrgByName(r.Context(), r.PathValue("org"))
	if err != nil {
		return err
	}
	if err := s.CheckPolicy(r.Context(), p, org.Name, OpRead); err != nil {
		return err
	}
	st, err := s.DB.OrgSettings(r.Context(), org)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, st)
}

func (s *Service) patchOrgSettings(w http.ResponseWriter, r *http.Request, p *Principal) error {
	org, err := s.DB.OrgByName(r.Context(), r.PathValue("org"))
	if err != nil {
		return err
	}
	if ok, err := s.isOrgOwner(r, p, org); err != nil || !ok {
		if err != nil {
			return err
		}
		return forbidden("changing the settings of %s requires one of its owners or an administrator", org.Name)
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var in ledger.OrgSettingsInput
	if err := web.Decode(r, &in); err != nil {
		return badRequest("%v", err)
	}
	if in.GitTransport == nil && in.Reason == nil {
		return badRequest("nothing to change: send git_transport (on, read-only or off) and/or reason")
	}
	st, _, err := s.DB.SetOrgSettings(r.Context(), org, in, p.Actor)
	if errors.Is(err, ledger.ErrInvalidSetting) {
		return badRequest("%v", err)
	}
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, st)
}
