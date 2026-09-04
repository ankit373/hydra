// SPDX-License-Identifier: MIT

package security

import (
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/provider"
)

func TestExposures_SplitsLocalFromRemoteByHead(t *testing.T) {
	heads := []provider.Head{
		{ID: "ollama/qwen", LocalOnly: true},
		{ID: "gpt-4o"}, // remote
	}
	events := []ledger.Event{
		{Tool: "ollama/qwen", Classification: "pii", PIITypes: []string{"email"}},
		{Tool: "gpt-4o", Classification: "pii", PIITypes: []string{"aws access key id"}},
		{Tool: "gpt-4o"}, // unclassified — not an exposure at all
	}

	exps := Exposures(events, heads)
	if len(exps) != 2 {
		t.Fatalf("Exposures = %+v, want only the two pii-classified events", exps)
	}
	if exps[0].Remote {
		t.Error("a local-only head was reported as remote")
	}
	if !exps[1].Remote {
		t.Error("a cloud head was not reported as remote")
	}
	if RemoteCount(exps) != 1 {
		t.Errorf("RemoteCount = %d, want 1", RemoteCount(exps))
	}
}

// An unrecognised head must count as remote. Reading an unknown destination
// as local would under-report a real leak, which is the wrong way to be wrong.
func TestExposures_UnknownHeadCountsAsRemote(t *testing.T) {
	events := []ledger.Event{{Tool: "mystery-head", Classification: "pii"}}
	exps := Exposures(events, nil)
	if len(exps) != 1 || !exps[0].Remote {
		t.Errorf("Exposures = %+v, want the unknown head treated as remote (fail-closed)", exps)
	}
	if exps[0].Known {
		t.Error("Known = true for a head that was never discovered")
	}
}

// The two must stay distinguishable: a head that simply isn't running (a
// stopped Ollama server) would otherwise inflate the confirmed-leak count
// every time, and a headline number that cries wolf gets ignored.
func TestExposures_ConfirmedRemoteExcludesUnknownHeads(t *testing.T) {
	heads := []provider.Head{
		{ID: "gpt-4o"},                       // known, remote
		{ID: "ollama/live", LocalOnly: true}, // known, local
	}
	events := []ledger.Event{
		{Tool: "gpt-4o", Classification: "pii"},
		{Tool: "ollama/live", Classification: "pii"},
		{Tool: "ollama/stopped", Classification: "pii"}, // not discovered right now
	}
	exps := Exposures(events, heads)

	if got := RemoteCount(exps); got != 2 {
		t.Errorf("RemoteCount = %d, want 2 (fail-closed includes the undiscovered head)", got)
	}
	if got := ConfirmedRemote(exps); got != 1 {
		t.Errorf("ConfirmedRemote = %d, want only the observed gpt-4o leak", got)
	}
}

// The detector names must survive from the ledger into the report — that is
// the whole reason PIITypes exists.
func TestExposures_CarriesPIITypes(t *testing.T) {
	events := []ledger.Event{{
		Tool: "gpt-4o", Classification: "pii",
		PIITypes: []string{"github token", "email"},
	}}
	exps := Exposures(events, nil)
	if len(exps) != 1 || len(exps[0].PIITypes) != 2 || exps[0].PIITypes[0] != "github token" {
		t.Errorf("PIITypes = %+v, want them carried through verbatim", exps)
	}
}
