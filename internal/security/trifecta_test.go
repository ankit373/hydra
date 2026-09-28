// SPDX-License-Identifier: MIT

package security

import (
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
)

func dispatchEvent(sources []string, origins []string, sink string, d ledger.Decision) ledger.Event {
	return ledger.Event{
		Agent: "hydra-dispatch", Tool: "openai/gpt-5", Action: ledger.Exec, Decision: d,
		Provenance: &ledger.EventProvenance{Sources: sources, Origins: origins, Sink: sink},
	}
}

// All three legs in one dispatch is the case the audit exists for.
func TestAssessTrifecta_CountsAllThreeLegs(t *testing.T) {
	got := AssessTrifecta([]ledger.Event{
		dispatchEvent([]string{"user", "file", "head"}, []string{"internal/auth.go"}, "remote", ledger.Allow),
	})

	if !got.HasData || got.Evaluated != 1 {
		t.Fatalf("provenance was not read: %+v", got)
	}
	if got.AllThree != 1 {
		t.Errorf("AllThree = %d, want 1", got.AllThree)
	}
	if got.Exposed() != 1 {
		t.Errorf("Exposed = %d, want 1: nothing stopped it", got.Exposed())
	}
	for _, c := range []struct {
		name string
		leg  TrifectaLeg
	}{{"private", got.PrivateData}, {"untrusted", got.Untrusted}, {"external", got.External}} {
		if c.leg.Dispatches != 1 {
			t.Errorf("%s leg = %d dispatch(es), want 1", c.name, c.leg.Dispatches)
		}
	}
}

// A trifecta the gate refused is the control working. Reporting it as an open
// finding is how a dashboard trains people to ignore its own number.
func TestAssessTrifecta_ARefusedTrifectaIsContainedNotExposed(t *testing.T) {
	got := AssessTrifecta([]ledger.Event{
		dispatchEvent([]string{"file", "head"}, []string{"deploy/.env"}, "remote", ledger.Deny),
	})

	if got.AllThree != 1 {
		t.Fatalf("AllThree = %d, want 1", got.AllThree)
	}
	if got.Contained != 1 {
		t.Errorf("Contained = %d, want 1: the gate refused it", got.Contained)
	}
	if got.Exposed() != 0 {
		t.Errorf("Exposed = %d, want 0", got.Exposed())
	}
}

// A local head is not an egress path, so the trifecta is incomplete however
// sensitive the content was. This is the whole point of the reroute.
func TestAssessTrifecta_LocalSinkBreaksTheTrifecta(t *testing.T) {
	got := AssessTrifecta([]ledger.Event{
		dispatchEvent([]string{"file", "head"}, []string{"deploy/.env"}, "local", ledger.Allow),
	})

	if got.External.Dispatches != 0 {
		t.Errorf("a local head was counted as an egress path")
	}
	if got.AllThree != 0 {
		t.Errorf("AllThree = %d, want 0: nothing left the machine", got.AllThree)
	}
}

// Only untrusted kinds feed the untrusted leg. Counting "user" there would
// make every dispatch look like it ingested something it did not.
func TestAssessTrifecta_AUserPromptAloneIsNotUntrustedContent(t *testing.T) {
	got := AssessTrifecta([]ledger.Event{
		dispatchEvent([]string{"user"}, nil, "remote", ledger.Allow),
	})

	if got.Untrusted.Dispatches != 0 {
		t.Errorf("a plain user prompt was counted as untrusted content")
	}
	if got.PrivateData.Dispatches != 0 {
		t.Errorf("a plain user prompt was counted as private data access")
	}
	if got.AllThree != 0 {
		t.Errorf("AllThree = %d, want 0", got.AllThree)
	}
}

// Every event written before the gate shipped carries no provenance. Rendering
// zeros for those would be indistinguishable from a clean bill of health.
func TestAssessTrifecta_EventsWithoutProvenanceAreNotData(t *testing.T) {
	got := AssessTrifecta([]ledger.Event{
		{Agent: "a", Tool: "t", Action: ledger.Exec, Decision: ledger.Allow},
		{Agent: "a", Tool: "t", Action: ledger.Exec, Decision: ledger.Allow},
	})

	if got.HasData || got.Evaluated != 0 {
		t.Errorf("legacy events were treated as evidence: %+v", got)
	}
}

// Ties break by name so the same log always renders the same order; a report
// that reshuffles between runs cannot be diffed.
func TestTopItems_IsStableAndBounded(t *testing.T) {
	counts := map[string]int{"b": 1, "a": 1, "c": 9}
	got := topItems(counts)

	if len(got) != 3 || got[0].Name != "c" {
		t.Fatalf("not ordered by count: %+v", got)
	}
	if got[1].Name != "a" || got[2].Name != "b" {
		t.Errorf("ties did not break by name: %+v", got)
	}

	many := map[string]int{}
	for i := range 40 {
		many[string(rune('a'+i%26))+string(rune('0'+i/26))] = i
	}
	if n := len(topItems(many)); n > 8 {
		t.Errorf("topItems returned %d entries, want at most 8", n)
	}
}
