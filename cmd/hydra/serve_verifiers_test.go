// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/decide"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/serve"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

const cascadeTestClass = "classification.test"

func labelsFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.labels")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestServeVerifiers_Validate(t *testing.T) {
	good := labelsFile(t, "# intents\ncard_arrival\n\nlost_card\n")
	ok := config.ServeVerifier{Class: cascadeTestClass, Kind: "match", ID: "t", Labels: good}
	for _, tc := range []struct {
		name    string
		entries []config.ServeVerifier
		want    string // in the error; empty means accepted
	}{
		{"a label file", []config.ServeVerifier{ok}, ""},
		{"a pattern", []config.ServeVerifier{{Class: cascadeTestClass, Kind: "match", ID: "n", Pattern: `\d+`}}, ""},
		{"unknown kind", []config.ServeVerifier{{Class: cascadeTestClass, Kind: "jsonschema", ID: "t", Labels: good}}, `entry 1: unknown kind "jsonschema"`},
		{"bad grammar", []config.ServeVerifier{{Class: "classify/banking77", Kind: "match", ID: "t", Labels: good}}, "entry 1: "},
		{"no id", []config.ServeVerifier{{Class: cascadeTestClass, Kind: "match", Labels: good}}, "entry 1: no id"},
		{"unreadable file", []config.ServeVerifier{{Class: cascadeTestClass, Kind: "match", ID: "t", Labels: "/nonexistent/t.labels"}}, "entry 1: labels:"},
		{"empty file", []config.ServeVerifier{{Class: cascadeTestClass, Kind: "match", ID: "t", Labels: labelsFile(t, "# none\n")}}, "holds no label"},
		{"colliding labels", []config.ServeVerifier{{Class: cascadeTestClass, Kind: "match", ID: "t", Labels: labelsFile(t, "Card Arrival\ncard_arrival\n")}}, "both normalise"},
		{"both labels and a pattern", []config.ServeVerifier{{Class: cascadeTestClass, Kind: "match", ID: "t", Labels: good, Pattern: "x"}}, "both labels and a pattern"},
		{"neither", []config.ServeVerifier{{Class: cascadeTestClass, Kind: "match", ID: "t"}}, "no labels and no pattern"},
		{"a regex that escapes its anchors", []config.ServeVerifier{{Class: cascadeTestClass, Kind: "match", ID: "t", Pattern: "a)|(.*"}}, "entry 1: "},
		{"duplicate", []config.ServeVerifier{ok, ok}, "entry 2: class classification.test already has verifier:match:t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := serveVerifiers(tc.entries)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("refused a good entry: %v", err)
			case tc.want == "" && len(got[cascadeTestClass]) != 1:
				t.Errorf("built %+v, want one verifier for %s", got, cascadeTestClass)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("err = %v, want it to name %q", err, tc.want)
			}
		})
	}
}

