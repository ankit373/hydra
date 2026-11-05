// SPDX-License-Identifier: MIT

package bench

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/oracle"
)

// The split and rank hashes are pinned to values, so a platform or library change
// that moved them would read here rather than as a quietly different sample.
func TestSplitAndRank_AreStableValues(t *testing.T) {
	if got := Rank("banking77", "train/00000"); got != 17139974644568928149 {
		t.Fatalf("Rank = %d", got)
	}
	cal, test := 0, 0
	for i := 0; i < 2000; i++ {
		if splitOf("s", strings.Repeat("x", i)) == SplitCalibration {
			cal++
		} else {
			test++
		}
	}
	if cal < 520 || cal > 680 {
		t.Fatalf("%d of 2000 in calibration, want about 30%%", cal)
	}
	// canon folds what the grader folds, so these are one utterance.
	if splitOf("s", "Hey, what's up.") != splitOf("s", "hey,  what's up") {
		t.Fatal("two copies the normaliser treats as one landed in different splits")
	}
}

func TestAssignSplits_NoIDOrCanonicalTextInBothSplits(t *testing.T) {
	var rows []Row
	for i := 0; i < 300; i++ {
		text := strings.Repeat("w ", i%120) // every text twice
		rows = append(rows, Row{ID: string(rune('a'+i%26)) + "/" + strings.Repeat("0", i), Text: text, Label: "l"})
	}
	rows = append(rows, Row{ID: "x/1", Text: "Same", Label: "a"}, Row{ID: "x/2", Text: "same.", Label: "b"})
	split, err := AssignSplits("s", rows)
	if err != nil {
		t.Fatal(err)
	}
	where := map[string]Split{}
	for i, r := range split {
		c := canon(rows[i].Text)
		if r.Split == SplitExcluded {
			if !strings.HasPrefix(r.ID, "x/") {
				t.Fatalf("%s excluded with agreeing labels", r.ID)
			}
			continue
		}
		if s, ok := where[c]; ok && s != r.Split {
			t.Fatalf("canonical text %q is in both %v and %v", c, s, r.Split)
		}
		where[c] = r.Split
	}
	if split[len(split)-1].Split != SplitExcluded || split[len(split)-2].Split != SplitExcluded {
		t.Fatal("a conflicting duplicate was not excluded in both copies")
	}
	if _, err := AssignSplits("s", []Row{{ID: "a"}, {ID: "a"}}); err == nil {
		t.Fatal("a repeated id was accepted")
	}
}

func TestSplits_RoundTripAndRefuseMalformedFiles(t *testing.T) {
	rows := []SplitRow{{"a/1", SplitTest, 7, "x"}, {"a/2", SplitCalibration, 9, "y"}, {"a/3", SplitExcluded, 1, "z"}}
	var buf bytes.Buffer
	if err := WriteSplits(&buf, rows); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSplits(&buf)
	if err != nil || len(got) != 3 || got[2] != rows[2] {
		t.Fatalf("round trip = %v, %v", got, err)
	}
	if err := WriteSplits(&buf, []SplitRow{{ID: "a\tb"}}); err == nil {
		t.Fatal("a tab in an id was written")
	}
	for name, in := range map[string]string{
		"no header":    "a\ttest\t1\tx\n",
		"short line":   splitsHeader + "\na\ttest\t1\n",
		"bad split":    splitsHeader + "\na\tdev\t1\tx\n",
		"bad rank":     splitsHeader + "\na\ttest\tone\tx\n",
		"empty header": "",
	} {
		if _, err := ReadSplits(strings.NewReader(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if s, err := ParseSplit("test"); err != nil || s != SplitTest || s.String() != "test" {
		t.Fatalf("ParseSplit = %v %v", s, err)
	}
}

func TestFirstByRank_TakesAPrefixOfOneSplit(t *testing.T) {
	items := []Item{{ID: "a", Split: SplitTest, Rank: 5}, {ID: "b", Split: SplitCalibration, Rank: 1},
		{ID: "c", Split: SplitTest, Rank: 2}, {ID: "d", Split: SplitTest, Rank: 2}, {ID: "e", Split: SplitTest, Rank: 9}}
	got := FirstByRank(items, SplitTest, 3)
	if len(got) != 3 || got[0].ID != "c" || got[1].ID != "d" || got[2].ID != "a" {
		t.Fatalf("got %v", got)
	}
	if len(FirstByRank(items, SplitTest, 100)) != 4 {
		t.Fatal("asking for more than exist did not return them all")
	}
}

// stubSuite is a Suite over fixed rows, for the join.
type stubSuite struct{ rows []Row }

func (s stubSuite) Name() string                          { return "stub" }
func (s stubSuite) Class() string                         { return "classification.stub" }
func (s stubSuite) Rows(Dataset, string) ([]Row, error)   { return s.rows, nil }
func (s stubSuite) Labels() []string                      { return []string{"x"} }
func (s stubSuite) Prompt(Item) (executor.Request, error) { return executor.Request{}, nil }
func (s stubSuite) Grade(context.Context, Item, string) (Grade, error) {
	return Grade{}, nil
}

func writeTSV(t *testing.T, rows []SplitRow) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.tsv")
	var buf bytes.Buffer
	if err := WriteSplits(&buf, rows); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p, buf.Bytes(), 0o644)
	return p
}

func TestLoad_JoinsAndRefusesEveryMismatch(t *testing.T) {
	rows := []Row{{"a", "hello", "x"}, {"b", "bye", "x"}}
	good, _ := AssignSplits("stub", rows)
	items, err := Load(stubSuite{rows}, Dataset{}, "", writeTSV(t, good))
	if err != nil || len(items) != 2 || items[0].Class != "classification.stub" {
		t.Fatalf("Load = %v, %v", items, err)
	}
	if txt, _ := items[0].Text(); txt != "hello" {
		t.Fatalf("text = %q", txt)
	}
	if l, _ := items[1].Label(); l != "x" {
		t.Fatalf("label = %q", l)
	}
	relabel := append([]SplitRow(nil), good...)
	relabel[1].Label = "y"
	rerank := append([]SplitRow(nil), good...)
	rerank[0].Rank++
	rename := append([]SplitRow(nil), good...)
	rename[0].ID = "z"
	for name, tsv := range map[string]string{
		"count":  writeTSV(t, good[:1]),
		"label":  writeTSV(t, relabel),
		"rank":   writeTSV(t, rerank),
		"id":     writeTSV(t, rename),
		"no tsv": filepath.Join(t.TempDir(), "missing"),
		"bad":    writeTSV(t, nil) + "x",
	} {
		if name == "bad" {
			os.WriteFile(tsv, []byte("nope"), 0o644)
		}
		if _, err := Load(stubSuite{rows}, Dataset{}, "", tsv); err == nil {
			t.Errorf("%s mismatch accepted", name)
		}
	}
	if _, err := (Item{ID: "q", Input: []byte("{")}).Text(); err == nil {
		t.Fatal("malformed input read")
	}
	if _, err := (Item{ID: "q", Gold: []byte("{")}).Label(); err == nil {
		t.Fatal("malformed gold read")
	}
}

// canon must stay the grader's normaliser, not a lookalike.
func TestCanon_IsNormaliseLabel(t *testing.T) {
	for _, s := range []string{" Card-Arrival. ", "hey  what's\tup", "**x**"} {
		if canon(s) != oracle.NormaliseLabel(s) {
			t.Fatalf("canon(%q) drifted from oracle.NormaliseLabel", s)
		}
	}
}
