// SPDX-License-Identifier: MIT

package bench

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/executor"
)

func fallbackArm(t *testing.T, exec *fakeExec, faults *FaultPlan) *Direct {
	t.Helper()
	d, err := NewDirect(StaticFallback, ArmConfig{Kind: "direct", Models: testArmSpecs[StaticFallback]},
		testPrices("m-front", "m-mid"), exec.For, faults)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func callOne(t *testing.T, d *Direct, id string) Attempt {
	t.Helper()
	it := fakeItems(1)["test/000"]
	it.ID = id
	req, _ := fakeSuite{}.Prompt(it)
	att, err := d.Call(context.Background(), it, req)
	if err != nil {
		t.Fatal(err)
	}
	return att
}

// static-fallback moves on a transport error, a 429 or a 5xx, never on a 4xx or a
// wrong answer: the LiteLLM/Portkey shape, nothing cleverer.
func TestStaticFallback_MovesOnlyOnTransport429And5xx(t *testing.T) {
	for name, c := range map[string]struct {
		err   error
		moves bool
		kind  string
	}{
		"503":       {&executor.StatusError{Code: 503}, true, ""},
		"429":       {&executor.StatusError{Code: 429}, true, ""},
		"transport": {errors.New("connection reset"), true, ""},
		"400":       {&executor.StatusError{Code: 400}, false, "status:400"},
	} {
		exec := newFakeExec()
		exec.set("bench/openrouter/m-front", "*", reply{err: c.err})
		att := callOne(t, fallbackArm(t, exec, nil), "test/000")
		if moved := exec.count("bench/openrouter/m-mid") == 1; moved != c.moves || att.ErrKind != c.kind {
			t.Errorf("%s: moved=%v kind=%q, want moved=%v kind=%q", name, moved, att.ErrKind, c.moves, c.kind)
		}
		if c.moves && (len(att.Calls) != 2 || att.Calls[0].PriceRef != "reserved" || att.Answer != "yes") {
			t.Errorf("%s: calls %+v answer %q", name, att.Calls, att.Answer)
		}
	}
	exec := newFakeExec()
	exec.set("bench/openrouter/m-front", "*", reply{answer: "no"}) // wrong, but an answer
	if att := callOne(t, fallbackArm(t, exec, nil), "test/000"); exec.count("bench/openrouter/m-mid") != 0 || att.Answer != "no" {
		t.Fatal("a wrong answer moved to the next model")
	}
	exec = newFakeExec()
	exec.set("bench/openrouter/m-front", "*", reply{err: &executor.StatusError{Code: 502}})
	exec.set("bench/openrouter/m-mid", "*", reply{err: &executor.StatusError{Code: 503}})
	if att := callOne(t, fallbackArm(t, exec, nil), "test/000"); att.ErrKind != "status:503" || len(att.Calls) != 2 {
		t.Fatalf("both failing: %+v", att)
	}
}

// An injected fault never reaches the provider, so it costs nothing and the next
// model answers.
func TestStaticFallback_InjectedFaultMakesNoCall(t *testing.T) {
	exec := newFakeExec()
	plan := &FaultPlan{Rate: 1, Seed: 1}
	att := callOne(t, fallbackArm(t, exec, plan), "test/000")
	if exec.count("bench/openrouter/m-front") != 0 || exec.count("bench/openrouter/m-mid") != 1 {
		t.Fatalf("calls front=%d mid=%d, want 0 and 1", exec.count("bench/openrouter/m-front"), exec.count("bench/openrouter/m-mid"))
	}
	if att.Calls[0].PriceRef != "injected-fault" || att.Calls[0].CostUSD != 0 || att.ErrKind != "" {
		t.Fatalf("fault call %+v", att)
	}
	// The plan is a function of the seed: about the rate, the same items twice, both codes.
	codes := map[int]int{}
	hit := 0
	for i := 0; i < 2000; i++ {
		id := strings.Repeat("x", i)
		c := (&FaultPlan{Rate: 0.1, Seed: 3}).code(id)
		if c != (&FaultPlan{Rate: 0.1, Seed: 3}).code(id) {
			t.Fatal("one seed faulted an item two ways")
		}
		if c != 0 {
			hit++
			codes[c]++
		}
	}
	if hit < 150 || hit > 250 || codes[429] == 0 || codes[503] == 0 {
		t.Fatalf("%d of 2000 faulted (%v), want about 10%% of both codes", hit, codes)
	}
	if (*FaultPlan)(nil).code("x") != 0 {
		t.Fatal("no plan faulted an item")
	}
}

func TestDirect_WorstCaseCoversEveryModelAndRefusesUnpriced(t *testing.T) {
	d := fallbackArm(t, newFakeExec(), nil)
	req := executor.Request{Prompt: "abcdef", MaxTokens: 32}
	wc, err := d.WorstCaseUSD(req)
	if err != nil || wc != 2*3*1000/1e6 {
		t.Fatalf("worst case = %v, %v; want two models × 3 tokens", wc, err)
	}
	if d.Name() != StaticFallback || len(d.Config().Models) != 2 {
		t.Fatalf("name/config %s %v", d.Name(), d.Config())
	}
	if _, err := NewDirect("x", ArmConfig{Models: []string{"openrouter:unpriced"}}, testPrices("m"), nil, nil); err == nil {
		t.Fatal("an arm with an unpriced model was built")
	}
	if _, err := NewDirect("x", ArmConfig{}, testPrices("m"), nil, nil); err == nil {
		t.Fatal("an arm with no model was built")
	}
	if _, err := NewDirect("x", ArmConfig{Models: []string{"bedrock:<id>"}}, testPrices("m"), nil, nil); err == nil {
		t.Fatal("an unpinned model was built")
	}
	d.prices = PriceSnapshot{}
	if _, err := d.WorstCaseUSD(req); err == nil {
		t.Fatal("worst case without a price")
	}
}

func TestParseModel(t *testing.T) {
	for spec, ok := range map[string]bool{
		"bedrock:amazon.nova-micro-v1:0":  true,
		"openrouter:anthropic/claude-4.5": true,
		"bedrock:<strongest Opus id>":     false,
		"anthropic:claude":                false,
		"bedrock":                         false,
		"bedrock:":                        false,
	} {
		if _, _, err := ParseModel(spec); (err == nil) != ok {
			t.Errorf("ParseModel(%q) err=%v, want ok=%v", spec, err, ok)
		}
	}
	h, m, _ := headFor("bedrock:amazon.nova-micro-v1:0")
	if h.Provider != "bedrock" || h.Source != "env" || h.Meta["model"] != m || m != "amazon.nova-micro-v1:0" {
		t.Fatalf("head %+v", h)
	}
}
