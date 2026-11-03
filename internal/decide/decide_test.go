// SPDX-License-Identifier: MIT

package decide

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ankit373/hydra/internal/decision"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

// fakeAsker answers each head from a table and records who was asked.
type fakeAsker struct {
	answers map[string]decision.Result
	asked   []string
}

func (f *fakeAsker) Execute(_ context.Context, h provider.Head, _ decision.Request) (decision.Result, error) {
	f.asked = append(f.asked, h.ID)
	res, ok := f.answers[h.ID]
	if !ok {
		return decision.Result{}, errors.New("down")
	}
	return res, nil
}

var tone = decision.Request{
	Input: "Thanks, that fixed it.",
	Questions: []decision.Question{{
		Name: "tone",
		Kind: decision.Choice,
		Options: []decision.Option{
			{Name: "angry"},
			{Name: "calm"},
		},
	}},
}

func answered(probs ...float64) decision.Result {
	return decision.Result{Model: "m", Answers: []decision.Answer{{Probabilities: probs}}}
}

func heads(ids ...string) []provider.Head {
	out := make([]provider.Head, len(ids))
	for i, id := range ids {
		out[i] = provider.Head{ID: id, Provider: id}
	}
	return out
}

func free(provider.Head, decision.Request) float64 { return 0 }

// router asks ids in order, at no cost, for a 0.95 target.
func router(ask *fakeAsker, cal *trust.Calibrator, ids ...string) Router {
	return Router{
		Heads:  heads(ids...),
		Price:  free,
		Ask:    ask,
		Cal:    cal,
		Target: trust.Target{Confidence: 0.95},
	}
}

