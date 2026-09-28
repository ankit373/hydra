// SPDX-License-Identifier: MIT

package classify

import (
	"math/rand"

	"github.com/ankit373/hydra/internal/reliability"
)

// DefaultHoldOuts bounds the evaluation. Leave-one-out over the whole corpus is
// quadratic, and the answer is a statistic: more held-out examples past a few
// thousand narrow the interval, they do not change the finding.
const DefaultHoldOuts = 2000

// nullPermutations sets the level of the permutation test: beating all of them
// is significance at 1/(n+1), so 19 is the 5% level. Cheap, because a shuffle
// rescores neighbourhoods already found rather than recomputing cosines.
const nullPermutations = 19

// Result is what the leave-one-out evaluation found.
type Result struct {
	Model  string `json:"model"`
	Corpus int    `json:"corpus"`

	// Answered is how many held-out examples the estimator spoke to, and
	// Refused how many it declined. Both are reported: an estimator that
	// answers twice and is right both times has said nothing, and a coverage
	// figure is the only thing that makes the score readable.
	Answered int `json:"answered"`
	Refused  int `json:"refused"`

	Report reliability.Report `json:"report"`

	// NullResolution is the highest resolution any shuffle of the outcomes
	// produced over the same neighbourhoods. A finite corpus yields resolution
	// from chance alone, 0.036 against an uncertainty of 0.25 on 120 coin
	// flips, so resolution above zero is not evidence of anything by itself.
	// Comparing against the mean of the shuffles is not a test either: a single
	// draw beats the mean of several about half the time.
	NullResolution float64 `json:"null_resolution"`
}

// Coverage is the share of held-out examples the estimator would speak to.
func (r Result) Coverage() float64 {
	if r.Answered+r.Refused == 0 {
		return 0
	}
	return float64(r.Answered) / float64(r.Answered+r.Refused)
}

// Beats says whether similarity carries signal about the outcome: resolution
// past every shuffle of the same corpus, which is significance at 1/(n+1).
// Resolution alone would report a finding on a corpus of coin flips.
func (r Result) Beats() bool { return r.Report.Resolution > r.NullResolution }

// Evaluate scores the estimator by leave-one-out: for each held-out example,
// predict the pass probability of the enum it actually used, then compare
// against what happened.
//
// It deliberately asks a question needing no counterfactual. "Would another
// enum have done better" is unidentifiable from this corpus, and internal/ope
// already refuses that class of question rather than answering it badly. "Does
// similarity to past work predict whether this enum passes" is answerable from
// what is recorded, and it is the assumption everything else rests on: if
// similarity carries no signal about outcome, no classifier over these vectors
// can help.
func Evaluate(c *Corpus, k, holdOuts int) (Result, error) {
	if c == nil || len(c.Examples) <= MinNeighbours {
		return Result{}, ErrTooFew
	}
	if holdOuts <= 0 {
		holdOuts = DefaultHoldOuts
	}
	res := Result{Model: c.Model, Corpus: len(c.Examples)}

	idx := make([]int, len(c.Examples))
	for i := range idx {
		idx[i] = i
	}
	// Seeded, so one corpus evaluates to the same number twice.
	rand.New(rand.NewSource(2)).Shuffle(len(idx), func(a, b int) { idx[a], idx[b] = idx[b], idx[a] })
	if len(idx) > holdOuts {
		idx = idx[:holdOuts]
	}

	var kept []held
	obs := make([]reliability.Observation, 0, len(idx))
	for _, i := range idx {
		e := c.Examples[i]
		nb, mean, err := c.nearest(e.Vec, k, i)
		if err != nil {
			res.Refused++
			continue
		}
		p, ok := PredictPass(c.summarise(nb, mean, nil), e.Enum)
		if !ok {
			res.Refused++
			continue
		}
		res.Answered++
		kept = append(kept, held{self: i, neighbour: nb, mean: mean})
		obs = append(obs, reliability.Observation{Predicted: p, Correct: e.Passed})
	}
	if len(obs) == 0 {
		return res, ErrTooFew
	}
	rep, err := reliability.Evaluate(obs, reliability.DefaultBins)
	if err != nil {
		return res, err
	}
	res.Report = rep
	res.NullResolution = nullResolution(c, kept)
	return res, nil
}

// held is one held-out example with the neighbourhood already found for it, so
// the permutation null costs tallies rather than cosines.
type held struct {
	self      int
	neighbour []int
	mean      float64
}

// nullResolution is the highest score the same neighbourhoods reach against
// shuffled outcomes, which is what the estimator reaches when there is nothing
// to find. Seeded, so the null is as reproducible as the finding.
func nullResolution(c *Corpus, kept []held) float64 {
	outcomes := make([]bool, len(c.Examples))
	for i, e := range c.Examples {
		outcomes[i] = e.Passed
	}
	rng := rand.New(rand.NewSource(4))
	worst := 0.0
	for p := 0; p < nullPermutations; p++ {
		rng.Shuffle(len(outcomes), func(a, b int) { outcomes[a], outcomes[b] = outcomes[b], outcomes[a] })
		obs := make([]reliability.Observation, 0, len(kept))
		for _, h := range kept {
			pred, ok := PredictPass(c.summarise(h.neighbour, h.mean, outcomes), c.Examples[h.self].Enum)
			if !ok {
				continue
			}
			obs = append(obs, reliability.Observation{Predicted: pred, Correct: outcomes[h.self]})
		}
		rep, err := reliability.Evaluate(obs, reliability.DefaultBins)
		if err != nil {
			continue
		}
		if rep.Resolution > worst {
			worst = rep.Resolution
		}
	}
	return worst
}
