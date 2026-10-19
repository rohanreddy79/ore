// SPDX-License-Identifier: AGPL-3.0-only

package pipelines

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// This file is the expression language of IR conditions and of `${{ }}`
// interpolations: the GitHub Actions expression grammar, which is the
// CEL-like subset of literals, property access, indexing, the operators
// ! == != < <= > >= && ||, parentheses and function calls. Values are nil,
// bool, float64, string, map[string]any and []any, as JSON would give them.

// Func is a function callable from an expression.
type Func func(args []any) (any, error)

// ExprEnv is the evaluation environment: named contexts (github, env,
// matrix, steps, needs, secrets, ...) and functions (success(), failure(),
// hashFiles(), ...). Lookups are case-insensitive like Actions.
type ExprEnv struct {
	Contexts map[string]any
	Funcs    map[string]Func
}

// NewExprEnv returns an environment with the built-in functions.
func NewExprEnv() *ExprEnv {
	e := &ExprEnv{Contexts: map[string]any{}, Funcs: map[string]Func{}}
	for name, fn := range builtinFuncs {
		e.Funcs[name] = fn
	}
	// Status functions default to a healthy job; executors override them.
	e.Funcs["success"] = func([]any) (any, error) { return true, nil }
	e.Funcs["failure"] = func([]any) (any, error) { return false, nil }
	e.Funcs["always"] = func([]any) (any, error) { return true, nil }
	e.Funcs["cancelled"] = func([]any) (any, error) { return false, nil }
	return e
}

// ---- lexer ----

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tNumber
	tString
	tLParen
	tRParen
	tLBracket
	tRBracket
	tDot
	tComma
	tNot
	tEq
	tNe
	tLt
	tLe
	tGt
	tGe
	tAnd
	tOr
	tStar
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
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(':
			toks = append(toks, token{tLParen, "(", i})
			i++
		case c == ')':
			toks = append(toks, token{tRParen, ")", i})
			i++
		case c == '[':
			toks = append(toks, token{tLBracket, "[", i})
			i++
		case c == ']':
			toks = append(toks, token{tRBracket, "]", i})
			i++
		case c == '.':
			toks = append(toks, token{tDot, ".", i})
			i++
		case c == ',':
			toks = append(toks, token{tComma, ",", i})
			i++
		case c == '*':
			toks = append(toks, token{tStar, "*", i})
			i++
		case c == '!':
			if i+1 < len(src) && src[i+1] == '=' {
				toks = append(toks, token{tNe, "!=", i})
				i += 2
			} else {
				toks = append(toks, token{tNot, "!", i})
				i++
			}
		case c == '=':
			if i+1 < len(src) && src[i+1] == '=' {
				toks = append(toks, token{tEq, "==", i})
				i += 2
			} else {
				return nil, fmt.Errorf("unexpected '=' at %d (did you mean '==')", i)
			}
		case c == '<':
			if i+1 < len(src) && src[i+1] == '=' {
				toks = append(toks, token{tLe, "<=", i})
				i += 2
			} else {
				toks = append(toks, token{tLt, "<", i})
				i++
			}
		case c == '>':
			if i+1 < len(src) && src[i+1] == '=' {
				toks = append(toks, token{tGe, ">=", i})
				i += 2
			} else {
				toks = append(toks, token{tGt, ">", i})
				i++
			}
		case c == '&':
			if i+1 < len(src) && src[i+1] == '&' {
				toks = append(toks, token{tAnd, "&&", i})
				i += 2
			} else {
				return nil, fmt.Errorf("unexpected '&' at %d", i)
			}
		case c == '|':
			if i+1 < len(src) && src[i+1] == '|' {
				toks = append(toks, token{tOr, "||", i})
				i += 2
			} else {
				return nil, fmt.Errorf("unexpected '|' at %d", i)
			}
		case c == '\'':
			j := i + 1
			var b strings.Builder
			for {
				if j >= len(src) {
					return nil, fmt.Errorf("unterminated string at %d", i)
				}
				if src[j] == '\'' {
					if j+1 < len(src) && src[j+1] == '\'' {
						b.WriteByte('\'')
						j += 2
						continue
					}
					j++
					break
				}
				b.WriteByte(src[j])
				j++
			}
			toks = append(toks, token{tString, b.String(), i})
			i = j
		case c == '"':
			return nil, fmt.Errorf("double-quoted string at %d: expressions use single quotes", i)
		case c >= '0' && c <= '9' || (c == '-' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9'):
			j := i + 1
			for j < len(src) && (isIdentChar(src[j]) || src[j] == '.' || ((src[j] == '+' || src[j] == '-') && (src[j-1] == 'e' || src[j-1] == 'E'))) {
				j++
			}
			toks = append(toks, token{tNumber, src[i:j], i})
			i = j
		case isIdentStart(c):
			j := i + 1
			for j < len(src) && isIdentChar(src[j]) {
				j++
			}
			toks = append(toks, token{tIdent, src[i:j], i})
			i = j
		default:
			return nil, fmt.Errorf("unexpected character %q at %d", c, i)
		}
	}
	toks = append(toks, token{tEOF, "", len(src)})
	return toks, nil
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isIdentChar(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' || c == '-' }

// ---- parser ----

type nodeKind int

const (
	nLit nodeKind = iota
	nIdent
	nProp
	nIndex
	nCall
	nNot
	nBinary
	nFilter // obj.* (object filter); parsed so it can be reported, not evaluated
)

// Expr is a parsed expression.
type Expr struct {
	kind  nodeKind
	lit   any
	name  string
	op    tokKind
	left  *Expr
	right *Expr
	args  []*Expr
	src   string
}

type parser struct {
	toks []token
	i    int
	src  string
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) next() token { t := p.toks[p.i]; p.i++; return t }

func (p *parser) expect(k tokKind, what string) (token, error) {
	t := p.next()
	if t.kind != k {
		return t, fmt.Errorf("expected %s at %d, got %q", what, t.pos, t.text)
	}
	return t, nil
}

// ParseExpr parses an expression (without the ${{ }} delimiters).
func ParseExpr(src string) (*Expr, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, src: src}
	e, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tEOF {
		return nil, fmt.Errorf("unexpected %q at %d", p.peek().text, p.peek().pos)
	}
	e.src = src
	return e, nil
}

