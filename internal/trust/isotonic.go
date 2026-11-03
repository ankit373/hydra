// SPDX-License-Identifier: MIT

package trust

import (
	"fmt"
	"slices"
	"sort"
	"sync"

	"github.com/ankit373/hydra/internal/reliability"
)

// correction maps a source's stated probability to the accuracy it was measured
// to have. Build one with fitCorrection; there is no identity value.
type correction struct {
	from  []float64 // lowest stated probability in each block, ascending
	to    []float64 // that block's smoothed accuracy, nondecreasing, inside (0,1)
	prior float64   // log-odds of the source's smoothed accuracy over all its verdicts
}

// tally is a run of verdicts: how many there were and how many were right.
type tally struct {
	hits, n float64
}

// rate is the rule of succession's estimate, (k+1)/(n+2), so a few verdicts that
// went all one way stay finite evidence instead of a certainty.
func (t tally) rate() float64 { return (t.hits + 1) / (t.n + 2) }

// fitCorrection fits a nondecreasing step function from stated probability to
// observed accuracy. Sorting is O(n log n); pooling adjacent violators is O(n).
func fitCorrection(obs []reliability.Observation) (correction, error) {
	if len(obs) < reliability.MinObservations {
		need := reliability.MinObservations
		return correction{}, fmt.Errorf("%w: have %d, need %d", reliability.ErrTooFew, len(obs), need)
	}
	if err := checkProbabilities(obs); err != nil {
		return correction{}, err
	}
	sorted := slices.Clone(obs)
	slices.SortFunc(sorted, func(a, b reliability.Observation) int {
		return compareFloat(a.Predicted, b.Predicted)
	})

	// Equal stated values pool into one tally first, so apply stays a function.
	var stated []float64
	var points []tally
	var total tally
	for _, o := range sorted {
		hit := 0.0
		if o.Correct {
			hit = 1
		}
		total.hits += hit
		total.n++
		last := len(stated) - 1
		if last >= 0 && stated[last] == o.Predicted {
			points[last].hits += hit
			points[last].n++
			continue
		}
		stated = append(stated, o.Predicted)
		points = append(points, tally{hits: hit, n: 1})
	}

	c := correction{prior: logit(total.rate())}
	for _, b := range pav(points) {
		c.from = append(c.from, stated[b.first])
		c.to = append(c.to, b.rate())
	}
	return c, nil
}

// apply returns the corrected probability. A stated value below the first block
// takes that block's accuracy, since nothing lower was ever observed.
func (c correction) apply(stated float64) float64 {
	next := sort.Search(len(c.from), func(i int) bool { return c.from[i] > stated })
	return c.to[max(next-1, 0)]
}

// llr is the likelihood ratio (nats) of the stated value: the corrected log-odds
// less the source's base rate, so summing it over sources counts no prior.
func (c correction) llr(stated float64) float64 {
	return logit(c.apply(stated)) - c.prior
}

// block is a run of adjacent points pooled together, starting at index first.
type block struct {
	tally
	first int
}

// pav pools adjacent points until each block's own rate is nondecreasing. Each
// point is pushed once and merged away at most once, so the pass is O(n).
func pav(points []tally) []block {
	stack := make([]block, 0, len(points))
	for i, p := range points {
		stack = append(stack, block{tally: p, first: i})
		for len(stack) > 1 {
			top := stack[len(stack)-1]
			prev := stack[len(stack)-2]
			if prev.rate() <= top.rate() {
				break
			}
			stack = stack[:len(stack)-1]
			stack[len(stack)-1].hits += top.hits
			stack[len(stack)-1].n += top.n
		}
	}
	return stack
}

// corrections holds one correction per (source, domain), keyed the way the
// calibration store is. Safe for concurrent use.
type corrections struct {
	mu    sync.RWMutex
	byKey map[calibKey]correction
}

// fit refits one (source, domain) from all its verdicts so far. Below the floor
// the cell keeps what it had, which starts as nothing.
func (cs *corrections) fit(source, domain string, obs []reliability.Observation) error {
	c, err := fitCorrection(obs)
	if err != nil {
		return err
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.byKey == nil {
		cs.byKey = map[calibKey]correction{}
	}
	cs.byKey[keyFor(source, domain)] = c
	return nil
}

// llr is the evidence (nats) a stated probability carries for the answer being
// correct. An unfitted source or a value that is not a probability carries none.
func (cs *corrections) llr(source, domain string, stated float64) float64 {
	if !isProbability(stated) {
		return 0
	}
	cs.mu.RLock()
	c, ok := cs.byKey[keyFor(source, domain)]
	cs.mu.RUnlock()
	if !ok {
		return 0
	}
	return c.llr(stated)
}

// checkProbabilities refuses rather than clamps, as reliability.Evaluate does: a
// value outside [0,1] means the caller handed over something that is not a probability.
func checkProbabilities(obs []reliability.Observation) error {
	for i, o := range obs {
		if !isProbability(o.Predicted) {
			return fmt.Errorf("trust: observation %d stated %v, not a probability", i, o.Predicted)
		}
	}
	return nil
}

func isProbability(p float64) bool { return p >= 0 && p <= 1 }

func compareFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
