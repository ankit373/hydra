// SPDX-License-Identifier: MIT

package security

import (
	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/provider"
)

// An Exposure is one recorded access to sensitive data, and — the part that
// matters — whether it went to a head that leaves this machine.
//
// "N PII detections" on its own is not a security finding: PII routed to a
// local head is the control working exactly as designed (that is what LLM02's
// local-only rule is for), while the same PII routed to a cloud provider is
// data that has left the building. Those two are opposite outcomes and were
// previously reported as the same number.
type Exposure struct {
	TS       string   `json:"ts"`
	Agent    string   `json:"agent"`
	Head     string   `json:"head"`
	Resource string   `json:"resource"`
	PIITypes []string `json:"piiTypes,omitempty"`
	// Remote is true when Head is not a local-only head — including when the
	// head is not in the discovered list at all, which is read as remote: an
	// unrecognised destination is the fail-closed assumption.
	Remote bool `json:"remote"`
	// Known is false when the head was not in the discovered list, so Remote
	// is an assumption rather than an observation.
	//
	// Both are reported because collapsing them would make the headline leak
	// count meaningless: any head that simply isn't running right now (a
	// stopped Ollama server) would inflate it, and a number that cries wolf
	// is one operators learn to ignore. Fail-closed on the decision, honest
	// about the confidence.
	Known bool `json:"known"`
}

// Exposures extracts every PII-classified event, resolving each against the
// discovered head list to decide whether it stayed on the machine.
func Exposures(events []ledger.Event, heads []provider.Head) []Exposure {
	local := make(map[string]bool, len(heads))
	for _, h := range heads {
		local[h.ID] = h.LocalOnly
	}

	var out []Exposure
	for _, e := range events {
		if e.Classification != "pii" {
			continue
		}
		isLocal, known := local[e.Tool]
		out = append(out, Exposure{
			TS: e.TS, Agent: e.Agent, Head: e.Tool, Resource: e.Resource,
			PIITypes: e.PIITypes,
			Remote:   !isLocal, // an unknown head is not local → remote
			Known:    known,
		})
	}
	return out
}

// RemoteCount is how many exposures left the machine — including the ones
// only assumed to have, since the decision is fail-closed. ConfirmedRemote
// narrows that to the ones actually observed on a known remote head.
func RemoteCount(exps []Exposure) int {
	n := 0
	for _, e := range exps {
		if e.Remote {
			n++
		}
	}
	return n
}

// ConfirmedRemote counts exposures to a head that was discovered and is known
// not to be local-only — a leak observed rather than assumed.
func ConfirmedRemote(exps []Exposure) int {
	n := 0
	for _, e := range exps {
		if e.Remote && e.Known {
			n++
		}
	}
	return n
}
