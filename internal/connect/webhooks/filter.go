// SPDX-License-Identifier: AGPL-3.0-only

package webhooks

import (
	"fmt"
	"path"
	"strings"

	"github.com/orevcs/ore/internal/ledger"
)

// MatchGlob matches a subject against a glob: "*" matches within one
// segment (path.Match rules), "**" matches any number of segments. An
// empty glob matches everything.
func MatchGlob(glob, subject string) bool {
	if glob == "" {
		return true
	}
	if !strings.Contains(glob, "**") {
		ok, err := path.Match(glob, subject)
		return err == nil && ok
	}
	return matchSegments(strings.Split(glob, "/"), strings.Split(subject, "/"))
}

func matchSegments(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(segs); i++ {
				if matchSegments(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], segs[0]); err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

// Validate checks a filter's shape and compiles its expression.
func (f Filter) Validate() error {
	for _, t := range f.Types {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("types: empty prefix")
		}
	}
	if f.Subject != "" {
		if _, err := path.Match(strings.ReplaceAll(f.Subject, "**", "*"), ""); err != nil {
			return fmt.Errorf("subject: bad glob %q", f.Subject)
		}
	}
	for _, k := range f.ActorKinds {
		switch k {
		case "human", "agent", "app", "system":
		default:
			return fmt.Errorf("actor_kinds: unknown kind %q", k)
		}
	}
	if f.Expr != "" {
		if _, err := Compile(f.Expr); err != nil {
			return fmt.Errorf("expr: %w", err)
		}
	}
	return nil
}

// Matches reports whether an event passes the filter. An expression that
// fails at evaluation time does not match.
func (f Filter) Matches(ev ledger.Event) bool {
	if len(f.Types) > 0 {
		ok := false
		for _, p := range f.Types {
			if ev.Type == p || strings.HasPrefix(ev.Type, p) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if !MatchGlob(f.Subject, ev.Subject) {
		return false
	}
	if len(f.ActorKinds) > 0 {
		ok := false
		for _, k := range f.ActorKinds {
			if ev.OreActor.Kind == k {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if f.Expr != "" {
		prog, err := Compile(f.Expr)
		if err != nil {
			return false
		}
		ok, err := prog.Eval(ev)
		if err != nil || !ok {
			return false
		}
	}
	return true
}

// Accepts reports whether an endpoint receives an event: the org must match,
// a repository-scoped endpoint only sees its repository's subjects, then the filter.
func (e Endpoint) Accepts(ev ledger.Event) bool {
	if ev.OreOrg != e.OrgID {
		return false
	}
	if e.Repo != "" && ev.Subject != e.Repo && !strings.HasPrefix(ev.Subject, e.Repo+"/") {
		return false
	}
	return e.Filter.Matches(ev)
}
