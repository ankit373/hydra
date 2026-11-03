// SPDX-License-Identifier: MIT

package trust

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"sync"

	"github.com/ankit373/hydra/internal/reliability"
)

// Correction maps a source's stated probability to the accuracy it was measured
// to have. The zero value is the identity, which is all a source below the floor gets.
type Correction struct {
	from []float64 // lowest stated probability in each block, ascending
	to   []float64 // that block's fitted accuracy, nondecreasing
}

// FitCorrection fits a nondecreasing step function from stated probability to
// observed accuracy. Sorting is O(n log n); pooling adjacent violators is O(n).
func FitCorrection(obs []reliability.Observation) (Correction, error) {
	if len(obs) < reliability.MinObservations {
		need := reliability.MinObservations
		return Correction{}, fmt.Errorf("%w: have %d, need %d", reliability.ErrTooFew, len(obs), need)
	}
	if err := checkProbabilities(obs); err != nil {
		return Correction{}, err
	}
	sorted := slices.Clone(obs)
	slices.SortFunc(sorted, func(a, b reliability.Observation) int {
		return compareFloat(a.Predicted, b.Predicted)
	})

	// Equal stated values pool into one weighted point first, so Apply stays a function.
	var stated, accuracy, weight []float64
	for _, o := range sorted {
		hit := 0.0
		if o.Correct {
			hit = 1
		}
		last := len(stated) - 1
		if last >= 0 && stated[last] == o.Predicted {
			accuracy[last] += hit
			weight[last]++
			continue
		}
		stated = append(stated, o.Predicted)
		accuracy = append(accuracy, hit)
		weight = append(weight, 1)
	}
	for i := range accuracy {
		accuracy[i] /= weight[i]
	}

	// n verdicts cannot support a rate past the rule of succession's, so a block
	// that went 0 or 1 does not become unbounded evidence.
	n := float64(len(obs))
	lo, hi := 1/(n+2), (n+1)/(n+2)
	var c Correction
	for i, v := range pav(accuracy, weight) {
		bounded := math.Min(math.Max(v, lo), hi)
		if len(c.to) > 0 && bounded == c.to[len(c.to)-1] {
			continue
		}
		c.from = append(c.from, stated[i])
		c.to = append(c.to, bounded)
	}
	return c, nil
}

// Apply returns the corrected probability. A stated value below the first block
// takes that block's accuracy, since nothing lower was ever observed.
func (c Correction) Apply(stated float64) float64 {
	if len(c.from) == 0 {
		return stated
	}
	next := sort.Search(len(c.from), func(i int) bool { return c.from[i] > stated })
	return c.to[max(next-1, 0)]
}

// pav returns the weighted least-squares nondecreasing fit to y. Each point is
// pushed once and merged away at most once, so the pass is O(n).
func pav(y, w []float64) []float64 {
	type block struct {
		sum, weight float64
		size        int
	}
	stack := make([]block, 0, len(y))
	for i := range y {
		stack = append(stack, block{sum: w[i] * y[i], weight: w[i], size: 1})
		for len(stack) > 1 {
			top := stack[len(stack)-1]
			prev := stack[len(stack)-2]
			if prev.sum/prev.weight <= top.sum/top.weight {
				break
			}
			stack = stack[:len(stack)-1]
			stack[len(stack)-1] = block{
				sum:    prev.sum + top.sum,
				weight: prev.weight + top.weight,
				size:   prev.size + top.size,
			}
		}
	}
	fit := make([]float64, 0, len(y))
	for _, b := range stack {
		mean := b.sum / b.weight
		for range b.size {
			fit = append(fit, mean)
		}
	}
	return fit
}

// Corrections holds one Correction per (source, domain), keyed the way the
// calibration store is. Safe for concurrent use.
type Corrections struct {
	mu    sync.RWMutex
	byKey map[calibKey]Correction
}

// Fit refits one (source, domain) from all its verdicts so far. Below the floor
// the cell keeps what it had, which starts as the identity.
func (cs *Corrections) Fit(source, domain string, obs []reliability.Observation) error {
	c, err := FitCorrection(obs)
	if err != nil {
		return err
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.byKey == nil {
		cs.byKey = map[calibKey]Correction{}
	}
	cs.byKey[keyFor(source, domain)] = c
	return nil
}

// LLR is the evidence (nats) a stated probability carries for the answer being
// correct, after this source's correction. A value that is not a probability carries none.
func (cs *Corrections) LLR(source, domain string, stated float64) float64 {
	if !isProbability(stated) {
		return 0
	}
	cs.mu.RLock()
	c := cs.byKey[keyFor(source, domain)]
	cs.mu.RUnlock()
	return logit(clamp01(c.Apply(stated)))
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
