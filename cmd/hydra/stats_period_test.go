// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"
)

// The grouping and the period are independent questions, and the period used to
// be decided by a condition over the grouping flags: `hyctl stats --model`,
// naming the grouping that is already the default, reported all time where the
// bare command reported today, 161 calls against 5 (#918).
func TestStats_TheGroupingFlagDoesNotChangeThePeriod(t *testing.T) {
	bare := statsPeriodLine(t)
	for _, flag := range []string{"--model", "--tier", "--day"} {
		if got := statsPeriodLine(t, flag); got != bare {
			t.Errorf("hyctl stats %s reports %q, bare reports %q: a grouping flag moved the period",
				flag, got, bare)
		}
	}
	if !strings.Contains(bare, "today") {
		t.Errorf("the bare command reports %q, want today", bare)
	}
}

// --days is the only thing that sets the period, and 0 means all time, which is
// what cost.FilterDays does and what the same flag means on `trace evaluate`
// and `trace export`. It meant the opposite here.
func TestStats_DaysSetsThePeriod(t *testing.T) {
	cases := map[string]string{
		"0": "all time",
		"2": "last 2 days",
		"7": "last 7 days",
	}
	for arg, want := range cases {
		if got := statsPeriodLine(t, "--days", arg); !strings.Contains(got, want) {
			t.Errorf("--days %s reports %q, want %q", arg, got, want)
		}
	}
}

// statsPeriodLine runs the real command in a sandbox and returns the period it
// reports, without the grouping that follows it: a grouping flag is meant to
// change that half and nothing else.
func statsPeriodLine(t *testing.T, args ...string) string {
	t.Helper()
	out, cobraOut, err := run(t, append([]string{"stats"}, args...)...)
	if err != nil {
		t.Fatalf("hyctl stats %v: %v\n%s", args, err, cobraOut)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Period:") {
			period, _, _ := strings.Cut(strings.TrimPrefix(line, "Period:"), "·")
			return strings.TrimSpace(period)
		}
	}
	return "(no period line)\n" + out
}
