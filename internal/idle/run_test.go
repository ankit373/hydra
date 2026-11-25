// SPDX-License-Identifier: MIT

package idle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/sysinfo"
)

const stubID = "ollama/stub"

// tickHead writes notes and state for a build and answers probes with the
// lookup head. state is the JSON it replies to the state prompt; block, when
// set, is waited on inside the first call.
type tickHead struct {
	*lookupHead
	state   string
	started func()
	block   func(ctx context.Context) error
	once    sync.Once
}

func (h *tickHead) Head() provider.Head {
	return provider.Head{ID: stubID, LocalOnly: true, AuthReady: true, Meta: map[string]string{"model_ctx": "100000"}}
}

func (h *tickHead) Ask(ctx context.Context, prompt string) (string, error) {
	if h.started != nil {
		h.once.Do(h.started)
	}
	if h.block != nil {
		if err := h.block(ctx); err != nil {
			return "", err
		}
	}
	switch {
	case strings.HasPrefix(prompt, notePrompt):
		return "a short note", nil
	case strings.HasPrefix(prompt, statePrompt):
		return h.state, nil
	}
	return h.lookupHead.Ask(ctx, prompt)
}

const goodState = `{"goal": "refund the double charge",
 "decisions": [{"what": "refund one charge", "why": "both lines are the same invoice", "ref": "a1"}],
 "next": ["email the customer"]}`

const inventedState = `{"goal": "refund the double charge",
 "facts": [{"text": "the account has 9999 users", "ref": "u1"}],
 "decisions": [{"what": "refund one charge", "why": "both lines are the same invoice", "ref": "a1"}]}`

type fakePins struct {
	mu     sync.Mutex
	pinned map[string][]string
}

func (f *fakePins) Pin(owner string, refs []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pinned[owner] = refs
	return nil
}
func (f *fakePins) Has(string) bool { return true }

type tickRig struct {
	env    Env
	pins   *fakePins
	events []runlog.Kind
	now    time.Time
	active atomic.Bool
}

func newRig(t *testing.T, h *tickHead) *tickRig {
	t.Helper()
	r := &tickRig{pins: &fakePins{pinned: map[string][]string{}}, now: time.Date(2026, 11, 24, 3, 0, 0, 0, time.UTC)}
	turns := probeTurns()
	turns.Turns = append(turns.Turns, Turn{Role: "tool", Text: strings.Repeat("ledger line without digits. ", 80), Ref: "t1", Seq: 3})
	var mu sync.Mutex
	r.env = Env{
		Dir:    t.TempDir(),
		Config: Config{IdleAfter: time.Minute, MinSpanTokens: 100},
		Poll:   time.Millisecond,
		Seen: func() ([]Seen, error) {
			return []Seen{{Key: "conv1", LastEvent: r.now.Add(-time.Hour), Head: stubID}}, nil
		},
		Activity: func() (time.Time, error) {
			return r.now.Add(map[bool]time.Duration{true: time.Second}[r.active.Load()]), nil
		},
		Heads:   func() []provider.Head { return []provider.Head{h.Head()} },
		Windows: func([]provider.Head) map[string]int { return map[string]int{stubID: 100000} },
		Machine: func() Machine {
			return Machine{OnAC: true, PowerKnown: true, Load: 0.1, LoadKnown: true, CPUs: 8, Pressure: sysinfo.PressureLow}
		},
		Transcript:  func(string) (Turns, error) { return turns, nil },
		NewHead:     func(provider.Head) Head { return h },
		Store:       r.pins,
		Log:         func(e runlog.Event) { mu.Lock(); r.events = append(r.events, e.Kind); mu.Unlock() },
		ConvWindows: map[string]int{stubID: 1000},
	}
	return r
}

func newTickHead(state string) *tickHead {
	return &tickHead{lookupHead: newLookupHead(), state: state}
}

func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

func TestTick_StagesVerifiedPackAndPins(t *testing.T) {
	r := newRig(t, newTickHead(goodState))
	res, err := NewScheduler(r.env).Tick(context.Background(), r.now)
	if err != nil || res.Outcome != OutcomeStaged || res.Pack != 1 {
		t.Fatalf("result %+v, err %v; want a staged pack 1", res, err)
	}
	pack, n, err := LatestPack(r.env.Dir, "conv1")
	if err != nil || n != 1 || pack.State.Goal != "refund the double charge" {
		t.Fatalf("latest pack %v %d %v", pack, n, err)
	}
	if got := r.pins.pinned[PinOwner("conv1")]; len(got) == 0 || got[0] != "a1" {
		t.Fatalf("pinned %v, want the originals the pack stands on", got)
	}
	want := []runlog.Kind{runlog.KindIdleStarted, runlog.KindIdleVerified, runlog.KindIdleStaged}
	if strings.Join(kinds(r.events), ",") != strings.Join(kinds(want), ",") {
		t.Fatalf("events %v, want %v", r.events, want)
	}
	sums, _ := Summaries(r.env.Dir)
	if len(sums) != 1 || sums[0].Key != "conv1" || sums[0].Pack != 1 || sums[0].Verdict == nil || !sums[0].Verdict.Pass {
		t.Fatalf("summaries %+v", sums)
	}
}

