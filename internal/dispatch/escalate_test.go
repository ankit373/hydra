// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

const climbClass = "classification.climb"

// climbing is one head of a climbingDispatcher: its in-class record out of 100 and its price.
type climbing struct {
	head    provider.Head
	correct int
	usd     float64
}

// climbingDispatcher measures each head in climbClass and prices it, so the cost
// constraint runs the cheapest competent head first and escalation has a measured order.
func climbingDispatcher(t *testing.T, hs ...climbing) *Dispatcher {
	t.Helper()
	cal, err := trust.New("")
	if err != nil {
		t.Fatal(err)
	}
	prices := map[string]float64{}
	var heads []provider.Head
	for _, c := range hs {
		heads = append(heads, c.head)
		prices[c.head.ID] = c.usd
		if c.correct >= 0 {
			record(t, cal, c.head.ID, climbClass, c.correct, 100)
		}
	}
	d := liveDispatcher(heads...)
	d.cal = cal
	d.price = func(h provider.Head) (float64, bool) { usd, ok := prices[h.ID]; return usd, ok }
	return d
}

// judgedHeads escalates past every answer and returns the heads it was shown, in order.
func judgedHeads(t *testing.T, d *Dispatcher) []string {
	t.Helper()
	var judged []string
	_, err := d.Dispatch(context.Background(), "hi", Options{
		Domain: climbClass,
		Escalate: func(_ context.Context, r *Result) (bool, string) {
			judged = append(judged, r.Head.ID)
			return true, "rejected"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return judged
}

// answeringHead is an OpenAI-shaped head that always says answer and reports usage.
func answeringHead(t *testing.T, id, answer string, capScore int) provider.Head {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":1000,"completion_tokens":500}}`, answer)
	}))
	t.Cleanup(srv.Close)
	return provider.Head{ID: id, Name: id, Provider: "openai", Source: "env", Endpoint: srv.URL, AuthReady: true, CapScore: capScore}
}

// pricedModel writes a fresh pricing cache naming one model, so Load reads it and fetches nothing.
func pricedModel(t *testing.T, model string, inPerM, outPerM float64) *pricing.DB {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"fetched_at": time.Now().UTC(), "source": "test",
		"models": map[string]any{model: map[string]float64{"input_per_mtok": inPerM, "output_per_mtok": outPerM}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.Dir(), "pricing_cache.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return pricing.Load()
}

func costRows(t *testing.T) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(config.Dir(), "logs", "cost.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, m)
	}
	return rows
}

func TestCallCost(t *testing.T) {
	testutil.NewSandbox(t)
	db := pricedModel(t, "test/priced", 2, 4)
	priced := provider.Head{ID: "p", CapScore: 90, Meta: map[string]string{"model": "test/priced"}}
	tierOnly := provider.Head{ID: "q", CapScore: 90}
	if got := CallCost(db, priced, 1_000_000, 1_000_000); got != 6 {
		t.Errorf("model-priced call cost %v, want 6 from the model's own rates", got)
	}
	if got, want := CallCost(db, tierOnly, 1000, 500), db.EstimateCost(rank.UITier(tierOnly), 1000, 500); got != want {
		t.Errorf("a head naming no model cost %v, want its tier price %v", got, want)
	}
	if got := CallCost(nil, priced, 1000, 500); got != 0 {
		t.Errorf("no pricing DB priced a call at %v, want 0", got)
	}
}

// G14. One number for one call: the result, its cost row and its runlog span
// agree, and it is the model's price, which a tier-only derivation would miss.
func TestOneCostValue_RowRunlogAndResultAgree(t *testing.T) {
	testutil.NewSandbox(t)
	h := answeringHead(t, "priced", "ok", 90)
	h.Meta = map[string]string{"model": "test/priced"}
	d := liveDispatcher(h)
	d.pricing = pricedModel(t, "test/priced", 2, 4)

	res, err := d.Dispatch(context.Background(), "hi", Options{RunID: "run-cost", TaskID: "task-cost"})
	if err != nil {
		t.Fatal(err)
	}
	rows := costRows(t)
	if len(rows) != 1 {
		t.Fatalf("cost.jsonl has %d rows, want 1", len(rows))
	}
	events, err := runlog.Load("run-cost")
	if err != nil {
		t.Fatal(err)
	}
	var span float64
	for _, e := range events {
		if e.Kind == runlog.KindDispatchFinished {
			span = e.CostUSD
		}
	}
	row, _ := rows[0]["est_cost_usd"].(float64)
	want := CallCost(d.pricing, h, 1000, 500)
	if res.CostUSD != want || row != want || span != want || want == d.EstimateCost(rank.UITier(h), 1000, 500) {
		t.Errorf("Result.CostUSD=%v, cost.jsonl est_cost_usd=%v, runlog CostUSD=%v: three numbers for one call (model price %v)",
			res.CostUSD, row, span, want)
	}
}

