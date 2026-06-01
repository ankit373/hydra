package provider

import (
	"fmt"
	"sync"
)

var global = &Registry{providers: make(map[string]Provider)}

// Register adds a provider to the global registry.
// Call this from your provider package's init() function.
func Register(p Provider) { global.add(p) }

// All returns every registered provider.
func All() []Provider { return global.all() }

// Get returns a specific provider by ID.
func Get(id string) (Provider, error) { return global.get(id) }

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

func (r *Registry) get(id string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[id]
	if !ok {
		return nil, fmt.Errorf("provider %q not registered", id)
	}
	return p, nil
}