func kinds(ks []runlog.Kind) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = string(k)
	}
	return out
}

func TestTick_RejectedPackIsNotStaged(t *testing.T) {
	r := newRig(t, newTickHead(inventedState))
	res, err := NewScheduler(r.env).Tick(context.Background(), r.now)
	if err != nil || res.Outcome != OutcomeRejected || !strings.Contains(res.Reason, "9999") {
		t.Fatalf("result %+v, err %v; want a rejection naming 9999", res, err)
	}
	dir := filepath.Join(r.env.Dir, "conv1")
	if fileExists(t, filepath.Join(dir, "pack-1.json")) || !fileExists(t, filepath.Join(dir, "pack-1.rejected.json")) {
		t.Fatal("a rejected pack must be kept beside the staged ones, never staged")
	}
	if len(r.pins.pinned) != 0 {
		t.Fatalf("pinned %v for a rejected pack", r.pins.pinned)
	}
	if last := r.events[len(r.events)-1]; last != runlog.KindIdleRejected {
		t.Fatalf("last event %s, want idle_rejected", last)
	}
	if st := statusOf(r.env.Dir, "conv1"); st.Outcome != OutcomeRejected || !strings.Contains(st.Reason, "9999") {
		t.Fatalf("status %+v", st)
	}
	// The same conversation, unchanged, is not rebuilt and rejected again.
	s := NewScheduler(r.env)
	if res, _ := s.Tick(context.Background(), r.now); res.Outcome != OutcomeNone {
		t.Fatalf("second tick %+v, want it to wait for the conversation to move", res)
	}
}

// statusOf reads a conversation's status as `hyctl idle status` does.
func statusOf(dir, conv string) Status {
	sums, _ := Summaries(dir)
	for _, s := range sums {
		if s.Key == conv {
			return s.Status
		}
	}
	return Status{}
}

func TestTick_NewRequestCancelsJob(t *testing.T) {
	h := newTickHead(goodState)
	r := newRig(t, h)
	h.started = func() { r.active.Store(true) }
	h.block = func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
			return nil
		}
	}
	res, err := NewScheduler(r.env).Tick(context.Background(), r.now)
	if err != nil || res.Outcome != OutcomeCancelled || res.Reason != "a request arrived" {
		t.Fatalf("result %+v, err %v; want cancelled by a request", res, err)
	}
	entries, _ := os.ReadDir(filepath.Join(r.env.Dir, "conv1"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "pack-") {
			t.Fatalf("%s written by a cancelled job", e.Name())
		}
	}
	if len(r.pins.pinned) != 0 {
		t.Fatalf("pinned %v by a cancelled job", r.pins.pinned)
	}
}

func TestTick_OneJobAtATime(t *testing.T) {
	h := newTickHead(goodState)
	r := newRig(t, h)
	running, release := make(chan struct{}), make(chan struct{})
	h.started = func() { close(running) }
	h.block = func(ctx context.Context) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s := NewScheduler(r.env)
	done := make(chan Result)
	go func() { res, _ := s.Tick(context.Background(), r.now); done <- res }()
	<-running
	if res, _ := s.Tick(context.Background(), r.now); res.Outcome != OutcomeNone || res.Reason != "a job is already running" {
		t.Fatalf("second tick %+v, want it to wait", res)
	}
	close(release)
	if res := <-done; res.Outcome != OutcomeStaged {
		t.Fatalf("first tick %+v, want a staged pack", res)
	}
}

func TestTick_NightCapStopsWork(t *testing.T) {
	r := newRig(t, newTickHead(goodState))
	r.env.MaxNight = time.Second
	s := NewScheduler(r.env)
	if err := writeJSON(s.spentPath(), spent{r.now.Format("2006-01-02"), 5}); err != nil {
		t.Fatal(err)
	}
	if res, _ := s.Tick(context.Background(), r.now); res.Outcome != OutcomeNone || !strings.Contains(res.Reason, "one night") {
		t.Fatalf("result %+v, want the night cap to refuse", res)
	}
}
