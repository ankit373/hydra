// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"errors"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ankit373/hydra/internal/classify"
	"github.com/ankit373/hydra/internal/embed"
	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/signals"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/util"
)

// countingEmbedder is how the test proves the call was skipped rather than
// merely that the answer was absent: two different bugs with the same output.
type countingEmbedder struct {
	mu    sync.Mutex
	calls int
	dim   int
}

func (c *countingEmbedder) Available() bool { return true }
func (c *countingEmbedder) Model() string   { return "counting" }

func (c *countingEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	v := make([]float32, c.dim)
	v[0] = 1
	return v, nil
}

func (c *countingEmbedder) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// dispatcherWithRules builds one whose embedder and corpus are already in
// place, so Decide touches neither the network nor the disk.
func dispatcherWithRules(t *testing.T, yaml string, corpus *classify.Corpus) (*Dispatcher, *countingEmbedder) {
	t.Helper()
	eng, err := signals.Parse([]byte(yaml), nil)
	if err != nil {
		t.Fatalf("parsing rules: %v", err)
	}
	emb := &countingEmbedder{dim: 8}
	d := &Dispatcher{rules: eng}
	d.corpusOnce.Do(func() { d.corpusEmb = emb })
	d.corpusLoad.Do(func() { d.corpusData = corpus })
	return d, emb
}

// vectorCorpus builds two well-separated clusters: the work the query is near,
// and unrelated work. A corpus of near-identical vectors would put the p90
// neighbourhood gate above everything and nothing would ever be near.
func vectorCorpus(t *testing.T, n int, nearPassed bool) *classify.Corpus {
	t.Helper()
	rng := rand.New(rand.NewSource(9))
	jitter := func(axis int) []float32 {
		v := make([]float32, 8)
		for i := range v {
			v[i] = float32(rng.NormFloat64()) * 0.08
		}
		v[axis] += 1
		return v
	}
	var all []evalset.Example
	for i := 0; i < n; i++ {
		axis, passed := 0, nearPassed
		if i%2 == 1 {
			axis, passed = 4, !nearPassed // the far cluster, deliberately opposite
		}
		all = append(all, evalset.Example{
			Candidate: "c" + strconv.Itoa(i), TaskHash: strconv.Itoa(i),
			Enum: "SIMPLE", Passed: passed,
			Embedding: util.EncodeVec(jitter(axis)), EmbedModel: "counting",
		})
	}
	c, err := classify.Load(all)
	if err != nil {
		t.Fatalf("building the corpus: %v", err)
	}
	return c
}

const ruleReadingCorpus = `version: 1
rules:
  - name: failing work
    priority: 10
    when: corpus.known && corpus.pass_rate < 0.5
    action: {type: route, tier: "4"}
`

// The whole reason this signal is opt-in. signals.yaml ships empty, so the
// default machine must not pay an embedding call per dispatch, the cost #750
// removed from the probe path.
func TestDecide_NoRuleAsksSoNothingIsEmbedded(t *testing.T) {
	d, emb := dispatcherWithRules(t, "version: 1\nrules: []\n", vectorCorpus(t, 60, false))
	dec := d.Decide(context.Background(), "rotate the signing key", "go", nil)
	if dec.Fired() {
		t.Errorf("an empty rules file fired: %+v", dec)
	}
	if n := emb.count(); n != 0 {
		t.Errorf("embedded %d times with no rule asking; the signal is not opt-in", n)
	}
}

// And the other half: a rule that names the signal does get it, or the opt-in
// buys nothing.
func TestDecide_ARuleAskingGetsTheCorpusEvidence(t *testing.T) {
	d, emb := dispatcherWithRules(t, ruleReadingCorpus, vectorCorpus(t, 60, false))
	dec := d.Decide(context.Background(), "rotate the signing key", "go", nil)
	if n := emb.count(); n != 1 {
		t.Fatalf("embedded %d times, want exactly 1", n)
	}
	if !dec.Fired() || dec.Rule != "failing work" {
		t.Errorf("a corpus of failures did not fire the rule written for it: %+v", dec)
	}
}

// A corpus where work like this passes must not fire a rule written to catch
// failing work, or the signal is a constant wearing a threshold.
func TestDecide_PassingCorpusDoesNotFireTheFailureRule(t *testing.T) {
	d, _ := dispatcherWithRules(t, ruleReadingCorpus, vectorCorpus(t, 60, true))
	if dec := d.Decide(context.Background(), "rotate the signing key", "go", nil); dec.Fired() {
		t.Errorf("a corpus of passes fired the failure rule: %+v", dec)
	}
}

