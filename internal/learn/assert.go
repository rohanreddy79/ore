// SPDX-License-Identifier: AGPL-3.0-only

package learn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// The assert language of lesson steps: a small declarative expression over
// the ledger and git, evaluated for one run (repository + learner).
//
//	expr  := or
//	or    := and ('||' and)*
//	and   := not ('&&' not)*
//	not   := '!' not | cmp
//	cmp   := call (('>=' | '>' | '==' | '!=' | '<=' | '<') NUMBER)?
//	call  := NAME '(' args ')' | '(' expr ')' | 'true' | 'false'
//	args  := (arg (',' arg)*)?
//	arg   := 'string' | NUMBER | learner | default
//
// Functions (bool unless noted):
//
//	ref.exists(pattern)                 a ref matches the glob (refs/heads/ is implied)
//	ref.commits_by(actor, pattern)      count of commits on matching refs pushed by actor, beyond the default branch
//	change.open(pattern)                an open change has a matching source ref
//	change.merged(pattern)              a merged change has a matching source ref
//	change.mergeable(pattern)           the latest open matching change merges into its target without conflicts
//	change.approved(pattern)            the latest matching change has at least one approving review
//	pipeline.green(ref)                 the latest pipeline run on the ref's commit succeeded
//	file.exists(ref, path)              the path exists at ref
//	file.contains(ref, path, text)      the file at ref contains text
//
// `learner` is the run's learner; `default` is the repository's default
// branch; patterns use * and ? within one path segment and ** across them.

type assertKind int

const (
	aCall assertKind = iota
	aAnd
	aOr
	aNot
	aCmp
	aLit
)

// AssertExpr is a parsed assert query.
type AssertExpr struct {
	kind  assertKind
	fn    string
	args  []assertArg
	left  *AssertExpr
	right *AssertExpr
	op    string
	num   float64
	lit   bool
	pos   int
}

type argKind int

const (
	argString argKind = iota
	argNumber
	argLearner
	argDefault
)

type assertArg struct {
	kind argKind
	s    string
	n    float64
}

func (a assertArg) String() string {
	switch a.kind {
	case argString:
		return "'" + a.s + "'"
	case argNumber:
		return strconv.FormatFloat(a.n, 'f', -1, 64)
	case argLearner:
		return "learner"
	}
	return "default"
}

// functions: name -> (arity, returns count)
var assertFuncs = map[string]struct {
	arity int
	count bool
}{
	"ref.exists":       {1, false},
	"ref.commits_by":   {2, true},
	"change.open":      {1, false},
	"change.merged":    {1, false},
	"change.mergeable": {1, false},
	"change.approved":  {1, false},
	"pipeline.green":   {1, false},
	"file.exists":      {2, false},
	"file.contains":    {3, false},
}

type assertTok struct {
	kind string // name, string, number, op, (, ), ,, eof
	text string
	pos  int
}

func lexAssert(src string) ([]assertTok, error) {
	var out []assertTok
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(' || c == ')' || c == ',':
			out = append(out, assertTok{string(c), string(c), i})
			i++
		case c == '!' && (i+1 >= len(src) || src[i+1] != '='):
			out = append(out, assertTok{"op", "!", i})
			i++
		case strings.HasPrefix(src[i:], "&&") || strings.HasPrefix(src[i:], "||") || strings.HasPrefix(src[i:], ">=") || strings.HasPrefix(src[i:], "<=") || strings.HasPrefix(src[i:], "==") || strings.HasPrefix(src[i:], "!="):
			out = append(out, assertTok{"op", src[i : i+2], i})
			i += 2
		case c == '>' || c == '<':
			out = append(out, assertTok{"op", string(c), i})
			i++
		case c == '\'' || c == '"':
			q := c
			j := i + 1
			var b strings.Builder
			for j < len(src) && src[j] != q {
				if src[j] == '\\' && j+1 < len(src) {
					j++
				}
				b.WriteByte(src[j])
				j++
			}
			if j >= len(src) {
				return nil, fmt.Errorf("column %d: unterminated string", i+1)
			}
			out = append(out, assertTok{"string", b.String(), i})
			i = j + 1
		case c >= '0' && c <= '9':
			j := i
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.') {
				j++
			}
			out = append(out, assertTok{"number", src[i:j], i})
			i = j
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			j := i
			for j < len(src) && (src[j] == '_' || src[j] == '.' || src[j] >= 'a' && src[j] <= 'z' || src[j] >= 'A' && src[j] <= 'Z' || src[j] >= '0' && src[j] <= '9') {
				j++
			}
			out = append(out, assertTok{"name", src[i:j], i})
			i = j
		default:
			return nil, fmt.Errorf("column %d: unexpected character %q", i+1, c)
		}
	}
	out = append(out, assertTok{"eof", "", len(src)})
	return out, nil
}

