// SPDX-License-Identifier: MIT

package oracle

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ankit373/hydra/internal/trust"
)

var normaliseRows = []struct{ in, want string }{
	{"Card Arrival.", "card_arrival"},
	{"card_arrival", "card_arrival"},
	{"`card_arrival`", "card_arrival"},
	{`"Card_Arrival"`, "card_arrival"},
	{`'card arrival'.`, "card_arrival"},
	{`"card_arrival".`, "card_arrival"},
	{"card-arrival", "card_arrival"},
	{"  card \t - -  arrival \n", "card_arrival"},
	{"card_ arrival", "card_arrival"},
	{"card _ arrival", "card_arrival"},
	{"card_arrival ", "card_arrival"},
	{"\u00a0card arrival\v\f\u3000", "card_arrival"},
	{"\u201cCard Arrival\u201d", "card_arrival"},
	{"\u2018card_arrival\u2019", "card_arrival"},
	{"\u200bcard_arrival\ufeff", "card_arrival"},
	{"**Card Arrival**", "card_arrival"},
	{"*card_arrival*", "card_arrival"},
	{"__card_arrival__", "card_arrival"},
	{"- card_arrival", "card_arrival"},
	{"* card_arrival", "card_arrival"},
	{"\u2022 card_arrival", "card_arrival"},
	{"- x", "x"},
	{"-1", "-1"},
	{"-1.", "-1"},
	{"'x.'.", "x"},
	{"x..", "x"},
	{"x. .", "x"},
	{"", ""},
	{` "". `, ""},
}

