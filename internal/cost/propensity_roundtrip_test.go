// SPDX-License-Identifier: MIT

package cost

import (
	"encoding/json"
	"strings"
	"testing"
)

// A zero Row must still emit both propensity fields. If they were omitempty a
// reader could not tell "not recorded" from "certain", and weighting by a
// missing probability is a divide by zero (#605).
func TestRowCarriesPropensity(t *testing.T) {
	var r Row
	if err := json.Unmarshal([]byte(`{"act_prob":0.05,"keep_prob":1}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.ActProb != 0.05 || r.KeepProb != 1 {
		t.Fatalf("act_prob=%v keep_prob=%v", r.ActProb, r.KeepProb)
	}
	b, err := json.Marshal(Row{})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"act_prob", "keep_prob"} {
		if !strings.Contains(string(b), field) {
			t.Errorf("zero Row omits %s: %s", field, b)
		}
	}
}

// The first pick round-trips, and a row written before it existed loads with
// FirstHead "" so a reader can tell it from a first pick that answered.
func TestRowCarriesFirstPick(t *testing.T) {
	in := Row{Head: "spare", ActProb: 0.9, FirstHead: "cheap", FirstModel: "m",
		FirstTier: 4, FirstPool: "p", FallbackStep: 1}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Row
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("round trip changed the row:\n got %+v\nwant %+v", out, in)
	}
	if b, _ := json.Marshal(Row{}); !strings.Contains(string(b), `"fallback_step":0`) {
		t.Errorf("zero Row omits fallback_step, so 0 cannot mean the first pick answered: %s", b)
	}

	var old Row
	if err := json.Unmarshal([]byte(`{"head":"h","act_prob":0.9,"keep_prob":1}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.FirstHead != "" || old.FallbackStep != 0 || old.Head != "h" {
		t.Errorf("old row loaded as %+v", old)
	}
}
