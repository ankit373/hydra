// SPDX-License-Identifier: MIT

package aver

import (
	"math"
	"sort"
)

// EValue is one verdict's evidence: s/q̄ on a pass, (1-s)/(1-q̄) on a fail.
// Its mean on a wrong answer is at most 1 whenever s > q̄, so a skipped cell is 1.
func EValue(c Cell, passed bool) float64 {
	if c.Skip {
		return 1
	}
	if passed {
		return c.EPass()
	}
	return (1 - c.S) / (1 - c.QBar)
}

// Step is one check run on one answer.
type Step struct {
	Check  string  `json:"check"`
	Passed bool    `json:"passed"`
	E      float64 `json:"e"`
}

// Run multiplies one answer's e-values until the product reaches Bar.
// Stopping early, on accept or futility, keeps the bound: P(product >= Bar) <= 1/Bar.
type Run struct {
	Bar     float64 `json:"bar"`
	Product float64 `json:"product"`
	Seen    []Step  `json:"steps"`
}

// NewRun starts an answer with no evidence. Bar is 1/alpha for one candidate.
func NewRun(bar float64) *Run { return &Run{Bar: bar, Product: 1} }

// Add records one step and multiplies its e in.
func (r *Run) Add(s Step) {
	r.Seen = append(r.Seen, s)
	r.Product *= s.E
}

// Accepted says the product has reached the bar.
func (r *Run) Accepted() bool { return r.Product >= r.Bar }

// Futile says the bar is out of reach even if every remaining check passes.
func (r *Run) Futile(remaining []Cell) bool {
	best := r.Product
	for _, c := range remaining {
		best *= c.EPass()
	}
	return best < r.Bar
}

// KL is KL(s || q̄) in nats, the mean log e of a pass-or-fail verdict on a right answer.
func KL(c Cell) float64 {
	if c.Skip {
		return 0
	}
	return xlogy(c.S, c.S/c.QBar) + xlogy(1-c.S, (1-c.S)/(1-c.QBar))
}

func xlogy(x, y float64) float64 {
	if x == 0 {
		return 0
	}
	return x * math.Log(y)
}

// Order sorts cells by KL per dollar, most first. A heuristic for cost only:
// the bound holds in any order. A free check comes first; ties go by name.
func Order(cells []Cell, cost func(check string) float64) []Cell {
	out := append([]Cell(nil), cells...)
	rate := func(c Cell) float64 {
		k := KL(c)
		if usd := cost(c.Check); usd > 0 {
			return k / usd
		}
		if k > 0 {
			return math.Inf(1)
		}
		return 0
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rate(out[i]), rate(out[j])
		if ri != rj {
			return ri > rj
		}
		if ki, kj := KL(out[i]), KL(out[j]); ki != kj {
			return ki > kj // two free checks: the stronger first
		}
		return out[i].Check < out[j].Check
	})
	return out
}

// Index finds a fitted cell by kind, check and head.
func (t Table) Index() map[[3]string]Cell {
	m := make(map[[3]string]Cell, len(t.Cells))
	for _, c := range t.Cells {
		m[[3]string{c.Kind, c.Check, c.Head}] = c
	}
	return m
}
