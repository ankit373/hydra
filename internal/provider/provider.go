// Package provider defines the interfaces and types every Hydra Head must implement.
// Add a new model source: implement Provider, call Register in your package's init().
package provider

import "context"

// Head is a discovered AI model that can act as a Hydra Head.
type Head struct {
	ID         string            // unique key within Hydra, e.g. "claude", "ollama/qwen2.5"
	Name       string            // human display label
	Provider   string            // vendor: "anthropic", "openai", "local", etc.
	Source     string            // how it was found: "cli", "env", "port"
	Executable string            // absolute path if CLI-sourced
	Endpoint   string            // base URL if HTTP-sourced
	CapScore   int               // 0–100 capability score; higher = smarter
	LocalOnly  bool              // never routes over the network
	AuthReady  bool              // immediately usable, no extra auth step required
	Meta       map[string]string // extensible metadata; providers add what they need
}

// Provider discovers Heads available on this machine.
// Implement this interface to add a new discovery source.
type Provider interface {
	// ID uniquely identifies this provider within the registry (e.g. "cli", "ollama").
	ID() string
	// Discover scans for available Heads. Implementations must respect ctx cancellation.
	Discover(ctx context.Context) ([]Head, error)
}
