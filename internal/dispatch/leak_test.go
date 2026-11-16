// SPDX-License-Identifier: MIT

package dispatch

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/testutil"
)

type corpusText struct {
	Work    string `json:"work"`
	Text    string `json:"text"`
	Planted []struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	} `json:"planted"`
}

// The leak half of #1886: every value a detector finds must be missing from
// what the hosted head received. Recall per kind is logged against the planted
// values, so a kind no pattern covers shows as a number, not a silence.
func TestRedaction_NoDetectedValueReachesRemote(t *testing.T) {
	testutil.NewSandbox(t)
	away, rec := recordingHead(t, "away", "done")
	d := liveDispatcher(away)

	f, err := os.Open("../policy/testdata/redaction_corpus.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	type tally struct{ planted, replaced, leaked int }
	kinds := map[string]*tally{}
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		var c corpusText
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatalf("line %d: %v", line, err)
		}
		if _, err := d.Dispatch(context.Background(), c.Text, Options{Redact: true, NoCache: true}); err != nil {
			t.Fatalf("line %d: %v", line, err)
		}
		sent := rec.sent()
		stored, _ := policy.Redact(c.Text)
		for _, p := range c.Planted {
			k := kinds[p.Kind]
			if k == nil {
				k = &tally{}
				kinds[p.Kind] = k
			}
			k.planted++
			reached := strings.Contains(sent, p.Value)
			if !reached {
				k.replaced++
			}
			// Detected means Redact removes it; a detected value that reached the head is a leak.
			if reached && !strings.Contains(stored, p.Value) {
				k.leaked++
				t.Errorf("line %d (%s): a detected %s reached the hosted head", line, c.Work, p.Kind)
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n%-18s %8s %8s %6s %7s\n", "kind", "planted", "replaced", "leaks", "recall")
	for _, name := range slices.Sorted(maps.Keys(kinds)) {
		k := kinds[name]
		fmt.Fprintf(&b, "%-18s %8d %8d %6d %6.0f%%\n", name, k.planted, k.replaced, k.leaked,
			100*float64(k.replaced)/float64(k.planted))
	}
	t.Log(b.String())
}
