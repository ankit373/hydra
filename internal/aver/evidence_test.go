// SPDX-License-Identifier: MIT

package aver

import (
	"math"
	"testing"
)

func TestEValue_PassAndFail(t *testing.T) {
	// s 0.9, q̄ 0.1: a pass is worth 0.9/0.1 = 9, a fail 0.1/0.9 = 1/9.
	c := Cell{Check: "tests", S: 0.9, QBar: 0.1}
	if got := EValue(c, true); math.Abs(got-9) > 1e-12 {
		t.Fatalf("pass e = %v, want 9", got)
	}
	if got := EValue(c, false); math.Abs(got-1.0/9) > 1e-12 {
		t.Fatalf("fail e = %v, want 1/9", got)
	}
}

func TestEValue_SkipCellIsOne(t *testing.T) {
	c := Cell{S: 0.2, QBar: 0.4, Skip: true}
	if EValue(c, true) != 1 || EValue(c, false) != 1 {
		t.Fatal("a skipped cell must be worth 1 either way")
	}
}

func TestRun_AcceptsAtBar(t *testing.T) {
	r := NewRun(20) // alpha 0.05
	c := Cell{Check: "a", S: 0.9, QBar: 0.1}
	r.Add(Step{Check: "a", Passed: true, E: EValue(c, true)})
	if r.Accepted() {
		t.Fatalf("product %v accepted below bar 20", r.Product)
	}
	r.Add(Step{Check: "b", Passed: true, E: EValue(c, true)})
	if !r.Accepted() || math.Abs(r.Product-81) > 1e-9 || len(r.Seen) != 2 {
		t.Fatalf("want accepted at 81 after two steps, got %+v", r)
	}
}

func TestRun_FutileWhenBestRemainingCannotReachBar(t *testing.T) {
	r := NewRun(100)
	strong := Cell{Check: "a", S: 0.9, QBar: 0.1} // e on pass 9
	r.Add(Step{Check: "x", Passed: false, E: EValue(strong, false)})
	// 1/9 * 9 * 9 = 9 < 100: two more passes cannot reach the bar.
	if !r.Futile([]Cell{strong, strong}) {
		t.Fatal("bar of 100 is out of reach but not futile")
	}
	if r := NewRun(50); r.Futile([]Cell{strong, strong}) {
		t.Fatal("81 >= 50 is reachable, but called futile")
	}
	skip := Cell{Check: "s", Skip: true}
	if r := NewRun(2); !r.Futile([]Cell{skip, skip}) {
		t.Fatal("skipped checks can never move the product")
	}
}

// Under the null every check passes a wrong answer with probability q <= q̄,
// independently. E[product] = prod_i (q s/q̄ + (1-q)(1-s)/(1-q̄)), which is 1 at q = q̄.
func TestRun_ExpectationUnderNullAtMostOne(t *testing.T) {
	cells := []Cell{
		{Check: "a", S: 0.9, QBar: 0.2},
		{Check: "b", S: 0.7, QBar: 0.3},
		{Check: "c", S: 0.95, QBar: 0.05},
	}
	expect := func(q []float64) float64 {
		var sum float64
		for mask := 0; mask < 1<<len(cells); mask++ {
			p, e := 1.0, 1.0
			for i, c := range cells {
				passed := mask&(1<<i) != 0
				if passed {
					p *= q[i]
				} else {
					p *= 1 - q[i]
				}
				e *= EValue(c, passed)
			}
			sum += p * e
		}
		return sum
	}
	if got := expect([]float64{0.2, 0.3, 0.05}); math.Abs(got-1) > 1e-12 {
		t.Fatalf("E[product] at q = q̄ is %v, want exactly 1", got)
	}
	// Worked example below the bound: q = (0.1, 0.1, 0) gives
	// (0.1*4.5 + 0.9*0.125) * (0.1*7/3 + 0.9*3/7) * (0 + 1*1/19) = 0.5625 * 0.6190 * 0.0526.
	want := 0.5625 * (0.7/3 + 2.7/7) * (1.0 / 19)
	if got := expect([]float64{0.1, 0.1, 0}); math.Abs(got-want) > 1e-12 || got > 1 {
		t.Fatalf("E[product] below q̄ is %v, want %v", got, want)
	}
}

func TestOrder_KLPerDollarFirst(t *testing.T) {
	cells := []Cell{
		{Check: "judge", S: 0.9, QBar: 0.1},  // KL 1.758 nats, costs 1
		{Check: "tests", S: 0.6, QBar: 0.2},  // KL 0.335 nats, free
		{Check: "lint", S: 0.9, QBar: 0.1},   // same KL as judge, costs 0.1
		{Check: "noise", S: 0.1, Skip: true}, // nothing to learn
	}
	cost := map[string]float64{"judge": 1, "lint": 0.1}
	got := Order(cells, func(c string) float64 { return cost[c] })
	want := []string{"tests", "lint", "judge", "noise"}
	for i := range want {
		if got[i].Check != want[i] {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
	free := Order([]Cell{cells[1], {Check: "z", S: 0.9, QBar: 0.1}}, func(string) float64 { return 0 })
	if free[0].Check != "z" {
		t.Fatalf("two free checks: %v, want the stronger first", free)
	}
	// KL(0.9 || 0.1) = 0.9 ln 9 + 0.1 ln(1/9) = 0.8 ln 9.
	if k := KL(cells[0]); math.Abs(k-0.8*math.Log(9)) > 1e-12 {
		t.Fatalf("KL = %v, want %v", k, 0.8*math.Log(9))
	}
	if KL(Cell{S: 0, QBar: 0.5}) != math.Log(2) {
		t.Fatalf("KL(0 || 0.5) = %v, want ln 2", KL(Cell{S: 0, QBar: 0.5}))
	}
}

func TestTable_Index(t *testing.T) {
	tab := Table{Cells: []Cell{{Kind: "k", Check: "c", Head: "h", QBar: 0.3}}}
	if c, ok := tab.Index()[[3]string{"k", "c", "h"}]; !ok || c.QBar != 0.3 {
		t.Fatalf("index missed the cell: %+v", c)
	}
}
