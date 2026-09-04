// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/runlog"
)

// seedOldRun writes a run and back-dates it so seal considers it.
func seedOldRun(t *testing.T, id string, events int, age time.Duration) {
	t.Helper()
	l := runlog.New(id)
	for i := 0; i < events; i++ {
		if err := l.Append(runlog.Event{Kind: runlog.KindDispatchFinished, Status: "ok",
			Detail: fmt.Sprintf("event %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-age)
	if err := os.Chtimes(runlog.Path(id), old, old); err != nil {
		t.Fatal(err)
	}
}

func oldRunID(i int) string { return fmt.Sprintf("20260101T12000%dZ-%016x", i%10, i) }

// --dry-run must not touch anything. Someone runs it precisely to find out
// whether it is safe to run for real.
func TestCLI_TraceSealDryRunWritesNothing(t *testing.T) {
	cliSandbox(t)
	seedOldRun(t, oldRunID(1), 3, 48*time.Hour)

	out, _, err := run(t, "trace", "seal", "--dry-run", "--older-than", "24h")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Would seal 1 run") {
		t.Errorf("dry-run output = %q", out)
	}
	if _, err := os.Stat(runlog.Path(oldRunID(1))); err != nil {
		t.Errorf("dry-run removed the loose run file: %v", err)
	}
	if months, _ := runlog.Months(); len(months) != 0 {
		t.Errorf("dry-run created segments: %v", months)
	}
}

func TestCLI_TraceSealDryRunJSONNamesTheRuns(t *testing.T) {
	cliSandbox(t)
	seedOldRun(t, oldRunID(1), 2, 48*time.Hour)
	seedOldRun(t, oldRunID(2), 2, 48*time.Hour)

	out, _, err := run(t, "trace", "seal", "--dry-run", "--older-than", "24h", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		WouldSeal int      `json:"would_seal"`
		Runs      []string `json:"runs"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--json did not emit parseable JSON: %v\n%s", err, out)
	}
	if got.WouldSeal != 2 || len(got.Runs) != 2 {
		t.Errorf("got %+v, want the two back-dated runs", got)
	}
}

// The reason this command exists is disk, so it has to report the saving —
// and the run must still read back afterwards, or it is retention, not
// relocation.
func TestCLI_TraceSealReportsTheSavingAndKeepsRunsReadable(t *testing.T) {
	cliSandbox(t)
	for i := 1; i <= 4; i++ {
		seedOldRun(t, oldRunID(i), 3, 48*time.Hour)
	}

	out, _, err := run(t, "trace", "seal", "--older-than", "24h")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "sealed 4 runs") {
		t.Errorf("seal output does not report four runs:\n%s", out)
	}
	if !strings.Contains(out, "12 events") {
		t.Errorf("seal output does not report the event count:\n%s", out)
	}
	if !strings.Contains(out, "on disk ->") {
		t.Errorf("seal output does not report the disk saving:\n%s", out)
	}
	for i := 1; i <= 4; i++ {
		events, err := runlog.Load(oldRunID(i))
		if err != nil {
			t.Fatalf("sealed run %d no longer loads: %v", i, err)
		}
		if len(events) != 3 {
			t.Errorf("sealed run %d has %d events, want 3 — sealing lost data", i, len(events))
		}
	}
}

func TestCLI_TraceSealNothingToDoSaysSo(t *testing.T) {
	cliSandbox(t)
	seedOldRun(t, oldRunID(1), 2, 0) // fresh

	out, _, err := run(t, "trace", "seal", "--older-than", "24h")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Nothing to seal") {
		t.Errorf("output = %q, want it to say there was nothing old enough", out)
	}
}

func TestCLI_TraceSealJSONIsParseable(t *testing.T) {
	cliSandbox(t)
	seedOldRun(t, oldRunID(1), 5, 48*time.Hour)

	out, _, err := run(t, "trace", "seal", "--older-than", "24h", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var res runlog.SealResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("--json did not emit parseable JSON: %v\n%s", err, out)
	}
	if res.Runs != 1 || res.Events != 5 || res.Months != 1 {
		t.Errorf("result = %+v, want 1 run / 5 events / 1 month", res)
	}
	if res.BytesAfter <= 0 {
		t.Error("BytesAfter is zero, so the compression ratio reads as infinite")
	}
}

// An empty seal must not print Inf or NaN as its ratio.
func TestRatioOf_GuardsTheEmptySeal(t *testing.T) {
	if got := ratioOf(1000, 0); got != 0 {
		t.Errorf("ratioOf(1000, 0) = %v, want 0", got)
	}
	if got := ratioOf(0, 0); got != 0 {
		t.Errorf("ratioOf(0, 0) = %v, want 0", got)
	}
	if got := ratioOf(1000, 250); got != 4 {
		t.Errorf("ratioOf(1000, 250) = %v, want 4", got)
	}
}

// A byte count that truncates to "0 KB" is the kind of user-visible wrongness
// that makes a saving look like nothing happened.
func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1024, "1.0 KB"},
		{245760, "240.0 KB"},
		{1024 * 1024, "1.0 MB"},
		{3 * 1024 * 1024 * 1024, "3.0 GB"},
		{5 * 1024 * 1024 * 1024 * 1024, "5.0 TB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