func newCal(t *testing.T) *trust.Calibrator {
	t.Helper()
	cal, err := trust.New(filepath.Join(t.TempDir(), "calibration.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

// calibrate gives a source n right answers and n right refusals in the default domain.
func calibrate(t *testing.T, cal *trust.Calibrator, id string, n int) {
	t.Helper()
	for range n {
		if err := cal.Update(id, trust.DefaultDomain, true, trust.OutcomeCorrect); err != nil {
			t.Fatal(err)
		}
		if err := cal.Update(id, trust.DefaultDomain, false, trust.OutcomeIncorrect); err != nil {
			t.Fatal(err)
		}
	}
}

// Calibrated so the SPRT would ask b, which only the stated-confidence shortcut prevents.
func TestDecide_OneSourceWhenItIsSureEnough(t *testing.T) {
	testutil.NewSandbox(t)
	cal := newCal(t)
	calibrate(t, cal, "a", 8)
	calibrate(t, cal, "b", 8)
	ask := &fakeAsker{answers: map[string]decision.Result{
		"a": answered(0.02, 0.98),
		"b": answered(0.5, 0.5),
	}}
	r := router(ask, cal, "a", "b")

	out, err := r.Decide(context.Background(), Call{Request: tone, RunID: "run-sure"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ask.asked, []string{"a"}) || !reflect.DeepEqual(out.Asked, []string{"a"}) {
		t.Errorf("asked %v, reported %v; want only a", ask.asked, out.Asked)
	}
	if out.Confidence != 0.98 {
		t.Errorf("Confidence = %v, want the source's own 0.98", out.Confidence)
	}
}

// With nothing calibrated another answer cannot move the posterior, so none is paid for.
func TestDecide_UncalibratedStopsAtTheFirstAnswer(t *testing.T) {
	testutil.NewSandbox(t)
	ask := &fakeAsker{answers: map[string]decision.Result{
		"a": answered(0.3, 0.7),
		"b": answered(0.2, 0.8),
	}}
	r := router(ask, newCal(t), "a", "b")

	out, err := r.Decide(context.Background(), Call{Request: tone, RunID: "run-uncal"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ask.asked) != 1 || out.Confidence != 0.7 {
		t.Errorf("asked %v at %v; want one source at its stated 0.7", ask.asked, out.Confidence)
	}
}

// An unsure first answer asks the next source, and two calibrated agreements clear the bar.
func TestDecide_AsksMoreOnlyWhenNeeded(t *testing.T) {
	testutil.NewSandbox(t)
	cal := newCal(t)
	calibrate(t, cal, "a", 8)
	calibrate(t, cal, "b", 8)
	ask := &fakeAsker{answers: map[string]decision.Result{
		"a": answered(0.4, 0.6),
		"b": answered(0.1, 0.9),
		"c": answered(0.1, 0.9),
	}}
	r := router(ask, cal, "a", "b", "c")

	out, err := r.Decide(context.Background(), Call{Request: tone, RunID: "run-more"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"a",
		"b",
	}
	if !reflect.DeepEqual(ask.asked, want) {
		t.Errorf("asked %v, want %v: the first answer reused, then one more", ask.asked, want)
	}
	if out.Confidence < 0.95 {
		t.Errorf("Confidence = %v, want the SPRT posterior past 0.95", out.Confidence)
	}
	if got := out.Result.Answers[0].Probabilities; !reflect.DeepEqual(got, []float64{0.4, 0.6}) {
		t.Errorf("returned %v, want the first source's answer to the accepted question", got)
	}
}

func TestDecide_BudgetSkipsASourceItCannotAfford(t *testing.T) {
	testutil.NewSandbox(t)
	ask := &fakeAsker{answers: map[string]decision.Result{
		"dear":  answered(0.02, 0.98),
		"cheap": answered(0.02, 0.98),
	}}
	price := func(h provider.Head, _ decision.Request) float64 {
		if h.ID == "dear" {
			return 1
		}
		return 0.001
	}
	r := router(ask, newCal(t), "dear", "cheap")
	r.Price, r.Target.MaxCostUSD = price, 0.01

	if _, err := r.Decide(context.Background(), Call{Request: tone, RunID: "run-budget"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ask.asked, []string{"cheap"}) {
		t.Errorf("asked %v, want only the source within budget", ask.asked)
	}
}

// b is ranked first and is past the budget, so the SPRT samples nothing and a's answer stands.
func TestDecide_SPRTThatSamplesNothingReturnsTheFirstAnswer(t *testing.T) {
	testutil.NewSandbox(t)
	cal := newCal(t)
	calibrate(t, cal, "b", 8)
	ask := &fakeAsker{answers: map[string]decision.Result{
		"a": answered(0.4, 0.6),
		"b": answered(0.4, 0.6),
	}}
	price := func(h provider.Head, _ decision.Request) float64 {
		if h.ID == "b" {
			return 0.5
		}
		return 0
	}
	r := router(ask, cal, "a", "b")
	r.Price, r.Target.MaxCostUSD = price, 0.1

	out, err := r.Decide(context.Background(), Call{Request: tone, RunID: "run-unsampled"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Result.Answers) != 1 || out.Confidence != 0.6 {
		t.Errorf("returned %+v at %v, want a's answer at its stated 0.6", out.Result, out.Confidence)
	}
}

// a is paid before the SPRT starts, so the SPRT gets only what is left: b, not b and c.
func TestDecide_FirstAnswerCountsAgainstTheBudget(t *testing.T) {
	testutil.NewSandbox(t)
	cal := newCal(t)
	calibrate(t, cal, "b", 8)
	calibrate(t, cal, "c", 8)
	ask := &fakeAsker{answers: map[string]decision.Result{
		"a": answered(0.4, 0.6),
		"b": answered(0.4, 0.6),
		"c": answered(0.4, 0.6),
	}}
	r := router(ask, cal, "a", "b", "c")
	r.Price = func(provider.Head, decision.Request) float64 { return 0.25 }
	r.Target.MaxCostUSD = 0.5

	if _, err := r.Decide(context.Background(), Call{Request: tone, RunID: "run-ceiling"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"a",
		"b",
	}
	if !reflect.DeepEqual(ask.asked, want) {
		t.Errorf("asked %v, want %v: three calls would spend $0.75 of $0.50", ask.asked, want)
	}
}

// When the first answer used the whole ceiling, a free source can still be asked.
func TestDecide_SpentCeilingStillAsksFreeSources(t *testing.T) {
	testutil.NewSandbox(t)
	cal := newCal(t)
	calibrate(t, cal, "b", 8)
	calibrate(t, cal, "c", 8)
	ask := &fakeAsker{answers: map[string]decision.Result{
		"a": answered(0.4, 0.6),
		"b": answered(0.4, 0.6),
		"c": answered(0.4, 0.6),
	}}
	price := func(h provider.Head, _ decision.Request) float64 {
		if h.ID == "b" {
			return 0
		}
		return 0.25
	}
	r := router(ask, cal, "a", "b", "c")
	r.Price, r.Target.MaxCostUSD = price, 0.25

	if _, err := r.Decide(context.Background(), Call{Request: tone, RunID: "run-spent"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"a",
		"b",
	}
	if !reflect.DeepEqual(ask.asked, want) {
		t.Errorf("asked %v, want %v: c is paid and nothing is left", ask.asked, want)
	}
}

// A source that failed is not asked again when the SPRT reaches it.
func TestDecide_FailedSourceIsAskedOnce(t *testing.T) {
	testutil.NewSandbox(t)
	cal := newCal(t)
	calibrate(t, cal, "dead", 8)
	calibrate(t, cal, "b", 8)
	ask := &fakeAsker{answers: map[string]decision.Result{"b": answered(0.4, 0.6)}}
	r := router(ask, cal, "dead", "b")

	if _, err := r.Decide(context.Background(), Call{Request: tone, RunID: "run-dead"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"dead",
		"b",
	}
	if !reflect.DeepEqual(ask.asked, want) {
		t.Errorf("asked %v, want %v", ask.asked, want)
	}
}

func TestDecide_NoSourceIsAnError(t *testing.T) {
	testutil.NewSandbox(t)
	r := router(&fakeAsker{}, newCal(t), "a")
	_, err := r.Decide(context.Background(), Call{Request: tone, RunID: "run-none"})
	if !errors.Is(err, ErrNoSource) {
		t.Errorf("err = %v, want ErrNoSource", err)
	}
}

// Each answer is a KindDecision span carrying what reliability audits: source, stated probability, domain.
func TestDecide_RecordsEachAnswerForTheReliabilityAudit(t *testing.T) {
	testutil.NewSandbox(t)
	predicate := decision.Request{Questions: []decision.Question{{Name: "billing", Kind: decision.Predicate}}}
	ask := &fakeAsker{answers: map[string]decision.Result{"a": answered(0.3, 0.7)}}
	r := router(ask, newCal(t), "a")

	call := Call{Request: predicate, Domain: "billing", RunID: "run-log"}
	if _, err := r.Decide(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	events, err := runlog.Load("run-log")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one decision span", events)
	}
	e := events[0]
	// A predicate's forecast is P(true), even when false is the likelier outcome.
	if e.Kind != runlog.KindDecision || e.Head != "a" || e.Confidence != 0.3 || e.Domain != "billing" {
		t.Errorf("event = %+v, want a decision by a at P(true) 0.3 in billing", e)
	}
	if e.SpanID == "" {
		t.Error("the decision has no span id, so no verdict can be attached to it")
	}
}