// G15. A rejected answer was paid for, so it is costed, and was not the answer,
// so it is never cached for the next caller.
func TestEscalate_EscalatedAnswerIsCostedNotCached(t *testing.T) {
	testutil.NewSandbox(t)
	d := climbingDispatcher(t,
		climbing{answeringHead(t, "h1", "first", 90), 92, 0.01}, climbing{answeringHead(t, "h2", "second", 90), 98, 0.02})
	d.cfg = &config.Config{CacheAnswers: true}
	const prompt = "rotate the signing key"

	// Every answer is escalated past, so none of them may reach the cache.
	res, err := d.Dispatch(context.Background(), prompt, Options{
		Domain:   climbClass,
		Escalate: func(context.Context, *Result) (bool, string) { return true, "rejected" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Attempts) != 1 || res.Attempts[0].Reason != "escalated: rejected" {
		t.Fatalf("attempts %+v; want h1 recorded as escalated", res.Attempts)
	}
	rows := 0
	for _, r := range costRows(t) {
		if r["head"] == "h1" {
			rows++
		}
	}
	again, err := d.Dispatch(context.Background(), prompt, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if cached := again.Cache != nil; rows != 1 || cached {
		t.Errorf("escalated answer from %s: cost rows=%d (want 1), cached=%v (want false)", "h1", rows, cached)
	}
	// The unjudged dispatch above did fill the cache, so the store was live throughout.
	if third, err := d.Dispatch(context.Background(), prompt, Options{}); err != nil || third.Cache == nil {
		t.Fatalf("an accepted answer was not cached either (%v), so this proves nothing", err)
	}
}

// G16. Every head answered and every answer was escalated past: that is a set of
// answers for the hook's owner to choose from, not a failure.
func TestEscalate_ExhaustedReturnsLastAnswerNotError(t *testing.T) {
	testutil.NewSandbox(t)
	d := climbingDispatcher(t,
		climbing{answeringHead(t, "h1", "first", 90), 92, 0.01}, climbing{answeringHead(t, "h2", "second", 90), 98, 0.02})
	judged := 0
	res, err := d.Dispatch(context.Background(), "hi", Options{
		RunID: "run-exhausted", TaskID: "task-exhausted", Domain: climbClass,
		Escalate: func(context.Context, *Result) (bool, string) { judged++; return true, "rejected" },
	})
	if err != nil || res == nil || res.Output != "second" {
		t.Fatalf("every candidate answered and was escalated; Dispatch returned err=%v, want the last answer", err)
	}
	if judged != 2 {
		t.Errorf("the hook judged %d answers, want both", judged)
	}
	events, err := runlog.Load("run-exhausted")
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != runlog.KindTaskFinished || last.Status != "ok" {
		t.Errorf("the task closed as %s/%s, want task_finished ok: it was answered", last.Kind, last.Status)
	}
}

// An escalation spends from what the answers it passed over left of the ceiling,
// so a head that fits the whole ceiling but not the remainder is never asked.
func TestEscalate_FurtherHeadsFitTheRemainingCeiling(t *testing.T) {
	testutil.NewSandbox(t)
	h1, h2 := answeringHead(t, "h1", "first", 90), answeringHead(t, "h2", "second", 90)
	h1.Meta = map[string]string{"model": "test/priced"}
	h2.Meta = h1.Meta
	d := climbingDispatcher(t, climbing{h1, 92, 0.01}, climbing{h2, 98, 0.02})
	d.pricing = pricedModel(t, "test/priced", 2, 4)
	// 4000 chars estimate 1000 in and 500 out, $0.004 a call, which the stub also bills.
	res, err := d.Dispatch(context.Background(), strings.Repeat("x", 4000), Options{
		MaxCostUSD: 0.006, Domain: climbClass,
		Escalate: func(context.Context, *Result) (bool, string) { return true, "rejected" },
	})
	if err != nil || res.Output != "first" {
		t.Fatalf("Dispatch = %v, %v; want the first answer standing", res, err)
	}
	for _, r := range costRows(t) {
		if r["head"] == "h2" {
			t.Fatal("h2 was paid $0.004 with $0.002 of the $0.006 ceiling left")
		}
	}
}

// Escalation climbs by in-class measurement, not by the price order the first
// head was chosen on: h3 is cheaper than h2 but measured stronger, so it comes last.
func TestEscalate_ClimbsByInClassMeasurement(t *testing.T) {
	testutil.NewSandbox(t)
	d := climbingDispatcher(t,
		climbing{answeringHead(t, "h1", "a", 90), 92, 0.01},
		climbing{answeringHead(t, "h2", "b", 90), 96, 0.03},
		climbing{answeringHead(t, "h3", "c", 90), 100, 0.02})
	if got := judgedHeads(t, d); strings.Join(got, ",") != "h1,h2,h3" {
		t.Errorf("escalation asked %v, want h1,h2,h3: each step the weakest head measured to beat the last", got)
	}
}

// A head measured weaker, or with no in-class record however high its declared
// score, is never escalated to: the rejected answer stands.
func TestEscalate_NeverToAWeakerOrUnmeasuredHead(t *testing.T) {
	testutil.NewSandbox(t)
	d := climbingDispatcher(t,
		climbing{answeringHead(t, "h1", "a", 90), 96, 0.01},
		climbing{answeringHead(t, "weaker", "b", 90), 92, 0.02},
		climbing{answeringHead(t, "unmeasured", "c", 99), -1, 0.03})
	if got := judgedHeads(t, d); strings.Join(got, ",") != "h1" {
		t.Errorf("escalation asked %v, want h1 alone", got)
	}
}