func (p *parser) parseOr() (*Expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tOr {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &Expr{kind: nBinary, op: tOr, left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseAnd() (*Expr, error) {
	left, err := p.parseCmp()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tAnd {
		p.next()
		right, err := p.parseCmp()
		if err != nil {
			return nil, err
		}
		left = &Expr{kind: nBinary, op: tAnd, left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseCmp() (*Expr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		k := p.peek().kind
		if k != tEq && k != tNe && k != tLt && k != tLe && k != tGt && k != tGe {
			return left, nil
		}
		p.next()
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = &Expr{kind: nBinary, op: k, left: left, right: right}
	}
}

func (p *parser) parseUnary() (*Expr, error) {
	if p.peek().kind == tNot {
		p.next()
		e, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &Expr{kind: nNot, left: e}, nil
	}
	return p.parsePostfix()
}

func (p *parser) parsePostfix() (*Expr, error) {
	e, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for {
		switch p.peek().kind {
		case tDot:
			p.next()
			t := p.next()
			switch t.kind {
			case tIdent:
				e = &Expr{kind: nProp, left: e, name: t.text}
			case tStar:
				e = &Expr{kind: nFilter, left: e}
			default:
				return nil, fmt.Errorf("expected property name at %d", t.pos)
			}
		case tLBracket:
			p.next()
			idx, err := p.parseOr()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(tRBracket, "']'"); err != nil {
				return nil, err
			}
			e = &Expr{kind: nIndex, left: e, right: idx}
		case tLParen:
			if e.kind != nIdent {
				return nil, fmt.Errorf("call of a non-function at %d", p.peek().pos)
			}
			p.next()
			call := &Expr{kind: nCall, name: e.name}
			for p.peek().kind != tRParen {
				a, err := p.parseOr()
				if err != nil {
					return nil, err
				}
				call.args = append(call.args, a)
				if p.peek().kind == tComma {
					p.next()
					continue
				}
				if p.peek().kind != tRParen {
					return nil, fmt.Errorf("expected ',' or ')' at %d", p.peek().pos)
				}
			}
			p.next()
			e = call
		default:
			return e, nil
		}
	}
}

func (p *parser) parsePrimary() (*Expr, error) {
	t := p.next()
	switch t.kind {
	case tNumber:
		f, err := parseNumber(t.text)
		if err != nil {
			return nil, fmt.Errorf("bad number %q at %d", t.text, t.pos)
		}
		return &Expr{kind: nLit, lit: f}, nil
	case tString:
		return &Expr{kind: nLit, lit: t.text}, nil
	case tIdent:
		switch strings.ToLower(t.text) {
		case "true":
			return &Expr{kind: nLit, lit: true}, nil
		case "false":
			return &Expr{kind: nLit, lit: false}, nil
		case "null":
			return &Expr{kind: nLit, lit: nil}, nil
		}
		return &Expr{kind: nIdent, name: t.text}, nil
	case tLParen:
		e, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tRParen, "')'"); err != nil {
			return nil, err
		}
		return e, nil
	case tEOF:
		return nil, errors.New("unexpected end of expression")
	}
	return nil, fmt.Errorf("unexpected %q at %d", t.text, t.pos)
}

func parseNumber(s string) (float64, error) {
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		n, err := strconv.ParseInt(s[2:], 16, 64)
		return float64(n), err
	}
	return strconv.ParseFloat(s, 64)
}

// ---- analysis ----

// Analysis is what a static pass learns about an expression.
type Analysis struct {
	Contexts  []string // root identifiers, lower-case, sorted, unique
	Functions []string // function names, lower-case, sorted, unique
	Refs      []string // dotted chains such as secrets.TOKEN or needs.build.outputs.x
	HasFilter bool     // uses the obj.* object filter
}

// Analyze parses src and reports the contexts, functions and references it uses.
func Analyze(src string) (*Analysis, error) {
	e, err := ParseExpr(src)
	if err != nil {
		return nil, err
	}
	a := &Analysis{}
	ctxs, funcs, refs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	var walk func(n *Expr) string
	walk = func(n *Expr) string {
		if n == nil {
			return ""
		}
		switch n.kind {
		case nIdent:
			ctxs[strings.ToLower(n.name)] = true
			return n.name
		case nProp:
			chain := walk(n.left)
			if chain != "" {
				chain += "." + n.name
				refs[chain] = true
			}
			return chain
		case nIndex:
			chain := walk(n.left)
			walk(n.right)
			if chain != "" {
				if n.right.kind == nLit {
					chain += "." + ToString(n.right.lit)
					refs[chain] = true
				}
			}
			return chain
		case nCall:
			funcs[strings.ToLower(n.name)] = true
			for _, arg := range n.args {
				walk(arg)
			}
		case nNot:
			walk(n.left)
		case nBinary:
			walk(n.left)
			walk(n.right)
		case nFilter:
			a.HasFilter = true
			walk(n.left)
		}
		return ""
	}
	walk(e)
	for k := range ctxs {
		a.Contexts = append(a.Contexts, k)
	}
	for k := range funcs {
		a.Functions = append(a.Functions, k)
	}
	for k := range refs {
		a.Refs = append(a.Refs, k)
	}
	sort.Strings(a.Contexts)
	sort.Strings(a.Functions)
	sort.Strings(a.Refs)
	// Keep only the longest chains with a given prefix? No: every prefix is
	// useful (secrets.X is wanted even when secrets.X.y is written).
	return a, nil
}

// ---- evaluation ----

// Eval evaluates src in env.
func (env *ExprEnv) Eval(src string) (any, error) {
	e, err := ParseExpr(src)
	if err != nil {
		return nil, err
	}
	return env.eval(e)
}

// EvalBool evaluates src and reports its truthiness.
func (env *ExprEnv) EvalBool(src string) (bool, error) {
	v, err := env.Eval(src)
	if err != nil {
		return false, err
	}
	return Truthy(v), nil
}

func (env *ExprEnv) eval(n *Expr) (any, error) {
	switch n.kind {
	case nLit:
		return n.lit, nil
	case nIdent:
		v, _ := lookupFold(env.Contexts, n.name)
		return v, nil
	case nProp:
		base, err := env.eval(n.left)
		if err != nil {
			return nil, err
		}
		return property(base, n.name), nil
	case nIndex:
		base, err := env.eval(n.left)
		if err != nil {
			return nil, err
		}
		idx, err := env.eval(n.right)
		if err != nil {
			return nil, err
		}
		switch b := base.(type) {
		case []any:
			f, ok := idx.(float64)
			if !ok {
				return nil, nil
			}
			i := int(f)
			if i < 0 || i >= len(b) {
				return nil, nil
			}
			return b[i], nil
		case map[string]any:
			return property(b, ToString(idx)), nil
		}
		return nil, nil
	case nCall:
		fn, ok := lookupFunc(env.Funcs, n.name)
		if !ok {
			return nil, fmt.Errorf("unknown function %s()", n.name)
		}
		args := make([]any, 0, len(n.args))
		for _, a := range n.args {
			v, err := env.eval(a)
			if err != nil {
				return nil, err
			}
			args = append(args, v)
		}
		return fn(args)
	case nNot:
		v, err := env.eval(n.left)
		if err != nil {
			return nil, err
		}
		return !Truthy(v), nil
	case nFilter:
		return nil, errors.New("object filters (obj.*) are not supported")
	case nBinary:
		left, err := env.eval(n.left)
		if err != nil {
			return nil, err
		}
		switch n.op {
		case tAnd:
			if !Truthy(left) {
				return left, nil
			}
			return env.eval(n.right)
		case tOr:
			if Truthy(left) {
				return left, nil
			}
			return env.eval(n.right)
		}
		right, err := env.eval(n.right)
		if err != nil {
			return nil, err
		}
		switch n.op {
		case tEq:
			return looseEqual(left, right), nil
		case tNe:
			return !looseEqual(left, right), nil
		case tLt:
			return compare(left, right) < 0, nil
		case tLe:
			return compare(left, right) <= 0, nil
		case tGt:
			return compare(left, right) > 0, nil
		case tGe:
			return compare(left, right) >= 0, nil
		}
	}
	return nil, fmt.Errorf("cannot evaluate node kind %d", n.kind)
}

func lookupFold(m map[string]any, key string) (any, bool) {
	if v, ok := m[key]; ok {
		return v, true
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return nil, false
}

func lookupFunc(m map[string]Func, name string) (Func, bool) {
	if f, ok := m[name]; ok {
		return f, true
	}
	for k, f := range m {
		if strings.EqualFold(k, name) {
			return f, true
		}
	}
	return nil, false
}

func property(base any, name string) any {
	m, ok := base.(map[string]any)
	if !ok {
		return nil
	}
	v, _ := lookupFold(m, name)
	return v
}

// Truthy follows the Actions rules: false, null, 0, NaN and ” are false.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	}
	return true
}

func toNumber(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case float64:
		return x
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0
		}
		f, err := parseNumber(s)
		if err != nil {
			return math.NaN()
		}
		return f
	}
	return math.NaN()
}

func looseEqual(a, b any) bool {
	as, aok := a.(string)
	bs, bok := b.(string)
	if aok && bok {
		return strings.EqualFold(as, bs)
	}
	if a == nil && b == nil {
		return true
	}
	switch a.(type) {
	case map[string]any, []any:
		return false
	}
	switch b.(type) {
	case map[string]any, []any:
		return false
	}
	x, y := toNumber(a), toNumber(b)
	if math.IsNaN(x) || math.IsNaN(y) {
		return false
	}
	return x == y
}

func compare(a, b any) int {
	as, aok := a.(string)
	bs, bok := b.(string)
	if aok && bok {
		return strings.Compare(strings.ToLower(as), strings.ToLower(bs))
	}
	x, y := toNumber(a), toNumber(b)
	switch {
	case math.IsNaN(x) || math.IsNaN(y):
		return 2 // every comparison false except we return non-zero; callers test < <= > >=
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

// ToString renders a value the way Actions does in interpolations.
func ToString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		return x
	case map[string]any:
		return "Object"
	case []any:
		return "Array"
	case int:
		return strconv.Itoa(x)
	}
	return fmt.Sprint(v)
}

var builtinFuncs = map[string]Func{
	"contains": func(args []any) (any, error) {
		if len(args) != 2 {
			return nil, errors.New("contains() takes 2 arguments")
		}
		if arr, ok := args[0].([]any); ok {
			for _, el := range arr {
				if looseEqual(el, args[1]) {
					return true, nil
				}
			}
			return false, nil
		}
		return strings.Contains(strings.ToLower(ToString(args[0])), strings.ToLower(ToString(args[1]))), nil
	},
	"startswith": func(args []any) (any, error) {
		if len(args) != 2 {
			return nil, errors.New("startsWith() takes 2 arguments")
		}
		return strings.HasPrefix(strings.ToLower(ToString(args[0])), strings.ToLower(ToString(args[1]))), nil
	},
	"endswith": func(args []any) (any, error) {
		if len(args) != 2 {
			return nil, errors.New("endsWith() takes 2 arguments")
		}
		return strings.HasSuffix(strings.ToLower(ToString(args[0])), strings.ToLower(ToString(args[1]))), nil
	},
	"format": func(args []any) (any, error) {
		if len(args) < 1 {
			return nil, errors.New("format() needs a format string")
		}
		f := ToString(args[0])
		var b strings.Builder
		for i := 0; i < len(f); i++ {
			c := f[i]
			if c == '{' {
				if i+1 < len(f) && f[i+1] == '{' {
					b.WriteByte('{')
					i++
					continue
				}
				j := strings.IndexByte(f[i:], '}')
				if j < 0 {
					return nil, errors.New("format(): unclosed '{'")
				}
				n, err := strconv.Atoi(f[i+1 : i+j])
				if err != nil || n+1 >= len(args) {
					return nil, fmt.Errorf("format(): bad placeholder %q", f[i:i+j+1])
				}
				b.WriteString(ToString(args[n+1]))
				i += j
				continue
			}
			if c == '}' && i+1 < len(f) && f[i+1] == '}' {
				b.WriteByte('}')
				i++
				continue
			}
			b.WriteByte(c)
		}
		return b.String(), nil
	},
	"join": func(args []any) (any, error) {
		if len(args) < 1 || len(args) > 2 {
			return nil, errors.New("join() takes 1 or 2 arguments")
		}
		sep := ","
		if len(args) == 2 {
			sep = ToString(args[1])
		}
		arr, ok := args[0].([]any)
		if !ok {
			return ToString(args[0]), nil
		}
		parts := make([]string, len(arr))
		for i, el := range arr {
			parts[i] = ToString(el)
		}
		return strings.Join(parts, sep), nil
	},
	"tojson": func(args []any) (any, error) {
		if len(args) != 1 {
			return nil, errors.New("toJSON() takes 1 argument")
		}
		b, err := json.MarshalIndent(args[0], "", "  ")
		return string(b), err
	},
	"fromjson": func(args []any) (any, error) {
		if len(args) != 1 {
			return nil, errors.New("fromJSON() takes 1 argument")
		}
		var v any
		if err := json.Unmarshal([]byte(ToString(args[0])), &v); err != nil {
			return nil, fmt.Errorf("fromJSON(): %w", err)
		}
		return v, nil
	},
}

// ---- interpolation ----

// HasInterpolation reports whether s contains a ${{ }} expression.
func HasInterpolation(s string) bool { return strings.Contains(s, "${{") }

// Interpolations returns the expressions inside each ${{ }} of s, in order.
func Interpolations(s string) ([]string, error) {
	var out []string
	err := walkInterpolations(s, func(lit string) {}, func(expr string) error { out = append(out, expr); return nil })
	return out, err
}

// Interpolate replaces every ${{ expr }} in s with its evaluation.
func (env *ExprEnv) Interpolate(s string) (string, error) {
	if !HasInterpolation(s) {
		return s, nil
	}
	var b strings.Builder
	err := walkInterpolations(s, func(lit string) { b.WriteString(lit) }, func(expr string) error {
		v, err := env.Eval(expr)
		if err != nil {
			return fmt.Errorf("${{ %s }}: %w", strings.TrimSpace(expr), err)
		}
		b.WriteString(ToString(v))
		return nil
	})
	return b.String(), err
}

// InterpolateWith rewrites only the ${{ }} blocks for which keep returns
// false, leaving the others verbatim (compile-time partial evaluation).
func (env *ExprEnv) InterpolateWith(s string, evaluable func(expr string) bool) (string, error) {
	if !HasInterpolation(s) {
		return s, nil
	}
	var b strings.Builder
	err := walkInterpolations(s, func(lit string) { b.WriteString(lit) }, func(expr string) error {
		if !evaluable(expr) {
			b.WriteString("${{" + expr + "}}")
			return nil
		}
		v, err := env.Eval(expr)
		if err != nil {
			return fmt.Errorf("${{ %s }}: %w", strings.TrimSpace(expr), err)
		}
		b.WriteString(ToString(v))
		return nil
	})
	return b.String(), err
}

// walkInterpolations splits s into literal runs and expressions. The closing
// }} is searched outside single-quoted strings.
func walkInterpolations(s string, lit func(string), expr func(string) error) error {
	for {
		i := strings.Index(s, "${{")
		if i < 0 {
			lit(s)
			return nil
		}
		lit(s[:i])
		rest := s[i+3:]
		end := -1
		inStr := false
		for j := 0; j+1 < len(rest); j++ {
			if rest[j] == '\'' {
				inStr = !inStr
				continue
			}
			if !inStr && rest[j] == '}' && rest[j+1] == '}' {
				end = j
				break
			}
		}
		if end < 0 {
			return fmt.Errorf("unterminated ${{ in %q", s)
		}
		if err := expr(rest[:end]); err != nil {
			return err
		}
		s = rest[end+2:]
	}
}

// StripExpr returns the expression inside a value that is exactly one
// ${{ }} block, or the value itself (an `if:` may be written either way).
func StripExpr(s string) string {
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "${{") && strings.HasSuffix(t, "}}") && strings.Count(t, "${{") == 1 {
		return strings.TrimSpace(t[3 : len(t)-2])
	}
	return t
}

