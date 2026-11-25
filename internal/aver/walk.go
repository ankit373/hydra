// SPDX-License-Identifier: MIT

package aver

// Walk hands each candidate answer its bar, from weights fixed before the first
// call, so no answer seen can change what a later one must reach.
type Walk struct {
	alpha   float64
	weights []float64
	next    int
}

// NewWalk fixes the weights for a budget of candidates under rule.
func NewWalk(alpha float64, candidates int, rule string) (*Walk, error) {
	w, err := Weights(candidates, rule)
	if err != nil {
		return nil, err
	}
	return &Walk{alpha: alpha, weights: w}, nil
}

// Next is the next candidate's bar; ok is false once the budget is spent.
func (w *Walk) Next() (bar float64, ok bool) {
	if w.next >= len(w.weights) {
		return 0, false
	}
	w.next++
	return Bar(w.alpha, w.weights[w.next-1]), true
}

// Bought is how many candidates have had a bar; Budget is how many may.
func (w *Walk) Bought() int { return w.next }

// Budget is the candidate budget the weights were fixed for.
func (w *Walk) Budget() int { return len(w.weights) }
