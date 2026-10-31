// SPDX-License-Identifier: MIT

package classify

import (
	"errors"
	"math"
	"math/rand"
	"testing"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/util"
)

// cluster puts a point near one axis, so two clusters are far apart in cosine
// and members of one are close to each other.
func cluster(rng *rand.Rand, axis, dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(rng.NormFloat64()) * 0.05
	}
	v[axis] += 1
	return v
}

func example(vec []float32, model, enum string, passed bool, n int) evalset.Example {
	return evalset.Example{
		Candidate: "c", TaskHash: string(rune('a'+n%26)) + string(rune('a'+n/26)),
		Enum: enum, Passed: passed, Embedding: util.EncodeVec(vec), EmbedModel: model,
	}
}

// Two embedding models are two spaces, so a neighbourhood must be drawn from
// one of them. Cosine across both returns a number that means nothing.
func TestLoad_KeepsOneModelsSpace(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var all []evalset.Example
	for i := 0; i < 30; i++ {
		all = append(all, example(cluster(rng, 0, 8), "big", "SIMPLE", true, i))
	}
	for i := 0; i < 9; i++ {
		all = append(all, example(cluster(rng, 1, 8), "small", "COMPLEX", true, i))
	}
	c, err := Load(all)
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "big" || len(c.Examples) != 30 {
		t.Fatalf("loaded %d examples from %q, want 30 from big", len(c.Examples), c.Model)
	}
}

// Every way the corpus cannot answer is its own error, because the remedies
// differ: record vectors, record more of them, or ask about something the
// corpus has seen.
func TestLoad_RefusalsAreDistinct(t *testing.T) {
	if _, err := Load(nil); !errors.Is(err, ErrNoVectors) {
		t.Errorf("empty corpus: %v, want ErrNoVectors", err)
	}
	plain := []evalset.Example{{Candidate: "c", Enum: "SIMPLE"}}
	if _, err := Load(plain); !errors.Is(err, ErrNoVectors) {
		t.Errorf("no embeddings: %v, want ErrNoVectors", err)
	}
	rng := rand.New(rand.NewSource(7))
	var few []evalset.Example
	for i := 0; i < MinNeighbours; i++ {
		few = append(few, example(cluster(rng, 0, 8), "m", "SIMPLE", true, i))
	}
	if _, err := Load(few); !errors.Is(err, ErrTooFew) {
		t.Errorf("%d examples: %v, want ErrTooFew", len(few), err)
	}
}

