// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// The headline case: an invented tier and an invented symbol are named.
func TestOracleGround_NamesWhatTheContextDoesNotSupport(t *testing.T) {
	testutil.NewSandbox(t)
	p, a := writeGroundFiles(t, "UITier puts a LocalOnly head at tier 12 via rank.EffectiveScoreIn.")

	out, _, err := run(t, "oracle", "ground", "--prompt", p, "--candidate", a)
	if err != nil {
		t.Fatal(err)
	}
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

// Both "nothing to check against" and "nothing to check" report not-checked,
// and neither exits non-zero: they are honest answers to the question asked,
// not failed checks.
func TestOracleGround_ReportsNotCheckedWithoutFailing(t *testing.T) {
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
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
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
	if err != nil {
		t.Fatal(err)
	}
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
