// SPDX-License-Identifier: MIT

package cost

import "time"

// DayOf is which day a row belongs to, and the only answer to that question in
// this package.
//
// The day is the reader's, not UTC's. Rows are stamped in UTC, which is what
// keeps the log portable, but "today" in a spend report means the day the
// person reading it is having: at 00:30 in IST the UTC date is still
// yesterday's, so a UTC bucket filed this morning's work under yesterday and
// disagreed with the cockpit's run list, which was already local (#916).
//
// It parses rather than slicing the first ten characters. That slice is a UTC
// comparison by construction, which is how the same mistake appeared at four
// separate sites.
func DayOf(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		// Timestamps this cannot read are grouped as one rather than
		// scattered: an unparseable row is a fact about the log, not a day.
		if len(ts) >= 10 {
			return ts[:10]
		}
		return "unknown"
	}
	return t.Local().Format("2006-01-02")
}

// Day is the local calendar day n days back, 0 being today. The label a reader
// is shown and the bucket rows are compared against come from here, so a
// heading can never name a different day from the rows under it.
func Day(n int) string {
	return time.Now().AddDate(0, 0, -n).Format("2006-01-02")
}

// InLastDays reports whether a row falls in the last n local days, n=1 meaning
// today alone.
func InLastDays(ts string, n int) bool {
	if n <= 0 {
		return true
	}
	return DayOf(ts) >= Day(n-1)
}
