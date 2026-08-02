// SPDX-License-Identifier: MIT

package provider

import (
	"sync"
)

var global = &Registry{providers: make(map[string]Provider)}

// Register adds a provider to the global registry.
// Call this from your provider package's init() function.
func Register(p Provider) { global.add(p) }

// All returns every registered provider.
func All() []Provider { return global.all() }

// Registry is a thread-safe map of Providers.
// The global instance is used by default; tests may construct their own.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

func (r *Registry) add(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.ID()] = p
}

func (r *Registry) all() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Provider, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p)
	}
	return out
}
