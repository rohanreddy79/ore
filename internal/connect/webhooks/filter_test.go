// SPDX-License-Identifier: AGPL-3.0-only

package webhooks

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/orevcs/ore/internal/ledger"
)

func ev(typ, subject, actorKind string, data string) ledger.Event {
	return ledger.Event{ID: "evt_1", Type: typ, Subject: subject, OreOrg: "org_1", OreActor: ledger.Actor{Kind: actorKind, ID: "x"}, Data: json.RawMessage(data), Seq: 7}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		glob, subject string
		want          bool
	}{
		{"", "anything", true},
		{"acme/app", "acme/app", true},
		{"acme/app", "acme/app/issues/1", false},
		{"acme/*", "acme/app", true},
		{"acme/*", "acme/app/issues/1", false},
		{"acme/**", "acme/app/issues/1", true},
		{"acme/**", "acme", true},
		{"**/issues/*", "acme/app/issues/1", true},
		{"**/issues/*", "acme/app/changes/1", false},
		{"acme/*/deployments/**", "acme/app/deployments/dep_1", true},
		{"acme/*/deployments/**", "acme/app/issues/1", false},
		{"*/app/**/comments/*", "acme/app/issues/1/comments/cmt_1", true},
		{"[", "x", false},
	}
	for _, c := range cases {
		if got := MatchGlob(c.glob, c.subject); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", c.glob, c.subject, got, c.want)
		}
	}
}

func TestFilterMatches(t *testing.T) {
	e := ev("io.ore.deployment.succeeded", "acme/app/deployments/dep_1", "agent", `{"environment":"production","status":"succeeded"}`)
	cases := []struct {
		name string
		f    Filter
		want bool
	}{
		{"empty", Filter{}, true},
		{"type prefix", Filter{Types: []string{"io.ore.deployment."}}, true},
		{"type exact", Filter{Types: []string{"io.ore.deployment.succeeded"}}, true},
		{"type other", Filter{Types: []string{"io.ore.issue."}}, false},
		{"type one of", Filter{Types: []string{"io.ore.issue.", "io.ore.deployment."}}, true},
		{"subject", Filter{Subject: "acme/app/**"}, true},
		{"subject other repo", Filter{Subject: "acme/web/**"}, false},
		{"actor", Filter{ActorKinds: []string{"agent"}}, true},
		{"actor other", Filter{ActorKinds: []string{"human"}}, false},
		{"expr", Filter{Expr: `data.environment == "production"`}, true},
		{"expr false", Filter{Expr: `data.environment == "preview"`}, false},
		{"expr non-boolean", Filter{Expr: `data.environment`}, false},
		{"all", Filter{Types: []string{"io.ore."}, Subject: "acme/**", ActorKinds: []string{"agent", "app"}, Expr: `endsWith(type, ".succeeded")`}, true},
	}
	for _, c := range cases {
		if got := c.f.Matches(e); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	ep := Endpoint{OrgID: "org_1", Repo: "acme/app"}
	if !ep.Accepts(e) || ep.Accepts(ev("io.ore.x", "acme/web/issues/1", "human", "{}")) {
		t.Fatal("repository scope")
	}
	if (Endpoint{OrgID: "org_2"}).Accepts(e) {
		t.Fatal("org scope")
	}
	if !(Endpoint{OrgID: "org_1"}).Accepts(ev("io.ore.repository.created", "acme/app", "human", "{}")) {
		t.Fatal("org-wide endpoint must see repository subjects")
	}
}

func TestFilterValidate(t *testing.T) {
	bad := []Filter{
		{Types: []string{" "}},
		{Subject: "["},
		{ActorKinds: []string{"robot"}},
		{Expr: `type ==`},
		{Expr: `unknown == 1`},
		{Expr: `frob(type)`},
		{Expr: `type == "a" &&`},
		{Expr: `(type == "a"`},
	}
	for _, f := range bad {
		if err := f.Validate(); err == nil {
			t.Errorf("accepted bad filter %+v", f)
		}
	}
	good := Filter{Types: []string{"io.ore."}, Subject: "acme/**", ActorKinds: []string{"agent"}, Expr: `type == "x" || (has(data.x) && !(actor.kind in ["system"]))`}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestExpressions(t *testing.T) {
	e := ev("io.ore.change.merged", "acme/app/changes/3", "agent", `{"change_id":"I1","number":3,"draft":false,"labels":["a","b"],"provenance":{"model":"claude"},"score":0.75}`)
	cases := []struct {
		expr string
		want bool
	}{
		{`type == "io.ore.change.merged"`, true},
		{`type != "io.ore.change.merged"`, false},
		{`startsWith(type, "io.ore.change.")`, true},
		{`endsWith(subject, "/changes/3")`, true},
		{`contains(subject, "acme/")`, true},
		{`matches(subject, "acme/*/changes/*")`, true},
		{`matches(subject, "acme/*/issues/*")`, false},
		{`actor.kind == "agent" && actor.id == "x"`, true},
		{`actor.kind in ["human", "app"]`, false},
		{`actor.kind in ["human", "agent"]`, true},
		{`"a" in data.labels`, true},
		{`"z" in data.labels`, false},
		{`data.number == 3`, true},
		{`data.number > 2 && data.number <= 3`, true},
		{`data.number < 3`, false},
		{`data.draft == false`, true},
		{`!data.draft`, true},
		{`data.provenance.model == "claude"`, true},
		{`data.missing == null`, true},
		{`has(data.missing)`, false},
		{`has(data.provenance)`, true},
		{`size(data.labels) == 2`, true},
		{`size(subject) > 5`, true},
		{`lower("ABC") == "abc"`, true},
		{`seq == 7`, true},
		{`org == "org_1" && (type == "nope" || subject == "acme/app/changes/3")`, true},
		{`data.score >= 0.75`, true},
		{`'single' == "single"`, true},
		{`data.number == "3"`, false},
	}
	for _, c := range cases {
		p, err := Compile(c.expr)
		if err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		got, err := p.Eval(e)
		if err != nil {
			t.Errorf("%s: eval %v", c.expr, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %v want %v", c.expr, got, c.want)
		}
	}
	// runtime type errors are errors, not matches
	for _, expr := range []string{`!type`, `type && true`, `data.number < "x"`, `type in 3`} {
		p, err := Compile(expr)
		if err != nil {
			t.Errorf("%s: compile %v", expr, err)
			continue
		}
		if _, err := p.Eval(e); err == nil {
			t.Errorf("%s: expected an evaluation error", expr)
		}
	}
	// syntax errors name the position
	_, err := Compile(`type == "a" && ^`)
	if err == nil || !strings.Contains(err.Error(), "at 15") {
		t.Fatalf("syntax error: %v", err)
	}
}
