// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/entity"
	"github.com/ankit373/hydra/internal/provider"
)

// The command sends 100 texts from a PII corpus to a head, so picking one that
// leaves the machine by default would be the wrong default for this command in
// particular.
func TestPickHead_DefaultsToALocalHead(t *testing.T) {
	heads := []provider.Head{
		{ID: "env/openai", Source: "env"},
		{ID: "ollama/qwen3:8b", Source: "port", Endpoint: "http://127.0.0.1:11434", LocalOnly: true},
	}
	got, err := pickHead(heads, "")
	if err != nil {
		t.Fatal(err)
	}
	if !got.LocalOnly {
		t.Errorf("picked %q, a head that leaves the machine", got.ID)
	}
}

func TestPickHead_NamedHeadWins(t *testing.T) {
	heads := []provider.Head{
		{ID: "ollama/a", Source: "port", Endpoint: "http://127.0.0.1:11434", LocalOnly: true},
		{ID: "env/openai", Source: "env"},
	}
	got, err := pickHead(heads, "env/openai")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "env/openai" {
		t.Errorf("got %q, want the head that was named", got.ID)
	}
}

// Naming a head that does not exist is a typo, and the refusal has to say how
// to find the real name rather than falling back to some other head.
func TestPickHead_UnknownHeadIsRefusedNotSubstituted(t *testing.T) {
	heads := []provider.Head{{ID: "ollama/a", Source: "port", LocalOnly: true}}
	_, err := pickHead(heads, "ollama/typo")
	if err == nil {
		t.Fatal("an unknown head resolved to something")
	}
	if !strings.Contains(err.Error(), "hyctl probe") {
		t.Errorf("the refusal does not say how to find the name: %v", err)
	}
}

func TestPickHead_NoLocalHeadSaysSo(t *testing.T) {
	_, err := pickHead([]provider.Head{{ID: "env/openai", Source: "env"}}, "")
	if err == nil {
		t.Fatal("a machine with no local head resolved one")
	}
	if !strings.Contains(err.Error(), "--head") {
		t.Errorf("the refusal does not name the way out: %v", err)
	}
}

// The report must never render an ineligible head as a usable one, whichever
// bar it failed.
func TestPrintNER_SaysWhenTheAnswersAreNotEvidence(t *testing.T) {
	for name, r := range map[string]entity.Report{
		"low recall":       {Positives: 40, Recalled: 10, Negatives: 60},
		"many false alarm": {Positives: 40, Recalled: 40, Negatives: 60, FalsePos: 50},
		"never answers":    {Positives: 40, Negatives: 60, Unreadable: 100},
	} {
		t.Run(name, func(t *testing.T) {
			if r.Eligible() {
				t.Fatalf("%+v was rated eligible", r)
			}
		})
	}
}