// Absent, never zero. A zero pass rate means work like this always failed, so
// a missing corpus that read as zero would fire exactly this rule.
func TestDecide_NoCorpusLeavesTheSignalAbsent(t *testing.T) {
	d, emb := dispatcherWithRules(t, ruleReadingCorpus, nil)
	if dec := d.Decide(context.Background(), "rotate the signing key", "go", nil); dec.Fired() {
		t.Errorf("an absent corpus read as a zero pass rate: %+v", dec)
	}
	if n := emb.count(); n != 0 {
		t.Errorf("embedded %d times with no corpus to compare against", n)
	}
}

// corpus.known has to separate "asked and found nothing" from "never asked",
// or a rule cannot tell a quiet machine from an unmeasured one.
func TestDecide_KnownIsFalseWhenAskedAndEmpty(t *testing.T) {
	const rule = `version: 1
rules:
  - name: nothing known
    priority: 10
    when: "!corpus.known"
    action: {type: route, tier: "4"}
`
	d, _ := dispatcherWithRules(t, rule, nil)
	dec := d.Decide(context.Background(), "rotate the signing key", "go", nil)
	if !dec.Fired() {
		t.Errorf("asked with no corpus did not report corpus.known false: %+v", dec)
	}
}

// A rule naming a signal that does not exist fails at load (#903), so the three
// new ones have to be declared or every rule using them is refused.
func TestSchema_DeclaresTheCorpusSignals(t *testing.T) {
	eng, err := signals.Parse([]byte(ruleReadingCorpus), nil)
	if err != nil {
		t.Fatalf("a rule reading the corpus signals will not load: %v", err)
	}
	got := strings.Join(eng.Signals(), " ")
	for _, name := range signals.CorpusSignals {
		if !strings.Contains(got, name) {
			t.Errorf("%s is not in the schema, so no rule can name it", name)
		}
	}
}

// An embedder that cannot answer is the normal state of a machine with no
// embedding model, so it must leave the signal absent rather than error out of
// the dispatch.
func TestCorpusEvidence_UnavailableEmbedderIsAbsentNotAnError(t *testing.T) {
	eng, err := signals.Parse([]byte(ruleReadingCorpus), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, emb := range []embed.Embedder{nil, embed.Unavailable{}, &failingEmbedder{}} {
		d := &Dispatcher{rules: eng}
		d.corpusOnce.Do(func() { d.corpusEmb = emb })
		d.corpusLoad.Do(func() { d.corpusData = vectorCorpus(t, 60, false) })
		if rate, support := d.corpusEvidence(context.Background(), "anything"); rate != nil || support != nil {
			t.Errorf("%T produced evidence: rate=%v support=%v", emb, rate, support)
		}
		if dec := d.Decide(context.Background(), "anything", "go", nil); dec.Fired() {
			t.Errorf("%T still fired the rule: %+v", emb, dec)
		}
	}
}

type failingEmbedder struct{}

func (failingEmbedder) Available() bool { return true }
func (failingEmbedder) Model() string   { return "failing" }
func (failingEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("no embedding server")
}

// A prompt nothing in the corpus is near gets no evidence, which is the
// neighbourhood gate doing its job on the dispatch path rather than only in
// hyctl eval classify.
func TestCorpusEvidence_FarPromptGetsNoNeighbourhood(t *testing.T) {
	eng, err := signals.Parse([]byte(ruleReadingCorpus), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{rules: eng}
	// A unit vector on an axis neither cluster occupies.
	far := make([]float32, 8)
	far[7] = 1
	d.corpusOnce.Do(func() { d.corpusEmb = fixedEmbedder{vec: far} })
	d.corpusLoad.Do(func() { d.corpusData = vectorCorpus(t, 60, false) })

	if rate, support := d.corpusEvidence(context.Background(), "unrelated"); rate != nil || support != nil {
		t.Errorf("a far prompt produced evidence: rate=%v support=%v", rate, support)
	}
}

type fixedEmbedder struct{ vec []float32 }

func (fixedEmbedder) Available() bool { return true }
func (fixedEmbedder) Model() string   { return "fixed" }
func (f fixedEmbedder) Embed(context.Context, string) ([]float32, error) {
	return f.vec, nil
}

// corpus() reads the eval set at most once, and an absent or unusable one
// leaves it nil rather than failing every dispatch that follows.
func TestCorpus_MissingEvalSetIsNilNotAFailure(t *testing.T) {
	testutil.NewSandbox(t)
	d := &Dispatcher{}
	if c := d.corpus(); c != nil {
		t.Errorf("an empty eval set produced a corpus: %+v", c)
	}
	// Second call must not read again; the Once is what keeps this off the
	// per-dispatch path.
	if c := d.corpus(); c != nil {
		t.Error("the second call built a corpus the first did not")
	}
}