// ---- globs and hashFiles ----

// MatchGlob matches a slash-separated path against an Actions-style glob:
// * (within a segment), ? , [...] and ** (zero or more segments).
func MatchGlob(pattern, name string) bool {
	pattern = strings.TrimPrefix(strings.TrimSuffix(pattern, "/"), "./")
	name = strings.TrimPrefix(name, "./")
	if pattern == "" {
		return false
	}
	ps := strings.Split(pattern, "/")
	for i, seg := range ps {
		if seg != "**" && strings.Contains(seg, "**") {
			ps[i] = strings.ReplaceAll(seg, "**", "*")
		}
	}
	return matchSegments(ps, strings.Split(name, "/"))
}

func matchSegments(pat, name []string) bool {
	if len(pat) == 0 {
		return len(name) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(name); i++ {
			if matchSegments(pat[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	if ok, err := path.Match(pat[0], name[0]); err != nil || !ok {
		return false
	}
	return matchSegments(pat[1:], name[1:])
}

// GlobFiles lists the files under root (relative, slash-separated, sorted)
// that match any positive pattern and no `!` pattern. A pattern naming a
// directory selects everything under it; `~`-relative and absolute patterns
// are not resolved here (they are rejected by callers that need them).
func GlobFiles(root string, patterns []string) ([]string, error) {
	var pos, neg []string
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "!") {
			neg = append(neg, strings.TrimPrefix(p, "!"))
		} else {
			pos = append(pos, p)
		}
	}
	if len(pos) == 0 {
		return nil, nil
	}
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !matchesAny(pos, rel) || matchesAny(neg, rel) {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out, err
}

// matchesAny matches a file path against patterns, where a pattern that
// names an ancestor directory matches too.
func matchesAny(patterns []string, rel string) bool {
	for _, p := range patterns {
		if MatchGlob(p, rel) {
			return true
		}
		// directory pattern: dist, dist/, or a glob matching an ancestor
		dir := rel
		for {
			i := strings.LastIndexByte(dir, '/')
			if i < 0 {
				break
			}
			dir = dir[:i]
			if MatchGlob(p, dir) {
				return true
			}
		}
	}
	return false
}

// HashFiles implements hashFiles(): SHA-256 over the SHA-256 of each matched
// file in sorted order; "" when nothing matches.
func HashFiles(root string, patterns ...string) (string, error) {
	files, err := GlobFiles(root, patterns)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", nil
	}
	outer := sha256.New()
	for _, f := range files {
		fh, err := os.Open(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil {
			return "", err
		}
		inner := sha256.New()
		_, err = io.Copy(inner, fh)
		fh.Close()
		if err != nil {
			return "", err
		}
		outer.Write(inner.Sum(nil))
	}
	return hex.EncodeToString(outer.Sum(nil)), nil
}

// HashFilesFunc returns a hashFiles() bound to a workspace.
func HashFilesFunc(root string) Func {
	return func(args []any) (any, error) {
		patterns := make([]string, 0, len(args))
		for _, a := range args {
			patterns = append(patterns, ToString(a))
		}
		return HashFiles(root, patterns...)
	}
}
