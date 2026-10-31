// SPDX-License-Identifier: MIT

// Package classify answers what the verified corpus says about work like this,
// from measured outcomes rather than from the caller's word.
package classify

import (
	"errors"
	"math/rand"
	"sort"

	"github.com/ankit373/hydra/internal/embed"
	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/util"
)

// DefaultK is how many verified examples one neighbourhood reads. Measured on
// 141 real commit subjects: accuracy plateaus from 3 to 10 and falls away
// above, so 10 is the top of the plateau rather than the sweep's argmax.
const DefaultK = 10

// MinNeighbours is how many of them must share an enum before that enum's pass
// rate is a reading rather than an anecdote.
const MinNeighbours = 5

// baselinePairs bounds the sample the corpus's own similarity distribution is
// estimated from, so a neighbourhood costs the same on a corpus of any size.
const baselinePairs = 4000

// BaselineQuantile is where the neighbourhood gate sits in that distribution.
// The median does nothing: measured against 120 commit subjects from an
// unrelated Go repository it refused none of them while keeping every real one.
const BaselineQuantile = 0.90

var (
	// ErrNoVectors is a corpus that carries no embeddings, which is every
	// corpus written before they were stored.
	ErrNoVectors = errors.New("classify: no example carries an embedding")

	// ErrTooFew is fewer examples than a neighbourhood can be drawn from.
	ErrTooFew = errors.New("classify: too few embedded examples")

	// ErrNoNeighbourhood is the nearest examples being no closer than two
	// random ones, which is a corpus with nothing like this in it.
	ErrNoNeighbourhood = errors.New("classify: nothing in the corpus is near this")
)

// Vectored is one example reduced to what a neighbourhood needs.
type Vectored struct {
	Vec    []float32
	Enum   string
	Passed bool
}

// EnumEvidence is one enum's record among the neighbours.
type EnumEvidence struct {
	Enum   string `json:"enum"`
	N      int    `json:"n"`
	Passed int    `json:"passed"`

	// PassRate is the Beta posterior mean under a Laplace prior, the form
	// internal/trust already uses, so one pass out of one reads as 0.67 rather
	// than as certainty.
	PassRate float64 `json:"pass_rate"`
}

// Neighbourhood is what the corpus holds near one prompt.
type Neighbourhood struct {
	Size     int            `json:"size"`
	MeanSim  float64        `json:"mean_similarity"`
	Baseline float64        `json:"baseline_similarity"`
	PerEnum  []EnumEvidence `json:"per_enum"`

	// Passed and PassRate are the whole neighbourhood, not one enum: how often
	// work like this held up at all, which is the question a routing rule asks
	// before it knows which enum it is choosing between.
	Passed   int     `json:"passed"`
	PassRate float64 `json:"pass_rate"`
}

// Corpus is the embedded slice of an eval set, in one embedding model's space.
// Two models are two spaces (#1004), so the largest single-model group is taken
// and the rest left out rather than mixed into a cosine that means nothing.
type Corpus struct {
	Model    string
	Examples []Vectored

	baseline float64
	haveBase bool
}

// Load reduces a corpus to the examples a neighbourhood can be drawn from.
func Load(examples []evalset.Example) (*Corpus, error) {
	byModel := map[string][]Vectored{}
	for _, e := range examples {
		if e.EmbedModel == "" || e.Enum == "" {
			continue
		}
		v := util.DecodeVec(e.Embedding)
		if len(v) == 0 {
			continue
		}
		byModel[e.EmbedModel] = append(byModel[e.EmbedModel],
			Vectored{Vec: v, Enum: e.Enum, Passed: e.Passed})
	}
	if len(byModel) == 0 {
		return nil, ErrNoVectors
	}
	best, name := []Vectored(nil), ""
	for m, vs := range byModel {
		// Ties broken on the name so one corpus loads the same way twice.
		if len(vs) > len(best) || (len(vs) == len(best) && m < name) {
			best, name = vs, m
		}
	}
	// A vector length that disagrees inside one model name cannot be cosined
	// against the rest, and dropping the minority is the only reading that
	// leaves a usable space.
	best = sameDimension(best)
	if len(best) <= MinNeighbours {
		return nil, ErrTooFew
	}
	return &Corpus{Model: name, Examples: best}, nil
}

func sameDimension(vs []Vectored) []Vectored {
	counts := map[int]int{}
	for _, v := range vs {
		counts[len(v.Vec)]++
	}
	dim, most := 0, 0
	for d, n := range counts {
		if n > most || (n == most && d < dim) {
			dim, most = d, n
		}
	}
	out := vs[:0:0]
	for _, v := range vs {
		if len(v.Vec) == dim {
			out = append(out, v)
		}
	}
	return out
}

