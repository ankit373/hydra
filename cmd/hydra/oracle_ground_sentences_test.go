// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/util"
)

const bySentenceAnswer = "Units sold were 1,200 and growth was 1.9 percent. Growth was 4.5 percent. The team worked hard."

// bySentenceFiles writes a fenced quarterly report and answer, and returns their paths.
func bySentenceFiles(t *testing.T, prompt, answer string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	p, a := filepath.Join(dir, "prompt.txt"), filepath.Join(dir, "answer.txt")
	if err := os.WriteFile(p, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a, []byte(answer), 0o600); err != nil {
		t.Fatal(err)
	}
	return p, a
}

var quarterly = "Summarise the report.\n" + util.WrapUntrusted("report", "Quarterly sales\nUnits sold: 1,200\nGrowth was 1.9 percent.")

func TestOracleGround_BySentenceText(t *testing.T) {
	testutil.NewSandbox(t)
	p, a := bySentenceFiles(t, quarterly, bySentenceAnswer)
	out, _, err := run(t, "oracle", "ground", "--by-sentence", "--prompt", p, "--candidate", a)
	wantExit(t, err, groundUnsupported)
	for _, want := range []string{
		" 1  Units sold were 1,200 and growth was 1.9 percent.", "supported [report L2-3]",
		" 2  Growth was 4.5 percent.", "NOT SUPPORTED: number 4.5",
		" 3  The team worked hard.", "nothing to check",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestOracleGround_BySentenceJSONShape(t *testing.T) {
	testutil.NewSandbox(t)
	p, a := bySentenceFiles(t, quarterly, bySentenceAnswer)
	out, _, err := run(t, "oracle", "ground", "--by-sentence", "--json", "--prompt", p, "--candidate", a)
	wantExit(t, err, groundUnsupported)
	golden, gerr := os.ReadFile(filepath.Join("testdata", "oracle_ground_by_sentence.json"))
	if gerr != nil {
		t.Fatal(gerr)
	}
	if strings.TrimSpace(out) != strings.TrimSpace(string(golden)) {
		t.Fatalf("JSON shape changed; it is a contract, so bump v to change it.\ngot:\n%s\nwant:\n%s", out, golden)
	}
}

func TestOracleGround_BySentenceExitCodes(t *testing.T) {
	testutil.NewSandbox(t)
	cases := []struct {
		prompt, answer string
		want           int
	}{
		{quarterly, "Units sold were 1,200.", 0},
		{quarterly, "Growth was 4.5 percent.", groundUnsupported},
		{quarterly, "The team worked hard.", groundNotChecked},
		{"Summarise the report: 1,200 units.", "Units sold were 1,200.", groundNotChecked},
	}
	for _, c := range cases {
		p, a := bySentenceFiles(t, c.prompt, c.answer)
		_, _, err := run(t, "oracle", "ground", "--by-sentence", "--prompt", p, "--candidate", a)
		if c.want == 0 {
			if err != nil {
				t.Errorf("%q: err = %v, want exit 0", c.answer, err)
			}
			continue
		}
		wantExit(t, err, c.want)
	}
	p, a := bySentenceFiles(t, quarterly, "Units sold were 1,200.")
	if _, _, err := run(t, "oracle", "ground", "--by-sentence", "--record", "correct", "--prompt", p, "--candidate", a); err == nil {
		t.Error("--by-sentence with --record was not refused")
	}
}
