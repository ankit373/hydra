// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/trust"
)

// seedRun logs one finished ensemble run: two heads backed "A", one flagged it.
// Written through the real LogRun so the test reads what the CLI would.
func seedTrustRun(t *testing.T, hash string, withHypotheses bool) {
	t.Helper()
	r := trust.RunLog{
		TaskHash:   hash,
		Domain:     "go",
		TargetConf: 0.95,
		FinalConf:  0.65,
		Samples:    3,
		Models:     []string{"ollama/qwen3:4b", "claude", "ollama/phi4:14b"},
		Decision:   "stopped_on_budget",
		Ledger: []trust.Evidence{
			{Source: "ollama/qwen3:4b", Agreed: true, LLR: 0.5, Candidate: "A", LambdaAfter: 0.5, ConfidenceAfter: 0.62},
			{Source: "claude", Agreed: true, LLR: 0.5, Candidate: "A", LambdaAfter: 1.0, ConfidenceAfter: 0.73},
			{Source: "ollama/phi4:14b", Agreed: false, LLR: -0.4, Candidate: "A", LambdaAfter: 0.6, ConfidenceAfter: 0.65},
		},
	}
	if withHypotheses {
		r.Hypotheses = []trust.Hypothesis{
			{Answer: "A", Lambda: 0.6, Confidence: 0.65, Votes: 2},
			{Answer: "B", Lambda: -0.6, Confidence: 0.35, Votes: 1},
		}
	}
	if err := trust.LogRun(trust.DefaultLogPath(), r); err != nil {
		t.Fatal(err)
	}
}

// The whole point of the command: a head that disagreed with an answer that
// turned out wrong earns the true negative specificity can only come from, and
// the heads that backed it take the false positive.
func TestCLI_TrustOutcomeTrainsEveryVoterIncludingTheDissenter(t *testing.T) {
	cliSandbox(t)
	seedTrustRun(t, "cafe1234", false)

	out, _, err := run(t, "trust", "outcome", "cafe1234", "--outcome", "incorrect")
	if err != nil {
		t.Fatalf("trust outcome: %v", err)
	}
	if !strings.Contains(out, "recorded 3 of 3") {
		t.Errorf("output does not report what it trained:\n%s", out)
	}

	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	var dissenter, backer trust.Stat
	for _, s := range cal.Report() {
		switch s.Source {
		case "ollama/phi4:14b":
			dissenter = s
		case "claude":
			backer = s
		}
	}
	if dissenter.Sp <= 0.5 {
		t.Errorf("sp(dissenter) = %v, want above the 0.5 prior: it correctly flagged a wrong answer", dissenter.Sp)
	}
	if dissenter.Neg == 0 {
		t.Error("dissenter recorded no negative verdict, so specificity is still unmeasured")
	}
	if backer.Sp >= 0.5 {
		t.Errorf("sp(backer) = %v, want below the prior after a false positive", backer.Sp)
	}
}

func TestCLI_TrustOutcomeRefusesWhatItCannotTrainFrom(t *testing.T) {
	cliSandbox(t)
	seedTrustRun(t, "cafe1234", false)

	if _, _, err := run(t, "trust", "outcome", "cafe1234", "--outcome", "maybe"); err == nil {
		t.Error("an unparseable --outcome must error rather than train on a guess")
	}
	if _, _, err := run(t, "trust", "outcome", "nosuchrun", "--outcome", "correct"); err == nil {
		t.Error("an unknown task hash must error, not silently do nothing")
	}
}

// A run logged with no ledger cannot say who voted, so there is nothing to
// train and the command has to say so rather than report success.
func TestCLI_TrustOutcomeOnALedgerlessRunSaysSo(t *testing.T) {
	cliSandbox(t)
	if err := trust.LogRun(trust.DefaultLogPath(), trust.RunLog{TaskHash: "bare1234", Domain: "go"}); err != nil {
		t.Fatal(err)
	}
	_, _, err := run(t, "trust", "outcome", "bare1234", "--outcome", "correct")
	if err == nil {
		t.Fatal("a ledgerless run must error")
	}
	if !strings.Contains(err.Error(), "no ledger") {
		t.Errorf("error = %q, want it to name the missing ledger", err)
	}
}

// Every answer stays under test for the whole run, so explain has to show the
// losing ones: they carry real evidence.
func TestCLI_TrustExplainShowsEveryHypothesis(t *testing.T) {
	cliSandbox(t)
	seedTrustRun(t, "beef5678", true)

	out, _, err := run(t, "trust", "explain", "beef5678")
	if err != nil {
		t.Fatalf("trust explain: %v", err)
	}
	for _, want := range []string{"HYPOTHESIS", "VOTES", "CONFIDENCE"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "35.0%") {
		t.Errorf("the losing hypothesis's confidence is not shown:\n%s", out)
	}
}

// A run logged before hypotheses existed must still explain, printing the
// ledger alone rather than an empty table or a crash.
func TestCLI_TrustExplainOnALegacyRunOmitsTheHypothesisTable(t *testing.T) {
	cliSandbox(t)
	seedTrustRun(t, "0ld00001", false)

	out, _, err := run(t, "trust", "explain", "0ld00001")
	if err != nil {
		t.Fatalf("trust explain on a pre-hypothesis run: %v", err)
	}
	if !strings.Contains(out, "SOURCE") {
		t.Errorf("ledger table missing:\n%s", out)
	}
	if strings.Contains(out, "HYPOTHESIS") {
		t.Errorf("printed a hypothesis header with no hypotheses to show:\n%s", out)
	}
}

// neg=0 is the difference between a thin cell and an unusable one, so the table
// has to distinguish them rather than showing sp as if it were measured.
func TestCLI_TrustCalibrationFlagsCellsWithNoNegativeVerdicts(t *testing.T) {
	cliSandbox(t)
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := cal.Update("ollama/qwen3:4b", "go", true, trust.OutcomeCorrect); err != nil {
			t.Fatal(err)
		}
	}
	out, _, err := run(t, "trust", "calibration")
	if err != nil {
		t.Fatalf("trust calibration: %v", err)
	}
	if !strings.Contains(out, "neg") {
		t.Errorf("no neg column, so a pinned cell is indistinguishable:\n%s", out)
	}
	if !strings.Contains(out, "no negative verdicts") {
		t.Errorf("positives-only cell not flagged:\n%s", out)
	}
}
