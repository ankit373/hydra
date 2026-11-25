// SPDX-License-Identifier: MIT

package replay

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func row(task, head string, cost float64, correct, pass bool) Row {
	return Row{Task: task, Kind: "math", Head: head, CostUSD: cost, Correct: correct,
		Verdicts: map[string]bool{"format": pass}, CheckCostUSD: map[string]float64{"format": 0.5}}
}

func TestReplay_FalseAcceptCountsWrongAcceptedOverAllTasks(t *testing.T) {
	wrong, right := row("t", "a", 1, false, true), row("t", "a", 1, true, true)
	out := make([]Outcome, 10) // 6 refused
	out[0], out[1], out[2], out[3] = Outcome{Accepted: &wrong}, Outcome{Accepted: &right},
		Outcome{Accepted: &right}, Outcome{Accepted: &right}
	r, err := Score(out)
	if err != nil {
		t.Fatal(err)
	}
	// 1 wrong of 10 tasks, not 1 of 4 accepted.
	if r.FalseAccept != 0.1 || r.WrongAccepted != 1 || r.Accepted != 4 || r.Refusal != 0.6 {
		t.Fatalf("got %+v", r)
	}
	if !(r.FalseAcceptLo > 0 && r.FalseAcceptLo < 0.1 && r.FalseAcceptHi > 0.1 && r.FalseAcceptHi < 1) {
		t.Fatalf("interval [%v, %v] does not straddle 0.1", r.FalseAcceptLo, r.FalseAcceptHi)
	}
}

func TestReplay_RefusedTaskCountsCostButNoAccept(t *testing.T) {
	p, err := New("first_check_pass", Options{Check: "format"})
	if err != nil {
		t.Fatal(err)
	}
	task := []Row{row("t", "cheap", 1, false, false), row("t", "dear", 10, true, false)}
	if err := p.Calibrate(task); err != nil {
		t.Fatal(err)
	}
	o := p.Decide(task, []string{"cheap", "dear"})
	if o.Accepted != nil || o.Spent != 12 || !slices.Equal(o.Bought, []string{"cheap", "dear"}) {
		t.Fatalf("got %+v; want a refusal that paid 1+10 and two 0.5 checks", o)
	}
	r, err := Score([]Outcome{o})
	if err != nil {
		t.Fatal(err)
	}
	if r.Refusal != 1 || r.CostPerTask != 12 || r.FalseAccept != 0 {
		t.Fatalf("got %+v", r)
	}
}

func TestReplay_SplitsAreSeededAndByTask(t *testing.T) {
	var rows []Row
	for i := 0; i < 9; i++ {
		for _, h := range []string{"a", "b"} {
			rows = append(rows, row(fmt.Sprintf("t%d", i), h, 1, true, true))
		}
	}
	a := SplitTasks(rows, 7)
	if len(a.Calibration) != 4 || len(a.Test) != 5 {
		t.Fatalf("9 tasks split %d/%d; want 4/5", len(a.Calibration), len(a.Test))
	}
	if slices.ContainsFunc(a.Calibration, func(s string) bool { return slices.Contains(a.Test, s) }) {
		t.Fatal("a task is on both sides")
	}
	reversed := slices.Clone(rows)
	slices.Reverse(reversed)
	if b := SplitTasks(reversed, 7); !slices.Equal(a.Test, b.Test) {
		t.Fatalf("file order changed the split: %v vs %v", a.Test, b.Test)
	}
	differs := false
	for seed := uint64(0); seed < 10; seed++ {
		differs = differs || !slices.Equal(SplitTasks(rows, seed).Test, a.Test)
	}
	if !differs {
		t.Fatal("ten seeds gave one split")
	}
}

