// SPDX-License-Identifier: MIT

package bench

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
)

// recordSleep is a backoff that never waits and remembers what it was asked to.
type recordSleep struct{ waits []time.Duration }

func (r *recordSleep) sleep(_ context.Context, d time.Duration) error {
	r.waits = append(r.waits, d)
	return nil
}

func fallbackArm(t *testing.T, exec *fakeExec, faults *FaultPlan) (*Direct, *recordSleep) {
	t.Helper()
	rs := &recordSleep{}
	d, err := NewDirect(StaticFallback, ArmConfig{Kind: "direct", Models: testArmSpecs[StaticFallback]},
		testPrices("m-front", "m-mid"), exec.For, faults, rs.sleep)
	if err != nil {
		t.Fatal(err)
	}
	return d, rs
}

func callOne(t *testing.T, d *Direct, id string) Attempt {
	t.Helper()
	it := fakeItems(1)["test/000"]
	it.ID = id
	req, _ := fakeSuite{}.Prompt(it)
	att, err := d.Call(context.Background(), it, req, NewBudget(100, 0), noSpend)
	if err != nil {
		t.Fatal(err)
	}
	return att
}

// static-fallback retries a transport error, a 429 or a 5xx on the same model, then
// moves; never on a 4xx or a wrong answer: the LiteLLM/Portkey shape, nothing cleverer.
func TestStaticFallback_MovesOnlyOnTransport429And5xx(t *testing.T) {
	for name, c := range map[string]struct {
		err   error
		moves bool
		kind  string
	}{
		"503":       {&executor.StatusError{Code: 503}, true, ""},
		"429":       {&executor.StatusError{Code: 429}, true, ""},
		"transport": {netErr, true, ""},
		"400":       {&executor.StatusError{Code: 400}, false, "status:400"},
	} {
		exec := newFakeExec()
		exec.set("bench/openrouter/m-front", "*", reply{err: c.err})
		d, _ := fallbackArm(t, exec, nil)
		att := callOne(t, d, "test/000")
		if moved := exec.count("bench/openrouter/m-mid") == 1; moved != c.moves || att.ErrKind != c.kind {
			t.Errorf("%s: moved=%v kind=%q, want moved=%v kind=%q", name, moved, att.ErrKind, c.moves, c.kind)
		}
		want := 1 // a 400 is final
		if c.moves {
			want = 1 + len(retryBackoff)
		}
		if exec.count("bench/openrouter/m-front") != want || (c.moves && att.Answer != "yes") || att.transient {
			t.Errorf("%s: %d calls on the first model, want %d; answer %q", name, exec.count("bench/openrouter/m-front"), want, att.Answer)
		}
	}
	exec := newFakeExec()
	exec.set("bench/openrouter/m-front", "*", reply{answer: "no"}) // wrong, but an answer
	d, _ := fallbackArm(t, exec, nil)
	if att := callOne(t, d, "test/000"); exec.count("bench/openrouter/m-mid") != 0 || att.Answer != "no" {
		t.Fatal("a wrong answer moved to the next model")
	}
	exec = newFakeExec()
	exec.set("bench/openrouter/m-front", "*", reply{err: &executor.StatusError{Code: 502}})
	exec.set("bench/openrouter/m-mid", "*", reply{err: &executor.StatusError{Code: 503}})
	d, _ = fallbackArm(t, exec, nil)
	if att := callOne(t, d, "test/000"); att.ErrKind != "status:503" || len(att.Calls) != 2*(1+len(retryBackoff)) || !att.transient {
		t.Fatalf("both failing: %+v", att)
	}
}

