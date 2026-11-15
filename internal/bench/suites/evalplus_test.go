// SPDX-License-Identifier: MIT

package suites

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/bench"
)

// The fixture task adds two numbers; the base inputs are all non-negative and
// the plus inputs are not, so an answer can pass base and fail plus.
var evalplusFixture = []map[string]any{{
	"task_id": "Fixture/0", "entry_point": "add", "atol": 0,
	"prompt":             "def add(a, b):\n    \"\"\"Return a + b.\"\"\"\n",
	"canonical_solution": "    return a + b\n",
	"base_input":         [][]int{{1, 2}, {3, 4}},
	"plus_input":         [][]int{{-1, 1}, {-5, -7}},
}}

// evalplusDataset writes records as the pinned gzip file and returns its lock entry.
func evalplusDataset(t *testing.T, root string, records []map[string]any) bench.Dataset {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	for _, r := range records {
		if err := json.NewEncoder(zw).Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	zw.Close()
	dir := bench.DataDir(root, "humanevalplus")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "HumanEvalPlus.jsonl.gz"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return bench.Dataset{Revision: "fixture", Files: []bench.LockEntry{{Name: "HumanEvalPlus.jsonl.gz",
		URL: "http://fixture/x", SHA256: hex.EncodeToString(sum[:])}}}
}

func loadEvalPlus(t *testing.T) (*evalplus, bench.Item) {
	t.Helper()
	root, bdir := t.TempDir(), t.TempDir()
	ds := evalplusDataset(t, root, evalplusFixture)
	if _, err := GenerateSplits(map[string]bench.Dataset{"humanevalplus": ds}, root, bdir); err != nil {
		t.Fatal(err)
	}
	s := Registry()["humanevalplus"]().(*evalplus)
	items, err := bench.Load(s, ds, bench.DataDir(root, "humanevalplus"), filepath.Join(bdir, "splits", "humanevalplus.tsv"))
	if err != nil || len(items) != 1 {
		t.Fatalf("%d items, %v", len(items), err)
	}
	return s, items[0]
}

func TestEvalPlus_RowsAndPrompt(t *testing.T) {
	s, it := loadEvalPlus(t)
	if it.ID != "Fixture/0" || it.Class != "code.humanevalplus" {
		t.Fatalf("item %+v", it)
	}
	req, err := s.Prompt(it)
	if err != nil || !strings.HasPrefix(req.Prompt, "def add(a, b):") || *req.Temperature != 0 || req.MaxTokens != evalplusAnswerTokens {
		t.Fatalf("prompt %+v, %v", req, err)
	}
	if _, err := s.Prompt(bench.Item{ID: "x", Input: []byte("{")}); err == nil || s.Labels() != nil {
		t.Error("a malformed input built a prompt, or a code suite claimed labels")
	}
	if got := solution("```python\ndef add(a, b):\n    return b + a\n```\nDone.", s.tasks[it.ID]); got != "def add(a, b):\n    return b + a\n" {
		t.Errorf("code block not taken: %q", got)
	}
	if got := solution("    return a + b\n", s.tasks[it.ID]); !strings.HasPrefix(got, "def add(a, b):") {
		t.Errorf("a bare body was not put under the stub: %q", got)
	}
}

func TestEvalPlus_RowsRefuseWhatTheyCannotTrust(t *testing.T) {
	root := t.TempDir()
	dup := append(append([]map[string]any{}, evalplusFixture...), evalplusFixture...)
	if _, err := Registry()["humanevalplus"]().Rows(evalplusDataset(t, root, dup), bench.DataDir(root, "humanevalplus")); err == nil {
		t.Error("a task id twice was accepted")
	}
	ds := rehash(t, evalplusDataset(t, root, evalplusFixture), bench.DataDir(root, "humanevalplus"), "HumanEvalPlus.jsonl.gz", "not gzip")
	if _, err := Registry()["humanevalplus"]().Rows(ds, bench.DataDir(root, "humanevalplus")); err == nil {
		t.Error("a file that is not gzip was read")
	}
	var bad bytes.Buffer
	zw := gzip.NewWriter(&bad)
	zw.Write([]byte(`{"task_id": "x", "entry_point": "f"}` + "\n{"))
	zw.Close()
	ds = rehash(t, evalplusDataset(t, root, evalplusFixture), bench.DataDir(root, "humanevalplus"), "HumanEvalPlus.jsonl.gz", bad.String())
	if _, err := Registry()["humanevalplus"]().Rows(ds, bench.DataDir(root, "humanevalplus")); err == nil {
		t.Error("a truncated record was read")
	}
	if _, err := Registry()["humanevalplus"]().Grade(t.Context(), bench.Item{ID: "Fixture/0"}, "x"); err == nil {
		t.Error("graded before it loaded")
	}
}
