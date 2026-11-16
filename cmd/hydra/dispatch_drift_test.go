// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/trust"
)

func TestDispatch_NoticeOnRecentChange(t *testing.T) {
	_, f := escalating(t, "summary", "The total is 120 dollars.", "The total is 120 dollars.")
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	// a, the cheaper head, drops from 380 of 400 to 1 in 3: dated today.
	recordN(t, cal, "litellm/a", "summary.general", 100, 300)

	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--kind", "summary", "summarise this invoice")
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if f.called("a") != 0 || f.called("b") != 1 {
		t.Fatalf("calls a=%d b=%d, want the dropped head passed over", f.called("a"), f.called("b"))
	}
	var changed time.Time
	for _, s := range cal.Report() {
		if s.Source == "litellm/a" {
			changed = s.Changed
		}
	}
	if changed.IsZero() {
		t.Fatal("the seeded drop was not found")
	}
	want := "passed over litellm/a, its summary record changed on " + changed.Local().Format("2 Jan")
	if !strings.Contains(out, want) {
		t.Errorf("no notice %q:\n%s", want, out)
	}
}

func TestDispatch_NoNoticeWithoutChange(t *testing.T) {
	escalating(t, "summary", "The total is 120 dollars.", "The total is 120 dollars.")

	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--kind", "summary", "summarise this invoice")
	if err != nil {
		t.Fatalf("dispatch failed: %v (%s)", err, cobraOut)
	}
	if strings.Contains(out, "record for") || strings.Contains(out, "passed over") {
		t.Errorf("a notice with no change on record:\n%s", out)
	}
}

// The answering head's own drop or recovery, read off fixed stats and clock.
func TestDriftNotice_AnsweringHead(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	r := &dispatch.Result{Head: provider.Head{ID: "h"}, Domain: "summary.general"}
	for _, tc := range []struct {
		s    trust.Stat
		want string
	}{
		{trust.Stat{Changed: now.AddDate(0, 0, -3)}, "summary record for h changed on "},
		{trust.Stat{Changed: now.AddDate(0, 0, -20), Recovered: now.AddDate(0, 0, -2)}, "summary record for h recovered on "},
		{trust.Stat{Changed: now.AddDate(0, 0, -20)}, ""},
		{trust.Stat{}, ""},
	} {
		tc.s.Source, tc.s.Domain = "h", "summary.general"
		got := driftNotice([]trust.Stat{tc.s}, r, "", now)
		if tc.want == "" && got != "" || !strings.HasPrefix(got, tc.want) {
			t.Errorf("stat %+v: notice %q, want prefix %q", tc.s, got, tc.want)
		}
	}
}
