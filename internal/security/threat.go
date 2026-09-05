// SPDX-License-Identifier: MIT

package security

import "github.com/ankit373/hydra/internal/ledger"

// probeThreshold is the number of denials on one resource past which the
// pattern reads as probing rather than a single mistake. Two is deliberately
// low: on a machine where denials are rare at all, a repeat is the signal.
const probeThreshold = 2

// Count is one labelled tally, ordered by the slices below.
type Count struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

// Threats is the forensic breakdown behind "N blocked, M flagged".
//
// The counts alone answer nothing actionable, a security view has to say
// *what* was attempted, *where*, and *how dangerous the operation was*. All
// three come from fields the ledger has always recorded (FlagReason, Resource,
// Action) and that nothing rendered until now.
type Threats struct {
	// ByMarker groups flagged events by the injection phrase that actually
	// matched, e.g. "ignore previous instructions".
	ByMarker []Count `json:"byMarker,omitempty"`
	// ProbedResources are resources denied at least probeThreshold times,
	// repeated denials against one target, which is the one behavioural
	// signal present in the data.
	ProbedResources []Count `json:"probedResources,omitempty"`
	// ByAction splits denied+flagged events by read/write/exec/network, so an
	// attempted exec does not hide inside the same total as a read.
	ByAction []Count `json:"byAction,omitempty"`
}

// ThreatBreakdown computes all three groupings in one pass over the events.
func ThreatBreakdown(events []ledger.Event) Threats {
	markers := map[string]int{}
	deniedByResource := map[string]int{}
	byAction := map[string]int{}

	for _, e := range events {
		risky := e.Decision == ledger.Deny || e.Flagged
		if !risky {
			continue
		}
		if e.Flagged && e.FlagReason != "" {
			markers[e.FlagReason]++
		}
		if e.Decision == ledger.Deny && e.Resource != "" {
			deniedByResource[e.Resource]++
		}
		action := string(e.Action)
		if action == "" {
			action = "unspecified"
		}
		byAction[action]++
	}

	probed := map[string]int{}
	for res, n := range deniedByResource {
		if n >= probeThreshold {
			probed[res] = n
		}
	}

	return Threats{
		ByMarker:        toCounts(markers),
		ProbedResources: toCounts(probed),
		ByAction:        toCounts(byAction),
	}
}

// toCounts reuses ledger.SortedCounts for the count-descending, key-ascending
// ordering the ledger already established, rather than adding another sorter.
func toCounts(m map[string]int) []Count {
	sorted := ledger.SortedCounts(m)
	if len(sorted) == 0 {
		return nil
	}
	out := make([]Count, 0, len(sorted))
	for _, kv := range sorted {
		out = append(out, Count{Label: kv.Key, Count: kv.Count})
	}
	return out
}