// A transient failure is retried on a bounded schedule, honouring a longer
// Retry-After up to a cap; a 429 or 503 was turned away unbilled and costs nothing,
// while a 500 or a transport error may have been billed and costs its ceiling.
func TestDirect_RetriesTransientFailuresAndPricesThemHonestly(t *testing.T) {
	exec := newFakeExec()
	exec.set("bench/openrouter/m-front", "*", reply{err: &executor.StatusError{Code: 429, RetryAfter: 3 * time.Second}})
	d, rs := fallbackArm(t, exec, nil)
	d.heads, d.models = d.heads[:1], d.models[:1]
	att := callOne(t, d, "test/000")
	if want := []time.Duration{3 * time.Second, 3 * time.Second, 4 * time.Second}; !reflect.DeepEqual(rs.waits, want) {
		t.Fatalf("waited %v, want %v (Retry-After where longer than the schedule)", rs.waits, want)
	}
	if !att.transient || att.ErrKind != "status:429" || len(att.Calls) != 4 {
		t.Fatalf("attempt %+v", att)
	}
	for _, c := range att.Calls {
		if c.CostUSD != 0 || c.PriceRef != "refused-unbilled" {
			t.Fatalf("a 429 was charged: %+v", c)
		}
	}
	if got := backoff(0, &executor.StatusError{Code: 429, RetryAfter: time.Hour}); got != maxRetryAfter {
		t.Fatalf("a Retry-After of an hour waited %v", got)
	}

	exec = newFakeExec()
	exec.set("bench/openrouter/m-front", "*", reply{err: &executor.StatusError{Code: 500}})
	d, _ = fallbackArm(t, exec, nil)
	d.heads, d.models = d.heads[:1], d.models[:1]
	att = callOne(t, d, "test/000")
	if att.Calls[0].CostUSD == 0 || att.Calls[0].PriceRef != "reserved" {
		t.Fatalf("a 500 may have been billed, so it costs its ceiling: %+v", att.Calls[0])
	}

	// A retry that succeeds is an answer, and only the errored attempts before it cost extra.
	exec = newFakeExec()
	exec.set("bench/openrouter/m-front", "q000", reply{err: netErr})
	d, _ = fallbackArm(t, exec, nil)
	flaky := &flakyExec{inner: exec, fails: 1}
	d.exec = flaky.For
	att = callOne(t, d, "test/000")
	if att.ErrKind != "" || att.Answer != "yes" || len(att.Calls) != 2 {
		t.Fatalf("a retried success: %+v", att)
	}

	// The budget is checked before every retry, not once per item.
	exec = newFakeExec()
	exec.set("bench/openrouter/m-front", "*", reply{err: &executor.StatusError{Code: 500}})
	d, _ = fallbackArm(t, exec, nil)
	it := fakeItems(1)["test/000"]
	req, _ := fakeSuite{}.Prompt(it)
	att, err := d.Call(context.Background(), it, req, NewBudget(2.5*0.002, 0), noSpend)
	if !errors.Is(err, ErrBudget) || len(att.Calls) != 2 || exec.calls.Load() != 2 {
		t.Fatalf("budget for two calls allowed %d (err %v)", exec.calls.Load(), err)
	}
}

// flakyExec fails its first n calls with a timeout, then defers to inner.
type flakyExec struct {
	inner *fakeExec
	fails int
}

func (f *flakyExec) For(h provider.Head) executor.Executor { return f }

func (f *flakyExec) Execute(ctx context.Context, req executor.Request) (*executor.Response, error) {
	if f.fails > 0 {
		f.fails--
		return nil, netErr
	}
	f.inner.set("bench/openrouter/m-front", "q000", reply{answer: "yes"})
	return f.inner.Execute(ctx, req)
}

