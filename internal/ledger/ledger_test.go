// SPDX-License-Identifier: MIT

package ledger

import (
	"math"
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
			got, _ := p.Decide(tt.agent, tt.tool, tt.resource, tt.action, "")
			if got != tt.want {
				t.Errorf("Decide = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPolicy_DefaultAllowWhenUnset(t *testing.T) {
	var p Policy // no rules, zero default
	if got, _ := p.Decide("a", "t", "r", Read, ""); got != Allow {
		t.Errorf("empty policy default = %v, want allow", got)
	}
}

func TestGlobMatch(t *testing.T) {
	p := Policy{Rules: []Rule{{Resource: "secrets/*.key", Decision: Deny}}, Default: Allow}
	if d, _ := p.Decide("a", "fs", "secrets/prod.key", Read, ""); d != Deny {
		t.Error("glob secrets/*.key should match secrets/prod.key")
	}
	if d, _ := p.Decide("a", "fs", "src/main.go", Read, ""); d != Allow {
		t.Error("non-matching resource should hit default allow")
	}
}

func TestPolicy_Decide_MatchesOnClassification(t *testing.T) {
	p := Policy{Rules: []Rule{{Classification: "pii", Decision: Deny}}, Default: Allow}
	if d, _ := p.Decide("a", "fs", "any", Read, "pii"); d != Deny {
		t.Error("pii-classified access should hit the classification rule")
	}
	if d, _ := p.Decide("a", "fs", "any", Read, ""); d != Allow {
		t.Error("unclassified access should skip the classification rule")
	}
}

func TestCheck_RecordsDecision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_ledger.jsonl")
	p := Policy{Rules: []Rule{{Tool: "fs", Resource: "/etc/*", Decision: Deny}}, Default: Allow}

	if d, err := Check(path, p, CheckRequest{Agent: "agentA", Tool: "fs", Resource: "/etc/hosts", Action: Write}); err != nil || d != Deny {
		t.Fatalf("Check = %v (err %v), want deny", d, err)
	}
	if d, err := Check(path, p, CheckRequest{Agent: "agentA", Tool: "fs", Resource: "/repo/x.go", Action: Write}); err != nil || d != Allow {
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

func TestHashParams_DeterministicRegardlessOfKeyOrder(t *testing.T) {
	a := map[string]any{"path": "/etc/passwd", "mode": "r"}
	b := map[string]any{"mode": "r", "path": "/etc/passwd"}
	ha, err := HashParams(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := HashParams(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Errorf("hash should not depend on map insertion order: %s != %s", ha, hb)
	}
}

func TestHashParams_DifferentParamsDifferentHash(t *testing.T) {
	h1, _ := HashParams(map[string]any{"path": "/etc/passwd"})
	h2, _ := HashParams(map[string]any{"path": "/etc/shadow"})
	if h1 == h2 {
		t.Error("different params must not hash to the same value")
	}
}

func TestVerifyParams(t *testing.T) {
	approved := map[string]any{"amount": 100, "account": "acct-1"}
	hash, err := HashParams(approved)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := VerifyParams(approved, hash); err != nil || !ok {
		t.Errorf("VerifyParams(same params) = %v, %v, want true, nil", ok, err)
	}
	tampered := map[string]any{"amount": 1000000, "account": "acct-1"}
	if ok, err := VerifyParams(tampered, hash); err != nil || ok {
		t.Errorf("VerifyParams(tampered params) = %v, %v, want false, nil", ok, err)
	}
}

func TestCheck_BindsParametersHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_ledger.jsonl")
	p := Policy{Default: Allow}
	params := map[string]any{"resource": "invoice-42", "amount": 500}

	if _, err := Check(path, p, CheckRequest{Agent: "a", Tool: "billing", Resource: "invoice-42", Action: Write, Params: params}); err != nil {
		t.Fatal(err)
	}
	events, err := Load(path)
	if err != nil || len(events) != 1 {
		t.Fatalf("Load = %v events, err %v", len(events), err)
	}
	wantHash, _ := HashParams(params)
	if events[0].ParametersHash != wantHash {
		t.Errorf("ParametersHash = %q, want %q", events[0].ParametersHash, wantHash)
	}
}

func TestCheck_ClassificationFromContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_ledger.jsonl")
	p := Policy{Rules: []Rule{{Classification: "pii", Decision: Deny}}, Default: Allow}

	d, err := Check(path, p, CheckRequest{Agent: "a", Tool: "export", Resource: "customers.csv", Action: Read,
		Content: "contact me at jane.doe@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if d != Deny {
		t.Errorf("PII content should be classified and denied by the classification rule, got %v", d)
	}
	events, _ := Load(path)
	if len(events) != 1 || events[0].Classification != "pii" {
		t.Fatalf("event classification = %+v, want pii", events)
	}
}

func TestCheck_ExplicitClassificationOverridesContentDetection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_ledger.jsonl")
	p := Policy{Default: Allow}

	if _, err := Check(path, p, CheckRequest{Agent: "a", Tool: "export", Resource: "r", Action: Read,
		Content: "jane.doe@example.com", Classification: "public"}); err != nil {
		t.Fatal(err)
	}
	events, _ := Load(path)
	if events[0].Classification != "public" {
		t.Errorf("explicit Classification should win over content-derived detection, got %q", events[0].Classification)
	}
}

func TestCheck_FailsClosedOnUnhashableParams(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_ledger.jsonl")
	p := Policy{Default: Allow}

	// NaN cannot be JSON-encoded, so the params cannot be bound to the decision.
	d, err := Check(path, p, CheckRequest{Agent: "a", Tool: "t", Resource: "r", Action: Write,
		Params: map[string]any{"bad": math.NaN()}})
	if err == nil {
		t.Fatal("unhashable params should surface an error")
	}
	if d != Deny {
		t.Errorf("Check must fail CLOSED on hash failure, got %q — a caller testing `d == Deny` would proceed", d)
	}
	// The refused attempt must still be accounted for.
	events, _ := Load(path)
	if len(events) != 1 || events[0].Decision != Deny {
		t.Errorf("the denial should itself be recorded, got %+v", events)
	}
}

func TestLatestBound(t *testing.T) {
	events := []Event{
		{Tool: "fs", Resource: "a", ParametersHash: "h1"},
		{Tool: "fs", Resource: "a"}, // no hash — must be skipped
		{Tool: "fs", Resource: "a", ParametersHash: "h2"},
		{Tool: "net", Resource: "b", ParametersHash: "h3"},
	}
	if e, ok := LatestBound(events, "fs", "a"); !ok || e.ParametersHash != "h2" {
		t.Errorf("LatestBound should return the newest bound event (h2), got %+v ok=%v", e, ok)
	}
	if e, ok := LatestBound(events, "", ""); !ok || e.ParametersHash != "h3" {
		t.Errorf("empty tool/resource should match any, got %+v", e)
	}
	if _, ok := LatestBound(events, "missing", ""); ok {
		t.Error("unknown tool should not resolve an approval")
	}
	if _, ok := LatestBound([]Event{{Tool: "fs"}}, "fs", ""); ok {
		t.Error("events without a hash must not be treated as approvals")
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
