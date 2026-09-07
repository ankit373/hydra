// SPDX-License-Identifier: MIT

package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The regression that matters: before #722 nothing ever wrote a policy file,
// so every install ran on LoadPolicy's default-allow fallback and the gate in
// the dispatch path recorded without blocking anything.
func TestEnsurePolicy_WritesALoadablePolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "mcp_policy.json")

	created, err := EnsurePolicy(path)
	if err != nil {
		t.Fatalf("EnsurePolicy: %v", err)
	}
	if !created {
		t.Fatal("EnsurePolicy reported no file created")
	}

	got, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("the policy it just wrote does not load: %v", err)
	}
	if len(got.Rules) != len(DefaultPolicy().Rules) {
		t.Errorf("loaded %d rules, want %d", len(got.Rules), len(DefaultPolicy().Rules))
	}
}

// An operator's tuned policy outranks a shipped default, and silently
// replacing a file whose whole job is to say no is the worst option available.
func TestEnsurePolicy_NeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_policy.json")
	mine := Policy{Default: Deny, Rules: []Rule{{Tool: "ollama", Decision: Allow}}}
	raw, err := json.Marshal(mine)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	created, err := EnsurePolicy(path)
	if err != nil {
		t.Fatalf("EnsurePolicy: %v", err)
	}
	if created {
		t.Error("EnsurePolicy overwrote an existing policy")
	}

	got, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Default != Deny || len(got.Rules) != 1 {
		t.Errorf("the operator's policy was modified: %+v", got)
	}
}

// A quarantined server is one a confirmed finding took out of service, and
// quarantine has no automatic exit, so it is the one classification the
// shipped default denies outright rather than asking about.
func TestDefaultPolicy_DeniesQuarantinedOutright(t *testing.T) {
	p := DefaultPolicy()

	if d, _ := p.Decide("a", "t", "r", Exec, classQuarantined); d != Deny {
		t.Errorf("quarantined = %q, want %q", d, Deny)
	}
	if d, _ := p.Decide("a", "t", "r", Exec, classFlagged); d != Ask {
		t.Errorf("flagged = %q, want %q", d, Ask)
	}
	// Default-allow is deliberate at this stage; the egress gate is what
	// makes a default-deny survivable. If this flips, hyctl security stops
	// withholding the coverage score, so the change has to be intentional.
	if d, _ := p.Decide("a", "t", "r", Exec, ""); d != Allow {
		t.Errorf("unclassified = %q, want %q", d, Allow)
	}
}
