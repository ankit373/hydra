// SPDX-License-Identifier: MIT

package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// withEmptyHome points the process at a home directory containing no ~/.hydra,
// which is the state of a machine that has just downloaded the app and never
// run hyctl. config.Dir() checks $HYDRA_HOME before $HOME (#442), so a
// developer or CI runner with one already exported must not leak through —
// clearing it is as much a part of isolating this as setting HOME/USERPROFILE.
func withEmptyHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HYDRA_HOME", "")
	return home
}

// Every view must render on a machine with no ~/.hydra. This became a shipped
// user journey when the app started being distributed as a download (#227):
// before that, the only people launching it had already been running the CLI.
//
// The bar is that no entry point errors — an error blanks the view body, so a
// first-time user would open the app to a wall of red rather than an empty
// state telling them what to run.
func TestFirstRun_EveryEntryPointIsSafeWithNoHydraHome(t *testing.T) {
	withEmptyHome(t)
	a := New()

	t.Run("dashboard", func(t *testing.T) {
		d, err := a.GetDashboard()
		if err != nil {
			t.Fatalf("GetDashboard errored on a fresh machine: %v", err)
		}
		if d.HasData {
			t.Error("HasData is true with no logs; the view would show a table of zeros instead of the empty state")
		}
	})

	t.Run("fleet", func(t *testing.T) {
		f, err := a.GetFleet()
		if err != nil {
			t.Fatalf("GetFleet errored on a fresh machine: %v", err)
		}
		if f.HasRuns {
			t.Error("HasRuns is true with no runs")
		}
		if f.Runs == nil {
			t.Error("Runs is nil, which marshals to null; types.ts declares it Run[]")
		}
		if f.GroupThreshold == 0 {
			t.Error("GroupThreshold is 0; the view uses it to decide when to collapse cards")
		}
	})

	t.Run("session", func(t *testing.T) {
		s, err := a.GetSession("")
		if err != nil {
			t.Fatalf("GetSession errored on a fresh machine: %v", err)
		}
		if s.Found {
			t.Error("Found is true for a run that does not exist")
		}
		if s.Timeline == nil || s.Agents == nil || s.Edges == nil {
			t.Error("a Session slice is nil; the view iterates all three")
		}
	})

	t.Run("code", func(t *testing.T) {
		e, err := a.GetEdits("")
		if err != nil {
			t.Fatalf("GetEdits errored on a fresh machine: %v", err)
		}
		if e == nil {
			t.Error("GetEdits returned nil rather than an empty slice")
		}
		d, err := a.GetDiff("", "", "")
		if err != nil {
			t.Fatalf("GetDiff errored on a fresh machine: %v", err)
		}
		if d.Reason == "" {
			t.Error("an unavailable diff must say why; a blank pane explains nothing")
		}
	})

	t.Run("chat enums", func(t *testing.T) {
		if len(a.ChatEnums()) == 0 {
			t.Error("the chat dock's routing selector would be empty")
		}
	})

	// Version is decoration, but it must not be blank — the footer renders it
	// directly and an empty string looks like a broken build.
	t.Run("version", func(t *testing.T) {
		v := a.GetVersion()
		if v.Version == "" {
			t.Error("Version is empty")
		}
	})

	// CheckHyctl reads $PATH, not ~/.hydra, so an empty home is unrelated to
	// it — but it is the very first call the app makes on a fresh machine
	// (#383), so it belongs in this contract too: it must return a value, not
	// panic, regardless of what is or is not installed.
	t.Run("hyctl status", func(t *testing.T) {
		_ = a.CheckHyctl()
	})
}

// The whole first-run payload must be marshallable and free of nulls where the
// frontend expects a list — Wails serialises these across the bridge, so a nil
// here is a runtime TypeError in the view, not a Go error anyone would see.
func TestFirstRun_PayloadHasNoNullsWhereTheViewExpectsAList(t *testing.T) {
	withEmptyHome(t)
	a := New()

	f, err := a.GetFleet()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"runs":null`) {
		t.Errorf("Fleet marshalled with runs:null on a fresh machine: %s", raw)
	}
}
