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
