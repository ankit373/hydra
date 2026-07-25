// SPDX-License-Identifier: MIT

// Package probe orchestrates all registered providers and returns a ranked
// list of available Heads with a recommended Cortex.
package probe

import (
	"context"
	"sync"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
)

// Result is the output of a full machine scan.
type Result struct {
	Heads  []provider.Head // all discovered heads, ranked best → worst
	Cortex *provider.Head  // highest-ranked head, recommended as Cortex
}

// Run concurrently queries every registered provider and returns a ranked Result.
// It never returns an error — individual provider failures are silently skipped
// so a broken provider doesn't block the init wizard.
func Run(ctx context.Context) *Result {
	providers := provider.All()

	var mu sync.Mutex
	var wg sync.WaitGroup
	var all []provider.Head

	for _, p := range providers {
		wg.Add(1)
		go func(p provider.Provider) {
			defer wg.Done()
			heads, err := p.Discover(ctx)
			if err != nil {
				return
			}
			mu.Lock()
			all = append(all, heads...)
			mu.Unlock()
		}(p)
	}
	wg.Wait()

	ranked := rank.ByCapScore(all)

	r := &Result{Heads: ranked}
	if len(ranked) > 0 {
		r.Cortex = &ranked[0]
	}
	return r
}
