// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/testutil"
)

// statsFixture is a sandbox holding two rows: one today, one a week ago. Two
// periods, so a report that quietly widens from one to the other shows in the
// totals and not only in the heading.
//
// The sandbox is the point. Without one these tests read the developer's own
// cost log, which is why they passed here and failed on a runner that has
// never dispatched anything.
func statsFixture(t *testing.T) {
	t.Helper()
	testutil.NewSandbox(t)
	if err := config.Save(&config.Config{Cortex: "none"}); err != nil {
		t.Fatal(err)
	}
	row := func(at time.Time, tokens int) string {
		return fmt.Sprintf(`{"ts":"%s","tier":10,"enum":"GRUNT","model":"qwen","executor":"ollama",`+
			`"pool":"local","prompt_tokens":%d,"response_tokens":1,"est_cost_usd":0,"wall_ms":10,`+
			`"tokens_source":"actual","cost_source":"estimated","task_id":"t","run_id":"r"}`,
			at.Format(time.RFC3339), tokens)
	}
	// Local noon, so each row is unambiguously inside its own local day.
	noon := func(daysAgo int) time.Time {
		d := time.Now().AddDate(0, 0, -daysAgo)
		return time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, time.Local)
	}
	seed(t, "logs/cost.jsonl", row(noon(0), 100)+"\n"+row(noon(7), 500)+"\n")
}

// The grouping and the period are independent questions, and the period used to
// be decided by a condition over the grouping flags: `hyctl stats --model`,
// naming the grouping that is already the default, reported all time where the
// bare command reported today, 161 calls against 5 on a real log (#918).
func TestStats_TheGroupingFlagDoesNotChangeThePeriod(t *testing.T) {
	statsFixture(t)

	bare := statsPeriod(t)
	if bare != "today" {
		t.Fatalf("the bare command reports %q, want today", bare)
	}
	for _, flag := range []string{"--model", "--tier", "--day"} {
		if got := statsPeriod(t, flag); got != bare {
			t.Errorf("hyctl stats %s reports %q, bare reports %q: a grouping flag moved the period",
				flag, got, bare)
		}
	}

	// And the numbers move with the period, which is what a reader actually
	// sees: the week-old row must not be counted under a heading saying today.
	if got := statsCalls(t); got != 1 {
		t.Errorf("today's report counts %d calls, want 1: the week-old row is in it", got)
	}
}

// --days is the only thing that sets the period, and 0 means all time, which is
// what cost.FilterDays does and what the same flag means on `trace evaluate`
// and `trace export`. It meant the opposite here.
func TestStats_DaysSetsThePeriod(t *testing.T) {
	statsFixture(t)

	for arg, want := range map[string]string{
		"0": "all time",
		"2": "last 2 days",
		"7": "last 7 days",
	} {
		if got := statsPeriod(t, "--days", arg); got != want {
			t.Errorf("--days %s reports %q, want %q", arg, got, want)
		}
	}

	// All time reaches the week-old row; two days does not. Counted rather than
	// matched on a token figure, since the rows group into one line by model
	// and the figure there is their sum.
	if got := statsCalls(t, "--days", "0"); got != 2 {
		t.Errorf("all time counts %d calls, want both rows", got)
	}
	if got := statsCalls(t, "--days", "2"); got != 1 {
		t.Errorf("the last two days count %d calls, want only today's", got)
	}
}

// statsCalls is the call count on the Total row, which is what a period
// widening or narrowing actually changes.
func statsCalls(t *testing.T, args ...string) int {
	t.Helper()
	for _, line := range strings.Split(statsRun(t, args...), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "Total") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			break
		}
		n, err := strconv.Atoi(strings.ReplaceAll(fields[1], ",", ""))
		if err != nil {
			t.Fatalf("Total row %q: %v", line, err)
		}
		return n
	}
	t.Fatalf("no Total row for hyctl stats %v", args)
	return 0
}

func statsRun(t *testing.T, args ...string) string {
	t.Helper()
	out, cobraOut, err := run(t, append([]string{"stats"}, args...)...)
	if err != nil {
		t.Fatalf("hyctl stats %v: %v\n%s", args, err, cobraOut)
	}
	return out
}

// statsPeriod is the period a run reports, without the grouping that follows
// it: a grouping flag is meant to change that half and nothing else.
func statsPeriod(t *testing.T, args ...string) string {
	t.Helper()
	for _, line := range strings.Split(statsRun(t, args...), "\n") {
		if strings.HasPrefix(line, "Period:") {
			period, _, _ := strings.Cut(strings.TrimPrefix(line, "Period:"), "·")
			return strings.TrimSpace(period)
		}
	}
	return "(no period line)"
}
