// SPDX-License-Identifier: MIT

package probe

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/provider"
)

// probe.Run is the machine scan every other surface is built on: hyctl probe,
// the init wizard, and dispatch's head selection. It was at 0%.

type fakeProvider struct {
	id      string
	heads   []provider.Head
	err     error
	panics  bool
	delay   time.Duration
	callsMu sync.Mutex
	calls   int
}

func (f *fakeProvider) ID() string { return f.id }

func (f *fakeProvider) Discover(ctx context.Context) ([]provider.Head, error) {
	f.callsMu.Lock()
	f.calls++
	f.callsMu.Unlock()

	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.panics {
		panic("provider exploded")
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.heads, nil
}

// head builds a distinct cloud head. The provider name matters: rank.ByCapScore
// dedupes non-local heads by Provider, deliberately, so a vendor with fifty
// models shows as one head, so fakes sharing a provider name collapse into one.
// The first version of these tests used Provider:"test" throughout and saw
// three heads become two, which is the dedup contract working, not a bug.
func head(id string, score int, local bool) provider.Head {
	return provider.Head{ID: id, Name: id, Provider: id, Source: "registry",
		CapScore: score, LocalOnly: local, AuthReady: true}
}

func TestRunWith_AggregatesEveryProvider(t *testing.T) {
	res := RunWith(context.Background(), []provider.Provider{
		&fakeProvider{id: "a", heads: []provider.Head{head("a1", 50, false)}},
		&fakeProvider{id: "b", heads: []provider.Head{head("b1", 90, false), head("b2", 10, true)}},
	})

	if len(res.Heads) != 3 {
		t.Fatalf("got %d heads, want 3: %+v", len(res.Heads), res.Heads)
	}
}

// The Cortex is the highest-ranked head. Getting it wrong sends every
// unpinned dispatch to the wrong place.
func TestRunWith_CortexIsTheTopRankedHead(t *testing.T) {
	res := RunWith(context.Background(), []provider.Provider{
		&fakeProvider{id: "a", heads: []provider.Head{head("weak", 10, false)}},
		&fakeProvider{id: "b", heads: []provider.Head{head("strong", 99, false)}},
		&fakeProvider{id: "c", heads: []provider.Head{head("mid", 55, false)}},
	})

	if res.Cortex == nil {
		t.Fatal("no Cortex chosen despite three heads")
	}
	if res.Cortex.ID != "strong" {
		t.Errorf("Cortex = %q, want the highest CapScore (%q)", res.Cortex.ID, "strong")
	}
	// Cortex must be the first ranked head, not a copy of something else.
	if res.Heads[0].ID != res.Cortex.ID {
		t.Errorf("Cortex %q is not Heads[0] (%q)", res.Cortex.ID, res.Heads[0].ID)
	}
	// …and the ranking must be descending.
	for i := 1; i < len(res.Heads); i++ {
		if res.Heads[i-1].CapScore < res.Heads[i].CapScore {
			t.Errorf("heads not ranked: %d before %d", res.Heads[i-1].CapScore, res.Heads[i].CapScore)
		}
	}
}

// The documented guarantee: one broken provider must not block the rest.
func TestRunWith_AFailingProviderDoesNotHideTheOthers(t *testing.T) {
	good := &fakeProvider{id: "good", heads: []provider.Head{head("g", 70, false)}}
	bad := &fakeProvider{id: "bad", err: errors.New("no network")}

	res := RunWith(context.Background(), []provider.Provider{bad, good})

	if len(res.Heads) != 1 || res.Heads[0].ID != "g" {
		t.Errorf("got %+v, want the one head from the working provider", res.Heads)
	}
	if bad.calls == 0 {
		t.Error("the failing provider was never called")
	}
}

// "Failures are silently skipped" has to cover a panic too, or the guarantee is
// only about errors a provider remembers to return. hyctl probe is often the
// first command a user runs; one bad provider must not crash it.
func TestRunWith_APanickingProviderDoesNotCrashTheProbe(t *testing.T) {
	res := RunWith(context.Background(), []provider.Provider{
		&fakeProvider{id: "boom", panics: true},
		&fakeProvider{id: "fine", heads: []provider.Head{head("f", 60, false)}},
	})

	if len(res.Heads) != 1 || res.Heads[0].ID != "f" {
		t.Errorf("got %+v, want the surviving provider's head", res.Heads)
	}
	if res.Cortex == nil || res.Cortex.ID != "f" {
		t.Errorf("Cortex = %+v, want the surviving head", res.Cortex)
	}
}

// Nothing discovered is a valid outcome, and must not be a nil-pointer trap for
// callers that read Cortex.
func TestRunWith_NoProvidersYieldsNoCortex(t *testing.T) {
	res := RunWith(context.Background(), nil)
	if res == nil {
		t.Fatal("RunWith returned nil")
	}
	if len(res.Heads) != 0 {
		t.Errorf("got %d heads from no providers", len(res.Heads))
	}
	if res.Cortex != nil {
		t.Errorf("Cortex = %+v with no heads", res.Cortex)
	}
}

func TestRunWith_ProvidersThatFindNothing(t *testing.T) {
	res := RunWith(context.Background(), []provider.Provider{
		&fakeProvider{id: "a"},
		&fakeProvider{id: "b", heads: []provider.Head{}},
	})
	if len(res.Heads) != 0 || res.Cortex != nil {
		t.Errorf("got %+v / cortex %+v, want nothing", res.Heads, res.Cortex)
	}
}

// Providers are queried concurrently; the shared slice must be race-free.
// Run under -race this is the assertion.
func TestRunWith_ConcurrentProvidersAreRaceFree(t *testing.T) {
	var ps []provider.Provider
	for i := 0; i < 24; i++ {
		id := "p" + strconv.Itoa(i)
		ps = append(ps, &fakeProvider{
			id: id,
			heads: []provider.Head{
				head(id+"-a", 50+i, false),
				head(id+"-b", 40+i, false),
			},
		})
	}
	res := RunWith(context.Background(), ps)
	if len(res.Heads) != 48 {
		t.Errorf("got %d heads, want 48", len(res.Heads))
	}
}

// The dedup contract itself, asserted rather than stumbled into: several heads
// from one cloud provider collapse to the strongest, while local heads each
// stay, a machine with four Ollama models has four usable heads, but a vendor
// with forty API models is still one entry.
func TestRunWith_CloudHeadsDedupePerProviderButLocalOnesDoNot(t *testing.T) {
	cloud := []provider.Head{
		{ID: "openai/gpt-a", Provider: "openai", Source: "env", CapScore: 70, AuthReady: true},
		{ID: "openai/gpt-b", Provider: "openai", Source: "env", CapScore: 90, AuthReady: true},
		{ID: "openai/gpt-c", Provider: "openai", Source: "env", CapScore: 50, AuthReady: true},
	}
	local := []provider.Head{
		{ID: "ollama/qwen", Provider: "local", Source: "port", CapScore: 60, LocalOnly: true, AuthReady: true},
		{ID: "ollama/llama", Provider: "local", Source: "port", CapScore: 55, LocalOnly: true, AuthReady: true},
	}

	res := RunWith(context.Background(), []provider.Provider{
		&fakeProvider{id: "cloud", heads: cloud},
		&fakeProvider{id: "local", heads: local},
	})

	var gotCloud, gotLocal int
	for _, h := range res.Heads {
		if h.LocalOnly {
			gotLocal++
			continue
		}
		gotCloud++
		if h.CapScore != 90 {
			t.Errorf("kept the %d-score openai head; dedup must keep the strongest", h.CapScore)
		}
	}
	if gotCloud != 1 {
		t.Errorf("got %d cloud heads, want 1, one entry per cloud provider", gotCloud)
	}
	if gotLocal != 2 {
		t.Errorf("got %d local heads, want 2, each local model is its own head", gotLocal)
	}
}

// A cancelled context must not be ignored: a provider that honours it returns
// promptly and the probe finishes rather than waiting out every timeout.
func TestRunWith_CancelledContextReturnsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	res := RunWith(ctx, []provider.Provider{
		&fakeProvider{id: "slow", delay: 10 * time.Second,
			heads: []provider.Head{head("never", 10, false)}},
	})
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Errorf("took %v with a cancelled context", elapsed)
	}
	if len(res.Heads) != 0 {
		t.Errorf("a cancelled probe still produced heads: %+v", res.Heads)
	}
}

