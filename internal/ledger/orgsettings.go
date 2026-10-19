// SPDX-License-Identifier: AGPL-3.0-only

package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Git transport modes of an organization (ADR 0016 §3). The git bridge (smart
// HTTP in internal/githttp, SSH in internal/identity/sshd) reads the mode on
// every request; the API, the web app, pipelines, agents, the GitHub adapter
// and the Ore client never go through the bridge, so the mode never touches
// them.
const (
	GitTransportOn       = "on"        // clone, fetch and push work
	GitTransportReadOnly = "read-only" // clone and fetch work; pushes are refused
	GitTransportOff      = "off"       // every git request is refused
)

// GitTransportModes lists the valid modes, in the order a team moves through them.
var GitTransportModes = []string{GitTransportOn, GitTransportReadOnly, GitTransportOff}

// ErrInvalidSetting wraps the errors SetOrgSettings returns for input out of range.
var ErrInvalidSetting = errors.New("invalid setting")

// MaxSettingReason bounds the free-text reason recorded with a settings change.
const MaxSettingReason = 500

// OrgSettings are an organization's settings. An organization that never
// changed any has the defaults, with UpdatedBy and UpdatedAt empty.
type OrgSettings struct {
	OrgID        string `json:"org_id"`
	Org          string `json:"org"`
	GitTransport string `json:"git_transport"`
	// GitTransportReason is what the owner wrote when setting the mode; git
	// users the bridge refuses see it after the message.
	GitTransportReason string `json:"git_transport_reason"`
	UpdatedBy          *Actor `json:"updated_by,omitempty"`
	UpdatedAt          string `json:"updated_at,omitempty"`
}

// OrgSettingsInput is a settings change; a nil field is left as it is. When
// GitTransport changes, the reason is replaced by Reason (or cleared when
// Reason is nil), since it explained the previous mode.
type OrgSettingsInput struct {
	GitTransport *string `json:"git_transport"`
	Reason       *string `json:"reason"`
}

// DefaultOrgSettings are the settings of an organization without a row.
func DefaultOrgSettings(org Org) OrgSettings {
	return OrgSettings{OrgID: org.ID, Org: org.Name, GitTransport: GitTransportOn}
}

// ValidGitTransport reports whether mode is one of GitTransportModes.
func ValidGitTransport(mode string) bool {
	for _, m := range GitTransportModes {
		if m == mode {
			return true
		}
	}
	return false
}

const orgSettingsColumns = `git_transport, git_transport_reason, updated_by_kind, updated_by_id, updated_at`

func scanOrgSettings(row interface{ Scan(...any) error }, st *OrgSettings) error {
	var kind, id string
	if err := row.Scan(&st.GitTransport, &st.GitTransportReason, &kind, &id, &st.UpdatedAt); err != nil {
		return err
	}
	if kind != "" || id != "" {
		st.UpdatedBy = &Actor{Kind: kind, ID: id}
	}
	return nil
}

// OrgSettings returns org's settings: the row when there is one, the defaults otherwise.
func (d *DB) OrgSettings(ctx context.Context, org Org) (OrgSettings, error) {
	st := DefaultOrgSettings(org)
	err := scanOrgSettings(d.QueryRow(ctx, `SELECT `+orgSettingsColumns+` FROM org_settings WHERE org_id = ?`, org.ID), &st)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	return st, err
}

// SetOrgSettings applies in to org's settings and emits
// io.ore.organization.settings.changed in the same transaction, with the
// settings after the change and a "changes" map of field → {from, to}. A
// change that leaves every setting as it was writes nothing and emits
// nothing; changed reports which case it was.
func (d *DB) SetOrgSettings(ctx context.Context, org Org, in OrgSettingsInput, actor Actor) (OrgSettings, bool, error) {
	if in.GitTransport != nil && !ValidGitTransport(*in.GitTransport) {
		return OrgSettings{}, false, fmt.Errorf("%w: git_transport must be one of %s, got %q", ErrInvalidSetting, strings.Join(GitTransportModes, ", "), *in.GitTransport)
	}
	if in.Reason != nil && len(*in.Reason) > MaxSettingReason {
		return OrgSettings{}, false, fmt.Errorf("%w: reason is longer than %d characters", ErrInvalidSetting, MaxSettingReason)
	}
	tx, err := d.Begin(ctx)
	if err != nil {
		return OrgSettings{}, false, err
	}
	defer tx.Rollback()
	cur := DefaultOrgSettings(org)
	exists := true
	err = scanOrgSettings(tx.QueryRowContext(ctx, `SELECT `+orgSettingsColumns+` FROM org_settings WHERE org_id = ?`, org.ID), &cur)
	if errors.Is(err, sql.ErrNoRows) {
		exists = false
	} else if err != nil {
		return OrgSettings{}, false, err
	}
	next := cur
	if in.GitTransport != nil && *in.GitTransport != cur.GitTransport {
		next.GitTransport = *in.GitTransport
		next.GitTransportReason = ""
	}
	if in.Reason != nil {
		next.GitTransportReason = *in.Reason
	}
	changes := map[string]any{}
	if next.GitTransport != cur.GitTransport {
		changes["git_transport"] = map[string]string{"from": cur.GitTransport, "to": next.GitTransport}
	}
	if next.GitTransportReason != cur.GitTransportReason {
		changes["git_transport_reason"] = map[string]string{"from": cur.GitTransportReason, "to": next.GitTransportReason}
	}
	if len(changes) == 0 {
		return cur, false, nil
	}
	next.UpdatedAt = Now()
	next.UpdatedBy = &Actor{Kind: actor.Kind, ID: actor.ID}
	if exists {
		_, err = tx.ExecContext(ctx, `UPDATE org_settings SET git_transport = ?, git_transport_reason = ?, updated_by_kind = ?, updated_by_id = ?, updated_at = ? WHERE org_id = ?`,
			next.GitTransport, next.GitTransportReason, actor.Kind, actor.ID, next.UpdatedAt, org.ID)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO org_settings(org_id, git_transport, git_transport_reason, updated_by_kind, updated_by_id, updated_at) VALUES(?,?,?,?,?,?)`,
			org.ID, next.GitTransport, next.GitTransportReason, actor.Kind, actor.ID, next.UpdatedAt)
	}
	if err != nil {
		return OrgSettings{}, false, fmt.Errorf("org settings: %w", err)
	}
	if _, err := tx.Emit(org.ID, "io.ore.organization.settings.changed", "orgs/"+org.Name+"/settings", actor,
		map[string]any{"org": org.Name, "settings": next, "changes": changes}); err != nil {
		return OrgSettings{}, false, err
	}
	return next, true, tx.Commit()
}
