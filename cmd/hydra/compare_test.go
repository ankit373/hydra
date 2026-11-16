// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/trust"
)

// seedCompare files passed of checked answers per head in kind, through recordN.
func seedCompare(t *testing.T, kind string, heads map[string][2]int) {
	t.Helper()
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, head := range slices.Sorted(maps.Keys(heads)) {
		recordN(t, cal, head, kind, heads[head][0], heads[head][1])
	}
}

func compareJSON(t *testing.T, args ...string) comparison {
	t.Helper()
	out, _, err := run(t, append([]string{"compare"}, append(args, "--json")...)...)
	if err != nil {
		t.Fatal(err)
	}
	var c comparison
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}
	return c
}

func TestCompare_FromRecord(t *testing.T) {
	populated(t)
	seedCompare(t, "summary.general", map[string][2]int{"sonnet": {95, 100}, "qwen": {60, 100}})
	seedCompare(t, "code.general", map[string][2]int{"qwen": {100, 100}})

	c := compareJSON(t, "sonnet", "qwen", "--kind", "summary")
	if c.Kind != "summary.general" || len(c.Heads) != 2 {
		t.Fatalf("got %+v", c)
	}
	s, q := c.Heads[0], c.Heads[1]
	if s.Checked != 100 || s.Passed != 95 || q.Checked != 100 || q.Passed != 60 {
		t.Errorf("counts sonnet %d/%d qwen %d/%d, want 95/100 and 60/100 (code answers left out)",
			s.Passed, s.Checked, q.Passed, q.Checked)
	}
	if s.Lower <= 0.85 || s.Lower >= 0.95 || s.CostPerChecked != nil {
		t.Errorf("sonnet lower %.3f cost %v, want a Wilson bound under 0.95 and no cost", s.Lower, s.CostPerChecked)
	}
	if c.Verdict != "ahead" || c.Ahead != "sonnet" {
		t.Errorf("verdict %q ahead %q, want sonnet ahead", c.Verdict, c.Ahead)
	}

	out, _, err := run(t, "compare", "sonnet", "qwen", "--kind", "summary")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "sonnet is ahead") || !strings.Contains(out, "unknown") {
		t.Errorf("want the verdict and an unknown cost:\n%s", out)
	}
}

func TestCompare_NoClearDifference(t *testing.T) {
	populated(t)
	seedCompare(t, "writing.general", map[string][2]int{"a": {16, 20}, "b": {14, 20}})

	c := compareJSON(t, "a", "b", "--kind", "writing")
	if c.Verdict != "no_clear_difference" || c.Ahead != "" {
		t.Fatalf("verdict %q ahead %q, want no clear difference", c.Verdict, c.Ahead)
	}
	// 0.80 against 0.70 parts at about 300 each with 95% Wilson bounds.
	if c.MoreEach < 150 || c.MoreEach > 500 {
		t.Errorf("more each %d, want a few hundred", c.MoreEach)
	}
	out, _, err := run(t, "compare", "a", "b", "--kind", "writing")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "No clear difference yet") || !strings.Contains(out, "more checked answers each") {
		t.Errorf("want the plain no-difference line:\n%s", out)
	}
}

func TestCompare_UnknownHeadErrors(t *testing.T) {
	populated(t)
	seedCompare(t, "summary.general", map[string][2]int{"sonnet": {9, 10}})

	_, _, err := run(t, "compare", "sonnet", "sonet", "--kind", "summary")
	if err == nil || !strings.Contains(err.Error(), `"sonet"`) || !strings.Contains(err.Error(), "sonnet") {
		t.Fatalf("err %v, want it to name the unknown head and list the known ones", err)
	}
	if _, _, err := run(t, "compare", "sonnet", "sonnet", "--kind", "summary"); err == nil {
		t.Error("comparing a head with itself should fail")
	}
	if _, _, err := run(t, "compare", "sonnet", "qwen", "--kind", "poetry"); err == nil {
		t.Error("an unknown kind should fail")
	}
}
