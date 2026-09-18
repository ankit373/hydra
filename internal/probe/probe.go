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
	"github.com/ankit373/hydra/internal/trust"
)

// Result is the output of a full machine scan.
type Result struct {
	Heads    []provider.Head // all discovered heads, ranked best → worst
	Cortex   *provider.Head  // highest-ranked head, recommended as Cortex
	Warnings []string        // non-fatal provider failures, e.g. a corrupted models.json overlay
	// Scores is why each head ranks where it does, keyed by head id: what the
	// catalogue declared and what this machine's verified history made of it.
	Scores map[string]rank.Score
}

// Run concurrently queries every registered provider and returns a ranked Result.
// It never returns an error, individual provider failures are silently skipped
// so a broken provider doesn't block the init wizard.
func Run(ctx context.Context) *Result {
	lookup, err := commitments()
	r := runWith(ctx, provider.All(), lookup)
	if err != nil {
		// Same discipline as a failed provider: degrading to declared scores is
		// fine, doing it silently is not, since the ranking a user sees would
		// differ from the one their history earned with nothing saying why.
		r.Warnings = append(r.Warnings, fmt.Sprintf("calibration: %v (ranking on declared scores)", err))
		sort.Strings(r.Warnings)
	}
	return r
}

// commitments reads each head's verified history out of the calibration store.
// A store that will not load yields no lookup rather than an empty one, so the
// caller can say so instead of reporting every head as never measured.
func commitments() (rank.Lookup, error) {
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		return nil, err
	}
	return func(headID string) rank.Measurement {
		correct, total := cal.Commitments(headID)
		return rank.Measurement{Correct: correct, Total: total}
	}, nil
}

// RunWith is Run against an explicit provider set, so discovery can be tested
// without registering fakes into the process-global registry, which every
// other test in the binary would then see. It ranks on declared scores: a test
// must not read whatever calibration the machine running it happens to hold.
func RunWith(ctx context.Context, providers []provider.Provider) *Result {
	return runWith(ctx, providers, nil)
}

func runWith(ctx context.Context, providers []provider.Provider, lookup rank.Lookup) *Result {
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

	ranked, scores := rank.ByMeasured(all, lookup)

	r := &Result{Heads: ranked, Warnings: warnings, Scores: scores}
	if len(ranked) > 0 {
		r.Cortex = &ranked[0]
	}
	return r
}
