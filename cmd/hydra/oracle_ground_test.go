// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/ground"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/util"
)

const groundSource = `// UITier keeps tier 10 as the free floor, local-only, and floors paid heads at 9.
func UITier(h provider.Head) int {
	if h.LocalOnly {
		return 10
	}
	return 9
}`

// writeGroundFiles lays down a fenced prompt and an answer, and returns their
// paths.
func writeGroundFiles(t *testing.T, answer string) (promptPath, answerPath string) {
	t.Helper()
	dir := t.TempDir()
	promptPath = filepath.Join(dir, "prompt.txt")
	answerPath = filepath.Join(dir, "answer.txt")
	body := "Summarise this file.\n\n" + util.WrapUntrusted("CONTEXT", groundSource)
	if err := os.WriteFile(promptPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(answerPath, []byte(answer), 0o600); err != nil {
		t.Fatal(err)
	}
	return promptPath, answerPath
}

// wantExit fails unless err carries exit status want, which is what a script gates on.
func wantExit(t *testing.T, err error, want int) {
	t.Helper()
	var status exitStatus
	if !errors.As(err, &status) || int(status) != want {
		t.Fatalf("err = %v, want exit status %d", err, want)
	}
}

// The headline case: an invented tier and an invented symbol are named, and
// the exit code says so (#1736).
func TestOracleGround_NamesWhatTheContextDoesNotSupport(t *testing.T) {
	testutil.NewSandbox(t)
	p, a := writeGroundFiles(t, "UITier puts a LocalOnly head at tier 12 via rank.EffectiveScoreIn.")

	out, _, err := run(t, "oracle", "ground", "--prompt", p, "--candidate", a)
	wantExit(t, err, groundUnsupported)
	for _, want := range []string{"UNSUPPORTED", "12", "rank.EffectiveScoreIn"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}

func TestOracleGround_PassesAGroundedAnswer(t *testing.T) {
	testutil.NewSandbox(t)
	p, a := writeGroundFiles(t, "UITier puts a LocalOnly head at tier 10, the free floor.")

	out, _, err := run(t, "oracle", "ground", "--prompt", p, "--candidate", a)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "GROUNDED") {
		t.Errorf("a grounded answer was not reported as such:\n%s", out)
	}
}

// Both "nothing to check against" and "nothing to check" report not-checked
// with their own exit code, so neither reads as a pass or as a failed check.
func TestOracleGround_ReportsNotCheckedApart(t *testing.T) {
	testutil.NewSandbox(t)
	dir := t.TempDir()

	unfenced := filepath.Join(dir, "plain.txt")
	if err := os.WriteFile(unfenced, []byte("just a question"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, prose := writeGroundFiles(t, "It does what the file says.")
	fenced, _ := writeGroundFiles(t, "unused")

	for _, c := range []struct{ name, prompt, answer, want string }{
		{"no context", unfenced, prose, "fenced no context"},
		{"no claims", fenced, prose, "no checkable claim"},
	} {
		out, _, err := run(t, "oracle", "ground", "--prompt", c.prompt, "--candidate", c.answer)
		wantExit(t, err, groundNotChecked)
		if !strings.Contains(out, "NOT CHECKED") || !strings.Contains(out, c.want) {
			t.Errorf("%s: want a not-checked report mentioning %q:\n%s", c.name, c.want, out)
		}
	}
}

// An evidence source with no history must not print a prior wearing a
// measurement's clothes.
func TestOracleGround_WithholdsAStrengthUntilItIsMeasured(t *testing.T) {
	testutil.NewSandbox(t)
	p, a := writeGroundFiles(t, "UITier puts a LocalOnly head at tier 10.")

	out, _, err := run(t, "oracle", "ground", "--prompt", p, "--candidate", a)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "insufficient evidence") {
		t.Errorf("an unobserved source stated a strength:\n%s", out)
	}
	if strings.Contains(out, "nats") {
		t.Errorf("a number was printed for a source nobody has observed:\n%s", out)
	}

	// Positives alone are still not a measurement: specificity stays pinned
	// and the evidence cannot exceed ln 2 however many arrive (#771).
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if err := cal.Update("verifier:grounding", "", true, trust.OutcomeCorrect); err != nil {
			t.Fatal(err)
		}
	}
	out, _, err = run(t, "oracle", "ground", "--prompt", p, "--candidate", a)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no negative") {
		t.Errorf("five positives read as a measurement:\n%s", out)
	}

	// With both verdicts recorded, a strength is stated.
	if err := cal.Update("verifier:grounding", "", false, trust.OutcomeIncorrect); err != nil {
		t.Fatal(err)
	}
	out, _, err = run(t, "oracle", "ground", "--prompt", p, "--candidate", a)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "nats") {
		t.Errorf("a measured source still withheld its strength:\n%s", out)
	}
}

