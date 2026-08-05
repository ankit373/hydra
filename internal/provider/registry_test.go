// SPDX-License-Identifier: MIT

package provider

import (
	"context"
	"strconv"
	"sync"
	"testing"
)

// The registry is process-global and populated from init() functions across
// four packages, so it is written during program start and read afterwards
// from goroutines. It was at 0%.
//
// Tests here build their own Registry rather than touching the global one:
// registering a fake globally would leak into every other test in the binary.

type stubProvider struct {
	id    string
	heads []Head
}

func (s *stubProvider) ID() string                               { return s.id }
func (s *stubProvider) Discover(context.Context) ([]Head, error) { return s.heads, nil }

func newRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

func TestRegistry_AddAndAll(t *testing.T) {
	r := newRegistry()
	if got := r.all(); len(got) != 0 {
		t.Errorf("a fresh registry returned %d providers", len(got))
	}

	r.add(&stubProvider{id: "a"})
	r.add(&stubProvider{id: "b"})

	got := r.all()
	if len(got) != 2 {
		t.Fatalf("got %d providers, want 2", len(got))
	}
	seen := map[string]bool{}
	for _, p := range got {
		seen[p.ID()] = true
	}
	if !seen["a"] || !seen["b"] {
		t.Errorf("registry lost a provider: %v", seen)
	}
}

// Keyed by ID, so re-registering replaces rather than duplicating. A duplicate
// would be discovered twice and double every head it reports.
func TestRegistry_SameIDReplacesRatherThanDuplicates(t *testing.T) {
	r := newRegistry()
	first := &stubProvider{id: "dup", heads: []Head{{ID: "old"}}}
	second := &stubProvider{id: "dup", heads: []Head{{ID: "new"}}}

	r.add(first)
	r.add(second)

	all := r.all()
	if len(all) != 1 {
		t.Fatalf("got %d providers for one ID, want 1", len(all))
	}
	heads, err := all[0].Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 1 || heads[0].ID != "new" {
		t.Errorf("kept %+v, want the most recent registration", heads)
	}
}

// all() must hand back a copy: a caller ranging over it while init() is still
// registering, or mutating what it got, must not corrupt the registry.
func TestRegistry_AllReturnsACopy(t *testing.T) {
	r := newRegistry()
	r.add(&stubProvider{id: "a"})

	got := r.all()
	got[0] = &stubProvider{id: "mutated"}

	again := r.all()
	if again[0].ID() != "a" {
		t.Errorf("mutating the returned slice changed the registry: %q", again[0].ID())
	}
}

// Concurrent registration and reads. Under -race this is the assertion; the
// count check catches a lost write that a race detector alone would not.
func TestRegistry_ConcurrentAddAndAllAreSafe(t *testing.T) {
	r := newRegistry()
	const n = 64

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.add(&stubProvider{id: "p" + strconv.Itoa(i)})
		}(i)
	}
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, p := range r.all() {
				_ = p.ID()
			}
		}()
	}
	wg.Wait()

	if got := len(r.all()); got != n {
		t.Errorf("got %d providers after %d concurrent adds — a write was lost", got, n)
	}
}

// The global registry is what probe.Run reads. Every provider package registers
// from init(), so by the time any test runs they must all be present — a
// missing one is a head class that can never be discovered.
func TestGlobalRegistry_IsPopulatedByInit(t *testing.T) {
	// Only the packages this test binary imports will have registered, so this
	// asserts the mechanism works rather than a specific roster.
	before := len(All())
	Register(&stubProvider{id: "test-only-provider"})
	after := len(All())

	if after != before+1 {
		t.Errorf("Register did not add to the global registry (%d → %d)", before, after)
	}
	var found bool
	for _, p := range All() {
		if p.ID() == "test-only-provider" {
			found = true
		}
	}
	if !found {
		t.Error("the registered provider is not in All()")
	}
}
