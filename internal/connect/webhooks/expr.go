// SPDX-License-Identifier: AGPL-3.0-only

package webhooks

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/orevcs/ore/internal/ledger"
)

// A small expression language for subscription filters (a CEL-like subset,
// standard library only). Fields: type, subject, id, source, time, org, seq,
// actor.kind, actor.id, data.<path>. Literals: "strings", numbers, true,
// false, null, [lists]. Operators: == != < <= > >= in && || ! ( ).
// Functions: startsWith(s, p), endsWith(s, p), contains(s, sub),
// matches(s, glob), has(x), lower(s), size(x).
//
//	type == "io.ore.deployment.succeeded" && data.environment == "production"
//	startsWith(type, "io.ore.change.") && actor.kind in ["agent", "app"]

// Program is a compiled expression.
type Program struct {
	src  string
	root node
}

// Compile parses an expression; a syntax error is returned as is.
func Compile(src string) (*Program, error) {
	p := &parser{toks: nil}
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p.toks = toks
	n, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tkEOF {
		return nil, fmt.Errorf("unexpected %q at %d", p.peek().text, p.peek().pos)
	}
	return &Program{src: src, root: n}, nil
}

// Eval evaluates the program against an event; the result must be a boolean.
func (p *Program) Eval(ev ledger.Event) (bool, error) {
	v, err := p.root.eval(eventScope(ev))
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("expression %q is not boolean (got %T)", p.src, v)
	}
	return b, nil
}

func eventScope(ev ledger.Event) map[string]any {
	var data any
	if len(ev.Data) > 0 {
		json.Unmarshal(ev.Data, &data)
	}
	return map[string]any{
		"type": ev.Type, "subject": ev.Subject, "id": ev.ID, "source": ev.Source, "time": ev.Time,
		"org": ev.OreOrg, "seq": float64(ev.Seq),
		"actor": map[string]any{"kind": ev.OreActor.Kind, "id": ev.OreActor.ID},
		"data":  data,
	}
}

// ---- lexer ----

type tokKind int

const (
	tkEOF tokKind = iota
	tkIdent
	tkString
	tkNumber
	tkOp
)

type token struct {
	kind tokKind
	text string
	pos  int
}

func lex(src string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(src) {
		c := rune(src[i])
		switch {
		case unicode.IsSpace(c):
			i++
		case c == '"' || c == '\'':
			j := i + 1
			var sb strings.Builder
			for j < len(src) && rune(src[j]) != c {
				if src[j] == '\\' && j+1 < len(src) {
					j++
				}
				sb.WriteByte(src[j])
				j++
			}
			if j >= len(src) {
				return nil, fmt.Errorf("unterminated string at %d", i)
			}
			toks = append(toks, token{tkString, sb.String(), i})
			i = j + 1
		case unicode.IsDigit(c):
			j := i
			for j < len(src) && (unicode.IsDigit(rune(src[j])) || src[j] == '.') {
				j++
			}
			toks = append(toks, token{tkNumber, src[i:j], i})
			i = j
		case unicode.IsLetter(c) || c == '_':
			j := i
			for j < len(src) && (unicode.IsLetter(rune(src[j])) || unicode.IsDigit(rune(src[j])) || src[j] == '_') {
				j++
			}
			toks = append(toks, token{tkIdent, src[i:j], i})
			i = j
		default:
			two := ""
			if i+1 < len(src) {
				two = src[i : i+2]
			}
			switch {
			case two == "==" || two == "!=" || two == "<=" || two == ">=" || two == "&&" || two == "||":
				toks = append(toks, token{tkOp, two, i})
				i += 2
			case strings.ContainsRune("<>!()[],.", c):
				toks = append(toks, token{tkOp, string(c), i})
				i++
			default:
				return nil, fmt.Errorf("unexpected character %q at %d", c, i)
			}
		}
	}
	toks = append(toks, token{tkEOF, "", len(src)})
	return toks, nil
}

// ---- parser ----

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) next() token { t := p.toks[p.pos]; p.pos++; return t }
func (p *parser) accept(kind tokKind, text string) bool {
	if t := p.peek(); t.kind == kind && t.text == text {
		p.pos++
		return true
	}
	return false
}

