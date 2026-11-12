// SPDX-License-Identifier: MIT

package rank

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

func TestParseObjective(t *testing.T) {
	for in, want := range map[string]Objective{"": "", "quality": Quality, " QUALITY ": Quality, "cost": Cost} {
		if got, err := ParseObjective(in); err != nil || got != want {
			t.Errorf("ParseObjective(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"balanced", "fast"} {
		if _, err := ParseObjective(in); err == nil || !strings.Contains(err.Error(), "quality, cost") {
			t.Errorf("ParseObjective(%q) = %v, want a refusal listing quality, cost", in, err)
		}
	}
}

func TestStrongest_Orders(t *testing.T) {
	inDomain := func(correct, total int) Measurement {
		return Measurement{Correct: correct, Total: total, InDomainCorrect: correct, InDomainTotal: total}
	}
	for _, tc := range []struct {
		name   string
		heads  []provider.Head
		lookup map[string]Measurement
		prices map[string]float64
		want   string
	}{
		{"measured above declared",
			[]provider.Head{{ID: "paper", CapScore: 90}, {ID: "proven", CapScore: 60}},
			map[string]Measurement{"proven": inDomain(100, 100), "paper": inDomain(0, 100)},
			nil, "proven,paper"},
		{"equal effective broken by price",
			[]provider.Head{{ID: "a", CapScore: 80}, {ID: "b", CapScore: 80}},
			nil, map[string]float64{"a": 0.02, "b": 0.01}, "b,a"},
		{"an unknown price sorts last",
			[]provider.Head{{ID: "a", CapScore: 80}, {ID: "b", CapScore: 80}},
			nil, map[string]float64{"b": 0.5}, "b,a"},
		{"equal everything broken by id",
			[]provider.Head{{ID: "z", CapScore: 80}, {ID: "y", CapScore: 80}},
			nil, map[string]float64{"y": 0, "z": 0}, "y,z"},
		{"nil scores fall back to declared",
			[]provider.Head{{ID: "low", CapScore: 50}, {ID: "high", CapScore: 70}},
			nil, nil, "high,low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var scores map[string]Score
			if tc.lookup != nil {
				scores = Scores(tc.heads, func(id string) Measurement { return tc.lookup[id] })
			}
			got, out := Strongest(tc.heads, scores, fixedPrice(tc.prices))
			if order(got) != tc.want {
				t.Errorf("order %s, want %s (scores %+v)", order(got), tc.want, out)
			}
		})
	}
}

// The one line between a measurement and a prior: the gate Cheapest admits on
// and the basis serve reports must be the same predicate.
func TestScoreMeasured_MatchesCheapestGate(t *testing.T) {
	heads := []provider.Head{{ID: "h", CapScore: 99}}
	for _, n := range []int{MinCommitments - 1, MinCommitments} {
		scores := Scores(heads, measured("h", n, n))
		_, out := Cheapest(heads, scores, fixedPrice(map[string]float64{"h": 0}), 0.5)
		if out["h"].Clears != scores["h"].Measured() {
			t.Errorf("%d in-domain: Clears=%v but Measured=%v", n, out["h"].Clears, scores["h"].Measured())
		}
	}
}

// G8. Two identical runs must route identically whatever order discovery
// handed the heads over in.
func TestStrongest_IsATotalOrder(t *testing.T) {
	heads := []provider.Head{
		{ID: "a", CapScore: 80}, {ID: "b", CapScore: 80}, {ID: "c", CapScore: 80}, {ID: "d", CapScore: 70},
		{ID: "e", CapScore: 70}, {ID: "f", CapScore: 90}, {ID: "g", CapScore: 80}, {ID: "h", CapScore: 70},
	}
	price := fixedPrice(map[string]float64{"a": 0.01, "b": 0.01, "c": 0.01, "d": 0.02, "e": 0.02, "g": 0.03})
	rng := rand.New(rand.NewSource(1206))
	var first string
	for i := range 100 {
		shuffled := append([]provider.Head(nil), heads...)
		rng.Shuffle(len(shuffled), func(x, y int) { shuffled[x], shuffled[y] = shuffled[y], shuffled[x] })
		got, _ := Strongest(shuffled, nil, price)
		if i == 0 {
			first = order(got)
		} else if order(got) != first {
			t.Fatalf("shuffle %d ordered %v, shuffle 0 ordered %v", i, order(got), first)
		}
	}
}
