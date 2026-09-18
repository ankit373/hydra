// SPDX-License-Identifier: MIT

package signals

import (
	"strings"
	"testing"
)

func mustParse(t *testing.T, src string, sc Schema) *node {
	t.Helper()
	n, err := parseExpr(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	if err := check(n, sc); err != nil {
		t.Fatalf("check %q: %v", src, err)
	}
	return n
}

func testSchema() Schema {
	return Schema{
		"a.matched": KindBool,
		"b.matched": KindBool,
		"n.count":   KindNumber,
		"s.name":    KindString,
	}
}

func TestExpr_Evaluates(t *testing.T) {
	sc := testSchema()
	vals := Set{"a.matched": true, "b.matched": false, "n.count": float64(20), "s.name": "go"}

	cases := map[string]bool{
		"a.matched":               true,
		"b.matched":               false,
		"!b.matched":              true,
		"a.matched && b.matched":  false,
		"a.matched || b.matched":  true,
		"a.matched && !b.matched": true,
		"n.count > 10":            true,
		"n.count > 20":            false,
		"n.count >= 20":           true,
		"n.count < 10":            false,
		"n.count <= 20":           true,
		"n.count == 20":           true,
		"n.count != 20":           false,
		`s.name == "go"`:          true,
		`s.name != "go"`:          false,
		"(a.matched || b.matched) && n.count > 5": true,
		"a.matched && (b.matched || n.count > 5)": true,
		"!(a.matched && b.matched)":               true,
		"true":                                    true,
		"false":                                   false,
		"true && !false":                          true,
	}
	for src, want := range cases {
		got, err := evalBool(mustParse(t, src, sc), vals)
		if err != nil {
			t.Errorf("%q: %v", src, err)
			continue
		}
		if got != want {
			t.Errorf("%q = %v, want %v", src, got, want)
		}
	}
}

// && binds tighter than ||, so "a || b && c" is "a || (b && c)".
func TestExpr_Precedence(t *testing.T) {
	sc := Schema{"a": KindBool, "b": KindBool, "c": KindBool}
	n := mustParse(t, "a || b && c", sc)
	got, err := evalBool(n, Set{"a": true, "b": true, "c": false})
	if err != nil || !got {
		t.Fatalf("got %v %v; && must bind tighter than ||", got, err)
	}
	n2 := mustParse(t, "(a || b) && c", sc)
	got2, _ := evalBool(n2, Set{"a": true, "b": true, "c": false})
	if got2 {
		t.Fatal("parens must override precedence")
	}
}

// A signal the set does not carry is its type's zero, so a provider that could
// not run leaves a rule unmatched rather than failing the dispatch.
func TestExpr_AbsentSignalIsTheTypeZero(t *testing.T) {
	sc := testSchema()
	empty := Set{}

	if got, _ := evalBool(mustParse(t, "a.matched", sc), empty); got {
		t.Error("an absent bool must read false")
	}
	if got, _ := evalBool(mustParse(t, "n.count > 0", sc), empty); got {
		t.Error("an absent number must read 0")
	}
	if got, _ := evalBool(mustParse(t, `s.name == ""`, sc), empty); !got {
		t.Error("an absent string must read empty")
	}
}

func TestExpr_ParseErrors(t *testing.T) {
	for _, src := range []string{
		"a.matched &&",
		"&& a.matched",
		"(a.matched",
		"a.matched)",
		`"unterminated`,
		"a.matched @ b.matched",
		"",
		"a.matched b.matched",
	} {
		if _, err := parseExpr(src); err == nil {
			t.Errorf("%q parsed, want an error", src)
		}
	}
}

// "a < b < c" parses in languages that allow it and means something nobody
// intends, so it is a parse error here.
func TestExpr_ComparisonIsNotAssociative(t *testing.T) {
	if _, err := parseExpr("n.count < 5 < 10"); err == nil {
		t.Fatal("chained comparison must not parse")
	}
}

func TestExpr_CheckErrors(t *testing.T) {
	sc := testSchema()
	cases := map[string]string{
		"nope.matched":         "unknown signal",
		"n.count && a.matched": "needs bools",
		"!n.count":             "needs a bool",
		`n.count > "go"`:       "compares",
		`s.name > "go"`:        "needs numbers",
		"a.matched == n.count": "compares",
	}
	for src, want := range cases {
		n, err := parseExpr(src)
		if err != nil {
			t.Errorf("%q: unexpected parse error %v", src, err)
			continue
		}
		err = check(n, sc)
		if err == nil {
			t.Errorf("%q checked clean, want %q", src, want)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %q, want it to mention %q", src, err, want)
		}
	}
}

// A bare number or string is a valid expression but not a condition, and a rule
// whose `when` is not a condition is a mistake worth naming at load.
func TestExpr_NonBoolTopLevelIsDetectable(t *testing.T) {
	n := mustParse(t, "n.count", testSchema())
	if n.kind == KindBool {
		t.Fatal("a number must not type as bool")
	}
}

func TestExpr_LexesLongestOperatorFirst(t *testing.T) {
	sc := Schema{"n": KindNumber}
	// ">=" must not lex as ">" then "=".
	if _, err := parseExpr("n >= 1"); err != nil {
		t.Fatalf(">= did not lex: %v", err)
	}
	got, err := evalBool(mustParse(t, "n >= 1", sc), Set{"n": float64(1)})
	if err != nil || !got {
		t.Fatalf("n>=1 with n=1 gave %v %v", got, err)
	}
}

func TestExpr_IdentifiersAreCollected(t *testing.T) {
	n := mustParse(t, "a.matched && (b.matched || n.count > 3)", testSchema())
	set := map[string]struct{}{}
	identifiers(n, set)
	for _, want := range []string{"a.matched", "b.matched", "n.count"} {
		if _, ok := set[want]; !ok {
			t.Errorf("missing %q in %v", want, set)
		}
	}
	if len(set) != 3 {
		t.Errorf("got %d identifiers, want 3: %v", len(set), set)
	}
}

func TestKind_String(t *testing.T) {
	for k, want := range map[Kind]string{KindBool: "bool", KindNumber: "number", KindString: "string"} {
		if got := k.String(); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}
