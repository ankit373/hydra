// SPDX-License-Identifier: MIT

// Package probe orchestrates all registered providers and returns a ranked
// list of available Heads with a recommended Cortex.
package probe

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
)

// Result is the output of a full machine scan.
type Result struct {
	Heads    []provider.Head // all discovered heads, ranked best → worst
	Cortex   *provider.Head  // highest-ranked head, recommended as Cortex
	Warnings []string        // non-fatal provider failures, e.g. a corrupted models.json overlay
}

// Run concurrently queries every registered provider and returns a ranked Result.
// It never returns an error, individual provider failures are silently skipped
// so a broken provider doesn't block the init wizard.
func Run(ctx context.Context) *Result {
	return RunWith(ctx, provider.All())
}

// RunWith is Run against an explicit provider set, so discovery can be tested
// without registering fakes into the process-global registry, which every
// other test in the binary would then see.
func RunWith(ctx context.Context, providers []provider.Provider) *Result {
	var mu sync.Mutex
	var wg sync.WaitGroup
	var all []provider.Head
	var warnings []string

	for _, p := range providers {
		wg.Add(1)
		go func(p provider.Provider) {
			defer wg.Done()
			// "Individual provider failures are silently skipped" has to include
			// a panic, or the guarantee is only about the errors a provider
			// remembers to return. Without this, one misbehaving provider takes
			// down the whole probe, and `hyctl probe` is often the first thing
			// a user runs.
			defer func() { _ = recover() }()

			heads, err := p.Discover(ctx)
			if err != nil {
				// The failure itself must stay non-fatal, one broken provider
				// (e.g. a corrupted ~/.hydra/models.json overlay) must not hide
				// every other head, but silently dropping it entirely
				// contradicts hyctl probe's own "✗ marks unroutable heads with
				// the reason" promise (#248): this provider's heads don't even
				// get that far. Recording it here is the only visible trace.
				mu.Lock()
				warnings = append(warnings, fmt.Sprintf("%s: %v", p.ID(), err))
				mu.Unlock()
				return
			}
			mu.Lock()
			all = append(all, heads...)
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	// Providers run concurrently, so append order is nondeterministic, sort so
	// a repeated probe on the same broken machine reports the same order.
	sort.Strings(warnings)

	ranked := rank.ByCapScore(all)

	r := &Result{Heads: ranked, Warnings: warnings}
	if len(ranked) > 0 {
		r.Cortex = &ranked[0]
	}
	return r
}
