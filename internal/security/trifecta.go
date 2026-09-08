// SPDX-License-Identifier: MIT

package security

import (
	"sort"

	"github.com/ankit373/hydra/internal/ledger"
)

// The lethal trifecta: private data, untrusted content, and the ability to
// communicate externally. Any one is fine; all three at once is what turns a
// poisoned input into exfiltration, with no software vulnerability involved.
//
// Counted per dispatch rather than per session, because that is the unit the
// ledger actually records and the stronger claim: "in this dispatch, content
// from a file reached a head that leaves the machine" is a fact, where "this
// session had access to all three at some point" is an inference.

// legs of the trifecta, as source-kind sets. A file is genuinely both private
// data and potentially attacker-written (a vendored dependency, a generated
// lockfile), and nothing in the provenance can tell those apart. It is counted
// as private data only, and Trifecta.Caveat says so rather than implying a
// precision that is not there.
var (
	privateSources   = map[string]bool{"file": true, "env": true}
	untrustedSources = map[string]bool{"head": true, "mcp": true, "web": true}
)

// TrifectaItem is one named contributor and how often it appeared.
type TrifectaItem struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// TrifectaLeg is one of the three capabilities and what fed it.
type TrifectaLeg struct {
	Dispatches int            `json:"dispatches"`
	Top        []TrifectaItem `json:"top,omitempty"`
}

// Trifecta is the audit CSA recommends before releasing an agent: what private
// data it touched, what untrusted content it ingested, what egress paths it
// used, and how often all three met.
type Trifecta struct {
	// HasData is false when no dispatch has recorded provenance. Rendering
	// zeros would be indistinguishable from a clean bill of health, and every
	// event written before #741 carries none.
	HasData bool `json:"hasData"`
	// Evaluated counts dispatches carrying provenance, the denominator.
	Evaluated int `json:"evaluated"`

	PrivateData TrifectaLeg `json:"privateData"`
	Untrusted   TrifectaLeg `json:"untrusted"`
	External    TrifectaLeg `json:"external"`

	// AllThree counts dispatches carrying every leg at once.
	AllThree int `json:"allThree"`
	// Contained counts AllThree dispatches the gate refused. A trifecta the
	// gate caught is evidence the control works, not an open finding, and
	// collapsing the two is how a dashboard turns a working control into an
	// alarm nobody reads.
	Contained int `json:"contained"`

	Caveat string `json:"caveat"`
}

// Exposed is the count that actually needs attention: all three legs present
// and nothing stopped it.
func (t Trifecta) Exposed() int { return t.AllThree - t.Contained }

// AssessTrifecta reads the provenance recorded on dispatch events.
func AssessTrifecta(events []ledger.Event) Trifecta {
	t := Trifecta{Caveat: "a file is counted as private data only; nothing in the provenance " +
		"distinguishes source you wrote from a vendored dependency an attacker could have"}

	origins := map[string]int{}
	sources := map[string]int{}
	sinks := map[string]int{}

	for _, e := range events {
		p := e.Provenance
		if p == nil {
			continue
		}
		t.Evaluated++

		var private, untrusted bool
		for _, s := range p.Sources {
			if privateSources[s] {
				private = true
			}
			if untrustedSources[s] {
				untrusted = true
				// Only untrusted kinds are counted here; the leg is named for
				// them, and folding in "user" would make every dispatch look
				// like it ingested something it did not.
				sources[s]++
			}
		}
		external := p.Sink == "remote"

		if private {
			t.PrivateData.Dispatches++
			for _, o := range p.Origins {
				origins[o]++
			}
		}
		if untrusted {
			t.Untrusted.Dispatches++
		}
		if external {
			t.External.Dispatches++
			if e.Tool != "" {
				sinks[e.Tool]++
			}
		}
		if private && untrusted && external {
			t.AllThree++
			// The gate refusing is the control working. Recorded as contained
			// rather than counted against the posture.
			if e.Decision == ledger.Deny {
				t.Contained++
			}
		}
	}

	t.HasData = t.Evaluated > 0
	t.PrivateData.Top = topItems(origins)
	t.Untrusted.Top = topItems(sources)
	t.External.Top = topItems(sinks)
	return t
}

// topItems is the contributors worth naming, most frequent first, ties broken
// by name so the same log always renders the same order.
func topItems(counts map[string]int) []TrifectaItem {
	if len(counts) == 0 {
		return nil
	}
	out := make([]TrifectaItem, 0, len(counts))
	for name, n := range counts {
		out = append(out, TrifectaItem{Name: name, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	const maxNamed = 8
	if len(out) > maxNamed {
		out = out[:maxNamed]
	}
	return out
}
