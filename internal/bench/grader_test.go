// SPDX-License-Identifier: MIT

package bench

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/executor"
)

const graderHead = "bench/openrouter/m-mid"

// secondSuite is fieldSuite with a second model: it asks "check:<text>", and a reply of
// "differs" flags the field on top of what the suite's own check flagged.
type secondSuite struct{ fieldSuite }

func (s secondSuite) GradeWithSecond(ctx context.Context, it Item, answer string, ask Ask) (Grade, error) {
	g, err := s.Grade(ctx, it, answer)
	text, _ := it.Text()
	zero := 0.0
	reply, aerr := ask(ctx, executor.Request{Prompt: "check:" + text, MaxTokens: 8, Temperature: &zero})
	if aerr != nil {
		return Grade{}, aerr
	}
	for kind, f := range g.Fields {
		f.Second, f.RightFlaggedSecond, f.WrongFlaggedSecond = true, f.RightFlagged, f.WrongFlagged
		switch {
		case reply != "differs" || f.RightFlagged+f.WrongFlagged > 0:
		case g.Passed:
			f.RightFlaggedSecond++
		default:
			f.WrongFlaggedSecond++
		}
		g.Fields[kind] = f
	}
	return g, err
}

// graderRunner is testRunner with the grader's second model on m-mid, which no arm uses.
func graderRunner(t *testing.T, n int, exec *fakeExec, arms ...string) *Runner {
	t.Helper()
	r := testRunner(t, n, exec, arms...)
	r.Suite = secondSuite{}
	r.Run.Arms[GraderArm] = ArmConfig{Kind: KindDirect, Models: testArmSpecs["mid"]}
	exec.set(graderHead, "*", reply{answer: "agrees"})
	return r
}

