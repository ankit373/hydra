// SPDX-License-Identifier: MIT

package ledger

import (
	"path/filepath"
	"reflect"
	"testing"
)

// The receipt reads these back, so they must survive Record's sanitize and the
// JSON round trip; a control byte in a kind name is neutralised, not kept.
func TestProvenance_RedactedCountsSurviveSanitize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_ledger.jsonl")
	in := EventProvenance{
		Sink: "remote", RunID: "run-1", Provider: "openai",
		Redacted: map[string]int{"EMAIL": 2, "AWS\x1b_KEY": 1}, Restored: 3,
	}
	if err := Record(path, Event{Agent: "a", Tool: "h", Action: Exec, Decision: Allow, Provenance: &in}); err != nil {
		t.Fatal(err)
	}
	events, err := Load(path)
	if err != nil || len(events) != 1 {
		t.Fatalf("Load = %v, %v", events, err)
	}
	got := events[0].Provenance
	want := map[string]int{"EMAIL": 2, "AWS _KEY": 1}
	if !reflect.DeepEqual(got.Redacted, want) || got.RunID != "run-1" || got.Provider != "openai" || got.Restored != 3 {
		t.Errorf("read back %+v, want counts %v, run-1, openai, 3", got, want)
	}
}
