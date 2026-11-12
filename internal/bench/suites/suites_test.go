// SPDX-License-Identifier: MIT

package suites

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/bench"
)

// fixtureLock pins the testdata files by their real hashes, as datasets.lock pins upstream.
func fixtureLock(t *testing.T) map[string]bench.Dataset {
	t.Helper()
	lock := map[string]bench.Dataset{}
	for suite, names := range map[string][]string{
		"banking77": {"categories.json", "train.csv", "test.csv"},
		"clinc150":  {"data_full.json"},
	} {
		ds := bench.Dataset{Revision: "fixture"}
		for _, n := range names {
			b, err := os.ReadFile(filepath.Join("testdata", suite, n))
			if err != nil {
				t.Fatal(err)
			}
			s := sha256.Sum256(b)
			ds.Files = append(ds.Files, bench.LockEntry{Name: n, URL: "http://fixture/" + n, SHA256: hex.EncodeToString(s[:])})
		}
		lock[suite] = ds
	}
	return lock
}

// fixtureRoot lays testdata out the way DataDir expects.
func fixtureRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, suite := range []string{"banking77", "clinc150"} {
		dst := bench.DataDir(root, suite)
		if err := os.MkdirAll(dst, 0o755); err != nil {
			t.Fatal(err)
		}
		entries, _ := os.ReadDir(filepath.Join("testdata", suite))
		for _, e := range entries {
			b, _ := os.ReadFile(filepath.Join("testdata", suite, e.Name()))
			if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

func loadFixture(t *testing.T, name string) (bench.Suite, []bench.Item) {
	t.Helper()
	lock, root := fixtureLock(t), fixtureRoot(t)
	bdir := t.TempDir()
	if _, err := GenerateSplits(lock, root, bdir); err != nil {
		t.Fatal(err)
	}
	s := Registry()[name]()
	items, err := bench.Load(s, lock[name], bench.DataDir(root, name), filepath.Join(bdir, "splits", name+".tsv"))
	if err != nil {
		t.Fatal(err)
	}
	return s, items
}

func TestBanking77_FixtureKeepsAQuotedNewline(t *testing.T) {
	s, items := loadFixture(t, "banking77")
	if len(items) != 5 || len(s.Labels()) != 3 {
		t.Fatalf("got %d items and %d labels, want 5 and 3", len(items), len(s.Labels()))
	}
	text, _ := items[1].Text()
	if items[1].ID != "train/1" || !strings.Contains(text, "\nand it has been") {
		t.Fatalf("item 1 = %s %q, want the quoted newline kept inside one row", items[1].ID, text)
	}
	if items[3].ID != "test/0" || items[0].Class != "classification.banking77" {
		t.Fatalf("ids/classes wrong: %s %s", items[3].ID, items[0].Class)
	}
}

func TestCLINC150_ConflictingDuplicatesAreExcludedAgreeingOnesShareASplit(t *testing.T) {
	s, items := loadFixture(t, "clinc150")
	by := map[string]bench.Item{}
	for _, it := range items {
		by[it.ID] = it
	}
	if len(items) != 9 || len(s.Labels()) != 5 {
		t.Fatalf("got %d items, %d labels, want 9 and 5 (oos is a label)", len(items), len(s.Labels()))
	}
	if by["train/0"].Split != bench.SplitExcluded || by["val/0"].Split != bench.SplitExcluded {
		t.Fatalf("todo_list/reminder duplicate not excluded: %v %v", by["train/0"].Split, by["val/0"].Split)
	}
	if a, b := by["train/1"].Split, by["val/1"].Split; a != b || a == bench.SplitExcluded {
		t.Fatalf("agreeing duplicate split %v / %v, want one shared split", a, b)
	}
}

func TestGrade_OneNormaliserDecidesPassAndFormat(t *testing.T) {
	s, items := loadFixture(t, "banking77")
	it := items[0] // card_arrival
	for answer, want := range map[string]bench.Grade{
		"card_arrival":           {Passed: true},
		" Card_Arrival ":         {Passed: true},
		"card arrival":           {Passed: true},
		"`card_arrival`.":        {Passed: true},
		"":                       {Format: true},
		"card-arrival":           {Format: true}, // '-' is significant
		"card_arrival because x": {Format: true},
		"card_arrivals":          {Format: true},
		"top_up_failed":          {},
	} {
		g, err := s.Grade(context.Background(), it, answer)
		if err != nil {
			t.Fatal(err)
		}
		if g.Passed != want.Passed || g.Format != want.Format {
			t.Errorf("Grade(%q) = %+v, want passed=%v format=%v", answer, g, want.Passed, want.Format)
		}
		if !g.Passed && g.Detail == "" {
			t.Errorf("Grade(%q) failed with no detail", answer)
		}
	}
}

func TestGrade_BeforeLoadIsAnError(t *testing.T) {
	if _, err := Registry()["clinc150"]().Grade(context.Background(), bench.Item{}, "x"); err == nil {
		t.Fatal("grading with no label set succeeded")
	}
}

func TestPrompt_IsTheSameRubricForEveryArm(t *testing.T) {
	s, items := loadFixture(t, "clinc150")
	req, err := s.Prompt(items[2])
	if err != nil {
		t.Fatal(err)
	}
	if req.Temperature == nil || *req.Temperature != 0 || req.MaxTokens != 32 {
		t.Fatalf("temperature/max tokens = %v/%d, want 0/32", req.Temperature, req.MaxTokens)
	}
	for _, l := range s.Labels() {
		if !strings.Contains(req.System, "\n"+l+"\n") {
			t.Errorf("rubric does not list %q on its own line", l)
		}
	}
	if !strings.Contains(req.Prompt, "set an alarm") || !strings.Contains(req.Prompt, "untrusted data") {
		t.Fatalf("utterance not fenced as untrusted: %q", req.Prompt)
	}
	if _, err := s.Prompt(bench.Item{ID: "x", Input: []byte("{")}); err == nil {
		t.Fatal("a malformed input built a prompt")
	}
}

func TestRows_RefuseWhatTheyCannotTrust(t *testing.T) {
	lock, root := fixtureLock(t), fixtureRoot(t)
	b := Registry()["banking77"]()
	short := lock["banking77"]
	short.Files = short.Files[:1]
	if _, err := b.Rows(short, bench.DataDir(root, "banking77")); err == nil {
		t.Error("a lock missing files was accepted")
	}
	dir := bench.DataDir(root, "banking77")
	os.WriteFile(filepath.Join(dir, "test.csv"), []byte("text,category\nx,not_a_label\n"), 0o644)
	if _, err := b.Rows(lock["banking77"], dir); err == nil {
		t.Error("a file off its pinned hash was read")
	}
	c := Registry()["clinc150"]()
	cdir := bench.DataDir(root, "clinc150")
	os.Remove(filepath.Join(cdir, "data_full.json"))
	if _, err := c.Rows(lock["clinc150"], cdir); err == nil || !strings.Contains(err.Error(), "bench fetch") {
		t.Errorf("a missing file should point at `hyctl bench fetch`, got %v", err)
	}
}

// rehash pins a rewritten fixture so the parser, not the hash check, is what refuses it.
func rehash(t *testing.T, ds bench.Dataset, dir, name, content string) bench.Dataset {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256([]byte(content))
	files := append([]bench.LockEntry(nil), ds.Files...)
	for i := range files {
		if files[i].Name == name {
			files[i].SHA256 = hex.EncodeToString(s[:])
		}
	}
	ds.Files = files
	return ds
}

func TestRows_RefuseMalformedPinnedData(t *testing.T) {
	lock, root := fixtureLock(t), fixtureRoot(t)
	bdir, cdir := bench.DataDir(root, "banking77"), bench.DataDir(root, "clinc150")
	for name, c := range map[string]struct {
		suite, file, content string
	}{
		"bad categories":   {"banking77", "categories.json", "{"},
		"duplicate labels": {"banking77", "categories.json", `["a","A"]`},
		"bad header":       {"banking77", "test.csv", "utterance,label\nx,card_arrival\n"},
		"unknown label":    {"banking77", "test.csv", "text,category\nx,not_a_label\n"},
		"unclosed quote":   {"banking77", "train.csv", "text,category\n\"x,card_arrival\n"},
		"bad json":         {"clinc150", "data_full.json", "["},
		"missing part":     {"clinc150", "data_full.json", `{"train":[],"val":[],"test":[],"oos_train":[],"oos_val":[],"other":[]}`},
		"too few parts":    {"clinc150", "data_full.json", `{"train":[]}`},
	} {
		dir := bdir
		if c.suite == "clinc150" {
			dir = cdir
		}
		ds := rehash(t, lock[c.suite], dir, c.file, c.content)
		if _, err := Registry()[c.suite]().Rows(ds, dir); err == nil {
			t.Errorf("%s: accepted", name)
		}
		lock, root = fixtureLock(t), fixtureRoot(t)
		bdir, cdir = bench.DataDir(root, "banking77"), bench.DataDir(root, "clinc150")
	}
}

func TestGenerateSplits_RefusesASuiteNobodyLoads(t *testing.T) {
	lock := fixtureLock(t)
	lock["cord"] = lock["clinc150"]
	if _, err := GenerateSplits(lock, fixtureRoot(t), t.TempDir()); err == nil {
		t.Fatal("generated splits for a suite with no loader")
	}
}