type assertParser struct {
	toks []assertTok
	i    int
}

func (p *assertParser) peek() assertTok { return p.toks[p.i] }
func (p *assertParser) next() assertTok { t := p.toks[p.i]; p.i++; return t }

// ParseAssert parses a query; errors carry the column.
func ParseAssert(src string) (*AssertExpr, error) {
	toks, err := lexAssert(src)
	if err != nil {
		return nil, err
	}
	p := &assertParser{toks: toks}
	e, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != "eof" {
		return nil, fmt.Errorf("column %d: unexpected %q", t.pos+1, t.text)
	}
	return e, nil
}

func (p *assertParser) parseOr() (*AssertExpr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == "op" && p.peek().text == "||" {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &AssertExpr{kind: aOr, left: left, right: right}
	}
	return left, nil
}

func (p *assertParser) parseAnd() (*AssertExpr, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == "op" && p.peek().text == "&&" {
		p.next()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = &AssertExpr{kind: aAnd, left: left, right: right}
	}
	return left, nil
}

func (p *assertParser) parseNot() (*AssertExpr, error) {
	if p.peek().kind == "op" && p.peek().text == "!" {
		p.next()
		e, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &AssertExpr{kind: aNot, left: e}, nil
	}
	return p.parseCmp()
}

func (p *assertParser) parseCmp() (*AssertExpr, error) {
	left, err := p.parseCall()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	if t.kind == "op" && (t.text == ">=" || t.text == ">" || t.text == "==" || t.text == "!=" || t.text == "<=" || t.text == "<") {
		if left.kind != aCall || !assertFuncs[left.fn].count {
			return nil, fmt.Errorf("column %d: only counting functions (ref.commits_by) can be compared with a number", t.pos+1)
		}
		p.next()
		n := p.next()
		if n.kind != "number" {
			return nil, fmt.Errorf("column %d: expected a number after %s", n.pos+1, t.text)
		}
		f, _ := strconv.ParseFloat(n.text, 64)
		return &AssertExpr{kind: aCmp, left: left, op: t.text, num: f, pos: t.pos}, nil
	}
	if left.kind == aCall && assertFuncs[left.fn].count {
		return nil, fmt.Errorf("column %d: %s returns a count; compare it, for example %s >= 1", left.pos+1, left.fn, left.fn+"(...)")
	}
	return left, nil
}

func (p *assertParser) parseCall() (*AssertExpr, error) {
	t := p.next()
	switch t.kind {
	case "(":
		e, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if c := p.next(); c.kind != ")" {
			return nil, fmt.Errorf("column %d: expected ')'", c.pos+1)
		}
		return e, nil
	case "name":
		if t.text == "true" || t.text == "false" {
			return &AssertExpr{kind: aLit, lit: t.text == "true", pos: t.pos}, nil
		}
		spec, ok := assertFuncs[t.text]
		if !ok {
			return nil, fmt.Errorf("column %d: unknown function %s (known: %s)", t.pos+1, t.text, strings.Join(assertFuncNames(), ", "))
		}
		if c := p.next(); c.kind != "(" {
			return nil, fmt.Errorf("column %d: expected '(' after %s", c.pos+1, t.text)
		}
		e := &AssertExpr{kind: aCall, fn: t.text, pos: t.pos}
		for p.peek().kind != ")" {
			a := p.next()
			switch a.kind {
			case "string":
				e.args = append(e.args, assertArg{kind: argString, s: a.text})
			case "number":
				f, _ := strconv.ParseFloat(a.text, 64)
				e.args = append(e.args, assertArg{kind: argNumber, n: f})
			case "name":
				switch a.text {
				case "learner":
					e.args = append(e.args, assertArg{kind: argLearner})
				case "default":
					e.args = append(e.args, assertArg{kind: argDefault})
				default:
					return nil, fmt.Errorf("column %d: unknown keyword %s (arguments are 'strings', numbers, learner or default)", a.pos+1, a.text)
				}
			default:
				return nil, fmt.Errorf("column %d: unexpected %q in arguments", a.pos+1, a.text)
			}
			if p.peek().kind == "," {
				p.next()
			} else if p.peek().kind != ")" {
				return nil, fmt.Errorf("column %d: expected ',' or ')'", p.peek().pos+1)
			}
		}
		p.next()
		if len(e.args) != spec.arity {
			return nil, fmt.Errorf("column %d: %s takes %d argument(s), got %d", t.pos+1, t.text, spec.arity, len(e.args))
		}
		if t.text == "ref.commits_by" && e.args[0].kind == argNumber {
			return nil, fmt.Errorf("column %d: ref.commits_by: the first argument is an actor (learner or 'name')", t.pos+1)
		}
		return e, nil
	case "eof":
		return nil, errors.New("unexpected end of query")
	}
	return nil, fmt.Errorf("column %d: unexpected %q", t.pos+1, t.text)
}

