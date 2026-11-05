// SPDX-License-Identifier: MIT

package oracle

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/trust"
)

func TestNormaliseLabel(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Card Arrival.", "card_arrival"},
		{"card_arrival", "card_arrival"},
		{"`card_arrival`", "card_arrival"},
		{`"Card_Arrival"`, "card_arrival"},
		{`'card arrival'.`, "card_arrival"},
		{`"card_arrival".`, "card_arrival"},
		{"card-arrival", "card_arrival"},
		{"  card \t - -  arrival \n", "card_arrival"},
		{"", ""},
		{` "". `, ""},
	} {
		if got := NormaliseLabel(tc.in); got != tc.want {
			t.Errorf("NormaliseLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMatchOracle_Verify(t *testing.T) {
	labels, err := NewMatch("banking77", []string{"card_arrival", "Exchange Rate"}, "")
	if err != nil {
		t.Fatal(err)
	}
	pattern, err := NewMatch("digits", nil, `^\d+$`)
	if err != nil {
		t.Fatal(err)
	}
	anything, err := NewMatch("anything", nil, `.*`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		o         *MatchOracle
		candidate string
		pass      bool
		detail    string
	}{
		{"label hit", labels, " Card-Arrival.\n", true, ""},
		{"label hit, set normalised too", labels, "exchange_rate", true, ""},
		{"label miss", labels, "card_arrivals", false, `not one of 2 labels: "card_arrivals"`},
		{"label miss with prose", labels, "card_arrival because the card is late", false,
			`not one of 2 labels: "card_arrival because the card is late"`},
		{"pattern hit", pattern, " 42\n", true, ""},
		{"pattern miss", pattern, "forty-two", false, `does not match ^\d+$: "forty-two"`},
		{"empty answer, labels", labels, "", false, `not one of 2 labels: ""`},
		{"empty answer, pattern", anything, "  \n", false, `does not match .*: "  \n"`},
		{"detail quotes 80 bytes", labels, strings.Repeat("x", 200), false,
			fmt.Sprintf("not one of 2 labels: %q", strings.Repeat("x", 80))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := tc.o.Verify(context.Background(), tc.candidate, trust.Task{})
			if err != nil {
				t.Fatalf("content is a verdict, never an oracle fault: %v", err)
			}
			if v.Passed != tc.pass || v.Detail != tc.detail {
				t.Errorf("Verify(%q) = %+v, want Passed=%v Detail=%q", tc.candidate, v, tc.pass, tc.detail)
			}
		})
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
		{"bad regex", nil, "(", "missing closing )"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := NewMatch("x", tc.labels, tc.pattern)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("NewMatch(%q, %q) = %v, %v; want an error containing %q", tc.labels, tc.pattern, o, err, tc.want)
			}
		})
	}
}

// G19: E7 grades Pass with NormaliseLabel, so the served verifier must apply exactly
// that function; any extra leniency or strictness here would make the two disagree.
func TestMatchOracle_SameNormaliserAsE7Pass(t *testing.T) {
	labels := []string{"card_arrival", "exchange_rate", "oos"}
	set := map[string]bool{}
	for _, l := range labels {
		set[NormaliseLabel(l)] = true
	}
	o, err := NewMatch("g19", labels, "")
	if err != nil {
		t.Fatal(err)
	}
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

// The grader E7 imports: Pass is equality under NormaliseLabel, nothing more lenient.
func ExampleNormaliseLabel() {
	fmt.Println(NormaliseLabel(" `Card Arrival.` ") == NormaliseLabel("card_arrival"))
	fmt.Println(NormaliseLabel("card_arrivals") == NormaliseLabel("card_arrival"))
	// Output:
	// true
	// false
}