// Escalation is opt-in twice over: a class with no verifier, a stream and a
// pinned route all dispatch with exactly the options they had before #1209.
func TestServeRouter_OnlyAVerifiedClassEscalates(t *testing.T) {
	verifiers, err := serveVerifiers([]config.ServeVerifier{{Class: cascadeTestClass, Kind: "match", ID: "t", Pattern: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	r := serveRouter{objective: "quality", verifiers: verifiers, confidence: 0.95, maxEscalations: 2}
	for _, tc := range []struct {
		name string
		req  serve.Request
		want bool
	}{
		{"no class", serve.Request{}, false},
		{"a class with no verifier", serve.Request{TaskClass: "classification.other"}, false},
		{"a stream", serve.Request{TaskClass: cascadeTestClass, OnEvent: func(serve.Event) {}}, false},
		{"a pinned tier", serve.Request{TaskClass: cascadeTestClass, Route: serve.Route{Enum: "standard"}}, false},
		{"tool calls", serve.Request{TaskClass: cascadeTestClass, Tools: []executor.ToolDef{{Type: "function"}}}, false},
		{"a verified class", serve.Request{TaskClass: cascadeTestClass}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, ev, err := r.options(tc.req)
			if err != nil {
				t.Fatal(err)
			}
			after := before
			esc := r.escalationFor(ev, tc.req, &after)
			if got := esc != nil; got != tc.want || (after.Escalate != nil) != tc.want {
				t.Fatalf("escalation=%v Escalate set=%v, want %v", got, after.Escalate != nil, tc.want)
			}
			if !tc.want {
				return
			}
			if !after.NoCache {
				t.Error("an escalating request may cache an answer it escalated past")
			}
		})
	}
}

// cascadeFixture is a sandboxed server whose LiteLLM stub answers each model
// from answers, with calls counted per model, every head measured in class.
type cascadeFixture struct {
	mu    sync.Mutex
	calls map[string]int
}

func (f *cascadeFixture) called(model string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[model]
}

func newCascadeFixture(t *testing.T, answers map[string]string, verifierMeasured bool) (*cascadeFixture, serveRouter) {
	t.Helper()
	testutil.NewSandbox(t)
	f := &cascadeFixture{calls: map[string]int{}}
	// Sorted, so each next model is both dearer and measured stronger: the cost
	// objective runs the first, and escalation climbs to the rest in order.
	names := slices.Sorted(maps.Keys(answers))
	var models []map[string]any
	for _, m := range names {
		models = append(models, map[string]any{"model_name": m, "model_info": map[string]string{"litellm_provider": "openai"}})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": models})
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.calls[body.Model]++
		f.mu.Unlock()
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
			body.Model, answers[body.Model])
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LITELLM_PROXY_URL", srv.URL)
	if err := config.Save(&config.Config{Cortex: "cody"}); err != nil {
		t.Fatal(err)
	}
	prices := map[string]any{}
	for i, m := range names {
		prices[m] = map[string]float64{"input_per_mtok": float64(i + 1), "output_per_mtok": float64(i + 1)}
	}
	raw, err := json.Marshal(map[string]any{"fetched_at": time.Now().UTC(), "source": "test", "models": prices})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.Dir(), "pricing_cache.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// Measured before the server starts, which is when serve reads calibration.
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	// Out of 400, so every head clears the cost constraint's bar whatever it declares.
	for i, m := range names {
		recordN(t, cal, "litellm/"+m, []int{380, 390, 395, 400}[i], 400)
	}
	if verifierMeasured {
		for i := range 20 {
			mustUpdate(t, cal, "verifier:match:t", true, i < 19)
			mustUpdate(t, cal, "verifier:match:t", false, i == 19)
		}
	}

	d, err := dispatch.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	verifiers, err := serveVerifiers([]config.ServeVerifier{{
		Class: cascadeTestClass, Kind: "match", ID: "t",
		Labels: labelsFile(t, "card_arrival\nlost_card\nother\n"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := trust.New(trust.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	return f, serveRouter{d: d, objective: rank.Cost, verifiers: verifiers, confidence: 0.95, maxEscalations: 2, cal: shared}
}

func recordN(t *testing.T, cal *trust.Calibrator, source string, correct, total int) {
	t.Helper()
	for i := range total {
		mustUpdate(t, cal, source, true, i < correct)
	}
}

func mustUpdate(t *testing.T, cal *trust.Calibrator, source string, saidCorrect, correct bool) {
	t.Helper()
	outcome := trust.OutcomeIncorrect
	if correct {
		outcome = trust.OutcomeCorrect
	}
	if err := cal.Update(source, cascadeTestClass, saidCorrect, outcome); err != nil {
		t.Fatal(err)
	}
}

func chatClass(t *testing.T, r serveRouter, id string) serve.Answer {
	t.Helper()
	ans, err := r.Chat(context.Background(), serve.Request{
		RequestID: id, TaskClass: cascadeTestClass,
		Messages: []executor.Message{{Role: "user", Content: "My card has not arrived yet."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return ans
}

func costRowsByHead(t *testing.T) map[string]int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(config.Dir(), "logs", "cost.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var row struct {
			Head string `json:"head"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		out[row.Head]++
	}
	return out
}

// The serve path end to end with no network: the cheapest competent head answers
// off the label set, the verifier fails it, and the stronger head's label is served.
func TestServe_ClassificationCascadeEndToEnd(t *testing.T) {
	f, r := newCascadeFixture(t, map[string]string{"first": "not a label", "second": "Card Arrival"}, true)
	ans := chatClass(t, r, "run-e2e")
	if ans.Output != "Card Arrival" || ans.Head != "litellm/second" {
		t.Errorf("served %q from %s, want the second head's label", ans.Output, ans.Head)
	}
	if f.called("first") != 1 || f.called("second") != 1 {
		t.Errorf("calls first=%d second=%d, want one each", f.called("first"), f.called("second"))
	}
	if ans.InputTokens != 20 || ans.OutputTokens != 4 {
		t.Errorf("usage %d/%d, want both calls summed (20/4)", ans.InputTokens, ans.OutputTokens)
	}
	if rows := costRowsByHead(t); rows["litellm/first"] != 1 || rows["litellm/second"] != 1 {
		t.Errorf("cost rows %v, want one per paid call", rows)
	}
	if entries, _ := os.ReadDir(filepath.Join(config.Dir(), "cache")); len(entries) != 0 {
		t.Errorf("an escalating request wrote the answer cache: %v", entries)
	}
}

// An uncalibrated verifier is decide's no-evidence rule: one head, its answer served.
func TestServe_AnUncalibratedVerifierAsksOneHead(t *testing.T) {
	f, r := newCascadeFixture(t, map[string]string{"first": "not a label", "second": "Card Arrival"}, false)
	if ans := chatClass(t, r, "run-uncal"); ans.Head != "litellm/first" || f.called("second") != 0 {
		t.Errorf("served %s and asked second %d times; want first alone", ans.Head, f.called("second"))
	}
}

// R1: match evidence is weak at A, so every answer can fail it. --max-escalations
// is then the bound on spend, and it must hold however many heads remain.
func TestServe_MaxEscalationsCapsCost(t *testing.T) {
	answers := map[string]string{"a": "nope", "b": "nope either", "c": "still no", "d": "no"}
	for _, maxEsc := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(maxEsc), func(t *testing.T) {
			_, r := newCascadeFixture(t, answers, true)
			r.maxEscalations = maxEsc
			ans := chatClass(t, r, fmt.Sprintf("run-cap-%d", maxEsc))
			paid := 0
			for _, n := range costRowsByHead(t) {
				paid += n
			}
			if paid != maxEsc+1 {
				t.Errorf("--max-escalations %d paid for %d calls, want %d", maxEsc, paid, maxEsc+1)
			}
			// Priors climb with the measurement, so the strongest asked is the most confident.
			if want := []string{"litellm/a", "litellm/b", "litellm/c"}[maxEsc]; ans.Head != want {
				t.Errorf("served %s; with every answer failing, the most confident asked, %s, stands", ans.Head, want)
			}
		})
	}
}

// Each request owns its cascade, and every request shares one Dispatcher and Calibrator.
func TestCascade_ConcurrentRequestsShareNothing(t *testing.T) {
	_, r := newCascadeFixture(t, map[string]string{"first": "not a label", "second": "Card Arrival"}, true)
	var wg sync.WaitGroup
	got := make([]serve.Answer, 16)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ans, err := r.Chat(context.Background(), serve.Request{
				RequestID: fmt.Sprintf("run-par-%d", i), TaskClass: cascadeTestClass,
				Messages: []executor.Message{{Role: "user", Content: "My card has not arrived yet."}},
			})
			if err != nil {
				t.Error(err)
			}
			got[i] = ans
		}()
	}
	wg.Wait()
	for i, ans := range got {
		if ans.Output != "Card Arrival" || ans.InputTokens != 20 {
			t.Errorf("request %d served %q with %d input tokens; another request's steps leaked in", i, ans.Output, ans.InputTokens)
		}
	}
}

// Under quota pressure the governor routes instead of the objective, and the
// orchestration protocol freezes escalations there, so a failed answer stands.
func TestEscalation_FreezesWhenTheGovernorRouted(t *testing.T) {
	cal, err := trust.New("")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		mustUpdate(t, cal, "verifier:match:t", true, i < 19)
		mustUpdate(t, cal, "verifier:match:t", false, i == 19)
	}
	verifiers, err := serveVerifiers([]config.ServeVerifier{{Class: cascadeTestClass, Kind: "match", ID: "t", Pattern: "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	r := serveRouter{objective: rank.Quality, verifiers: verifiers, confidence: 0.95, maxEscalations: 2, cal: cal}
	req := serve.Request{TaskClass: cascadeTestClass}
	for _, tc := range []struct {
		objective rank.Objective
		want      bool
	}{{rank.Quality, true}, {"", false}} {
		opts, ev, err := r.options(req)
		if err != nil {
			t.Fatal(err)
		}
		esc := r.escalationFor(ev, req, &opts)
		res := &dispatch.Result{Output: "wrong", Objective: tc.objective}
		if next, _ := opts.Escalate(context.Background(), res); next != tc.want || esc.served() != res {
			t.Errorf("objective %q: escalate=%v, want %v", tc.objective, next, tc.want)
		}
	}
}

// A relative labels path is config.toml's neighbour, wherever the server started.
func TestServeVerifiers_LabelsBesideConfig(t *testing.T) {
	testutil.NewSandbox(t)
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.Dir(), "t.labels"), []byte("card_arrival\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if _, err := serveVerifiers([]config.ServeVerifier{{Class: cascadeTestClass, Kind: "match", ID: "t", Labels: "t.labels"}}); err != nil {
		t.Fatalf("a labels file beside config.toml did not load: %v", err)
	}
}

// A catalogue prior of 96 against --confidence 0.95 is not evidence: with no
// measured verifier the answer is served unaccepted, never Accepted.
func TestEscalation_ThePriorAloneNeverAccepts(t *testing.T) {
	cal, err := trust.New("")
	if err != nil {
		t.Fatal(err)
	}
	verifiers, err := serveVerifiers([]config.ServeVerifier{{Class: cascadeTestClass, Kind: "match", ID: "t", Pattern: "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	r := serveRouter{objective: rank.Quality, verifiers: verifiers, confidence: 0.95, maxEscalations: 2, cal: cal}
	req := serve.Request{TaskClass: cascadeTestClass}
	opts, ev, err := r.options(req)
	if err != nil {
		t.Fatal(err)
	}
	esc := r.escalationFor(ev, req, &opts)
	res := &dispatch.Result{Output: "ok", Objective: rank.Quality, Head: provider.Head{ID: "h"},
		Scores: map[string]rank.Score{"h": {Declared: 96, Effective: 96}}}
	opts.Escalate(context.Background(), res)
	if d := esc.cascade.Outcome().Decision; d == decide.Accepted {
		t.Errorf("a prior of 0.96 alone was %q", d)
	}
}

// Under quality the measured strongest runs first, so no head is measured to
// beat it and a rejected answer stands rather than being escalated downhill.
func TestServe_QualityNeverEscalatesDownhill(t *testing.T) {
	f, r := newCascadeFixture(t, map[string]string{"first": "Card Arrival", "second": "not a label"}, true)
	r.objective = rank.Quality
	if ans := chatClass(t, r, "run-quality"); ans.Head != "litellm/second" || f.called("first") != 0 {
		t.Errorf("served %s and asked first %d times; want second alone", ans.Head, f.called("first"))
	}
}