// An injected fault never reaches the provider, so it costs nothing and the next
// model answers.
func TestStaticFallback_InjectedFaultMakesNoCall(t *testing.T) {
	exec := newFakeExec()
	plan := &FaultPlan{Rate: 1, Seed: 1}
	d, _ := fallbackArm(t, exec, plan)
	att := callOne(t, d, "test/000")
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
	d, _ := fallbackArm(t, newFakeExec(), nil)
	req := executor.Request{Prompt: "abcdef", MaxTokens: 32}
	wc, err := d.WorstCaseUSD(req)
	if err != nil || wc != 2*4*3*1000/1e6 {
		t.Fatalf("worst case = %v, %v; want two models × four attempts × 3 tokens", wc, err)
	}
	if d.Name() != StaticFallback || len(d.Config().Models) != 2 {
		t.Fatalf("name/config %s %v", d.Name(), d.Config())
	}
	if _, err := NewDirect("x", ArmConfig{Models: []string{"openrouter:unpriced"}}, testPrices("m"), nil, nil, nil); err == nil {
		t.Fatal("an arm with an unpriced model was built")
	}
	if _, err := NewDirect("x", ArmConfig{}, testPrices("m"), nil, nil, nil); err == nil {
		t.Fatal("an arm with no model was built")
	}
	if _, err := NewDirect("x", ArmConfig{Models: []string{"bedrock:<id>"}}, testPrices("m"), nil, nil, nil); err == nil {
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

// An answer the snapshot cannot price still cost money: it is recorded at its
// ceiling, marked unpriced, and handed to spend before the run stops.
func TestDirect_AnUnpricedAnswerIsRecordedAtItsCeiling(t *testing.T) {
	exec := newFakeExec()
	d, _ := fallbackArm(t, exec, nil)
	exec.before = func(string, string) { delete(d.prices, "m-front") } // lost while the call ran
	it := fakeItems(1)["test/000"]
	req, _ := fakeSuite{}.Prompt(it)
	var spent []Call
	att, err := d.Call(context.Background(), it, req, NewBudget(1, 0), func(c Call) error { spent = append(spent, c); return nil })
	if err == nil || len(att.Calls) != 1 || att.Calls[0].PriceRef != "unpriced" || att.Calls[0].CostUSD == 0 {
		t.Fatalf("err=%v calls=%+v", err, att.Calls)
	}
	if len(spent) != 1 || spent[0] != att.Calls[0] {
		t.Fatalf("spend saw %+v", spent)
	}
}

// A local error never left the machine: no retry, no fallback, no cost, and a
// permanent failure rather than an unscored one.
func TestDirect_ALocalErrorFailsTheRowWithoutRetry(t *testing.T) {
	exec := newFakeExec()
	exec.set("bench/openrouter/m-front", "*", reply{err: errors.New("http exec: marshal request: unsupported value")})
	d, rs := fallbackArm(t, exec, nil)
	att := callOne(t, d, "test/000")
	if att.ErrKind != "local" || att.transient || len(att.Calls) != 1 || att.Calls[0].CostUSD != 0 || len(rs.waits) != 0 {
		t.Fatalf("local error: %+v, waited %v", att, rs.waits)
	}
	if exec.count("bench/openrouter/m-mid") != 0 {
		t.Fatal("a local error fell back to the next model")
	}
	for err, want := range map[error]bool{
		netErr:                           true,
		io.ErrUnexpectedEOF:              true,
		context.DeadlineExceeded:         true,
		errors.New("bad config"):         false,
		&executor.StatusError{Code: 502}: true,
		&executor.StatusError{Code: 404}: false,
	} {
		if transient(err) != want {
			t.Errorf("transient(%v) = %v, want %v", err, !want, want)
		}
	}
}

// lateKill answers, but the run's context is cancelled while it does: the call was
// completed and billed for real.
type lateKill struct{ cancel context.CancelFunc }

func (l lateKill) For(provider.Head) executor.Executor { return l }

func (l lateKill) Execute(_ context.Context, req executor.Request) (*executor.Response, error) {
	l.cancel()
	return &executor.Response{Output: "yes", InputTokens: 1, Model: req.Head.Meta["model"]}, nil
}

// A call that completed as the kill arrived is charged its real cost, not its ceiling,
// and its answer is kept.
func TestDirect_ACallThatFinishedAsTheKillCameIsPricedAndKept(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d, _ := fallbackArm(t, newFakeExec(), nil)
	d.exec = lateKill{cancel}.For
	it := fakeItems(1)["test/000"]
	req, _ := fakeSuite{}.Prompt(it)
	var spent []Call
	att, err := d.Call(ctx, it, req, NewBudget(1, 0), func(c Call) error { spent = append(spent, c); return nil })
	if err != nil || att.Answer != "yes" || len(att.Calls) != 1 {
		t.Fatalf("answer %q, err %v, calls %+v: want the answer kept", att.Answer, err, att.Calls)
	}
	if c := att.Calls[0]; c.CostUSD != 0.001 || c.PriceRef != "ref/m-front" || len(spent) != 1 {
		t.Fatalf("call %+v: want its real $0.001, not the $0.002 ceiling", c)
	}
}
