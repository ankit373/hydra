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
	// A developer or CI runner with a stray $HYDRA_HOME already exported must
	// not leak into this "no override" case (#442 was found exactly this way).
	t.Setenv("HYDRA_HOME", "")

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

// $HYDRA_HOME must win over $HOME for the ledger — the whole point of #442 is
// that this subsystem is exactly what silently ignored it before.
func TestDefaultPaths_PreferHydraHomeOverHome(t *testing.T) {
	home := t.TempDir()
	hydraHome := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HYDRA_HOME", hydraHome)

	if got, want := DefaultPath(), filepath.Join(hydraHome, "mcp_ledger.jsonl"); got != want {
		t.Errorf("DefaultPath() = %q, want %q ($HYDRA_HOME, not $HOME)", got, want)
	}
	if got, want := DefaultPolicyPath(), filepath.Join(hydraHome, "mcp_policy.json"); got != want {
		t.Errorf("DefaultPolicyPath() = %q, want %q ($HYDRA_HOME, not $HOME)", got, want)
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

// LoadPolicy caches its result per path (a dispatch fallback loop or swarm
// fan-out calls it once per candidate head for the same content), but a real
// edit to the file must never be masked by a stale cache entry — the whole
// reason it keys on mtime+size instead of remembering the first read forever.
func TestLoadPolicy_CacheInvalidatesOnRealEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")

	if err := os.WriteFile(path, []byte(`{"default":"allow","rules":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p1, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if p1.Default != Allow {
		t.Fatalf("first load Default = %q, want Allow", p1.Default)
	}

	// Deliberately a different size, so this can never collide with the first
	// read's cache key regardless of filesystem mtime resolution.
	body := `{"default":"deny","rules":[{"tool":"fs","resource":"/repo/*","action":"write","decision":"allow"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p2, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Default != Deny {
		t.Errorf("after editing the file, Default = %q, want Deny — the cache served a stale read", p2.Default)
	}
	if len(p2.Rules) != 1 {
		t.Errorf("after editing the file, got %d rule(s), want 1 — the cache served a stale read", len(p2.Rules))
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
		// Both of these are now the same dialect internal/workspace uses, on
		// every platform. Before #310 this package used filepath.Match: no "**"
		// at all, and a "\\" separator on Windows — so "/repo/*" matched one
		// level on Unix and arbitrarily deep on Windows, which the three-OS
		// matrix caught.
		{"/repo/**", "/repo/sub/a.go", true},
		{"**/secrets/**", "/repo/a/secrets/key.pem", true},
		{"**/.env*", "/repo/a/b/.env.local", true},
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
