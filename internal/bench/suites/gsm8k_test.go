// SPDX-License-Identifier: MIT

package suites

import (
	"context"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/bench"
)

func TestGSM8K_LoadsGoldFromTheHashesLine(t *testing.T) {
	s, items := loadFixture(t, "gsm8k")
	if len(items) != 4 || items[0].ID != "test/0" || items[0].Class != "answer.gsm8k" {
		t.Fatalf("items %+v", items)
	}
	var golds []string
	for _, it := range items {
		g, err := it.Label()
		if err != nil {
			t.Fatal(err)
		}
		golds = append(golds, g)
	}
	if strings.Join(golds, " ") != "36 1300 -2 2.5" {
		t.Fatalf("golds %v; want commas dropped and signs and decimals kept", golds)
	}
	req, err := s.Prompt(items[0])
	if err != nil || !strings.Contains(req.Prompt, "12 rolls") || !strings.Contains(req.System, "#### <number>") ||
		*req.Temperature != 0 || req.MaxTokens != gsm8kAnswerTokens {
		t.Fatalf("prompt %+v, %v", req, err)
	}
}

func TestGSM8K_GradeLastNumberWithoutHashes(t *testing.T) {
	s, items := loadFixture(t, "gsm8k")
	it := items[1] // gold 1300
	for answer, want := range map[string]bench.Grade{
		"#### 1300":        {Passed: true},
		"Total: $1,300.":   {Passed: true}, // no #### line: the last number
		"1,300.00 dollars": {Passed: true},
		"The bag is 50, so 1250+50 = 1300 then #### 1250": {}, // a #### line wins over the last number
		"I think it is 1200":                              {},
		"I cannot tell":                                   {Format: true},
	} {
		g, err := s.Grade(context.Background(), it, answer)
		if err != nil {
			t.Fatal(err)
		}
		if g.Passed != want.Passed || g.Format != want.Format || (!g.Passed && g.Detail == "") {
			t.Errorf("Grade(%q) = %+v, want passed=%v format=%v", answer, g, want.Passed, want.Format)
		}
	}
	if _, err := s.Grade(context.Background(), bench.Item{ID: "x", Gold: []byte("{")}, "1"); err == nil {
		t.Error("graded against a malformed gold")
	}
}

func TestGSM8K_ArithmeticCheckCatchesWrongNote(t *testing.T) {
	for answer, want := range map[string]bool{
		"12*3=<<12*3=36>>36\n#### 36":            true,
		"<<1,250+50=1,300>> then <<(7-9)/2=-1>>": true,
		"<<5/2=2.5>> and <<10/3=3.3333>>":        true,
		"12*3=<<12*3=38>>38\n#### 38":            false, // the note is wrong
		"<<12*3=36>> but <<4+4=9>>":              false, // one wrong note fails the answer
		"twelve threes are 36\n#### 36":          false, // no notes: nothing was checked
		"<<7//2=3>>":                             false, // Go would read // as a comment
		"<<2**3=8>>":                             false,
		"<<x+1=2>>":                              false,
		"<<4/0=0>>":                              false,
		"<<-(3)=-3>>":                            true,
		"<<3=three>>":                            false,
	} {
		if got := GSM8KArithmeticCheck(answer); got != want {
			t.Errorf("GSM8KArithmeticCheck(%q) = %v, want %v", answer, got, want)
		}
	}
}

func TestGSM8K_FormatCheckWantsTheHashesLine(t *testing.T) {
	if !GSM8KFormatCheck("work\n#### 42") || GSM8KFormatCheck("the answer is 42") || GSM8KFormatCheck("#### forty") {
		t.Fatal("format check must pass only a #### <number> line")
	}
}

func TestGSM8K_RowsRefuseAnAnswerWithoutHashes(t *testing.T) {
	lock, root := fixtureLock(t), fixtureRoot(t)
	dir := bench.DataDir(root, "gsm8k")
	for name, content := range map[string]string{
		"no hashes":   `{"question":"q","answer":"it is 4"}` + "\n",
		"no question": `{"question":" ","answer":"#### 4"}` + "\n",
		"bad json":    "{\n",
	} {
		ds := rehash(t, lock["gsm8k"], dir, "test.jsonl", content)
		if _, err := Registry()["gsm8k"]().Rows(ds, dir); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
