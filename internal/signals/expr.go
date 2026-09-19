// SPDX-License-Identifier: MIT

package signals

import (
	"fmt"
	"strconv"
	"strings"
)

// Kind is a value's type. The language has three and no way to add a fourth,
// which is what keeps it checkable at load rather than at dispatch time.
type Kind int

const (
	KindBool Kind = iota
	KindNumber
	KindString
)

func (k Kind) String() string {
	switch k {
	case KindNumber:
		return "number"
	case KindString:
		return "string"
	}
	return "bool"
}

// Schema declares every signal a rule may name, and its type. A rule naming
// anything else fails at load: a typo that quietly evaluates false looks
// exactly like a rule that works.
type Schema map[string]Kind

// node is one expression. Deliberately not an interface with user-supplied
// implementations: the language is closed, so the evaluator can be exhaustive.
type node struct {
	op    string // "||" "&&" "!" "==" "!=" "<" "<=" ">" ">=" "id" "num" "str" "bool"
	left  *node
	right *node

	name string  // op == "id"
	num  float64 // op == "num"
	str  string  // op == "str"
	b    bool    // op == "bool"

	kind Kind // result type, filled by check
}

// --- lexer ---

type token struct {
	kind string // "ident" "number" "string" "op" "eof"
	text string
	pos  int
}

func lex(src string) ([]token, error) {
	var out []token
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(' || c == ')':
			out = append(out, token{kind: "op", text: string(c), pos: i})
			i++
		case c == '"' || c == '\'':
			quote := c
			j := i + 1
			for j < len(src) && src[j] != quote {
				j++
			}
			if j >= len(src) {
				return nil, fmt.Errorf("unterminated string at %d", i)
			}
			out = append(out, token{kind: "string", text: src[i+1 : j], pos: i})
			i = j + 1
		case c >= '0' && c <= '9':
			j := i
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.') {
				j++
			}
			out = append(out, token{kind: "number", text: src[i:j], pos: i})
			i = j
		case isIdentStart(c):
			j := i
			for j < len(src) && isIdentChar(src[j]) {
				j++
			}
			out = append(out, token{kind: "ident", text: src[i:j], pos: i})
			i = j
		default:
			if op, n := lexOp(src[i:]); n > 0 {
				out = append(out, token{kind: "op", text: op, pos: i})
				i += n
				continue
			}
			return nil, fmt.Errorf("unexpected character %q at %d", c, i)
		}
	}
	return append(out, token{kind: "eof", pos: len(src)}), nil
}

// lexOp matches the longest operator first, so ">=" is never read as ">" then
// "=", which would parse and mean something else.
func lexOp(s string) (string, int) {
	for _, op := range []string{"&&", "||", "==", "!=", ">=", "<=", "!", ">", "<"} {
		if strings.HasPrefix(s, op) {
			return op, len(op)
		}
	}
	return "", 0
}

func isIdentStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}

// isIdentChar allows dots, so "pii.aws_access_key_id.matched" is one identifier
// rather than a field access the language would have to define.
func isIdentChar(c byte) bool {
	return isIdentStart(c) || c >= '0' && c <= '9' || c == '.' || c == '-'
}

// --- parser ---

type parser struct {
	toks []token
	i    int
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) next() token { t := p.toks[p.i]; p.i++; return t }
func (p *parser) at(s string) bool {
	t := p.peek()
	return t.kind == "op" && t.text == s
}

// parseExpr builds an expression tree. It does not resolve signal names; check
// does that, so a caller can parse once and validate against a schema the
// rules file itself contributes to.
func parseExpr(src string) (*node, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	n, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != "eof" {
		return nil, fmt.Errorf("unexpected %q at %d", p.peek().text, p.peek().pos)
	}
	return n, nil
}