type node interface {
	eval(scope map[string]any) (any, error)
}

type literal struct{ v any }
type field struct{ path []string }
type list struct{ items []node }
type binary struct {
	op   string
	l, r node
}
type unary struct{ x node }
type call struct {
	fn   string
	args []node
}

func (p *parser) parseOr() (node, error) {
	l, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.accept(tkOp, "||") {
		r, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		l = &binary{"||", l, r}
	}
	return l, nil
}

func (p *parser) parseAnd() (node, error) {
	l, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.accept(tkOp, "&&") {
		r, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		l = &binary{"&&", l, r}
	}
	return l, nil
}

func (p *parser) parseNot() (node, error) {
	if p.accept(tkOp, "!") {
		x, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &unary{x}, nil
	}
	return p.parseCmp()
}

func (p *parser) parseCmp() (node, error) {
	l, err := p.parseTerm()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	switch {
	case t.kind == tkOp && (t.text == "==" || t.text == "!=" || t.text == "<" || t.text == "<=" || t.text == ">" || t.text == ">="),
		t.kind == tkIdent && t.text == "in":
		p.next()
		r, err := p.parseTerm()
		if err != nil {
			return nil, err
		}
		return &binary{t.text, l, r}, nil
	}
	return l, nil
}

func (p *parser) parseTerm() (node, error) {
	t := p.next()
	switch t.kind {
	case tkString:
		return &literal{t.text}, nil
	case tkNumber:
		f, err := strconv.ParseFloat(t.text, 64)
		if err != nil {
			return nil, fmt.Errorf("bad number %q at %d", t.text, t.pos)
		}
		return &literal{f}, nil
	case tkIdent:
		switch t.text {
		case "true":
			return &literal{true}, nil
		case "false":
			return &literal{false}, nil
		case "null":
			return &literal{nil}, nil
		}
		if p.accept(tkOp, "(") {
			var args []node
			if !p.accept(tkOp, ")") {
				for {
					a, err := p.parseOr()
					if err != nil {
						return nil, err
					}
					args = append(args, a)
					if p.accept(tkOp, ")") {
						break
					}
					if !p.accept(tkOp, ",") {
						return nil, fmt.Errorf("expected , or ) at %d", p.peek().pos)
					}
				}
			}
			switch t.text {
			case "startsWith", "endsWith", "contains", "matches", "has", "lower", "size":
			default:
				return nil, fmt.Errorf("unknown function %q at %d", t.text, t.pos)
			}
			return &call{t.text, args}, nil
		}
		path := []string{t.text}
		for p.accept(tkOp, ".") {
			n := p.next()
			if n.kind != tkIdent {
				return nil, fmt.Errorf("expected a field name at %d", n.pos)
			}
			path = append(path, n.text)
		}
		switch path[0] {
		case "type", "subject", "id", "source", "time", "org", "seq", "actor", "data":
		default:
			return nil, fmt.Errorf("unknown field %q at %d", t.text, t.pos)
		}
		return &field{path}, nil
	case tkOp:
		switch t.text {
		case "(":
			n, err := p.parseOr()
			if err != nil {
				return nil, err
			}
			if !p.accept(tkOp, ")") {
				return nil, fmt.Errorf("expected ) at %d", p.peek().pos)
			}
			return n, nil
		case "[":
			var items []node
			if !p.accept(tkOp, "]") {
				for {
					it, err := p.parseOr()
					if err != nil {
						return nil, err
					}
					items = append(items, it)
					if p.accept(tkOp, "]") {
						break
					}
					if !p.accept(tkOp, ",") {
						return nil, fmt.Errorf("expected , or ] at %d", p.peek().pos)
					}
				}
			}
			return &list{items}, nil
		}
	}
	if t.kind == tkEOF {
		return nil, fmt.Errorf("unexpected end of expression")
	}
	return nil, fmt.Errorf("unexpected %q at %d", t.text, t.pos)
}

// ---- evaluation ----

func (n *literal) eval(map[string]any) (any, error) { return n.v, nil }