func corpusOf(t *testing.T, n int, axis int, enum string, pass func(int) bool) *Corpus {
	t.Helper()
	rng := rand.New(rand.NewSource(11))
	var all []evalset.Example
	for i := 0; i < n; i++ {
		all = append(all, example(cluster(rng, axis, 8), "m", enum, pass(i), i))
	}
	c, err := Load(all)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A prompt with nothing like it in the corpus gets no neighbourhood, however
// close the absolute cosine happens to look.
func TestNear_RefusesWhenNothingIsNear(t *testing.T) {
	c := corpusOf(t, 40, 0, "SIMPLE", func(int) bool { return true })
	far := make([]float32, 8)
	far[5] = 1
	if _, err := c.Near(far, DefaultK, -1); !errors.Is(err, ErrNoNeighbourhood) {
		t.Errorf("a far prompt got a neighbourhood: %v", err)
	}
	near := make([]float32, 8)
	near[0] = 1
	if _, err := c.Near(near, DefaultK, -1); err != nil {
		t.Errorf("a prompt inside the cluster was refused: %v", err)
	}
}

// One pass out of one is not certainty. The Laplace prior is the same one
// internal/trust puts behind every calibration cell.
func TestPassRate_IsAPosteriorNotACount(t *testing.T) {
	if got := laplace(1, 1); got != 2.0/3.0 {
		t.Errorf("laplace(1,1) = %v, want 2/3", got)
	}
	if got := laplace(0, 1); got != 1.0/3.0 {
		t.Errorf("laplace(0,1) = %v, want 1/3", got)
	}
	if laplace(50, 50) >= 1 || laplace(0, 50) <= 0 {
		t.Error("a posterior reached certainty")
	}
}

// An enum a handful of neighbours used is an anecdote, and reporting its rate
// as a reading is how a router ends up acting on three examples.
func TestPredictPass_RefusesAnAnecdote(t *testing.T) {
	n := Neighbourhood{PerEnum: []EnumEvidence{
		{Enum: "PLENTY", N: MinNeighbours, Passed: 3, PassRate: 0.5},
		{Enum: "FEW", N: MinNeighbours - 1, Passed: 1, PassRate: 0.5},
	}}
	if _, ok := PredictPass(n, "PLENTY"); !ok {
		t.Error("an enum at the floor was refused")
	}
	if _, ok := PredictPass(n, "FEW"); ok {
		t.Error("an enum below the floor was reported as a reading")
	}
	if _, ok := PredictPass(n, "ABSENT"); ok {
		t.Error("an enum with no neighbours was reported as a reading")
	}
}

// The load-bearing claim: when similarity really does predict the outcome, the
// estimator finds it, and resolution is the term that says so.
func TestEvaluate_FindsSignalThatIsThere(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	var all []evalset.Example
	for i := 0; i < 60; i++ {
		all = append(all, example(cluster(rng, 0, 8), "m", "SIMPLE", true, i))
	}
	for i := 0; i < 60; i++ {
		all = append(all, example(cluster(rng, 1, 8), "m", "SIMPLE", false, 60+i))
	}
	c, err := Load(all)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Evaluate(c, DefaultK, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Beats() {
		t.Errorf("no resolution on a corpus where the clusters decide the outcome: %+v", res.Report)
	}
	if res.Report.Brier >= res.Report.Uncertainty {
		t.Errorf("Brier %.4f did not beat always predicting the base rate %.4f",
			res.Report.Brier, res.Report.Uncertainty)
	}
}

// And the other half: on a corpus where the outcome is a coin flip, it must not
// manufacture a finding. Resolution near zero is the honest answer.
func TestEvaluate_FindsNoSignalThatIsNotThere(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	var all []evalset.Example
	for i := 0; i < 120; i++ {
		all = append(all, example(cluster(rng, i%2, 8), "m", "SIMPLE", rng.Intn(2) == 0, i))
	}
	c, err := Load(all)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Evaluate(c, DefaultK, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Not "resolution is zero": a finite corpus yields some from chance alone,
	// which is exactly why the null exists and why Beats reads against it.
	if res.Beats() {
		t.Errorf("reported signal in noise: resolution %.4f against a null of %.4f",
			res.Report.Resolution, res.NullResolution)
	}
	if res.NullResolution <= 0 {
		t.Errorf("the null resolution is %.4f, so it is not measuring anything",
			res.NullResolution)
	}
}

// One corpus must evaluate to one number, or a reader cannot tell a change in
// the corpus from a reshuffle of the sample.
func TestEvaluate_IsDeterministic(t *testing.T) {
	c := corpusOf(t, 80, 0, "SIMPLE", func(i int) bool { return i%3 != 0 })
	a, err := Evaluate(c, DefaultK, 40)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Evaluate(corpusOf(t, 80, 0, "SIMPLE", func(i int) bool { return i%3 != 0 }), DefaultK, 40)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(a.Report.Brier-b.Report.Brier) > 1e-12 || a.Answered != b.Answered {
		t.Errorf("two runs disagreed: %+v vs %+v", a.Report.Brier, b.Report.Brier)
	}
}

// Leave-one-out over everything is quadratic, so the held-out set is bounded
// and the bound has to actually bind.
func TestEvaluate_HoldOutBoundBinds(t *testing.T) {
	c := corpusOf(t, 200, 0, "SIMPLE", func(i int) bool { return i%2 == 0 })
	res, err := Evaluate(c, DefaultK, 50)
	if err != nil {
		t.Fatal(err)
	}
	if res.Answered+res.Refused != 50 {
		t.Errorf("held out %d, want 50", res.Answered+res.Refused)
	}
	if res.Corpus != 200 {
		t.Errorf("neighbours came from %d examples, want the whole corpus", res.Corpus)
	}
}
