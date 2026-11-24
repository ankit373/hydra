// SPDX-License-Identifier: MIT

package idle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/sysinfo"
)

func tick(t *testing.T, r *tickRig) Result {
	t.Helper()
	res, err := NewScheduler(r.env).Tick(context.Background(), r.now)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestTick_SkipsAndExplainsWhatIsNotReady(t *testing.T) {
	r := newRig(t, newTickHead(goodState))
	r.env.Seen = func() ([]Seen, error) {
		return []Seen{
			{Key: "busy", LastEvent: r.now.Add(-time.Second), Head: stubID},
			{Key: "nostore", LastEvent: r.now.Add(-time.Hour), Head: stubID},
		}, nil
	}
	r.env.Transcript = func(key string) (Turns, error) { return Turns{}, ErrNoOriginals }
	if res := tick(t, r); res.Outcome != OutcomeNone || res.Reason != "nothing to compact" {
		t.Fatalf("result %+v", res)
	}
	reasons := map[string]string{}
	sums, _ := Summaries(r.env.Dir)
	for _, s := range sums {
		reasons[s.Key] = s.Reason
	}
	if !strings.HasPrefix(reasons["busy"], "active ") || !strings.Contains(reasons["nostore"], "not stored") {
		t.Fatalf("reasons %v, want the activity and the missing originals named", reasons)
	}

	r = newRig(t, newTickHead(goodState))
	r.env.Machine = func() Machine { return Machine{PowerKnown: true, OnAC: false, Pressure: sysinfo.PressureLow} }
	if res := tick(t, r); res.Outcome != OutcomeNone {
		t.Fatalf("on battery: %+v", res)
	}
	if st := statusOf(r.env.Dir, "conv1"); st.Reason != "on battery" {
		t.Fatalf("status %+v, want on battery", st)
	}
	r.env.Seen = func() ([]Seen, error) { return nil, errors.New("run log unreadable") }
	if _, err := NewScheduler(r.env).Tick(context.Background(), r.now); err == nil {
		t.Fatal("a run log that cannot be read was ignored")
	}
}

func TestTick_AStagedPackIsNotRebuiltUntilThereIsNewText(t *testing.T) {
	r := newRig(t, newTickHead(goodState))
	if res := tick(t, r); res.Outcome != OutcomeStaged {
		t.Fatalf("first tick %+v", res)
	}
	res := tick(t, r)
	if res.Outcome != OutcomeNone {
		t.Fatalf("second tick %+v, want the covered turns not to count as new", res)
	}
	if st := statusOf(r.env.Dir, "conv1"); !strings.Contains(st.Reason, "new tokens") {
		t.Fatalf("status %+v", st)
	}
}

func TestTick_JobEndsAtItsLimitOrWhenStopped(t *testing.T) {
	hang := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	h := newTickHead(goodState)
	h.block = hang
	r := newRig(t, h)
	r.env.MaxJob = 20 * time.Millisecond
	if res := tick(t, r); res.Outcome != OutcomeCancelled || !strings.HasPrefix(res.Reason, "ran past the ") {
		t.Fatalf("result %+v, want the job limit", res)
	}

	h = newTickHead(goodState)
	r = newRig(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	h.block = func(c context.Context) error { cancel(); <-c.Done(); return c.Err() }
	res, err := NewScheduler(r.env).Tick(ctx, r.now)
	if err != nil || res.Outcome != OutcomeCancelled || res.Reason != "stopped" {
		t.Fatalf("result %+v, %v; want stopped", res, err)
	}
}

func TestTick_RejectsAnUnreadableReplyAndAFailedPin(t *testing.T) {
	r := newRig(t, newTickHead("I could not summarise this."))
	if res := tick(t, r); res.Outcome != OutcomeRejected || !strings.Contains(res.Reason, "not JSON") {
		t.Fatalf("result %+v, want the reply refused as not JSON", res)
	}
	if !fileExists(t, filepath.Join(r.env.Dir, "conv1", "pack-1.rejected.json")) {
		t.Fatal("the failed build was not kept for the replay")
	}

	r = newRig(t, newTickHead(goodState))
	r.env.Store = brokenPins{}
	res := tick(t, r)
	if res.Outcome != OutcomeRejected || !strings.Contains(res.Reason, "not staged") {
		t.Fatalf("result %+v, want a pack that could not be pinned not to be staged", res)
	}
	if fileExists(t, filepath.Join(r.env.Dir, "conv1", "pack-1.json")) {
		t.Fatal("a pack whose originals could not be pinned was staged")
	}
}

type brokenPins struct{}

func (brokenPins) Pin(string, []string) error { return errors.New("disk full") }
func (brokenPins) Has(string) bool            { return true }

func TestTick_UnsafeKeysGetAHashedDirectory(t *testing.T) {
	r := newRig(t, newTickHead(goodState))
	key := "../escape/key with spaces"
	r.env.Seen = func() ([]Seen, error) { return []Seen{{Key: key, LastEvent: r.now.Add(-time.Hour), Head: stubID}}, nil }
	if res := tick(t, r); res.Outcome != OutcomeStaged {
		t.Fatalf("result %+v", res)
	}
	entries, _ := os.ReadDir(r.env.Dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 || !strings.HasPrefix(names[0], "h-") && !strings.HasPrefix(names[1], "h-") {
		t.Fatalf("directory entries %v, want one hashed directory and no escape", names)
	}
}

func TestSummaries_SkipsWhatIsNotAConversation(t *testing.T) {
	dir := t.TempDir()
	if got, err := Summaries(filepath.Join(dir, "none")); err != nil || got != nil {
		t.Fatalf("missing dir: %v, %v", got, err)
	}
	for name, body := range map[string]string{"a/status.json": "not json", "b/other.txt": "x"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "loose.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Summaries(dir); err != nil || len(got) != 0 {
		t.Fatalf("summaries %v, %v; want none", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "pack-1.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LatestPack(dir, "a"); err == nil {
		t.Fatal("a corrupt pack read as fine")
	}
	if p, n, err := LatestPack(dir, "b"); p != nil || n != 0 || err != nil {
		t.Fatalf("no staged pack: %v %d %v", p, n, err)
	}
	if err := writeJSON(filepath.Join(dir, "loose.txt", "x.json"), 1); err == nil {
		t.Fatal("a file where a directory belongs was written through")
	}
}

func TestNewScheduler_FillsDefaults(t *testing.T) {
	s := NewScheduler(Env{})
	if s.env.MaxJob != DefaultMaxJob || s.env.MaxNight != DefaultMaxNight || s.env.Poll != 2*time.Second {
		t.Fatalf("defaults %v %v %v", s.env.MaxJob, s.env.MaxNight, s.env.Poll)
	}
}

func TestTick_UnknownHeadGetsTheDefaultWindow(t *testing.T) {
	r := newRig(t, newTickHead(goodState))
	r.env.Seen = func() ([]Seen, error) {
		return []Seen{{Key: "conv1", LastEvent: r.now.Add(-time.Hour), Head: "other"}}, nil
	}
	if res := tick(t, r); res.Outcome != OutcomeNone {
		t.Fatalf("result %+v, want a small conversation in a 200000-token window to wait", res)
	}
	if st := statusOf(r.env.Dir, "conv1"); !strings.Contains(st.Reason, "% full") {
		t.Fatalf("status %+v", st)
	}
}

// The second pack builds on the first: only the new turns are summarised.
func TestTick_SecondPackBuildsOnTheFirst(t *testing.T) {
	h := newTickHead(goodState)
	r := newRig(t, h)
	r.env.Log = nil
	if res := tick(t, r); res.Outcome != OutcomeStaged || res.Pack != 1 {
		t.Fatalf("first tick %+v", res)
	}
	first, _ := r.env.Transcript("conv1")
	grown := Turns{Turns: append([]Turn{}, first.Turns...), Files: []File{{Path: "refunds.csv", Action: FileEdited, Ref: "e1"}}}
	grown.Turns = append(grown.Turns,
		Turn{Role: "user", Text: strings.Repeat("Please also check the ledger again. ", 40), Ref: "u2", Seq: 4},
		Turn{Role: "assistant", Text: strings.Repeat("The ledger line was checked again. ", 40), Ref: "a2", Seq: 5})
	r.env.Transcript = func(string) (Turns, error) { return grown, nil }
	res := tick(t, r)
	if res.Outcome != OutcomeStaged || res.Pack != 2 {
		t.Fatalf("second tick %+v, want pack 2", res)
	}
	if pack, n, _ := LatestPack(r.env.Dir, "conv1"); n != 2 || pack.Covers.To != 6 || len(pack.Notes) != 6 {
		t.Fatalf("pack %d covers %d with %d notes, want 2, 6, 6", n, pack.Covers.To, len(pack.Notes))
	}
	if refs := r.pins.pinned[PinOwner("conv1")]; !strings.Contains(strings.Join(refs, ","), "e1") {
		t.Fatalf("pinned %v, want the file's ref too", refs)
	}
}

func TestTick_StoppedWhileProbing(t *testing.T) {
	h := newTickHead(goodState)
	r := newRig(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	h.block = func(c context.Context) error {
		if calls++; calls == 8 {
			cancel()
		}
		return c.Err()
	}
	res, err := NewScheduler(r.env).Tick(ctx, r.now)
	if err != nil || res.Outcome != OutcomeCancelled || res.Reason != "stopped" || calls < 8 {
		t.Fatalf("result %+v after %d asks, %v; want stopped while probing", res, calls, err)
	}
}

func TestTick_UnwritableDirectoryStillReports(t *testing.T) {
	r := newRig(t, newTickHead(inventedState))
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r.env.Dir = filepath.Join(blocker, "idle")
	if res := tick(t, r); res.Outcome != OutcomeRejected || !strings.Contains(res.Reason, "not saved") {
		t.Fatalf("result %+v, want the rejection reported even though it could not be kept", res)
	}
	if _, err := Summaries(r.env.Dir); err == nil {
		t.Fatal("a directory that is a file read as empty")
	}
}

func TestPackFiles_ReadAndWriteErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "c", "pack-1.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LatestPack(dir, "c"); err == nil {
		t.Fatal("a pack that cannot be read read as fine")
	}
	if err := writeJSON(filepath.Join(dir, "x.json"), make(chan int)); err == nil {
		t.Fatal("a value JSON cannot hold was written")
	}
	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(ro, 0o700)
	if os.Geteuid() != 0 {
		if err := writeJSON(filepath.Join(ro, "x.json"), 1); err == nil {
			t.Fatal("a read-only directory was written to")
		}
	}
}

func TestPackID_NamesTheDirectoryAndNumber(t *testing.T) {
	if id := PackID("support-42", 3); id != "support-42/3" {
		t.Fatalf("pack id %q", id)
	}
	if id := PackID("../escape/key with spaces", 2); !strings.HasPrefix(id, "h-") || !strings.HasSuffix(id, "/2") || strings.Contains(id, "..") {
		t.Fatalf("pack id %q, want a hashed directory", id)
	}
}