func (n *field) eval(scope map[string]any) (any, error) {
	var cur any = scope
	for _, k := range n.path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, nil
		}
		cur, ok = m[k]
		if !ok {
			return nil, nil
		}
	}
	return cur, nil
}

func (n *list) eval(scope map[string]any) (any, error) {
	out := make([]any, 0, len(n.items))
	for _, it := range n.items {
		v, err := it.eval(scope)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (n *unary) eval(scope map[string]any) (any, error) {
	v, err := n.x.eval(scope)
	if err != nil {
		return nil, err
	}
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("! applied to a non-boolean")
	}
	return !b, nil
}

func (n *binary) eval(scope map[string]any) (any, error) {
	l, err := n.l.eval(scope)
	if err != nil {
		return nil, err
	}
	if n.op == "&&" || n.op == "||" {
		lb, ok := l.(bool)
		if !ok {
			return nil, fmt.Errorf("%s applied to a non-boolean", n.op)
		}
		if n.op == "&&" && !lb {
			return false, nil
		}
		if n.op == "||" && lb {
			return true, nil
		}
		r, err := n.r.eval(scope)
		if err != nil {
			return nil, err
		}
		rb, ok := r.(bool)
		if !ok {
			return nil, fmt.Errorf("%s applied to a non-boolean", n.op)
		}
		return rb, nil
	}
	r, err := n.r.eval(scope)
	if err != nil {
		return nil, err
	}
	switch n.op {
	case "==":
		return equal(l, r), nil
	case "!=":
		return !equal(l, r), nil
	case "in":
		items, ok := r.([]any)
		if !ok {
			if s, ok := r.(string); ok {
				ls, _ := l.(string)
				return strings.Contains(s, ls), nil
			}
			return nil, fmt.Errorf("in: right side is not a list")
		}
		for _, it := range items {
			if equal(l, it) {
				return true, nil
			}
		}
		return false, nil
	case "<", "<=", ">", ">=":
		c, ok := compare(l, r)
		if !ok {
			return nil, fmt.Errorf("%s: incomparable values", n.op)
		}
		switch n.op {
		case "<":
			return c < 0, nil
		case "<=":
			return c <= 0, nil
		case ">":
			return c > 0, nil
		}
		return c >= 0, nil
	}
	return nil, fmt.Errorf("unknown operator %s", n.op)
}

func (n *call) eval(scope map[string]any) (any, error) {
	args := make([]any, len(n.args))
	for i, a := range n.args {
		v, err := a.eval(scope)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	str := func(i int) string {
		if i < len(args) {
			if s, ok := args[i].(string); ok {
				return s
			}
		}
		return ""
	}
	switch n.fn {
	case "has":
		return len(args) == 1 && args[0] != nil, nil
	case "lower":
		return strings.ToLower(str(0)), nil
	case "size":
		if len(args) != 1 {
			return nil, fmt.Errorf("size takes one argument")
		}
		switch v := args[0].(type) {
		case string:
			return float64(len(v)), nil
		case []any:
			return float64(len(v)), nil
		case map[string]any:
			return float64(len(v)), nil
		}
		return float64(0), nil
	}
	if len(args) != 2 {
		return nil, fmt.Errorf("%s takes two arguments", n.fn)
	}
	switch n.fn {
	case "startsWith":
		return strings.HasPrefix(str(0), str(1)), nil
	case "endsWith":
		return strings.HasSuffix(str(0), str(1)), nil
	case "contains":
		return strings.Contains(str(0), str(1)), nil
	case "matches":
		return MatchGlob(str(1), str(0)), nil
	}
	return nil, fmt.Errorf("unknown function %s", n.fn)
}

func equal(a, b any) bool {
	switch av := a.(type) {
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case nil:
		return b == nil
	}
	return false
}

func compare(a, b any) (int, bool) {
	switch av := a.(type) {
	case float64:
		bv, ok := b.(float64)
		if !ok {
			return 0, false
		}
		switch {
		case av < bv:
			return -1, true
		case av > bv:
			return 1, true
		}
		return 0, true
	case string:
		bv, ok := b.(string)
		if !ok {
			return 0, false
		}
		return strings.Compare(av, bv), true
	}
	return 0, false
}
