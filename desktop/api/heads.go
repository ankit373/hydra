// SPDX-License-Identifier: MIT

package api

import (
	"github.com/ankit373/hydra/internal/health"
	"github.com/ankit373/hydra/internal/probe"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
	"time"
)

// Head is one discovered head and whether anything can actually drive it.
//
// Routable is the distinction the app was missing entirely. Models rendered a
// live/off dot from models.yaml's `enabled` flag, which is an install-specific
// default and says nothing about reachability, so a head with no API key, or
// an Ollama model whose server is down, still showed as on.
type Head struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Source   string `json:"source"`
	Tier     int    `json:"tier"`
	CapScore int    `json:"capScore"`

	Routable  bool `json:"routable"`
	LocalOnly bool `json:"localOnly"`
	// Reason is why nothing can drive this head, in words, and is empty when
	// it is routable. `hyctl probe` has printed this since #248; the desktop
	// showed neither the mark nor the reason.
	Reason string `json:"reason,omitempty"`
}

// HeadPanel is what this machine can route to right now.
type HeadPanel struct {
	Heads []Head `json:"heads"`
	// Routable is counted separately so a view can say "3 of 12 reachable"
	// without re-deriving it, and can tell "none discovered" apart from
	// "discovered, none usable", which have completely different fixes.
	Routable int `json:"routable"`
}

// GetHeads probes the machine and reports each head's routability.
//
// probe.Run already ran on every GetSecurity call and its head list never
// reached the UI as head data, so nothing in the app answered "what can I
// actually send work to right now".
func (a *API) GetHeads() HeadPanel {
	res := probe.Run(a.ctx)
	if res == nil {
		return HeadPanel{Heads: []Head{}}
	}
	return headsFrom(res.Heads)
}

// headsFrom is the mapping, split out from the probe so it can be tested
// against real routable and unroutable heads. Driven only through GetHeads it
// was exercised against whatever the machine happened to have, which in a
// sandbox is nothing at all, a test that iterates an empty list asserts
// nothing while passing.
func headsFrom(discovered []provider.Head) HeadPanel {
	out := HeadPanel{Heads: make([]Head, 0, len(discovered))}
	hs, now := health.Open(health.DefaultPath()), time.Now()
	for _, h := range discovered {
		reason := health.Reason(hs, h, now)
		e := Head{
			ID: h.ID, Name: h.Name, Provider: h.Provider, Source: h.Source,
			Tier: rank.UITier(h), CapScore: h.CapScore,
			Routable: reason == "", LocalOnly: h.LocalOnly, Reason: reason,
		}
		if e.Routable {
			out.Routable++
		}
		out.Heads = append(out.Heads, e)
	}
	return out
}
