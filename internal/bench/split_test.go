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
		c := splitText(rows[i].Text)
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

// splitText is a leakage key, not a grader: case, apostrophes, punctuation and
// Unicode spacing never separate two copies of one utterance.
func TestSplitText_FoldsWhatALeakAcrossSplitsWouldHideBehind(t *testing.T) {
	for a, b := range map[string]string{
		"what's my balance?":       "whats my balance",
		"What\u2019s  my BALANCE":  "whats my balance",
		"top-up failed\u00a0again": "top up failed again",
		"£10 fee…":                 "10 fee",
	} {
		if splitText(a) != splitText(b) {
			t.Errorf("splitText(%q) = %q, splitText(%q) = %q", a, splitText(a), b, splitText(b))
		}
		if splitOf("s", a) != splitOf("s", b) {
			t.Errorf("%q and %q landed in different splits", a, b)
		}
	}
	if splitText("card arrival") == splitText("card arrived") {
		t.Fatal("two different utterances share a key")
	}
	// Labels are still compared with the grader's normaliser, never this key.
	if oracle.NormaliseLabel("card-arrival") == oracle.NormaliseLabel("card arrival") {
		t.Fatal("the label normaliser now folds '-', which grading must not")
	}
}

// A hand-edited TSV is refused: a flipped split, and two copies of one utterance
// moved apart, both disagree with what the data gives.
func TestLoad_RefusesAHandEditedSplit(t *testing.T) {
	rows := []Row{{"a", "What's up?", "x"}, {"b", "whats up", "x"}, {"c", "bye", "x"}}
	good, _ := AssignSplits("stub", rows)
	if good[0].Split != good[1].Split {
		t.Fatal("copies of one utterance were assigned apart")
	}
	if _, err := Load(stubSuite{rows}, Dataset{}, "", writeTSV(t, good)); err != nil {
		t.Fatal(err)
	}
	flip := func(s Split) Split {
		if s == SplitTest {
			return SplitCalibration
		}
		return SplitTest
	}
	for name, i := range map[string]int{"flipped bye": 2, "copies moved apart": 1} {
		edited := append([]SplitRow(nil), good...)
		edited[i].Split = flip(edited[i].Split)
		if _, err := Load(stubSuite{rows}, Dataset{}, "", writeTSV(t, edited)); err == nil || !strings.Contains(err.Error(), "the data gives") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Load(stubSuite{append(rows, rows[0])}, Dataset{}, "", writeTSV(t, append(good, good[0]))); err == nil {
		t.Error("a data file with a repeated id loaded")
	}
}

// Text only Unicode normalisation could fold is refused by name, since the split key
// would leave its two spellings free to straddle the splits.
func TestLoad_RefusesTextThatNeedsUnicodeNormalisation(t *testing.T) {
	rows := []Row{{"ok/1", "what's my balance", "x"}, {"mark/1", "café", "x"},
		{"wide/1", "ｂalance", "x"}, {"lig/1", "ﬁle", "x"}, {"ok/2", "£10 fee… ’", "x"},
		{"greek/1", "ά", "x"}, {"ok/3", "नमस्ते", "x"}, {"ok/4", "กิน", "x"}}
	good, _ := AssignSplits("stub", rows)
	_, err := Load(stubSuite{rows}, Dataset{}, "", writeTSV(t, good))
	if err == nil {
		t.Fatal("loaded text with a combining mark, a fullwidth form and a ligature")
	}
	// A mark on a Latin or Greek letter is a variant NFC would fold; the virama and vowel
	// sign in Devanagari "namaste", or a Thai vowel, are how the word is spelt.
	for _, id := range []string{"mark/1", "wide/1", "lig/1", "greek/1"} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("error does not name %s: %v", id, err)
		}
	}
	if strings.Contains(err.Error(), "ok/") || !strings.Contains(err.Error(), "4 items") {
		t.Errorf("error names the wrong items: %v", err)
	}
}
