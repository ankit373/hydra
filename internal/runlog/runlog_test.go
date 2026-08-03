// SPDX-License-Identifier: MIT

package runlog

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

// tempHome points config.Dir() at a scratch directory.
func tempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestAppendLoad_RoundTrip(t *testing.T) {
	tempHome(t)
	l := New("run-1")

	in := []Event{
		{Kind: KindRunStarted, Detail: "hyctl dispatch"},
		{Kind: KindHeadSelected, TaskID: "t1", Head: "h1", Model: "M", Tier: 3},
		{Kind: KindDispatchFinished, TaskID: "t1", Status: "ok", CostUSD: 0.012, DurationMS: 1500},
	}
	for _, e := range in {
		if err := l.Append(e); err != nil {
			t.Fatal(err)
		}
	}

	got, err := Load("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(in) {
		t.Fatalf("loaded %d events, want %d", len(got), len(in))
	}
	for i := range got {
		if got[i].Kind != in[i].Kind {
			t.Errorf("event %d kind = %q, want %q (append order must be preserved)", i, got[i].Kind, in[i].Kind)
		}
		if got[i].V != SchemaVersion {
			t.Errorf("event %d schema version = %d, want %d", i, got[i].V, SchemaVersion)
		}
		if got[i].RunID != "run-1" {
			t.Errorf("event %d run_id = %q, want run-1", i, got[i].RunID)
		}
		if got[i].TS == "" {
			t.Errorf("event %d has no timestamp", i)
		}
	}
	// Field fidelity on the richest event.
	if got[2].CostUSD != 0.012 || got[2].DurationMS != 1500 || got[2].Status != "ok" {
		t.Errorf("event 2 round-trip lost data: %+v", got[2])
	}
}

func TestAppend_SeqIsMonotonic(t *testing.T) {
	tempHome(t)
	l := New("run-seq")
	for i := 0; i < 20; i++ {
		if err := l.Append(Event{Kind: KindAttempt}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Load("run-seq")
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range got {
		if e.Seq != uint64(i+1) {
			t.Errorf("event %d seq = %d, want %d", i, e.Seq, i+1)
		}
	}
}

// The design's load-bearing assumption: concurrent appends are atomic under
// O_APPEND, so N goroutines produce N intact, parseable lines with no mutex.
// If this fails, the whole no-lock premise is wrong.
func TestAppend_ConcurrentWritersDoNotTear(t *testing.T) {
	tempHome(t)
	l := New("run-concurrent")

	const goroutines, perGoroutine = 16, 25
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				if err := l.Append(Event{
					Kind:   KindAttempt,
					Agent:  fmt.Sprintf("g%d", g),
					Detail: strings.Repeat("x", 200), // non-trivial line length
				}); err != nil {
					t.Errorf("append: %v", err)
				}
			}
		}(g)
	}
	wg.Wait()

	want := goroutines * perGoroutine
	events, skipped, err := LoadCounted(Path("run-concurrent"))
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Errorf("%d lines were unparseable — concurrent appends tore", skipped)
	}
	if len(events) != want {
		t.Fatalf("loaded %d events, want %d — lines were lost or merged", len(events), want)
	}
	// Every sequence number must appear exactly once.
	seen := map[uint64]bool{}
	for _, e := range events {
		if seen[e.Seq] {
			t.Errorf("duplicate seq %d", e.Seq)
		}
		seen[e.Seq] = true
	}
	if len(seen) != want {
		t.Errorf("%d distinct seqs, want %d", len(seen), want)
	}
}

// A crash mid-write leaves a truncated tail. Silently dropping it would render
// an incomplete run as a complete one.
func TestLoadCounted_ReportsTruncatedTail(t *testing.T) {
	tempHome(t)
	l := New("run-trunc")
	for i := 0; i < 3; i++ {
		if err := l.Append(Event{Kind: KindAttempt}); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate a crash mid-append.
	f, err := os.OpenFile(Path("run-trunc"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"v":1,"seq":4,"kind":"att`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	events, skipped, err := LoadCounted(Path("run-trunc"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Errorf("parsed %d events, want the 3 intact ones", len(events))
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 — a discarded record must be reported", skipped)
	}
}

// Runs that never logged must not error, and must not create files.
func TestLoad_MissingRunIsEmpty(t *testing.T) {
	tempHome(t)
	events, err := Load("never-existed")
	if err != nil {
		t.Fatalf("missing run should not error: %v", err)
	}
	if events != nil {
		t.Errorf("missing run yielded %d events, want none", len(events))
	}
	if _, err := os.Stat(Path("never-existed")); !os.IsNotExist(err) {
		t.Error("Load created a file for a run that never logged")
	}
}

func TestNew_DoesNotCreateFileUntilAppend(t *testing.T) {
	tempHome(t)
	l := New("lazy")
	if _, err := os.Stat(Path("lazy")); !os.IsNotExist(err) {
		t.Fatal("New created the file eagerly")
	}
	if err := l.Append(Event{Kind: KindRunStarted}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Path("lazy")); err != nil {
		t.Errorf("file missing after Append: %v", err)
	}
}

// Per-run files are the retention story: one run's events never mix with another's.
func TestRuns_ListsNewestFirstAndIsolatesRuns(t *testing.T) {
	tempHome(t)
	for _, id := range []string{"20260801T100000Z-aaa", "20260801T110000Z-bbb", "20260801T120000Z-ccc"} {
		if err := New(id).Append(Event{Kind: KindRunStarted, Detail: id}); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := Runs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 {
		t.Fatalf("Runs() = %d ids, want 3", len(ids))
	}
	if ids[0] != "20260801T120000Z-ccc" {
		t.Errorf("Runs()[0] = %q, want the newest run first", ids[0])
	}
	// Each run's file holds only its own events.
	for _, id := range ids {
		events, err := Load(id)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 || events[0].Detail != id {
			t.Errorf("run %s leaked events from another run: %+v", id, events)
		}
	}
}

func TestRuns_NoDirectoryIsEmpty(t *testing.T) {
	tempHome(t)
	ids, err := Runs()
	if err != nil {
		t.Fatalf("missing runs dir should not error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("got %d ids, want none", len(ids))
	}
}

// Schema version must be on the wire, not just the struct — the desktop app
// will read these bytes without importing this package.
func TestAppend_SchemaVersionIsSerialized(t *testing.T) {
	tempHome(t)
	if err := New("run-schema").Append(Event{Kind: KindRunStarted}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(Path("run-schema"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &m); err != nil {
		t.Fatal(err)
	}
	if v, ok := m["v"]; !ok || int(v.(float64)) != SchemaVersion {
		t.Errorf("wire format missing schema version: %v", m)
	}
	if _, ok := m["seq"]; !ok {
		t.Error("wire format missing seq")
	}
}