func (p *parser) parseOr() (*node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.at("||") {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &node{op: "||", left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseAnd() (*node, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for p.at("&&") {
		p.next()
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = &node{op: "&&", left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseUnary() (*node, error) {
	if p.at("!") {
		p.next()
		inner, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &node{op: "!", left: inner}, nil
	}
	return p.parseComparison()
}

func (p *parser) parseComparison() (*node, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for _, op := range []string{"==", "!=", ">=", "<=", ">", "<"} {
		if p.at(op) {
			p.next()
			right, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			// Non-associative on purpose: "a < b < c" is a bug in every
			// language that allows it, so it is a parse error here.
			return &node{op: op, left: left, right: right}, nil
		}
	}
	return left, nil
}

func (p *parser) parsePrimary() (*node, error) {
	t := p.next()
	switch {
	case t.kind == "op" && t.text == "(":
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if !p.at(")") {
			return nil, fmt.Errorf("missing ) at %d", p.peek().pos)
		}
		p.next()
		return inner, nil
	case t.kind == "number":
		f, err := strconv.ParseFloat(t.text, 64)
		if err != nil {
			return nil, fmt.Errorf("bad number %q at %d", t.text, t.pos)
		}
		return &node{op: "num", num: f, kind: KindNumber}, nil
	case t.kind == "string":
		return &node{op: "str", str: t.text, kind: KindString}, nil
	case t.kind == "ident":
		switch t.text {
		case "true", "false":
			return &node{op: "bool", b: t.text == "true", kind: KindBool}, nil
		}
		return &node{op: "id", name: t.text}, nil
	}
	return nil, fmt.Errorf("unexpected %q at %d", t.text, t.pos)
}

// --- type check ---

// check resolves identifiers against the schema and types the tree, so a rule
// comparing a number to a string, or naming a signal nobody provides, is
// refused at load rather than evaluating false forever.
func check(n *node, sc Schema) error {
	switch n.op {
	case "num", "str", "bool":
		return nil
	case "id":
		k, ok := sc[n.name]
		if !ok {
			return fmt.Errorf("unknown signal %q", n.name)
		}
		n.kind = k
		return nil
	case "!":
		if err := check(n.left, sc); err != nil {
			return err
		}
		if n.left.kind != KindBool {
			return fmt.Errorf("! needs a bool, got %s", n.left.kind)
		}
		n.kind = KindBool
		return nil
	case "&&", "||":
		for _, side := range []*node{n.left, n.right} {
			if err := check(side, sc); err != nil {
				return err
			}
			if side.kind != KindBool {
				return fmt.Errorf("%s needs bools, got %s", n.op, side.kind)
			}
		}
		n.kind = KindBool
		return nil
	case "==", "!=", "<", "<=", ">", ">=":
		if err := check(n.left, sc); err != nil {
			return err
		}
		if err := check(n.right, sc); err != nil {
			return err
		}
		if n.left.kind != n.right.kind {
			return fmt.Errorf("%s compares %s with %s", n.op, n.left.kind, n.right.kind)
		}
		if n.op != "==" && n.op != "!=" && n.left.kind != KindNumber {
			return fmt.Errorf("%s needs numbers, got %s", n.op, n.left.kind)
		}
		n.kind = KindBool
		return nil
	}
	return fmt.Errorf("unknown operator %q", n.op)
}

// identifiers lists every signal an expression names, for reporting a rule
// that can never fire because nothing supplies its inputs.
func identifiers(n *node, into map[string]struct{}) {
	if n == nil {
		return
	}
	if n.op == "id" {
		into[n.name] = struct{}{}
	}
	identifiers(n.left, into)
	identifiers(n.right, into)
}

// --- evaluation ---

// eval runs a checked tree against a set of signal values.
//
// A signal the set does not carry is its type's zero, not an error: a provider
// that could not run (no code graph, no calibration store) must leave a rule
// unmatched rather than fail the dispatch it was only advising.
func eval(n *node, vals Set) (any, error) {
	switch n.op {
	case "num":
		return n.num, nil
	case "str":
		return n.str, nil
	case "bool":
		return n.b, nil
	case "id":
		if v, ok := vals[n.name]; ok {
			return v, nil
		}
		switch n.kind {
		case KindNumber:
			return float64(0), nil
		case KindString:
			return "", nil
		}
		return false, nil
	case "!":
		v, err := evalBool(n.left, vals)
		return !v, err
	case "&&":
		l, err := evalBool(n.left, vals)
		if err != nil || !l {
			return false, err
		}
		return evalBool(n.right, vals)
	case "||":
		l, err := evalBool(n.left, vals)
		if err != nil || l {
			return true, err
		}
		return evalBool(n.right, vals)
	}

	l, err := eval(n.left, vals)
	if err != nil {
		return nil, err
	}
	r, err := eval(n.right, vals)
	if err != nil {
		return nil, err
	}
	return compare(n.op, l, r)
}

func evalBool(n *node, vals Set) (bool, error) {
	v, err := eval(n, vals)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("expected a bool, got %T", v)
	}
	return b, nil
}

func compare(op string, l, r any) (any, error) {
	switch op {
	case "==":
		return l == r, nil
	case "!=":
		return l != r, nil
	}
	ln, lok := l.(float64)
	rn, rok := r.(float64)
	if !lok || !rok {
		return nil, fmt.Errorf("%s needs numbers, got %T and %T", op, l, r)
	}
	switch op {
	case "<":
		return ln < rn, nil
	case "<=":
		return ln <= rn, nil
	case ">":
		return ln > rn, nil
	case ">=":
		return ln >= rn, nil
	}
	return nil, fmt.Errorf("unknown operator %q", op)
}
