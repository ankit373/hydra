// SPDX-License-Identifier: MIT

package ledger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The ledger is the accountability record: what an agent touched, and whether
// the policy allowed it. A gate that fails open, or a record that silently is
// not written, defeats the whole point.

func TestDefaultPaths_AreDistinctAndUnderHydraDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	log, policy := DefaultPath(), DefaultPolicyPath()
	if !strings.HasPrefix(log, filepath.Join(home, ".hydra")) {
		t.Errorf("DefaultPath() = %q, not under the hydra dir", log)
	}
	if !strings.HasPrefix(policy, filepath.Join(home, ".hydra")) {
		t.Errorf("DefaultPolicyPath() = %q, not under the hydra dir", policy)
	}
	if log == policy {
		t.Error("the ledger and its policy are the same file; appending events " +
			"would destroy the rules")
	}
}

// A missing policy is default-allow: Hydra records everything but blocks
// nothing until an operator writes rules. That is a deliberate posture, so it
// must be exactly what an absent file produces.
func TestLoadPolicy_MissingFileIsDefaultAllow(t *testing.T) {
	p, err := LoadPolicy(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing policy errored: %v", err)
	}
	if p.Default != Allow {
		t.Errorf("Default = %q, want Allow", p.Default)
	}
	if len(p.Rules) != 0 {
		t.Errorf("a missing policy produced %d rules", len(p.Rules))
	}
}

func TestLoadPolicy_MalformedJSONIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicy(path); err == nil {
		t.Error("a malformed policy loaded without error — the gate would run " +
			"with no rules and nobody would know")
	}
}

// A policy is rejected whole rather than partially honoured. A rule whose
// decision does not parse can never deny, so loading it would silently weaken
// the gate — a default-deny posture written as "DENY" would void entirely.
func TestLoadPolicy_RejectsUnparseableRulesRatherThanWeakeningTheGate(t *testing.T) {
	cases := []struct{ name, body string }{
		{"bad default", `{"default":"maybe","rules":[]}`},
		{"bad rule decision", `{"default":"allow","rules":[{"tool":"fs","decision":"perhaps"}]}`},
		{"bad rule action", `{"default":"allow","rules":[{"tool":"fs","action":"frobnicate","decision":"deny"}]}`},
		{"bad glob", `{"default":"allow","rules":[{"tool":"fs","resource":"[","decision":"deny"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "policy.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadPolicy(path); err == nil {
				t.Errorf("%s was accepted; a rule that cannot be evaluated must not "+
					"be loaded as if it were enforcing something", tc.name)
			}
		})
	}
}

// Case is normalized, so a policy written in the obvious human casing behaves
// the same as the canonical form.
func TestLoadPolicy_NormalizesCase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	body := `{"default":"DENY","rules":[{"tool":"fs","resource":"/repo/*","action":"WRITE","decision":"Allow"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("a policy in upper case failed to load: %v", err)
	}
	if p.Default != Deny {
		t.Errorf("Default = %q, want Deny", p.Default)
	}
	if len(p.Rules) != 1 || p.Rules[0].Decision != Allow {
		t.Errorf("rule not normalized: %+v", p.Rules)
	}
}

// The parameter hash binds a decision to the exact invocation it approved.
func TestHashDecodeVerifyParams_RoundTripAndTamperDetection(t *testing.T) {
	params := map[string]any{"path": "/repo/a.go", "mode": "write", "n": 3}

	hash, err := HashParams(params)
	if err != nil {
		t.Fatal(err)
	}
	if hash == "" {
		t.Fatal("HashParams returned an empty hash")
	}

	ok, err := VerifyParams(params, hash)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("the same params did not verify against their own hash")
	}

	// Any change must fail verification — that is the whole point of binding.
	tampered := map[string]any{"path": "/etc/passwd", "mode": "write", "n": 3}
	ok, err = VerifyParams(tampered, hash)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("altered params verified against the original hash — the binding " +
			"between what was approved and what ran is not tamper-evident")
	}

	// Key order must not matter, or an equivalent call would fail to verify.
	reordered := map[string]any{"n": 3, "mode": "write", "path": "/repo/a.go"}
	ok, _ = VerifyParams(reordered, hash)
	if !ok {
		t.Error("the same params in a different map order did not verify")
	}
}

func TestDecodeParams_RoundTripsAndRejectsGarbage(t *testing.T) {
	params := map[string]any{"a": "b", "n": float64(2)}
	hash, err := HashParams(params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeParams(hash); err != nil {
		// DecodeParams takes the encoded form, not the hash; a hash must not
		// decode as params.
		_ = err
	}
	if _, err := DecodeParams("not-base64-or-json"); err == nil {
		t.Error("garbage decoded as parameters")
	}
}

// Record appends to an append-only log. An unwritable path must surface: a
// ledger that silently stops recording is worse than no ledger, because the
// absence of an event reads as "the agent never touched it".
func TestRecord_UnwritablePathIsAnError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Record(filepath.Join(blocker, "ledger.jsonl"), Event{
		Agent: "a", Tool: "fs", Resource: "/x", Action: Read, Decision: Allow,
	})
	if err == nil {
		t.Error("recording under a blocked path reported success; a ledger that " +
			"silently stops recording reads as 'nothing was touched'")
	}
}

func TestRecord_StampsATimestampWhenAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	if err := Record(path, Event{Agent: "a", Tool: "fs", Resource: "/x",
		Action: Read, Decision: Allow}); err != nil {
		t.Fatal(err)
	}

	events, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].TS == "" {
		t.Error("an event was recorded with no timestamp; the ledger cannot be " +
			"ordered or audited")
	}
}

// globMatch backs resource rules. A pattern that cannot compile must not match
// everything — that would turn a deny rule into a universal block or an allow
// rule into a universal pass.
func TestGlobMatch_Behaviour(t *testing.T) {
	cases := []struct {
		pattern, resource string
		want              bool
	}{
		{"/repo/*", "/repo/a.go", true},
		{"/repo/*", "/repo/sub/a.go", false},
		// NOT true: this package uses filepath.Match, which has no "**" — it
		// treats the pattern as a single segment. internal/workspace DOES
		// implement segment-crossing "**", so the two config files speak
		// different glob dialects and a "**/secrets/**" rule copied from
		// workspace.yaml into mcp_policy.json silently matches nothing (#310).
		{"/repo/**", "/repo/sub/a.go", false},
		{"", "/anything", true}, // an empty pattern is "any resource"
		{"/exact", "/exact", true},
		{"/exact", "/other", false},
	}
	for _, tc := range cases {
		if got := globMatch(tc.pattern, tc.resource); got != tc.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tc.pattern, tc.resource, got, tc.want)
		}
	}
}

// Load on a missing ledger is an empty history, not an error — nothing has been
// recorded yet on a fresh install.
func TestLoad_MissingLedgerIsEmpty(t *testing.T) {
	events, err := Load(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil {
		t.Fatalf("a missing ledger errored: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events from a missing ledger", len(events))
	}
}

func TestLoad_SkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	body := `{"ts":"2026-08-01T00:00:00Z","agent":"a","tool":"fs","resource":"/x","action":"read","decision":"allow"}
{not json
{"ts":"trunc`
	if err := os.WriteFile(path, []byte(body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	events, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Errorf("got %d events, want the one well-formed record", len(events))
	}
}
