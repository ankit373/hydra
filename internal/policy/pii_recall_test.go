// SPDX-License-Identifier: MIT

package policy

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
)

// Recall is measured against a vendored corpus rather than asserted, because a
// regex nobody scored is a claim about detection and not a measurement (#1031).
type labelled struct {
	Text     string   `json:"text"`
	Entities []string `json:"entities"`
}

func loadSample(t *testing.T) []labelled {
	t.Helper()
	f, err := os.Open("testdata/presidio_sample.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []labelled
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		var l labelled
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		out = append(out, l)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func has(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// The floors are what the detectors measured when they were written. A change
// that lowers one is a regression in the direction that leaks, so it is red.
func TestPII_RecallOnVendoredCorpus(t *testing.T) {
	for _, c := range []struct {
		entity, detector string
		floor            int
	}{
		{"PHONE_NUMBER", "phone number", 44},
		{"IBAN_CODE", "iban", 21},
	} {
		t.Run(c.entity, func(t *testing.T) {
			total, hit := 0, 0
			for _, l := range loadSample(t) {
				if !has(l.Entities, c.entity) {
					continue
				}
				total++
				if has(DetectPII(Request{Prompt: l.Text}), c.detector) {
					hit++
				}
			}
			if total == 0 {
				t.Fatalf("the corpus carries no %s, so this measures nothing", c.entity)
			}
			t.Logf("%s: %d/%d texts (%.0f%%)", c.detector, hit, total, 100*float64(hit)/float64(total))
			if hit < c.floor {
				t.Errorf("recall fell to %d/%d, floor is %d", hit, total, c.floor)
			}
		})
	}
}

// The other direction: a text the corpus labels with neither must not trip
// either detector. A phone regex that fires on any grouped digits is worse
// than the gap it fills, because it routes ordinary work local for nothing.
func TestPII_NoPhoneOrIBANWhereTheCorpusLabelsNone(t *testing.T) {
	var bad []string
	for _, l := range loadSample(t) {
		if has(l.Entities, "PHONE_NUMBER") || has(l.Entities, "IBAN_CODE") {
			continue
		}
		for _, n := range DetectPII(Request{Prompt: l.Text}) {
			if n == "phone number" || n == "iban" {
				bad = append(bad, n+": "+l.Text)
			}
		}
	}
	if len(bad) > 0 {
		t.Errorf("%d unlabelled texts tripped a new detector:", len(bad))
		for i, b := range bad {
			if i == 5 {
				t.Logf("... and %d more", len(bad)-5)
				break
			}
			t.Logf("  %s", b)
		}
	}
}
