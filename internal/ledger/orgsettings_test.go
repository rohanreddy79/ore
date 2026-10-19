// SPDX-License-Identifier: AGPL-3.0-only

package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestOrgSettingsDefaultsAndChanges(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	org, err := db.CreateOrg(ctx, "acme", System)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := db.CreateOrg(ctx, "other", System)
	actor := Actor{Kind: "human", ID: "alice"}
	str := func(s string) *string { return &s }

	// no row: the defaults, and nothing recorded
	st, err := db.OrgSettings(ctx, org)
	if err != nil || st.GitTransport != GitTransportOn || st.OrgID != org.ID || st.Org != "acme" || st.UpdatedBy != nil || st.UpdatedAt != "" {
		t.Fatalf("defaults: %+v %v", st, err)
	}

	// off with a reason: a row, the event in the same transaction, the actor on both
	st, changed, err := db.SetOrgSettings(ctx, org, OrgSettingsInput{GitTransport: str(GitTransportOff), Reason: str("moved to Ore")}, actor)
	if err != nil || !changed || st.GitTransport != GitTransportOff || st.GitTransportReason != "moved to Ore" || st.UpdatedBy == nil || st.UpdatedBy.ID != "alice" || st.UpdatedAt == "" {
		t.Fatalf("set off: %+v %v %v", st, changed, err)
	}
	got, _ := db.OrgSettings(ctx, org)
	if !reflect.DeepEqual(got, st) {
		t.Fatalf("read back %+v, want %+v", got, st)
	}
	evs, err := db.Events(ctx, EventsOptions{Type: "io.ore.organization.settings.changed"})
	if err != nil || len(evs) != 1 {
		t.Fatalf("events: %v %d", err, len(evs))
	}
	if evs[0].OreOrg != org.ID || evs[0].Subject != "orgs/acme/settings" || evs[0].OreActor != actor {
		t.Fatalf("event envelope: %+v", evs[0])
	}
	var data struct {
		Org      string                       `json:"org"`
		Settings OrgSettings                  `json:"settings"`
		Changes  map[string]map[string]string `json:"changes"`
	}
	if err := json.Unmarshal(evs[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Org != "acme" || data.Settings.GitTransport != GitTransportOff || data.Changes["git_transport"]["from"] != "on" || data.Changes["git_transport"]["to"] != "off" || data.Changes["git_transport_reason"]["to"] != "moved to Ore" {
		t.Fatalf("event data: %s", evs[0].Data)
	}

	// the same value again: nothing written, nothing emitted
	st, changed, err = db.SetOrgSettings(ctx, org, OrgSettingsInput{GitTransport: str(GitTransportOff), Reason: str("moved to Ore")}, actor)
	if err != nil || changed || st.GitTransport != GitTransportOff || st.UpdatedBy == nil || st.UpdatedBy.ID != "alice" {
		t.Fatalf("no-op: %+v %v %v", st, changed, err)
	}
	if evs, _ = db.Events(ctx, EventsOptions{Type: "io.ore.organization.settings.changed"}); len(evs) != 1 {
		t.Fatalf("no-op emitted: %d events", len(evs))
	}

	// a new mode without a reason clears the old reason (it explained the old mode)
	st, changed, err = db.SetOrgSettings(ctx, org, OrgSettingsInput{GitTransport: str(GitTransportReadOnly)}, Actor{Kind: "human", ID: "bob"})
	if err != nil || !changed || st.GitTransport != GitTransportReadOnly || st.GitTransportReason != "" || st.UpdatedBy.ID != "bob" {
		t.Fatalf("read-only: %+v %v %v", st, changed, err)
	}
	// the reason alone can change
	st, changed, err = db.SetOrgSettings(ctx, org, OrgSettingsInput{Reason: str("pushes go through ore save now")}, actor)
	if err != nil || !changed || st.GitTransport != GitTransportReadOnly || st.GitTransportReason != "pushes go through ore save now" {
		t.Fatalf("reason only: %+v %v %v", st, changed, err)
	}
	evs, _ = db.Events(ctx, EventsOptions{Type: "io.ore.organization.settings.changed"})
	if len(evs) != 3 {
		t.Fatalf("expected 3 events, got %d", len(evs))
	}
	var reasonOnly struct {
		Changes map[string]map[string]string `json:"changes"`
	}
	json.Unmarshal(evs[2].Data, &reasonOnly)
	if _, has := reasonOnly.Changes["git_transport"]; has || reasonOnly.Changes["git_transport_reason"]["from"] != "" {
		t.Fatalf("reason-only change: %s", evs[2].Data)
	}

	// back on
	if st, changed, err = db.SetOrgSettings(ctx, org, OrgSettingsInput{GitTransport: str(GitTransportOn)}, actor); err != nil || !changed || st.GitTransport != GitTransportOn || st.GitTransportReason != "" {
		t.Fatalf("back on: %+v %v %v", st, changed, err)
	}

	// another organization is untouched
	if st, _ = db.OrgSettings(ctx, other); st.GitTransport != GitTransportOn || st.UpdatedBy != nil {
		t.Fatalf("other org: %+v", st)
	}

	// invalid input is refused before anything is written
	for _, in := range []OrgSettingsInput{{GitTransport: str("sometimes")}, {GitTransport: str("")}, {GitTransport: str("OFF")}, {Reason: str(strings.Repeat("x", MaxSettingReason+1))}} {
		if _, _, err := db.SetOrgSettings(ctx, org, in, actor); !errors.Is(err, ErrInvalidSetting) {
			t.Fatalf("%+v: %v", in, err)
		}
	}
	if evs, _ = db.Events(ctx, EventsOptions{Type: "io.ore.organization.settings.changed"}); len(evs) != 4 {
		t.Fatalf("invalid input emitted: %d events", len(evs))
	}
	// a nil input is a no-op
	if _, changed, err := db.SetOrgSettings(ctx, org, OrgSettingsInput{}, actor); err != nil || changed {
		t.Fatalf("empty input: %v %v", changed, err)
	}
	if !ValidGitTransport("read-only") || ValidGitTransport("readonly") {
		t.Fatal("ValidGitTransport")
	}
}
