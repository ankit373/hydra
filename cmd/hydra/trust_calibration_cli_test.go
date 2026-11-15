// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/trust"
)

// seedDrop writes a calibration log where "worse" drops from 0.92 to 0.70 in
// its last 300 answers, ending an hour ago, and "steady" stays at 0.92.
func seedDrop(t *testing.T) {
	t.Helper()
	r := rand.New(rand.NewPCG(1547, 1))
	var b strings.Builder
	const n = 2300
	start := time.Now().UTC().Add(-time.Hour - n*time.Minute)
	line := func(i int, source string, rate float64) {
		outcome := 2 // trust.OutcomeIncorrect
		if r.Float64() < rate {
			outcome = 1
		}
		fmt.Fprintf(&b, `{"ts":%q,"source":%q,"domain":"go","said_correct":true,"outcome":%d}`+"\n",
			start.Add(time.Duration(i)*time.Minute).Format(time.RFC3339), source, outcome)
	}
	for i := range n {
		rate := 0.92
		if i >= n-300 {
			rate = 0.70
		}
		line(i, "worse", rate)
		line(i, "steady", 0.92)
	}
	if err := os.MkdirAll(filepath.Dir(trust.DefaultPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trust.DefaultPath(), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTrustCalibration_ShowsChange(t *testing.T) {
	populated(t)
	seedDrop(t)

	out, _, err := run(t, "trust", "calibration")
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) > 0 {
			rows[f[0]] = l
		}
	}
	// Dated in local time, so read the day back rather than assume today.
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	var day string
	for _, s := range cal.Report() {
		if s.Source == "worse" && !s.Changed.IsZero() {
			day = s.Changed.Local().Format(time.DateOnly)
		}
	}
	if w := rows["worse"]; day == "" || !strings.Contains(w, day) {
		t.Errorf("the dropped source shows no change dated %q:\n%s", day, w)
	}
	// Eight columns and the one-way mark, so no changed date.
	if s := rows["steady"]; len(strings.Fields(s)) != 9 || !strings.Contains(s, "/2300") {
		t.Errorf("the steady source shows a change or less than its whole record:\n%s", s)
	}
	if !strings.Contains(out, "1 of 2 cells changed in the last 7 days") {
		t.Errorf("no footer for the recent change:\n%s", out)
	}
}

func TestTrustCalibration_ShowsChangeJSON(t *testing.T) {
	populated(t)
	seedDrop(t)

	out, _, err := run(t, "trust", "calibration", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var stats []trust.Stat
	if err := json.Unmarshal([]byte(out), &stats); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}
	by := map[string]trust.Stat{}
	for _, s := range stats {
		by[s.Source] = s
	}
	worse, steady := by["worse"], by["steady"]
	if worse.Changed.IsZero() || worse.SinceTotal < 200 || worse.SinceTotal > 400 {
		t.Errorf("worse: changed %v, since %d/%d, want a dated change and about 300 since",
			worse.Changed, worse.SinceCorrect, worse.SinceTotal)
	}
	if !steady.Changed.IsZero() || steady.SinceTotal != 2300 {
		t.Errorf("steady: changed %v, since %d, want no change and the whole 2300", steady.Changed, steady.SinceTotal)
	}
	if !strings.Contains(out, `"since_total"`) || strings.Count(out, `"changed"`) != 1 {
		t.Errorf("json carries since on every row and changed only where one was found:\n%s", out)
	}
}