func assertFuncNames() []string {
	out := make([]string, 0, len(assertFuncs))
	for n := range assertFuncs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// String renders the expression back.
func (e *AssertExpr) String() string {
	switch e.kind {
	case aLit:
		return strconv.FormatBool(e.lit)
	case aCall:
		parts := make([]string, len(e.args))
		for i, a := range e.args {
			parts[i] = a.String()
		}
		return e.fn + "(" + strings.Join(parts, ", ") + ")"
	case aNot:
		return "!" + e.left.String()
	case aAnd:
		return "(" + e.left.String() + " && " + e.right.String() + ")"
	case aOr:
		return "(" + e.left.String() + " || " + e.right.String() + ")"
	case aCmp:
		return e.left.String() + " " + e.op + " " + strconv.FormatFloat(e.num, 'f', -1, 64)
	}
	return "?"
}

// ---- evaluation ----

// AssertEnv is what a query is evaluated against.
type AssertEnv struct {
	Ctx      context.Context
	DB       *ledger.DB
	ReposDir string
	Repo     ledger.Repo
	Learner  ledger.Actor
}

// CheckResult is the outcome of one function call, for the learner.
type CheckResult struct {
	Call   string `json:"call"`
	Result any    `json:"result"`
	Detail string `json:"detail,omitempty"`
}

// AssertResult is the outcome of a query.
type AssertResult struct {
	Passed bool          `json:"passed"`
	Checks []CheckResult `json:"checks"`
}

func (e *AssertEnv) repoPath() string {
	return filepath.Join(e.ReposDir, e.Repo.Org, e.Repo.Name+".git")
}

// Evaluate runs the query. An error means the query could not be evaluated
// (for example the repository no longer exists); a false result is not an error.
func (e *AssertEnv) Evaluate(x *AssertExpr) (AssertResult, error) {
	if e.Repo.ID == "" {
		return AssertResult{}, errors.New("assert: the run has no repository")
	}
	if _, err := os.Stat(e.repoPath()); err != nil {
		return AssertResult{}, fmt.Errorf("assert: repository %s/%s is missing on disk (%s)", e.Repo.Org, e.Repo.Name, e.repoPath())
	}
	res := AssertResult{Checks: []CheckResult{}}
	ok, err := e.eval(x, &res)
	if err != nil {
		return res, err
	}
	res.Passed = ok
	return res, nil
}

func (e *AssertEnv) eval(x *AssertExpr, res *AssertResult) (bool, error) {
	switch x.kind {
	case aLit:
		return x.lit, nil
	case aNot:
		v, err := e.eval(x.left, res)
		return !v, err
	case aAnd:
		l, err := e.eval(x.left, res)
		if err != nil {
			return false, err
		}
		if !l {
			return false, nil
		}
		return e.eval(x.right, res)
	case aOr:
		l, err := e.eval(x.left, res)
		if err != nil {
			return false, err
		}
		if l {
			return true, nil
		}
		return e.eval(x.right, res)
	case aCmp:
		n, detail, err := e.count(x.left)
		if err != nil {
			return false, err
		}
		var ok bool
		switch x.op {
		case ">=":
			ok = float64(n) >= x.num
		case ">":
			ok = float64(n) > x.num
		case "==":
			ok = float64(n) == x.num
		case "!=":
			ok = float64(n) != x.num
		case "<=":
			ok = float64(n) <= x.num
		case "<":
			ok = float64(n) < x.num
		}
		res.Checks = append(res.Checks, CheckResult{Call: x.String(), Result: n, Detail: detail})
		return ok, nil
	case aCall:
		ok, detail, err := e.call(x)
		if err != nil {
			return false, err
		}
		res.Checks = append(res.Checks, CheckResult{Call: x.String(), Result: ok, Detail: detail})
		return ok, nil
	}
	return false, fmt.Errorf("assert: bad node")
}

func (e *AssertEnv) argString(a assertArg) string {
	switch a.kind {
	case argLearner:
		return e.Learner.ID
	case argDefault:
		return "refs/heads/" + e.Repo.DefaultBranch
	case argNumber:
		return strconv.FormatFloat(a.n, 'f', -1, 64)
	}
	return a.s
}

// normalizeRef makes a pattern a full ref pattern.
func normalizeRef(p string) string {
	if strings.HasPrefix(p, "refs/") {
		return p
	}
	return "refs/heads/" + p
}

// matchRef matches a ref against a pattern with ** across segments.
func matchRef(pattern, ref string) bool {
	ps, rs := strings.Split(pattern, "/"), strings.Split(ref, "/")
	var m func(p, r []string) bool
	m = func(p, r []string) bool {
		if len(p) == 0 {
			return len(r) == 0
		}
		if p[0] == "**" {
			for i := 0; i <= len(r); i++ {
				if m(p[1:], r[i:]) {
					return true
				}
			}
			return false
		}
		if len(r) == 0 {
			return false
		}
		if ok, err := path.Match(p[0], r[0]); err != nil || !ok {
			return false
		}
		return m(p[1:], r[1:])
	}
	return m(ps, rs)
}

func (e *AssertEnv) matchingRefs(pattern string) ([]string, error) {
	refs, err := gitx.Refs(e.Ctx, e.repoPath())
	if err != nil {
		return nil, fmt.Errorf("assert: list refs: %w", err)
	}
	pattern = normalizeRef(pattern)
	var out []string
	for r := range refs {
		if matchRef(pattern, r) {
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out, nil
}

// resolveRef picks the first matching ref for a pattern (or the exact ref).
func (e *AssertEnv) resolveRef(pattern string) (string, bool, error) {
	refs, err := e.matchingRefs(pattern)
	if err != nil {
		return "", false, err
	}
	if len(refs) == 0 {
		return "", false, nil
	}
	return refs[0], true, nil
}

type changeRow struct {
	ID        string
	Number    int
	SourceRef string
	TargetRef string
	State     string
	HeadOID   string
	MergeOID  *string
}

func (e *AssertEnv) changes(pattern string) ([]changeRow, error) {
	rows, err := e.DB.Query(e.Ctx, `SELECT id, number, source_ref, target_ref, state, head_oid, merge_oid FROM changes WHERE repo_id = ? ORDER BY created_at DESC`, e.Repo.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pattern = normalizeRef(pattern)
	var out []changeRow
	for rows.Next() {
		var c changeRow
		if err := rows.Scan(&c.ID, &c.Number, &c.SourceRef, &c.TargetRef, &c.State, &c.HeadOID, &c.MergeOID); err != nil {
			return nil, err
		}
		if matchRef(pattern, c.SourceRef) {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

func (e *AssertEnv) call(x *AssertExpr) (bool, string, error) {
	rp := e.repoPath()
	switch x.fn {
	case "ref.exists":
		refs, err := e.matchingRefs(e.argString(x.args[0]))
		if err != nil {
			return false, "", err
		}
		if len(refs) == 0 {
			return false, "no ref matches " + normalizeRef(e.argString(x.args[0])), nil
		}
		return true, strings.Join(refs, ", "), nil
	case "change.open", "change.merged":
		want := "open"
		if x.fn == "change.merged" {
			want = "merged"
		}
		cs, err := e.changes(e.argString(x.args[0]))
		if err != nil {
			return false, "", err
		}
		for _, c := range cs {
			if c.State == want {
				return true, fmt.Sprintf("change #%d (%s)", c.Number, c.SourceRef), nil
			}
		}
		if len(cs) == 0 {
			return false, "no change has a source ref matching " + normalizeRef(e.argString(x.args[0])), nil
		}
		return false, fmt.Sprintf("change #%d is %s, not %s", cs[0].Number, cs[0].State, want), nil
	case "change.mergeable":
		cs, err := e.changes(e.argString(x.args[0]))
		if err != nil {
			return false, "", err
		}
		for _, c := range cs {
			if c.State != "open" {
				continue
			}
			target, err := gitx.RevParse(e.Ctx, rp, c.TargetRef)
			if err != nil {
				return false, "", err
			}
			// the live head of the source ref: a push creates a new patch set
			// and the stored head may lag behind it
			head := c.HeadOID
			if live, err := gitx.RevParse(e.Ctx, rp, c.SourceRef); err == nil && live != "" {
				head = live
			}
			_, conflicts, err := gitx.MergeTree(e.Ctx, rp, target, head)
			if errors.Is(err, gitx.ErrConflict) {
				return false, fmt.Sprintf("change #%d conflicts with %s in %s", c.Number, c.TargetRef, strings.TrimSpace(conflicts)), nil
			}
			if err != nil {
				return false, "", err
			}
			return true, fmt.Sprintf("change #%d merges cleanly into %s", c.Number, c.TargetRef), nil
		}
		return false, "no open change has a source ref matching " + normalizeRef(e.argString(x.args[0])), nil
	case "change.approved":
		cs, err := e.changes(e.argString(x.args[0]))
		if err != nil {
			return false, "", err
		}
		for _, c := range cs {
			var n int
			if err := e.DB.QueryRow(e.Ctx, `SELECT COUNT(1) FROM reviews WHERE change_id = ? AND verdict = 'approve'`, c.ID).Scan(&n); err != nil {
				return false, "", fmt.Errorf("assert: reviews: %w", err)
			}
			if n > 0 {
				return true, fmt.Sprintf("change #%d has %d approval(s)", c.Number, n), nil
			}
			return false, fmt.Sprintf("change #%d has no approving review yet", c.Number), nil
		}
		return false, "no change has a source ref matching " + normalizeRef(e.argString(x.args[0])), nil
	case "pipeline.green":
		ref, ok, err := e.resolveRef(e.argString(x.args[0]))
		if err != nil {
			return false, "", err
		}
		if !ok {
			return false, "no ref matches " + normalizeRef(e.argString(x.args[0])), nil
		}
		oid, err := gitx.RevParse(e.Ctx, rp, ref)
		if err != nil {
			return false, "", err
		}
		var status, id string
		err = e.DB.QueryRow(e.Ctx, `SELECT id, status FROM pipeline_runs WHERE repo_id = ? AND commit_oid = ? ORDER BY created_at DESC LIMIT 1`, e.Repo.ID, oid).Scan(&id, &status)
		if err != nil {
			return false, fmt.Sprintf("no pipeline run on %s (%s) yet", ref, oid[:8]), nil
		}
		return status == "succeeded", fmt.Sprintf("run %s on %s: %s", id, ref, status), nil
	case "file.exists", "file.contains":
		ref, ok, err := e.resolveRef(e.argString(x.args[0]))
		if err != nil {
			return false, "", err
		}
		if !ok {
			return false, "no ref matches " + normalizeRef(e.argString(x.args[0])), nil
		}
		p := e.argString(x.args[1])
		content, err := gitx.ReadFile(e.Ctx, rp, ref, p)
		if err != nil {
			return false, fmt.Sprintf("%s does not exist at %s", p, ref), nil
		}
		if x.fn == "file.exists" {
			return true, fmt.Sprintf("%s exists at %s", p, ref), nil
		}
		text := e.argString(x.args[2])
		if strings.Contains(string(content), text) {
			return true, fmt.Sprintf("%s at %s contains %q", p, ref, text), nil
		}
		return false, fmt.Sprintf("%s at %s does not contain %q", p, ref, text), nil
	}
	return false, "", fmt.Errorf("assert: unknown function %s", x.fn)
}

func (e *AssertEnv) count(x *AssertExpr) (int, string, error) {
	rp := e.repoPath()
	switch x.fn {
	case "ref.commits_by":
		actor := e.argString(x.args[0])
		refs, err := e.matchingRefs(e.argString(x.args[1]))
		if err != nil {
			return 0, "", err
		}
		def := "refs/heads/" + e.Repo.DefaultBranch
		total := 0
		var details []string
		for _, ref := range refs {
			if ref == def {
				continue
			}
			var kind, id string
			err := e.DB.QueryRow(e.Ctx, `SELECT actor_kind, actor_id FROM ref_updates WHERE repo_id = ? AND ref = ? ORDER BY created_at DESC, id DESC LIMIT 1`, e.Repo.ID, ref).Scan(&kind, &id)
			if err != nil {
				details = append(details, ref+": no recorded push")
				continue
			}
			if id != actor {
				details = append(details, fmt.Sprintf("%s: last pushed by %s/%s, not %s", ref, kind, id, actor))
				continue
			}
			out, err := gitx.Run(e.Ctx, rp, nil, "rev-list", "--count", def+".."+ref)
			if err != nil {
				out, err = gitx.Run(e.Ctx, rp, nil, "rev-list", "--count", ref)
				if err != nil {
					return 0, "", err
				}
			}
			n, _ := strconv.Atoi(strings.TrimSpace(out))
			total += n
			details = append(details, fmt.Sprintf("%s: %d commit(s) beyond %s pushed by %s", ref, n, e.Repo.DefaultBranch, actor))
		}
		if len(refs) == 0 {
			details = append(details, "no ref matches "+normalizeRef(e.argString(x.args[1])))
		}
		return total, strings.Join(details, "; "), nil
	}
	return 0, "", fmt.Errorf("assert: %s is not a counting function", x.fn)
}
