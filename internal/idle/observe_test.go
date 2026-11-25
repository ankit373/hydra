// SPDX-License-Identifier: MIT

package idle

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/runlog"
)

func writeRun(t *testing.T, id string, events ...runlog.Event) {
	t.Helper()
	l := runlog.New(id)
	for _, e := range events {
		if err := l.Append(e); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunLogSeen_ListsConversationsByNewestEvent(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	now := time.Now().UTC()
	ts := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339Nano) }
	started := func(conv, at string) runlog.Event {
		return runlog.Event{Kind: runlog.KindTaskStarted, TS: at, Meta: map[string]any{runlog.MetaConversation: conv}}
	}
	if got, err := RunLogSeen(now); err != nil || len(got) != 0 {
		t.Fatalf("no runs dir: %v, %v", got, err)
	}
	writeRun(t, "20261124-a", started("c1", ts(2*time.Hour)), runlog.Event{Kind: runlog.KindDispatchFinished, TS: ts(2 * time.Hour), Head: "ollama/old"})
	writeRun(t, "20261124-b", started("c1", ts(time.Hour)), runlog.Event{Kind: runlog.KindDispatchFinished, TS: ts(time.Hour), Head: "ollama/new"})
	writeRun(t, "20261124-c", runlog.Event{Kind: runlog.KindDispatchFinished, TS: ts(time.Minute), Head: "ollama/x"})
	writeRun(t, RunPrefix+"20261124", started("idle-conv", ts(time.Minute)))
	writeRun(t, "20261123-old", started("old", ts(48*time.Hour)))
	if err := os.Chtimes(runlog.Path("20261123-old"), now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := RunLogSeen(now)
	if err != nil || len(got) != 1 {
		t.Fatalf("seen %+v, %v; want only c1 (no key, idle's own run and a run past the lookback are left out)", got, err)
	}
	if got[0].Key != "c1" || got[0].Head != "ollama/new" || got[0].LastEvent.Before(now.Add(-61*time.Minute)) || got[0].LastEvent.After(now.Add(-59*time.Minute)) {
		t.Fatalf("seen %+v, want c1 at its newest event, answered by ollama/new", got[0])
	}
}

func TestRunLogActivity_IgnoresIdleRuns(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	if at, err := RunLogActivity(); err != nil || !at.IsZero() {
		t.Fatalf("no runs: %v, %v", at, err)
	}
	writeRun(t, "20261124-a", runlog.Event{Kind: runlog.KindTaskStarted})
	user := time.Date(2026, 11, 24, 3, 0, 0, 0, time.UTC)
	if err := os.Chtimes(runlog.Path("20261124-a"), user, user); err != nil {
		t.Fatal(err)
	}
	writeRun(t, RunPrefix+"20261124", runlog.Event{Kind: runlog.KindIdleStarted})
	at, err := RunLogActivity()
	if err != nil || !at.Equal(user) {
		t.Fatalf("activity %v, %v; want the user's run, not idle's own newer one", at, err)
	}
}

// Permission and path semantics differ on Windows, so the error paths are
// asserted where a directory read of a file and a mode bit are enforced.
func TestRunLog_UnreadableRunsAreReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		return
	}
	home := t.TempDir()
	t.Setenv("HYDRA_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runlog.Dir(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RunLogSeen(time.Now()); err == nil {
		t.Fatal("a runs directory that is a file read as empty")
	}
	if _, err := RunLogActivity(); err == nil {
		t.Fatal("a runs directory that is a file read as empty")
	}
	if err := os.Remove(runlog.Dir()); err != nil {
		t.Fatal(err)
	}
	writeRun(t, "20261124-a", runlog.Event{Kind: runlog.KindTaskStarted})
	if os.Geteuid() != 0 {
		if err := os.Chmod(runlog.Path("20261124-a"), 0); err != nil {
			t.Fatal(err)
		}
		if _, err := RunLogSeen(time.Now()); err == nil {
			t.Fatal("a run file that cannot be read was skipped")
		}
	}
}
