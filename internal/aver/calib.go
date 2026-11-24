// SPDX-License-Identifier: MIT

// Package aver is the certified acceptance mode: an answer is accepted only when
// its checks bound the chance of accepting a wrong one, per task.
package aver

import (
	"fmt"
	"sort"

	"github.com/ankit373/hydra/internal/stats"
)

// Answer is one labelled answer with one verdict per check. The verdict log and
// the replay rows both map to it, so both share one fit.
type Answer struct {
	Kind     string
	Head     string
	Correct  bool
	Verdicts map[string]bool
}

// Cell is one (check, head, kind). QBar bounds how often the check passes a
// wrong answer from this head; S is how often it passed a right one.
type Cell struct {
	Kind      string  `json:"kind"`
	Check     string  `json:"check"`
	Head      string  `json:"head"`
	Wrong     int     `json:"wrong"`
	FalsePass int     `json:"false_pass"`
	Right     int     `json:"right"`
	TruePass  int     `json:"true_pass"`
	QBar      float64 `json:"q_bar"`
	S         float64 `json:"s"`
	// Skip marks s <= q̄: a fail would then be worth more than 1 on a wrong answer.
	Skip bool `json:"skip"`
}

// EPass is the e-value of a pass, s/q̄. A skipped cell's verdict is worth 1.
func (c Cell) EPass() float64 {
	if c.Skip {
		return 1
	}
	return c.S / c.QBar
}

// Table is every cell of one fit, sorted by kind, check, then head.
type Table struct {
	Delta float64 `json:"delta"`
	Cells []Cell  `json:"cells"`
}

type cellKey struct{ kind, check, head string }

// Fit bounds each cell's false-pass rate at delta/M, M the checks in its kind.
// Heads are never pooled: a strong head's wrong answers fool tests far more often.
func Fit(answers []Answer, delta float64) (Table, error) {
	if !(delta > 0 && delta < 1) {
		return Table{}, fmt.Errorf("aver: delta %v is outside (0, 1)", delta)
	}
	cells := map[cellKey]*Cell{}
	checks := map[string]map[string]bool{}
	for _, a := range answers {
		if checks[a.Kind] == nil {
			checks[a.Kind] = map[string]bool{}
		}
		for check, passed := range a.Verdicts {
			checks[a.Kind][check] = true
			k := cellKey{a.Kind, check, a.Head}
			c := cells[k]
			if c == nil {
				c = &Cell{Kind: a.Kind, Check: check, Head: a.Head}
				cells[k] = c
			}
			if a.Correct {
				c.Right++
				if passed {
					c.TruePass++
				}
			} else {
				c.Wrong++
				if passed {
					c.FalsePass++
				}
			}
		}
	}
	t := Table{Delta: delta, Cells: make([]Cell, 0, len(cells))}
	for _, c := range cells {
		c.QBar = 1
		if c.Wrong > 0 {
			// Cannot fail: Wrong > 0, FalsePass <= Wrong, and delta/M is in (0, 1).
			c.QBar, _ = stats.ClopperPearsonUpper(c.FalsePass, c.Wrong, delta/float64(len(checks[c.Kind])))
		}
		if c.Right > 0 {
			c.S = float64(c.TruePass) / float64(c.Right)
		}
		c.Skip = c.S <= c.QBar
		t.Cells = append(t.Cells, *c)
	}
	sort.Slice(t.Cells, func(i, j int) bool {
		a, b := t.Cells[i], t.Cells[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Check != b.Check {
			return a.Check < b.Check
		}
		return a.Head < b.Head
	})
	return t, nil
}
