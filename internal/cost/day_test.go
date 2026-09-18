// SPDX-License-Identifier: MIT

package cost

import (
	"testing"
	"time"
)

// withZone runs f with time.Local fixed, so the boundary a test states is the
// one it gets rather than whatever the machine running it happens to use.
func withZone(t *testing.T, name string, f func()) {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("zone %s unavailable: %v", name, err)
	}
	orig := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = orig })
	f()
}

// The day a reader is shown is the reader's. Rows stay stamped in UTC, so east
// of UTC the last hours of a UTC day are already tomorrow, and west of it the
// first hours are still yesterday (#916).
func TestDayOf_IsTheReadersDay(t *testing.T) {
	withZone(t, "Asia/Kolkata", func() { // UTC+5:30
		// 19:00Z is 00:30 the next morning in Kolkata: the case this machine
		// was in when the report was headed with yesterday's date.
		if got := DayOf("2026-09-18T19:00:00Z"); got != "2026-09-19" {
			t.Errorf("DayOf = %q, want 2026-09-19: 19:00Z is half past midnight in IST", got)
		}
		if got := DayOf("2026-09-18T10:00:00Z"); got != "2026-09-18" {
			t.Errorf("DayOf = %q, want 2026-09-18", got)
		}
	})

	withZone(t, "America/Los_Angeles", func() { // UTC-7/8
		// 02:00Z is still the previous evening on the US west coast.
		if got := DayOf("2026-09-19T02:00:00Z"); got != "2026-09-18" {
			t.Errorf("DayOf = %q, want 2026-09-18: 02:00Z is the evening before in PDT", got)
		}
	})

	withZone(t, "UTC", func() {
		if got := DayOf("2026-09-18T19:00:00Z"); got != "2026-09-18" {
			t.Errorf("DayOf = %q, want the same day in UTC", got)
		}
	})
}

// A timestamp this cannot read is one bucket, not a scattering of them, and
// never silently a real day.
func TestDayOf_UnreadableTimestamps(t *testing.T) {
	if got := DayOf(""); got != "unknown" {
		t.Errorf("DayOf(empty) = %q, want unknown", got)
	}
	// A date-only stamp is not RFC3339; grouping it under its own text beats
	// inventing a time of day for it.
	if got := DayOf("2026-09-18"); got != "2026-09-18" {
		t.Errorf("DayOf(date only) = %q", got)
	}
}

// Every row lands in exactly one day, and the heading a reader sees is built
// from the same function the rows are bucketed with.
func TestByDay_BucketsOnTheReadersDay(t *testing.T) {
	withZone(t, "Asia/Kolkata", func() {
		rows := []Row{
			{TS: "2026-09-18T10:00:00Z", Model: "a", EstCostUSD: 1},
			{TS: "2026-09-18T19:00:00Z", Model: "b", EstCostUSD: 1}, // next day in IST
			{TS: "2026-09-18T20:00:00Z", Model: "c", EstCostUSD: 1}, // next day in IST
		}
		groups := ByDay(rows)
		if len(groups) != 2 {
			t.Fatalf("groups = %d, want 2 local days: %+v", len(groups), groups)
		}
		byKey := map[string]int{}
		for _, g := range groups {
			byKey[g.Key] = g.Calls
		}
		if byKey["2026-09-18"] != 1 || byKey["2026-09-19"] != 2 {
			t.Errorf("buckets = %v, want 1 on the 18th and 2 on the 19th", byKey)
		}
	})
}

// Day(0) is what the heading says, and InLastDays is what the rows are
// filtered by. A heading naming a day the filter does not use is the defect.
func TestDayAndFilterAgree(t *testing.T) {
	withZone(t, "Asia/Kolkata", func() {
		now := time.Now()
		if got := Day(0); got != now.Format("2006-01-02") {
			t.Errorf("Day(0) = %q, want the local date %q", got, now.Format("2006-01-02"))
		}
		today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.Local)
		if !InLastDays(today.Format(time.RFC3339), 1) {
			t.Error("a row at local noon today is not in the last one day")
		}
		if InLastDays(today.AddDate(0, 0, -1).Format(time.RFC3339), 1) {
			t.Error("yesterday's row is in the last one day")
		}
		if !InLastDays(today.AddDate(0, 0, -1).Format(time.RFC3339), 2) {
			t.Error("yesterday's row is not in the last two days")
		}
	})
}
