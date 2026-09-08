// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/capabilities"
	"github.com/ankit373/hydra/internal/health"
	"github.com/ankit373/hydra/internal/provider"
)

// The model overlay carries capability scores for models a provider must still
// discover. `models list` rendered it as an inventory of what this machine can
// run, listing env/anthropic and env/openai with no key set and a dozen CLI
// agents that are not installed, and `models add` reported "added" for a model
// nothing could route to (#742). Same defect as #714, in the surface that pass
// did not cover.

// modelRow is one catalogue entry plus whether anything can drive it now.
type modelRow struct {
	capabilities.Entry
	// Live is true when discovery found this id and health.Reason accepts it.
	Live bool `json:"live"`
	// Why explains a non-live entry: the routability reason where the head was
	// discovered, or that nothing discovered it at all.
	Why string `json:"why,omitempty"`
}

// modelRows joins the catalogue to discovery. reason is health.Reason in
// production, so this surface cannot disagree with probe, status or the router
// about what can run.
func modelRows(entries []capabilities.Entry, heads []provider.Head, reason reasonFunc) []modelRow {
	found := make(map[string]provider.Head, len(heads))
	for _, h := range heads {
		found[h.ID] = h
	}
	out := make([]modelRow, 0, len(entries))
	for _, e := range entries {
		r := modelRow{Entry: e}
		h, ok := found[e.ID]
		switch {
		case !ok:
			r.Why = "not discovered on this machine"
		case reason(h) != "":
			r.Why = reason(h)
		default:
			r.Live = true
		}
		out = append(out, r)
	}
	return out
}

// liveModelRows is modelRows against the real machine.
func liveModelRows(entries []capabilities.Entry, heads []provider.Head) []modelRow {
	hs, now := health.Open(health.DefaultPath()), time.Now()
	return modelRows(entries, heads, func(h provider.Head) string {
		return health.Reason(hs, h, now)
	})
}

// addedModelNote says what recording a score did and did not do.
//
// "added" alone read as "this model is now routable", which it is not: the
// overlay annotates a head some provider must discover. The note names what
// would make it routable, by provider shape rather than a guess at the user's
// setup.
func addedModelNote(e capabilities.Entry) string {
	switch {
	case strings.HasPrefix(e.ID, "env/"):
		return "this records a capability score. " + e.ID +
			" becomes routable once its provider's API key is set in the environment."
	case e.Provider == "local" || e.Provider == "ollama" || e.Provider == "lmstudio":
		return "this records a capability score. " + e.ID +
			" becomes routable once a local server is serving it (`hyctl probe` shows what is)."
	default:
		return "this records a capability score, not a routable model. " + e.ID +
			" is routed only if a provider discovers it: a CLI on PATH, an API key in the" +
			" environment, a local server, or a registry/models.yaml entry. `hyctl probe` says what is routable."
	}
}
