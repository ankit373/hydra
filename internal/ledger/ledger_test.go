// SPDX-License-Identifier: MIT

package ledger

import (
	"path/filepath"
	"testing"
)

func TestPolicy_Decide_FirstMatchWins(t *testing.T) {
	p := Policy{
		Rules: []Rule{
			{Agent: "untrusted", Decision: Deny},
			{Tool: "fs", Resource: "/etc/*", Action: Write, Decision: Deny},
			{Tool: "fs", Resource: "/repo/*", Decision: Allow},
		},
		Default: Deny,
	}

	tests := []struct {
		name                  string
		agent, tool, resource string
		action                Action
		want                  Decision
	}{
		{"untrusted agent denied outright", "untrusted", "fs", "/repo/a.go", Read, Deny},
		{"write to /etc denied by rule", "svc", "fs", "/etc/passwd", Write, Deny},
		{"read of /etc: write-rule skipped, no allow rule → default deny", "svc", "fs", "/etc/passwd", Read, Deny},
		{"repo write allowed", "svc", "fs", "/repo/a.go", Write, Allow},
		{"repo read allowed", "svc", "fs", "/repo/a.go", Read, Allow},
		{"unmatched falls to default deny", "svc", "network", "example.com", Network, Deny},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := p.Decide(tt.agent, tt.tool, tt.resource, tt.action)
			if got != tt.want {
				t.Errorf("Decide = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPolicy_DefaultAllowWhenUnset(t *testing.T) {
	var p Policy // no rules, zero default
	if got, _ := p.Decide("a", "t", "r", Read); got != Allow {
		t.Errorf("empty policy default = %v, want allow", got)
	}
}

func TestGlobMatch(t *testing.T) {
	p := Policy{Rules: []Rule{{Resource: "secrets/*.key", Decision: Deny}}, Default: Allow}
	if d, _ := p.Decide("a", "fs", "secrets/prod.key", Read); d != Deny {
		t.Error("glob secrets/*.key should match secrets/prod.key")
	}
	if d, _ := p.Decide("a", "fs", "src/main.go", Read); d != Allow {
		t.Error("non-matching resource should hit default allow")
	}
}

func TestCheck_RecordsDecision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_ledger.jsonl")
	p := Policy{Rules: []Rule{{Tool: "fs", Resource: "/etc/*", Decision: Deny}}, Default: Allow}

	if d, err := Check(path, p, "agentA", "fs", "/etc/hosts", Write); err != nil || d != Deny {
		t.Fatalf("Check = %v (err %v), want deny", d, err)
	}
	if d, err := Check(path, p, "agentA", "fs", "/repo/x.go", Write); err != nil || d != Allow {
		t.Fatalf("Check = %v (err %v), want allow", d, err)
	}

	events, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("ledger has %d events, want 2 (every check is recorded)", len(events))
	}
	if events[0].Decision != Deny || events[0].TS == "" {
		t.Errorf("first event should be a timestamped deny: %+v", events[0])
	}
}

func TestLoad_MissingIsEmpty(t *testing.T) {
	events, err := Load(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil {
		t.Fatalf("missing ledger should not error: %v", err)
	}
	if events != nil {
		t.Errorf("missing ledger should be nil, got %v", events)
	}
}

func TestSummarize(t *testing.T) {
	events := []Event{
		{Agent: "a", Tool: "fs", Decision: Allow},
		{Agent: "a", Tool: "fs", Decision: Deny},
		{Agent: "b", Tool: "net", Decision: Allow},
	}
	s := Summarize(events)
	if s.Total != 3 || s.Allowed != 2 || s.Denied != 1 {
		t.Errorf("totals = %+v, want 3/2/1", s)
	}
	if s.ByAgent["a"] != 2 || s.ByTool["fs"] != 2 {
		t.Errorf("breakdowns wrong: %+v", s)
	}
}

func TestFilter(t *testing.T) {
	events := []Event{
		{Agent: "a", Decision: Allow},
		{Agent: "a", Decision: Deny},
		{Agent: "b", Decision: Deny},
	}
	if got := Filter(events, "a", false); len(got) != 2 {
		t.Errorf("Filter(agent=a) = %d, want 2", len(got))
	}
	if got := Filter(events, "", true); len(got) != 2 {
		t.Errorf("Filter(deniedOnly) = %d, want 2", len(got))
	}
	if got := Filter(events, "a", true); len(got) != 1 {
		t.Errorf("Filter(agent=a, denied) = %d, want 1", len(got))
	}
}