// Every provider is asked exactly once, a double call would double-count heads
// and, for the port provider, double the network probes.
func TestRunWith_EachProviderIsAskedOnce(t *testing.T) {
	p := &fakeProvider{id: "once", heads: []provider.Head{head("h", 50, false)}}
	RunWith(context.Background(), []provider.Provider{p})

	if p.calls != 1 {
		t.Errorf("provider called %d times, want 1", p.calls)
	}
}

// A provider failure (e.g. cli/env/port's capabilities.Load choking on a
// corrupted ~/.hydra/models.json overlay) must stay non-fatal, the documented
// contract every other test in this file exercises, but silently dropping it
// with no trace at all contradicts hyctl probe's own "✗ marks unroutable heads
// with the reason" promise (#248): this provider's heads never even reached
// that accounting. Warnings is the visible signal (#505).
func TestRunWith_AFailingProviderIsRecordedAsAWarning(t *testing.T) {
	good := &fakeProvider{id: "good", heads: []provider.Head{head("g", 70, false)}}
	bad := &fakeProvider{id: "cli", err: errors.New("corrupted models.json overlay")}

	res := RunWith(context.Background(), []provider.Provider{bad, good})

	if len(res.Heads) != 1 || res.Heads[0].ID != "g" {
		t.Fatalf("got %+v, want the one head from the working provider", res.Heads)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("got %d warnings, want 1: %+v", len(res.Warnings), res.Warnings)
	}
	if !strings.Contains(res.Warnings[0], "cli") || !strings.Contains(res.Warnings[0], "corrupted models.json overlay") {
		t.Errorf("warning = %q, want it to name the provider and the error", res.Warnings[0])
	}
}

// A provider that returns no error must not manufacture a warning, Warnings
// is specifically about failures, not an audit log of every provider run.
func TestRunWith_NoWarningsWhenEveryProviderSucceeds(t *testing.T) {
	res := RunWith(context.Background(), []provider.Provider{
		&fakeProvider{id: "a", heads: []provider.Head{head("a1", 50, false)}},
		&fakeProvider{id: "b"},
	})
	if len(res.Warnings) != 0 {
		t.Errorf("got warnings %+v with no failing provider", res.Warnings)
	}
}

// Warnings must be in a stable order despite concurrent providers, or a probe
// run against the same broken machine twice would print differently each time.
func TestRunWith_WarningsAreDeterministicallyOrdered(t *testing.T) {
	res := RunWith(context.Background(), []provider.Provider{
		&fakeProvider{id: "zzz", err: errors.New("boom")},
		&fakeProvider{id: "aaa", err: errors.New("boom")},
		&fakeProvider{id: "mmm", err: errors.New("boom")},
	})
	if len(res.Warnings) != 3 {
		t.Fatalf("got %d warnings, want 3: %+v", len(res.Warnings), res.Warnings)
	}
	if !sort.StringsAreSorted(res.Warnings) {
		t.Errorf("warnings not sorted: %+v", res.Warnings)
	}
}
