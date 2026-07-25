// SPDX-License-Identifier: MIT

package trust

import (
	"context"
	"math/rand"
)

// FixedSwarmBaseline is the fixed fan-out an SPRT run is compared against.
const FixedSwarmBaseline = 5

// BenchCase is the measured result for one task difficulty.
type BenchCase struct {
	Label       string  `json:"label"`
	Accuracy    float64 `json:"accuracy"`     // fraction of runs that returned the correct answer
	MeanSamples float64 `json:"mean_samples"` // average model calls
	SavedPct    float64 `json:"saved_pct"`    // vs a fixed-N swarm
}

// BenchmarkResult is the output of Benchmark.
type BenchmarkResult struct {
	Trials  int         `json:"trials"`
	FixedN  int         `json:"fixed_n"`
	Cases   []BenchCase `json:"cases"`
	Blended BenchCase   `json:"blended"` // 71% easy / 29% hard task mix
}

// benchExec answers `truth` with probability p, else a fixed wrong answer.
type benchExec struct {
	truth, wrong string
	p            float64
	rng          *rand.Rand
}

func (e *benchExec) Execute(_ context.Context, src Source, _ Task) (Answer, error) {
	if e.rng.Float64() < e.p {
		return Answer{Text: e.truth, CostUSD: src.EstCostUSD}, nil
	}
	return Answer{Text: e.wrong, CostUSD: src.EstCostUSD}, nil
}

// Benchmark runs the real SPRT ensemble (Run) over synthetic sources of known
// reliability and measures samples-vs-fixed-N and accuracy. It is the [MEASURED]
// counterpart to the Manifesto's [MODEL] Law 3 numbers — deterministic for a
// given seed. Easy tasks use 90%-reliable sources; hard tasks 74%.
func Benchmark(trials int, seed int64) BenchmarkResult {
	if trials <= 0 {
		trials = 20000
	}
	rng := rand.New(rand.NewSource(seed))
	const pool = 30

	run := func(label, id string, p float64) BenchCase {
		cal := &Calibrator{store: map[calibKey]*confusion{}}
		calibrateSynthetic(cal, id, "bench", p, 2000)
		sources := make([]Source, pool)
		for i := range sources {
			sources[i] = Source{ID: id, EstCostUSD: 1}
		}
		var totSamples, correct int
		for i := 0; i < trials; i++ {
			exec := &benchExec{truth: "A", wrong: "B", p: p, rng: rng}
			res, _ := Run(context.Background(), Task{Domain: "bench"}, sources, exec, cal, Target{Confidence: 0.95})
			totSamples += res.Samples
			if res.Candidate == "A" {
				correct++
			}
		}
		mean := float64(totSamples) / float64(trials)
		return BenchCase{
			Label:       label,
			Accuracy:    float64(correct) / float64(trials),
			MeanSamples: mean,
			SavedPct:    100 * (1 - mean/float64(FixedSwarmBaseline)),
		}
	}

	easy := run("easy (p=.90)", "bench-easy", 0.90)
	hard := run("hard (p=.74)", "bench-hard", 0.74)

	blended := BenchCase{
		Label:       "blended (71/29)",
		Accuracy:    0.71*easy.Accuracy + 0.29*hard.Accuracy,
		MeanSamples: 0.71*easy.MeanSamples + 0.29*hard.MeanSamples,
	}
	blended.SavedPct = 100 * (1 - blended.MeanSamples/float64(FixedSwarmBaseline))

	return BenchmarkResult{
		Trials:  trials,
		FixedN:  FixedSwarmBaseline,
		Cases:   []BenchCase{easy, hard},
		Blended: blended,
	}
}

// calibrateSynthetic trains (id,domain) to se=sp≈p with n balanced samples.
// Production twin of the test helper, used only by Benchmark.
func calibrateSynthetic(c *Calibrator, id, domain string, p float64, n int) {
	correct := int(p * float64(n))
	wrong := n - correct
	for i := 0; i < correct; i++ {
		_ = c.Update(id, domain, true, OutcomeCorrect)
		_ = c.Update(id, domain, false, OutcomeIncorrect)
	}
	for i := 0; i < wrong; i++ {
		_ = c.Update(id, domain, false, OutcomeCorrect)
		_ = c.Update(id, domain, true, OutcomeIncorrect)
	}
}