func TestReplay_CostPerCorrectIsNaNWhenNothingCorrect(t *testing.T) {
	wrong := row("t", "a", 2, false, true)
	r, err := Score([]Outcome{{Accepted: &wrong, Spent: 2}, {Spent: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if !math.IsNaN(float64(r.CostPerCorrect)) || r.CostPerTask != 2.5 {
		t.Fatalf("got %+v", r)
	}
	b, err := json.Marshal(r)
	if err != nil || !strings.Contains(string(b), `"cost_per_correct_usd":null`) {
		t.Fatalf("NaN must marshal as null: %s, %v", b, err)
	}
}

func TestRead_RefusesWhatWouldMiscount(t *testing.T) {
	ok := `{"task":"t","kind":"code","head":"a","cost_usd":1,"correct":true,"answer_key":"x","verdicts":{"tests":true},"check_cost_usd":{"tests":0}}`
	if rows, err := Read(strings.NewReader(ok + "\n\n")); err != nil || len(rows) != 1 || !rows[0].Verdicts["tests"] {
		t.Fatalf("a good row: %v, %+v", err, rows)
	}
	for name, in := range map[string]string{
		"second answer":  ok + "\n" + ok,
		"two kinds":      ok + "\n" + strings.Replace(strings.Replace(ok, `"code"`, `"math"`, 1), `"a"`, `"b"`, 1),
		"negative cost":  strings.Replace(ok, `"cost_usd":1`, `"cost_usd":-1`, 1),
		"unknown field":  strings.Replace(ok, `"correct"`, `"corect"`, 1),
		"missing head":   strings.Replace(ok, `"head":"a",`, ``, 1),
		"no candidates":  "\n",
		"negative check": strings.Replace(ok, `"tests":0`, `"tests":-1`, 1),
	} {
		if _, err := Read(strings.NewReader(in)); err == nil {
			t.Errorf("%s: read without error", name)
		}
	}
}

func TestPolicies_Baselines(t *testing.T) {
	cal := []Row{row("c1", "weak", 1, false, true), row("c1", "strong", 5, true, true),
		row("c2", "weak", 1, true, true), row("c2", "strong", 5, true, true)}
	order := CheapestFirst(cal)
	if !slices.Equal(order, []string{"weak", "strong"}) {
		t.Fatalf("order %v", order)
	}
	s, _ := New("always_strongest", Options{})
	if err := s.Calibrate(cal); err != nil {
		t.Fatal(err)
	}
	task := []Row{row("t", "weak", 1, false, true), row("t", "strong", 5, true, false)}
	if o := s.Decide(task, order); o.Accepted == nil || o.Accepted.Head != "strong" || o.Spent != 5 {
		t.Fatalf("always_strongest: %+v", o)
	}
	if o := s.Decide(task[:1], order); o.Accepted != nil || o.Spent != 0 {
		t.Fatalf("no strong answer must refuse: %+v", o)
	}
	f, _ := New("first_check_pass", Options{Check: "format"})
	if err := f.Calibrate(cal); err != nil {
		t.Fatal(err)
	}
	if o := f.Decide(task, order); o.Accepted == nil || o.Accepted.Head != "weak" || o.Spent != 1.5 {
		t.Fatalf("first_check_pass: %+v", o)
	}
	if _, err := New("first_check_pass", Options{}); err == nil {
		t.Fatal("first_check_pass made with no check")
	}
	g, _ := New("first_check_pass", Options{Check: "tests"})
	if err := g.Calibrate(cal); err == nil {
		t.Fatal("calibrated on a check no row has")
	}
	if _, err := New("oracle", Options{}); err == nil {
		t.Fatal("made an unknown policy")
	}
}

func TestRun_EachKindOnItsOwnAndRepeatable(t *testing.T) {
	var rows []Row
	for i := 0; i < 6; i++ {
		for _, kind := range []string{"code", "math"} {
			task := fmt.Sprintf("%s%d", kind, i)
			cheap, dear := row(task, "cheap", 1, kind == "math", true), row(task, "dear", 3, true, true)
			cheap.Kind, dear.Kind = kind, kind
			rows = append(rows, cheap, dear)
		}
	}
	cfg := Config{Policies: []string{"always_strongest", "first_check_pass"}, Alphas: []float64{0.05, 0.1},
		Seeds: []uint64{0, 1, 2}, Check: "format"}
	rep, err := Run(rows, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 2*2*2*3 || len(rep.Summaries) != 2*2*2 {
		t.Fatalf("%d results, %d summaries", len(rep.Results), len(rep.Summaries))
	}
	// On code the cheap head is always wrong yet passes, so first_check_pass is wrong on every task.
	code := rep.Summaries[2]
	if code.Kind != "code" || code.Policy != "first_check_pass" || code.MeanFalseAccept != 1 ||
		code.OverAlpha != 1 || !math.IsNaN(float64(code.CostPerCorrect)) || code.CostPerTask != 1.5 {
		t.Fatalf("code first_check_pass: %+v", code)
	}
	// On math the cheap head is right, so it is both the strongest (tie, cheaper) and the first pass.
	for _, s := range rep.Summaries[4:] {
		if s.Kind != "math" || s.MeanFalseAccept != 0 || s.OverAlpha != 0 || float64(s.CostPerCorrect) > 1.5 {
			t.Fatalf("math: %+v", s)
		}
	}
	again, err := Run(rows, cfg)
	if err != nil || Table(again) != Table(rep) {
		t.Fatalf("same seeds, different table: %v", err)
	}
	if tab := Table(rep); !strings.Contains(tab, "seeds: 0 1 2") || !strings.Contains(tab, "n/a (none correct)") {
		t.Fatalf("table:\n%s", tab)
	}
}

func TestRun_RefusesWhatItCannotReplay(t *testing.T) {
	two := []Row{row("a", "h", 1, true, true), row("b", "h", 1, true, true)}
	for name, c := range map[string]struct {
		rows []Row
		cfg  Config
	}{
		"no policy":     {two, Config{Alphas: []float64{0.05}, Seeds: []uint64{0}}},
		"alpha of 1":    {two, Config{Policies: []string{"always_strongest"}, Alphas: []float64{1}, Seeds: []uint64{0}}},
		"one task":      {two[:1], Config{Policies: []string{"always_strongest"}, Alphas: []float64{0.05}, Seeds: []uint64{0}}},
		"unknown":       {two, Config{Policies: []string{"oracle"}, Alphas: []float64{0.05}, Seeds: []uint64{0}}},
		"unknown check": {two, Config{Policies: []string{"first_check_pass"}, Check: "tests", Alphas: []float64{0.05}, Seeds: []uint64{0}}},
	} {
		if _, err := Run(c.rows, c.cfg); err == nil {
			t.Errorf("%s: ran", name)
		}
	}
	if _, err := Score(nil); err == nil {
		t.Error("scored no tasks")
	}
}

func TestReplay_ReadsConverterFixture(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "..", "bench", "aver", "testdata", "replay.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := Read(f)
	if err != nil {
		t.Fatalf("Read refused the converter's fixture: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("got %d rows, want 5", len(rows))
	}
	kinds := map[string]int{}
	for _, r := range rows {
		kinds[r.Kind]++
	}
	if kinds["code"] != 2 || kinds["math"] != 2 || kinds["grounded"] != 1 {
		t.Fatalf("kinds %v, want 2 code, 2 math, 1 grounded", kinds)
	}
	for _, r := range rows {
		if r.Task == "humaneval/12" && r.Head == "code-llama-7b" {
			if r.Correct || !r.Verdicts["base_tests"] || r.CostUSD != 6.4e-05 {
				t.Fatalf("code-llama-7b row read as %+v", r)
			}
			return
		}
	}
	t.Fatal("the code-llama-7b row is missing")
}