// A fixture run with four wrong fields, one the suite's check misses: the second model
// flags it, and the report shows the catch rate and the cost per verified field both ways.
func TestExecute_GraderSecondModelAddsToTheCatchRate(t *testing.T) {
	exec := newFakeExec()
	for _, q := range []string{"q000", "q002", "q004"} {
		exec.set("bench/openrouter/m-cheap", q, reply{answer: "no"}) // wrong, flagged by the check
	}
	exec.set("bench/openrouter/m-cheap", "q001", reply{answer: "yes"}) // wrong, missed by the check
	exec.set(graderHead, "check:q001", reply{answer: "differs"})
	r := graderRunner(t, 10, exec, "frontier", "cheapest")

	o, err := r.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := exec.count(graderHead); got != 20 {
		t.Errorf("the second model was called %d times, want once per answer (20)", got)
	}
	// Its spend is on the pair it graded, marked, and not a row of its own.
	sum, n := spentOn(t, o, "test/001", "cheapest")
	if n != 2 || math.Abs(sum-(0.002+0.004)) > 1e-9 {
		t.Errorf("test/001 cheapest: $%v in %d spend rows, want the arm's call and the grader's", sum, n)
	}
	var marked int
	for _, row := range allRowsOf(t, o) {
		if row.Grader {
			marked++
		}
	}
	if marked != 20 {
		t.Errorf("%d spend rows marked as the grader's, want 20", marked)
	}

	s, err := Report(o.Dir, r.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	a := s.Arms["cheapest"]
	c := a.Fields
	if math.Abs(a.GraderCostUSD-0.04) > 1e-9 {
		t.Errorf("grader cost %v, want 10 calls of $0.004", a.GraderCostUSD)
	}
	counts := func(d DetectionStats) [4]int { return [4]int{d.Positives, d.Caught, d.Negatives, d.FalseFlags} }
	if c == nil || c.WithSecond == nil || counts(c.DetectionStats) != [4]int{4, 3, 6, 4} || counts(c.WithSecond.DetectionStats) != [4]int{4, 4, 6, 4} {
		t.Fatalf("fields %+v, want 3 of 4 caught without the second model and 4 of 4 with it", c)
	}
	// Without it, the arm's own spend over 3 verified fields; with it, all the spend over 2.
	if got, want := *c.CostPerVerified, (a.CostUSD-a.GraderCostUSD)/3; math.Abs(got-want) > 1e-9 {
		t.Errorf("cost per verified without the second model %v, want %v", got, want)
	}
	if got, want := *c.WithSecond.CostPerVerified, a.CostUSD/2; math.Abs(got-want) > 1e-9 {
		t.Errorf("cost per verified with the second model %v, want %v", got, want)
	}
	if f := s.Arms["frontier"].Fields; f == nil || f.WithSecond == nil || f.WithSecond.Caught != 0 {
		t.Errorf("frontier fields %+v", f)
	}
	table := Table([]Summary{s})
	for _, want := range []string{"| cheapest | all | 10 | 60.0 | 75.0 (", "| cheapest + second model | all | 10 | 60.0 | 100.0 ("} {
		if !strings.Contains(table, want) {
			t.Errorf("table has no %q:\n%s", want, table)
		}
	}
}

// No grader_model, no second call: a suite that could use one is graded as before.
func TestExecute_NoGraderMeansNoSecondCall(t *testing.T) {
	exec := newFakeExec()
	r := testRunner(t, 6, exec, "frontier")
	r.Suite = secondSuite{}
	o, err := r.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s, err := Report(o.Dir, r.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if f := s.Arms["frontier"].Fields; f == nil || f.WithSecond != nil || s.Arms["frontier"].GraderCostUSD != 0 || exec.count(graderHead) != 0 {
		t.Errorf("a run with no grader reports %+v", f)
	}
}

// A second model that is down says nothing about the item: the row is unscored and asked again.
func TestExecute_GraderTransientFailureIsUnscoredAndAskedAgain(t *testing.T) {
	exec := newFakeExec()
	r := graderRunner(t, 6, exec, "frontier")
	exec.set(graderHead, "check:q003", reply{err: &executor.StatusError{Code: 503}})
	o, err := r.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var row Result
	for _, x := range rowsOf(t, r, o) {
		if x.ItemID == "test/003" {
			row = x
		}
	}
	if !row.Retryable || row.ErrKind != "status:503" || !strings.Contains(row.Detail, "grader") {
		t.Fatalf("row %+v, want an unscored row that names the grader", row)
	}
	exec.set(graderHead, "check:q003", reply{answer: "agrees"})
	if o2, err := r.Execute(context.Background()); err != nil || o2.Written != 1 {
		t.Fatalf("resume wrote %d rows (%v), want the one unscored", o2.Written, err)
	}
}

// A paid second model is held to the cap like an arm: the run stops before the call.
func TestExecute_GraderSpendCountsAgainstTheCap(t *testing.T) {
	exec := newFakeExec()
	r := graderRunner(t, 4, exec, "frontier")
	r.MaxUSD = 0.007 // one arm call ($0.002) and one grader call ($0.004) fit; the next pair does not
	o, err := r.Execute(context.Background())
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v, want the cap to stop the run", err)
	}
	if o.Spent > 0.007+1e-9 {
		t.Errorf("spent $%v, over the $0.007 cap", o.Spent)
	}
}

func TestParseGrader_TakesOllamaAndTheHostedTwo(t *testing.T) {
	for spec, want := range map[string]string{"ollama:qwen2.5:7b": "ollama", "openrouter:m-mid": "openrouter", "bedrock:m": "bedrock"} {
		if prov, _, err := ParseGrader(spec); err != nil || prov != want {
			t.Errorf("ParseGrader(%q) = %q, %v", spec, prov, err)
		}
	}
	for _, spec := range []string{"", "ollama:", "openai:gpt", "ollama:a b", "qwen"} {
		if _, _, err := ParseGrader(spec); err == nil {
			t.Errorf("ParseGrader(%q) was accepted", spec)
		}
	}
	// Arms stay hosted-only: the cheapest and the frontier are priced calls.
	if _, _, err := ParseModel("ollama:qwen2.5:7b"); err == nil {
		t.Error("an arm may not be a local model")
	}
	h, model, err := headFor("ollama:qwen2.5:7b")
	if err != nil || model != "qwen2.5:7b" || h.Source != "port" || !h.LocalOnly || h.Endpoint == "" || h.Meta["model"] != "qwen2.5:7b" {
		t.Errorf("headFor(ollama) = %+v, %q, %v", h, model, err)
	}
}

const graderConfig = `tier = "fixture"
track = "core"
suites = ["snips"]
n_test = 5
arms = ["frontier"]
%s
[models]
frontier = "openrouter:m-front"
[cutoffs]
m-front = "2025-01"
m-mid = "2025-01"
`

func writeGraderConfig(t *testing.T, extra string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(p, []byte(strings.Replace(graderConfig, "%s", extra, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadConfig_GraderModelIsOptionalAndChecked(t *testing.T) {
	c, err := ReadConfig(writeGraderConfig(t, ""))
	if err != nil || c.GraderModel != "" {
		t.Fatalf("no grader_model: %+v, %v", c, err)
	}
	c, err = ReadConfig(writeGraderConfig(t, `grader_model = "ollama:qwen2.5:7b"`))
	if err != nil || strings.Join(c.ModelIDs(), ",") != "m-front" {
		t.Fatalf("a local grader: %v, ids %v; want no price to look up", err, c)
	}
	c, err = ReadConfig(writeGraderConfig(t, `grader_model = "openrouter:m-mid"`))
	if err != nil || strings.Join(c.ModelIDs(), ",") != "m-front,m-mid" {
		t.Fatalf("a hosted grader: %v, ids %v; want it priced like an arm", err, c)
	}
	for _, bad := range []string{`grader_model = "gpt-5"`, `grader_model = "openai:gpt-5"`} {
		if _, err := ReadConfig(writeGraderConfig(t, bad)); err == nil || !strings.Contains(err.Error(), "grader_model") {
			t.Errorf("%s: err = %v", bad, err)
		}
	}
}

// Naming a grader changes what a run is, so its run id differs; with none, ids are as before.
func TestPlan_GraderModelIsFrozenIntoTheRun(t *testing.T) {
	c, err := ReadConfig(writeGraderConfig(t, `grader_model = "ollama:qwen2.5:7b"`))
	if err != nil {
		t.Fatal(err)
	}
	a, b := Run{Arms: map[string]ArmConfig{"frontier": {Kind: KindDirect}}}, Run{Arms: map[string]ArmConfig{
		"frontier": {Kind: KindDirect}, GraderArm: {Kind: KindDirect, Models: []string{c.GraderModel}}}}
	if a.cfg8() == b.cfg8() {
		t.Error("a run with a grader hashes like one without")
	}
	if _, err := (&Config{Arms: []string{GraderArm}, Models: map[string]string{GraderArm: "openrouter:m"}}).ArmConfigs(); err == nil {
		t.Error("an arm may not take the grader's name")
	}
}

// The oracle arm keeps each item's best row; its cost without the second model leaves
// out the grader calls those rows carried, as every other arm's does.
func TestSummarize_OracleSeparatesTheGradersSpend(t *testing.T) {
	exec := newFakeExec()
	r := graderRunner(t, 6, exec, "frontier", "cheapest")
	o, err := r.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s, err := Report(o.Dir, r.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	oracle := s.Arms[OracleArm]
	if math.Abs(oracle.GraderCostUSD-6*0.004) > 1e-9 {
		t.Errorf("oracle grader cost %v, want one $0.004 grader call per item", oracle.GraderCostUSD)
	}
}

// A second model that breaks the contract (estimated tokens) aborts the run like an arm would.
func TestExecute_GraderContractViolationAborts(t *testing.T) {
	exec := newFakeExec()
	r := graderRunner(t, 4, exec, "frontier")
	exec.set(graderHead, "*", reply{answer: "agrees", estimated: true})
	_, err := r.Execute(context.Background())
	var ce *ContractError
	if !errors.As(err, &ce) || ce.Arm != GraderArm {
		t.Fatalf("err = %v, want a contract error on the grader", err)
	}
}
