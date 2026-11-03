// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/reliability"
	"github.com/ankit373/hydra/internal/runlog"
)

// seedScoredRuns writes count confidence-routed runs stating conf, correct at
// rate actual, each with a verdict attached. Written through the real Append and
// AppendScore so the test reads exactly what production would have left behind.
func seedScoredRuns(t *testing.T, rng *rand.Rand, conf, actual float64, count int, scored bool) {
	t.Helper()
	for i := 0; i < count; i++ {
		runID := fmt.Sprintf("run-%.2f-%d", conf, i)
		spanID := runlog.SpanIDFor(runID + "/sprt")
		rl := runlog.New(runID)
		if err := rl.Append(runlog.Event{Kind: runlog.KindRunStarted, TaskID: "t"}); err != nil {
			t.Fatal(err)
		}
		if err := rl.Append(runlog.Event{
			Kind: runlog.KindTaskFinished, TaskID: "t",
			SpanID: spanID, Agent: "sprt", Confidence: conf,
		}); err != nil {
			t.Fatal(err)
		}
		if !scored {
			continue
		}
		value := 0.0
		if rng.Float64() < actual {
			value = 1
		}
		if err := runlog.AppendScore(runID, spanID, runlog.Score{
			Name: "tests", Value: value, Source: "oracle:go-test",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// An empty log must say how to fill it. Erroring here reads as a broken
// command, when the honest answer is "nothing has been judged yet".
func TestCLI_TrustReliabilityOnAnEmptyLogSaysHowToFillIt(t *testing.T) {
	cliSandbox(t)

	out, _, err := run(t, "trust", "reliability")
	if err != nil {
		t.Fatalf("reliability on an empty log errored: %v", err)
	}
	if !strings.Contains(out, "trace score") {
		t.Errorf("does not name the command that records a verdict:\n%s", out)
	}
}

// Too few observations must refuse rather than draw a diagram from noise.
func TestCLI_TrustReliabilityRefusesBelowTheObservationFloor(t *testing.T) {
	cliSandbox(t)
	seedScoredRuns(t, rand.New(rand.NewSource(1)), 0.9, 0.9, reliability.MinObservations-1, true)

	out, _, err := run(t, "trust", "reliability")
	if err != nil {
		t.Fatalf("errored instead of refusing: %v", err)
	}
	if !strings.Contains(out, "too few") {
		t.Errorf("output does not say why it will not report:\n%s", out)
	}
	if strings.Contains(out, "brier") {
		t.Errorf("reported a Brier score below the floor:\n%s", out)
	}
}

// The case the command exists for: stated confidence far above the rate the
// verdicts actually show has to be named as overconfidence, in the human
// output, not only in --json.
func TestCLI_TrustReliabilityNamesOverconfidence(t *testing.T) {
	cliSandbox(t)
	rng := rand.New(rand.NewSource(9))
	seedScoredRuns(t, rng, 0.95, 0.55, 30, true)
	seedScoredRuns(t, rng, 0.85, 0.55, 30, true)

	out, _, err := run(t, "trust", "reliability")
	if err != nil {
		t.Fatalf("trust reliability: %v", err)
	}
	for _, want := range []string{"STATED", "ACTUALLY CORRECT", "brier", "reliability", "resolution", "ECE"} {
		if !strings.Contains(out, want) {
			t.Errorf("render is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "overconfident") {
		t.Errorf("stated ~0.90 against a ~0.55 outcome rate without saying overconfident:\n%s", out)
	}
}

// A run nothing has judged is counted and reported, not silently dropped: a
// report over an unstated subset of the log is misleading.
func TestCLI_TrustReliabilityReportsWhatIsStillUnjudged(t *testing.T) {
	cliSandbox(t)
	rng := rand.New(rand.NewSource(13))
	seedScoredRuns(t, rng, 0.8, 0.8, 25, true)
	seedScoredRuns(t, rng, 0.6, 0.6, 7, false) // logged, never judged

	out, _, err := run(t, "trust", "reliability")
	if err != nil {
		t.Fatalf("trust reliability: %v", err)
	}
	if !strings.Contains(out, "7 awaiting a verdict") {
		t.Errorf("unjudged runs not surfaced:\n%s", out)
	}
}

func TestCLI_TrustReliabilityJSONCarriesTheDecomposition(t *testing.T) {
	cliSandbox(t)
	seedScoredRuns(t, rand.New(rand.NewSource(17)), 0.75, 0.75, 40, true)

	out, _, err := run(t, "trust", "reliability", "--json")
	if err != nil {
		t.Fatalf("trust reliability --json: %v", err)
	}
	var rep struct {
		reliability.Report
		Unscored int `json:"unscored"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if rep.N != 40 {
		t.Errorf("n = %d, want 40", rep.N)
	}
	if want := rep.Reliability - rep.Resolution + rep.Uncertainty; abs(rep.Brier-want) > 1e-9 {
		t.Errorf("brier %.9f != reliability-resolution+uncertainty %.9f", rep.Brier, want)
	}
	if len(rep.Bins) == 0 {
		t.Error("no bins in the JSON, so nothing can plot it")
	}
}

// The refusal path has to be machine-readable too, or a caller polling --json
// cannot tell "not enough data" from a crash.
func TestCLI_TrustReliabilityJSONRefusalIsStructured(t *testing.T) {
	cliSandbox(t)
	seedScoredRuns(t, rand.New(rand.NewSource(21)), 0.9, 0.9, 3, true)

	out, _, err := run(t, "trust", "reliability", "--json")
	if err != nil {
		t.Fatalf("trust reliability --json: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("refusal is not JSON: %v\n%s", err, out)
	}
	if got["error"] == nil {
		t.Errorf("refusal carries no error field: %v", got)
	}
	if got["scored"] == nil {
		t.Errorf("refusal does not say how many were scored: %v", got)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// seedDecisions writes count decision answers from source in domain, each
// stating stated and right at rate actual. Through the real Append and
// AppendScore, standing in for the decision head until it writes these itself.
func seedDecisions(t *testing.T, rng *rand.Rand, source, domain string, stated, actual float64, count int) {
	t.Helper()
	// Hashed, because a head id like ollama/qwen3:4b is not a file name.
	rl := runlog.New("run-decide-" + runlog.SpanIDFor(source+"\x00"+domain))
	for i := 0; i < count; i++ {
		seedDecision(t, rl, source, domain, stated, rng.Float64() < actual)
	}
}

// seedDecision writes one decision answer to rl with its verdict, and returns
// the answer's span id.
func seedDecision(
	t *testing.T, rl *runlog.Logger, source, domain string, stated float64, correct bool,
) string {
	t.Helper()
	spanID := runlog.NewSpanID()
	if err := rl.Append(runlog.Event{
		Kind: runlog.KindDecision, TaskID: "t", SpanID: spanID,
		Head: source, Domain: domain, Confidence: stated,
	}); err != nil {
		t.Fatal(err)
	}
	value := 0.0
	if correct {
		value = 1
	}
	if err := runlog.AppendScore(rl.RunID(), spanID, runlog.Score{
		Name: "label", Value: value, Source: "human",
	}); err != nil {
		t.Fatal(err)
	}
	return spanID
}

// sourcesJSON runs the command with args and decodes the sources it reports.
func sourcesJSON(t *testing.T, args ...string) []reliability.SourceReport {
	t.Helper()
	out, _, err := run(t, append([]string{"trust", "reliability", "--json"}, args...)...)
	if err != nil {
		t.Fatalf("trust reliability --json: %v", err)
	}
	var got struct {
		Sources []reliability.SourceReport `json:"sources"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	return got.Sources
}

// The point of the audit: with no --source every source is listed beside the
// others, and the one whose numbers do not hold up reads worse.
func TestCLI_TrustReliabilityListsEverySourceSideBySide(t *testing.T) {
	cliSandbox(t)
	rng := rand.New(rand.NewSource(23))
	seedDecisions(t, rng, "ollama/honest", "go", 0.7, 0.7, 200)
	seedDecisions(t, rng, "boastful", "go", 0.95, 0.6, 200)

	out, _, err := run(t, "trust", "reliability")
	if err != nil {
		t.Fatalf("trust reliability: %v", err)
	}
	for _, want := range []string{
		"DECISION SOURCE",
		"ollama/honest",
		"boastful",
		"GAP",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render is missing %q:\n%s", want, out)
		}
	}

	ece := map[string]float64{}
	for _, sr := range sourcesJSON(t) {
		if sr.Report == nil {
			t.Fatalf("%s in %s has no report on %d verdicts", sr.Source, sr.Domain, sr.Scored)
		}
		ece[sr.Source] = sr.Report.ECE
	}
	if ece["boastful"] <= ece["ollama/honest"] {
		t.Errorf("overconfident ECE %.4f is not worse than calibrated %.4f", ece["boastful"], ece["ollama/honest"])
	}
}

// --source keeps one head and splits it by domain, since a source can be
// honest on one kind of work and not another.
func TestCLI_TrustReliabilitySourceReportsPerDomain(t *testing.T) {
	cliSandbox(t)
	rng := rand.New(rand.NewSource(29))
	seedDecisions(t, rng, "a", "go", 0.8, 0.8, 30)
	seedDecisions(t, rng, "a", "py", 0.8, 0.8, 30)
	seedDecisions(t, rng, "b", "go", 0.8, 0.8, 30)

	got := sourcesJSON(t, "--source", "a")
	want := []reliability.Key{
		{Source: "a", Domain: "go"},
		{Source: "a", Domain: "py"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i, sr := range got {
		if sr.Key != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, sr.Key, want[i])
		}
	}
}

// Below the floor a source gets the count it still needs, never a number.
func TestCLI_TrustReliabilitySourceRefusesBelowTheFloor(t *testing.T) {
	cliSandbox(t)
	seedDecisions(t, rand.New(rand.NewSource(31)), "a", "go", 0.9, 0.9, 5)

	out, _, err := run(t, "trust", "reliability", "--source", "a")
	if err != nil {
		t.Fatalf("trust reliability --source: %v", err)
	}
	need := fmt.Sprintf("not enough verdicts yet, %d more needed", reliability.MinObservations-5)
	if !strings.Contains(out, need) {
		t.Errorf("refusal does not say how many verdicts it needs:\n%s", out)
	}

	got := sourcesJSON(t, "--source", "a")
	if len(got) != 1 || got[0].Report != nil || got[0].Need != reliability.MinObservations-5 {
		t.Errorf("JSON refusal = %+v, want no report and need %d", got, reliability.MinObservations-5)
	}
}

// A source with nothing recorded says so rather than printing an empty table.
func TestCLI_TrustReliabilityUnknownSourceSaysSo(t *testing.T) {
	cliSandbox(t)

	out, _, err := run(t, "trust", "reliability", "--source", "nobody")
	if err != nil {
		t.Fatalf("trust reliability --source: %v", err)
	}
	if !strings.Contains(out, "No decision answers recorded from nobody") {
		t.Errorf("does not say the source has no record:\n%s", out)
	}
}

// Hydra's own report and the sources must not count each other's spans.
func TestCLI_TrustReliabilityKeepsHydraAndSourcesApart(t *testing.T) {
	cliSandbox(t)
	rng := rand.New(rand.NewSource(37))
	seedScoredRuns(t, rng, 0.8, 0.8, 25, true)
	seedDecisions(t, rng, "a", "go", 0.8, 0.8, 30)

	out, _, err := run(t, "trust", "reliability", "--json")
	if err != nil {
		t.Fatalf("trust reliability --json: %v", err)
	}
	var rep struct {
		reliability.Report
		Sources []reliability.SourceReport `json:"sources"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if rep.N != 25 {
		t.Errorf("hydra n = %d, want 25", rep.N)
	}
	if len(rep.Sources) != 1 || rep.Sources[0].Scored != 30 {
		t.Errorf("sources = %+v, want one source with 30 verdicts", rep.Sources)
	}
}

// One source writing a value that is not a probability must not take Hydra's
// report or any other source down with it, and its row has to say where to look.
func TestCLI_TrustReliabilityBadProbabilityStaysOnItsOwnRow(t *testing.T) {
	cliSandbox(t)
	rng := rand.New(rand.NewSource(41))
	seedScoredRuns(t, rng, 0.8, 0.8, 25, true)
	seedDecisions(t, rng, "broken", "go", 0.8, 0.8, reliability.MinObservations-1)
	seedDecisions(t, rng, "fine", "go", 0.8, 0.8, reliability.MinObservations)
	rl := runlog.New("run-decide-bad")
	badSpan := seedDecision(t, rl, "broken", "go", 1.5, true)

	out, _, err := run(t, "trust", "reliability")
	if err != nil {
		t.Fatalf("one bad probability failed the whole report: %v", err)
	}
	for _, want := range []string{
		"brier",
		"run-decide-bad",
		badSpan,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render is missing %q:\n%s", want, out)
		}
	}

	rows := map[string]reliability.SourceReport{}
	for _, sr := range sourcesJSON(t) {
		rows[sr.Source] = sr
	}
	broken := rows["broken"]
	if broken.Report != nil || !strings.Contains(broken.Err, badSpan) {
		t.Errorf("broken row = %+v, want no report and an error naming span %s", broken, badSpan)
	}
	if rows["fine"].Report == nil {
		t.Errorf("a healthy source lost its report beside a broken one: %+v", rows["fine"])
	}
}