func TestNormaliseLabel(t *testing.T) {
	for _, tc := range normaliseRows {
		if got := NormaliseLabel(tc.in); got != tc.want {
			t.Errorf("NormaliseLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Answer and gold are both normalised; a second pass that changed anything would make a
// grade depend on whether the text had already been normalised upstream.
func TestNormaliseLabel_Idempotent(t *testing.T) {
	inputs := []string{"'x.'.", "x..", "x. .", `"x".`, "oos..", "x. -", "x_.", "-_x_-", "- -1", "x -"}
	for _, tc := range normaliseRows {
		inputs = append(inputs, tc.in)
	}
	for _, in := range inputs {
		once := NormaliseLabel(in)
		if twice := NormaliseLabel(once); twice != once {
			t.Errorf("NormaliseLabel(%q) = %q, but normalising that again gives %q", in, once, twice)
		}
	}
}

func mustMatch(t *testing.T, id string, labels []string, pattern string) *MatchOracle {
	t.Helper()
	o, err := NewMatch(id, labels, pattern)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestMatchOracle_Verify(t *testing.T) {
	labels := mustMatch(t, "banking77", []string{"card_arrival", "Exchange Rate"}, "")
	signs := mustMatch(t, "signs", []string{"-1", "0", "1"}, "")
	unsigned := mustMatch(t, "unsigned", []string{"0", "1"}, "")
	digits := mustMatch(t, "digits", nil, `\d+`)
	version := mustMatch(t, "version", nil, `v\d+\.`)
	quoted := mustMatch(t, "quoted", nil, `'[a-z]+'`)
	alt := mustMatch(t, "alt", nil, `a|b`)
	anything := mustMatch(t, "anything", nil, `.*`)
	for _, tc := range []struct {
		name      string
		o         *MatchOracle
		candidate string
		pass      bool
		detail    string
	}{
		{"label hit", labels, " Card-Arrival.\n", true, ""},
		{"label hit, set normalised too", labels, "exchange_rate", true, ""},
		{"label hit in emphasis", labels, "**Card Arrival**", true, ""},
		{"label miss", labels, "card_arrivals", false, `not one of 2 labels: "card_arrivals"`},
		{"label miss with prose", labels, "card_arrival because the card is late", false,
			`not one of 2 labels: "card_arrival because the card is late"`},
		{"a negative label", signs, "-1", true, ""},
		{"a sign is part of the value", unsigned, "-1", false, `not one of 2 labels: "-1"`},
		{"pattern hit", digits, " 42\n", true, ""},
		{"pattern hit inside Unicode space", digits, "\u00a0\v42\f\u3000", true, ""},
		{"pattern sees the dot it asks for", version, "v1.", true, ""},
		{"pattern sees the quotes it asks for", quoted, "'abc'", true, ""},
		{"pattern does not strip backticks", digits, "`42`", false, "does not match ^(?:\\d+)$: \"`42`\""},
		{"pattern miss", digits, "forty-two", false, `does not match ^(?:\d+)$: "forty-two"`},
		{"pattern is anchored", digits, "maybe 42 or 7", false, `does not match ^(?:\d+)$: "maybe 42 or 7"`},
		{"alternation is anchored as a whole", alt, "xa", false, `does not match ^(?:a|b)$: "xa"`},
		{"empty answer, labels", labels, "", false, `not one of 2 labels: ""`},
		{"empty answer, pattern", anything, "  \n", false, `does not match ^(?:.*)$: "  \n"`},
		{"detail quotes 80 bytes", labels, strings.Repeat("x", 200), false,
			fmt.Sprintf("not one of 2 labels: %q", strings.Repeat("x", 80)+"...(truncated)")},
		{"detail cuts on a rune boundary", labels, strings.Repeat("x", 79) + "é", false,
			fmt.Sprintf("not one of 2 labels: %q", strings.Repeat("x", 79)+"...(truncated)")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := tc.o.Verify(context.Background(), tc.candidate, trust.Task{})
			if err != nil {
				t.Fatalf("content is a verdict, never an oracle fault: %v", err)
			}
			if v.Passed != tc.pass || v.Detail != tc.detail {
				t.Errorf("Verify(%q) = %+v, want Passed=%v Detail=%q", tc.candidate, v, tc.pass, tc.detail)
			}
			if !utf8.ValidString(v.Detail) {
				t.Errorf("Detail %q is not valid UTF-8", v.Detail)
			}
		})
	}
	long := mustMatch(t, "long", nil, strings.Repeat("a", 2*detailMaxLen))
	v, err := long.Verify(context.Background(), "b", trust.Task{})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Detail) > detailMaxLen+len("...(truncated)") || !strings.HasSuffix(v.Detail, "...(truncated)") {
		t.Errorf("a %d-byte pattern gave a %d-byte Detail, want at most %d and a truncation marker",
			2*detailMaxLen, len(v.Detail), detailMaxLen)
	}
	if labels.Source != "verifier:match:banking77" {
		t.Errorf("Source = %q, want the verifier:match:<id> calibration key", labels.Source)
	}
}

func TestNewMatch_RefusesBadConfig(t *testing.T) {
	for _, tc := range []struct {
		name    string
		labels  []string
		pattern string
		want    string
	}{
		{"both", []string{"a"}, "a", "both labels and a pattern"},
		{"neither", nil, "", "no labels and no pattern"},
		{"empty label set", []string{}, "", "no labels and no pattern"},
		{"label empty once normalised", []string{"card_arrival", ` ". `}, "", "empty once normalised"},
		{"labels collide once normalised", []string{"card_arrival", "oos", "Card Arrival"}, "",
			`labels "card_arrival" and "Card Arrival" both normalise to "card_arrival"`},
		{"bad regex", nil, "(", "missing closing )"},
		{"pattern escapes its anchors", nil, `a)|(.*`, "unexpected )"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := NewMatch("x", tc.labels, tc.pattern)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("NewMatch(%q, %q) = %v, %v; want an error containing %q", tc.labels, tc.pattern, o, err, tc.want)
			}
		})
	}
}

// G19, this package's half: Verify applies exactly NormaliseLabel, no more and no less lenient.
func TestMatchOracle_VerifyAgreesWithNormaliseLabel(t *testing.T) {
	labels := []string{"card_arrival", "exchange_rate", "oos"}
	set := map[string]bool{}
	for _, l := range labels {
		set[NormaliseLabel(l)] = true
	}
	o := mustMatch(t, "g19", labels, "")
	for _, c := range []string{
		"card_arrival", "Card Arrival.", "`card-arrival`", `"OOS"`, "card_arrivals",
		"cardarrival", "card_arrival because", "exchange rate!", "card.arrival", "oos..", "",
	} {
		v, err := o.Verify(context.Background(), c, trust.Task{})
		if err != nil {
			t.Fatal(err)
		}
		n := NormaliseLabel(c)
		if v.Passed && !set[n] {
			t.Errorf("MatchOracle accepted %q but NormaliseLabel(%q)=%q is not in the set", c, c, n)
		}
		if !v.Passed && set[n] {
			t.Errorf("MatchOracle refused %q but NormaliseLabel(%q)=%q is in the set", c, c, n)
		}
	}
}

// Two answers grade alike exactly when NormaliseLabel maps them to the same key.
func ExampleNormaliseLabel() {
	fmt.Println(NormaliseLabel(" `Card Arrival.` ") == NormaliseLabel("card_arrival"))
	fmt.Println(NormaliseLabel("card_arrivals") == NormaliseLabel("card_arrival"))
	// Output:
	// true
	// false
}