// Near reads the k verified examples closest to vec, skipping any index the
// caller is holding out.
//
// The gate is a quantile of the corpus's own similarity distribution rather
// than an absolute cosine, so it means the same on a corpus of any subject.
// It is weak by measurement, not by oversight: unrelated software prose sits
// close to real work in this space, and about half of it still gets through.
func (c *Corpus) Near(vec []float32, k, holdOut int) (Neighbourhood, error) {
	idx, mean, err := c.nearest(vec, k, holdOut)
	if err != nil {
		return Neighbourhood{Size: len(idx), MeanSim: mean, Baseline: c.Baseline()}, err
	}
	return c.summarise(idx, mean, nil), nil
}

// nearest is the expensive half, the cosines. Separated so a permutation test
// can rescore the same neighbourhoods against shuffled outcomes for free.
func (c *Corpus) nearest(vec []float32, k, holdOut int) ([]int, float64, error) {
	if len(vec) == 0 {
		return nil, 0, ErrNoVectors
	}
	if k <= 0 {
		k = DefaultK
	}
	type scored struct {
		sim float64
		i   int
	}
	sims := make([]scored, 0, len(c.Examples))
	for i, e := range c.Examples {
		if i == holdOut {
			continue
		}
		sims = append(sims, scored{embed.Cosine(vec, e.Vec), i})
	}
	if len(sims) <= MinNeighbours {
		return nil, 0, ErrTooFew
	}
	sort.Slice(sims, func(a, b int) bool { return sims[a].sim > sims[b].sim })
	if k > len(sims) {
		k = len(sims)
	}
	sims = sims[:k]

	idx := make([]int, 0, k)
	mean := 0.0
	for _, s := range sims {
		idx = append(idx, s.i)
		mean += s.sim / float64(k)
	}
	if mean <= c.Baseline() {
		return idx, mean, ErrNoNeighbourhood
	}
	return idx, mean, nil
}

// summarise tallies one neighbourhood. outcomes overrides the recorded pass
// flags, which is how the permutation null rescores without new cosines.
func (c *Corpus) summarise(idx []int, mean float64, outcomes []bool) Neighbourhood {
	n := Neighbourhood{Size: len(idx), MeanSim: mean, Baseline: c.Baseline()}
	agg := map[string]*EnumEvidence{}
	for _, i := range idx {
		e := c.Examples[i]
		ev := agg[e.Enum]
		if ev == nil {
			ev = &EnumEvidence{Enum: e.Enum}
			agg[e.Enum] = ev
		}
		ev.N++
		passed := e.Passed
		if outcomes != nil {
			passed = outcomes[i]
		}
		if passed {
			ev.Passed++
		}
	}
	for _, ev := range agg {
		ev.PassRate = laplace(ev.Passed, ev.N)
		n.Passed += ev.Passed
		n.PerEnum = append(n.PerEnum, *ev)
	}
	n.PassRate = laplace(n.Passed, n.Size)
	sort.Slice(n.PerEnum, func(a, b int) bool {
		if n.PerEnum[a].N != n.PerEnum[b].N {
			return n.PerEnum[a].N > n.PerEnum[b].N
		}
		return n.PerEnum[a].Enum < n.PerEnum[b].Enum
	})
	return n
}

// Baseline is the BaselineQuantile point of the corpus's pairwise similarity,
// over a bounded sample with a fixed seed so one corpus answers the same twice.
func (c *Corpus) Baseline() float64 {
	if c.haveBase {
		return c.baseline
	}
	c.haveBase = true
	if len(c.Examples) < 2 {
		return c.baseline
	}
	rng := rand.New(rand.NewSource(1))
	sims := make([]float64, 0, baselinePairs)
	for len(sims) < baselinePairs {
		i, j := rng.Intn(len(c.Examples)), rng.Intn(len(c.Examples))
		if i == j {
			continue
		}
		sims = append(sims, embed.Cosine(c.Examples[i].Vec, c.Examples[j].Vec))
	}
	sort.Float64s(sims)
	at := int(BaselineQuantile * float64(len(sims)))
	if at >= len(sims) {
		at = len(sims) - 1
	}
	c.baseline = sims[at]
	return c.baseline
}

// PredictPass is the estimator under test: what the neighbourhood says the
// chance is that this enum passes. Absent when too few neighbours used it, so
// an anecdote is never dressed as a rate.
func PredictPass(n Neighbourhood, enum string) (float64, bool) {
	for _, ev := range n.PerEnum {
		if ev.Enum == enum {
			return ev.PassRate, ev.N >= MinNeighbours
		}
	}
	return 0, false
}

// laplace keeps a single observation away from 0 and 1, the same prior
// internal/trust puts behind every calibration cell.
func laplace(passed, n int) float64 {
	if n <= 0 {
		return 0
	}
	return float64(passed+1) / float64(n+2)
}
