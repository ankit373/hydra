// SPDX-License-Identifier: MIT

package dispatch

import (
	"testing"

	"github.com/ankit373/hydra/internal/budget"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/provider"
)

// benchDispatcher returns a Dispatcher pre-loaded with a realistic head list
// and tier config. No real API keys or file I/O needed — this is the pure
// routing path (policy eval + claudeMode + selectHeads).
func benchDispatcher() *Dispatcher {
	heads := []provider.Head{
		{ID: "claude-core", Name: "claude-opus-4-7", Provider: "anthropic", CapScore: 100, AuthReady: true},
		{ID: "claude-expert", Name: "claude-sonnet-4-6", Provider: "anthropic", CapScore: 85, AuthReady: true},
		{ID: "gemini-pro", Name: "gemini-2.5-pro", Provider: "google", CapScore: 70, AuthReady: true},
		{ID: "gemini-flash-high", Name: "gemini-2.0-flash", Provider: "google", CapScore: 55, AuthReady: true},
		{ID: "gemini-flash-low", Name: "gemini-2.0-flash", Provider: "google", CapScore: 40, AuthReady: true},
		{ID: "qwen-local", Name: "qwen3:8b", Provider: "local", CapScore: 10, LocalOnly: true, AuthReady: true},
	}

	cfg := &config.Config{
		Tiers: []config.Tier{
			{Name: "1", Heads: []string{"claude-core"}},
			{Name: "7", Heads: []string{"gemini-pro"}},
			{Name: "8", Heads: []string{"gemini-flash-high"}},
			{Name: "10", Heads: []string{"qwen-local"}},
		},
	}

	return &Dispatcher{
		cfg:     cfg,
		heads:   heads,
		policy:  policy.New(policy.DefaultRules(false)),
		pricing: pricing.Load(),
		budget:  budget.NewRegistry(nil),
	}
}

// BenchmarkSelectHeads_NoTier measures head selection with no tier hint —
// every live head filtered and returned in score order.
func BenchmarkSelectHeads_NoTier(b *testing.B) {
	d := benchDispatcher()
	b.ResetTimer()
	for range b.N {
		_ = d.selectHeads("", false)
	}
}

// BenchmarkSelectHeads_Tier measures head selection for a specific tier.
func BenchmarkSelectHeads_Tier(b *testing.B) {
	d := benchDispatcher()
	b.ResetTimer()
	for range b.N {
		_ = d.selectHeads("7", false)
	}
}

// BenchmarkClaudeMode measures the budget pressure check (reads state.json if
// present; falls back to 0 on missing file — same cold-start path as prod).
func BenchmarkClaudeMode(b *testing.B) {
	d := benchDispatcher()
	b.ResetTimer()
	for range b.N {
		_, _, _ = d.claudeMode("7")
	}
}

// BenchmarkRoutingPath measures the full pre-execution routing path:
// claudeMode + policy.Evaluate + selectHeads. This is the number that
// appears on the landing page as "route overhead".
func BenchmarkRoutingPath(b *testing.B) {
	d := benchDispatcher()
	prompt := "write a User DTO for profile settings"
	b.ResetTimer()
	for range b.N {
		tier, _, _ := d.claudeMode("7")
		req := policy.Request{Prompt: prompt, TierHint: tier}
		action := d.policy.Evaluate(req)
		if !action.Deny {
			_ = d.selectHeads(tier, false)
		}
	}
}
