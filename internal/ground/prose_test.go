// SPDX-License-Identifier: MIT

package ground

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// prosePassage is one passage of openly licensed prose and the labelled answers
// about it. Sources and licences are in testdata/SOURCES.md.
type prosePassage struct {
	ID      string        `json:"id"`
	Context string        `json:"context"`
	Answers []proseAnswer `json:"answers"`
}

// proseAnswer is a faithful summary, or one with a single detail swapped (Swap
// says which kind), which is the synthetic half of the set.
type proseAnswer struct {
	Answer    string `json:"answer"`
	Grounded  bool   `json:"grounded"`
	Swap      string `json:"swap,omitempty"`
	Synthetic bool   `json:"synthetic,omitempty"`
}

// proseSwaps are the details a hard negative may swap.
var proseSwaps = map[string]bool{"name": true, "date": true, "quote": true, "number": true}

func loadProse(t *testing.T) []prosePassage {
	t.Helper()
	f, err := os.Open("testdata/prose.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []prosePassage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for line := 1; sc.Scan(); line++ {
		dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
		dec.DisallowUnknownFields()
		var p prosePassage
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("prose.jsonl line %d: %v", line, err)
		}
		out = append(out, p)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// The prose set is data a measurement rests on, so its labels and its licence
// record are checked on every run rather than when someone next measures.
func TestProseSet_IsWellFormed(t *testing.T) {
	sources, err := os.ReadFile("testdata/SOURCES.md")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var answers int
	for _, p := range loadProse(t) {
		if p.ID == "" || seen[p.ID] {
			t.Errorf("passage id %q is empty or repeated", p.ID)
		}
		seen[p.ID] = true
		if !strings.Contains(string(sources), "`"+p.ID+"`") {
			t.Errorf("%s: no source or licence for it in SOURCES.md", p.ID)
		}
		if strings.TrimSpace(p.Context) == "" || len(p.Answers) == 0 {
			t.Errorf("%s: empty context or no answers", p.ID)
		}
		grounded := 0
		for i, a := range p.Answers {
			answers++
			switch {
			case strings.TrimSpace(a.Answer) == "":
				t.Errorf("%s[%d]: empty answer", p.ID, i)
			case a.Grounded && (a.Swap != "" || a.Synthetic):
				t.Errorf("%s[%d]: a grounded answer is labelled as a swap", p.ID, i)
			case !a.Grounded && (!proseSwaps[a.Swap] || !a.Synthetic):
				t.Errorf("%s[%d]: an ungrounded answer needs a swap kind and the synthetic label, got %q", p.ID, i, a.Swap)
			}
			if a.Grounded {
				grounded++
			}
		}
		if grounded == 0 {
			t.Errorf("%s: no grounded answer, so nothing measures false alarms on it", p.ID)
		}
	}
	if answers < 150 {
		t.Errorf("%d labelled answers, want at least 150", answers)
	}
}