func TestOracleGround_JSONCarriesTheSpansAndTheRefusal(t *testing.T) {
	testutil.NewSandbox(t)
	p, a := writeGroundFiles(t, "UITier puts a LocalOnly head at tier 12.")

	out, _, err := run(t, "oracle", "ground", "--prompt", p, "--candidate", a, "--json")
	wantExit(t, err, groundUnsupported)
	var got struct {
		Checked  bool   `json:"checked"`
		Grounded bool   `json:"grounded"`
		Why      string `json:"insufficient_evidence"`
		Verdict  struct {
			Claims      int `json:"claims"`
			Unsupported []struct {
				Text string `json:"text"`
				Kind string `json:"kind"`
			} `json:"unsupported"`
		} `json:"verdict"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unparseable json: %v\n%s", err, out)
	}
	if !got.Checked || got.Grounded {
		t.Errorf("checked=%v grounded=%v", got.Checked, got.Grounded)
	}
	if len(got.Verdict.Unsupported) != 1 || got.Verdict.Unsupported[0].Text != "12" {
		t.Errorf("unsupported = %+v", got.Verdict.Unsupported)
	}
	if got.Why == "" {
		t.Error("json omitted why no strength was stated")
	}
}

// Both files are required: guessing one from the other would mean checking an
// answer against itself.
func TestOracleGround_RequiresBothInputs(t *testing.T) {
	testutil.NewSandbox(t)
	p, a := writeGroundFiles(t, "anything")

	if _, _, err := run(t, "oracle", "ground", "--prompt", p); err == nil {
		t.Error("ran with no candidate")
	}
	if _, _, err := run(t, "oracle", "ground", "--candidate", a); err == nil {
		t.Error("ran with no prompt")
	}
}

// Without a span the answer is still keyed, by its text's hash, so a later
// check on the same text can join it.
func TestOracleGround_WritesAVerdictKeyedByCandidate(t *testing.T) {
	testutil.NewSandbox(t)
	answer := "UITier puts a LocalOnly head at tier 10, the free floor."
	p, a := writeGroundFiles(t, answer)

	if _, _, err := run(t, "oracle", "ground", "--prompt", p, "--candidate", a, "--domain", "docs"); err != nil {
		t.Fatal(err)
	}
	answers, err := evalset.Joint(evalset.VerdictsPath())
	if err != nil || len(answers) != 1 || len(answers[0].Checks) != 1 {
		t.Fatalf("verdict log = %+v, err=%v; want one answer with one check", answers, err)
	}
	c := answers[0].Checks[0]
	if c.CandidateHash != evalset.Hash(answer) || c.Check != ground.DefaultSource || !c.Passed || c.Domain != "docs" {
		t.Errorf("verdict = %+v", c)
	}
}

// Two different checks on one span are one answer, in the order they ran.
func TestTwoChecksOnOneSpanFoldToOneAnswer(t *testing.T) {
	cliSandbox(t)
	const runID = "20260908T060000Z-7777777777777777"
	_, okSpan := seedRun(t, runID)
	p, a := writeGroundFiles(t, "UITier puts a LocalOnly head at tier 12.")

	_, _, err := run(t, "oracle", "ground", "--prompt", p, "--candidate", a,
		"--run", runID, "--span", okSpan[:8])
	wantExit(t, err, groundUnsupported)
	score := cmdTraceScore()
	score.SetArgs([]string{runID, "--span", okSpan, "--name", "review", "--value", "1"})
	captureStdout(t, func() {
		if err := score.Execute(); err != nil {
			t.Fatal(err)
		}
	})

	answers, err := evalset.Joint(evalset.VerdictsPath())
	if err != nil || len(answers) != 1 {
		t.Fatalf("verdict log = %+v, err=%v; want one answer", answers, err)
	}
	got := answers[0]
	if got.Key != okSpan || len(got.Checks) != 2 {
		t.Fatalf("answer = %+v, want two checks on %s", got, okSpan)
	}
	if got.Checks[0].Check != ground.DefaultSource || got.Checks[0].Passed {
		t.Errorf("first check = %+v, want a failed grounding", got.Checks[0])
	}
	if got.Checks[1].Check != "human" || !got.Checks[1].Passed {
		t.Errorf("second check = %+v, want a passing human verdict", got.Checks[1])
	}
}
