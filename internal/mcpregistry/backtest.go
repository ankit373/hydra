// SPDX-License-Identifier: MIT

package mcpregistry

import "context"

// KnownIncident is one documented, real MCP-security incident used to prove
// the scoring pipeline actually catches what it was built to catch — the
// design doc's §13.4 gate: don't publish a public score (Phase 3) until this
// passes. Each entry here has an independently-verifiable source; this list
// is not a general threat-intel feed and isn't meant to grow into one.
type KnownIncident struct {
	Name         string
	RegistryType string
	Identifier   string
	Description  string
}

// KnownIncidents is intentionally short: each entry is a specific, checkable
// real-world event, hand-verified against OSV.dev at the time it was added
// here (registered by `MAL-2025-47604` and `CVE-2025-6514` respectively) —
// not a scraped or speculative list.
var KnownIncidents = []KnownIncident{
	{
		Name:         "postmark-mcp rug-pull",
		RegistryType: "npm",
		Identifier:   "postmark-mcp",
		Description:  "ran clean for 15 npm versions, then v1.0.16 silently BCC'd every outgoing email to an attacker domain (Sept 2025) — the incident that motivated this registry's version-bump automaton",
	},
	{
		Name:         "mcp-remote OS command injection",
		RegistryType: "npm",
		Identifier:   "mcp-remote",
		Description:  "CVE-2025-6514: a crafted authorization_endpoint redirect from an untrusted MCP server achieves OS command injection",
	},
}

// BacktestResult is one incident's outcome against the live pipeline.
type BacktestResult struct {
	Incident KnownIncident `json:"incident"`
	Caught   bool          `json:"caught"`
	Detail   string        `json:"detail"`
}

// Backtest runs every KnownIncident's package through the real
// knownBadSignal check and reports whether today's pipeline still flags it.
// This is a point-in-time proof, not a guarantee — OSV.dev's data could
// change — which is exactly why this is a command to re-run before a launch
// decision, not a one-time claim to trust forever.
func Backtest(ctx context.Context) []BacktestResult {
	results := make([]BacktestResult, 0, len(KnownIncidents))
	for _, inc := range KnownIncidents {
		sig := knownBadSignal(ctx, Package{RegistryType: inc.RegistryType, Identifier: inc.Identifier})
		results = append(results, BacktestResult{
			Incident: inc,
			Caught:   sig.Available && sig.Impact < 0,
			Detail:   sig.Detail,
		})
	}
	return results
}

// TyposquatBacktestPair is the OX Security-style near-duplicate pattern this
// validates: an existing registered identifier and a plausible typosquat of
// it. Modeled on the real published attack (OX Security's "Malicious Trial
// Balloon": a typosquatted clone of a Postgres MCP server accepted by 9 of
// 11 public registries with zero review) — not a claim that this exact pair
// is live in the production registry today, which isn't something to probe.
type TyposquatBacktestPair struct {
	Registered string
	Typosquat  string
}

var typosquatBacktestPairs = []TyposquatBacktestPair{
	{Registered: "mcp-server-postgres", Typosquat: "mcp-server-postgress"},
	{Registered: "mcp-server-github", Typosquat: "mcp-server-githup"},
}

// TyposquatBacktestResult is one pair's outcome against the edit-distance
// detector.
type TyposquatBacktestResult struct {
	Pair     TyposquatBacktestPair `json:"pair"`
	Detected bool                  `json:"detected"`
	Distance int                   `json:"distance"`
}

// BacktestTyposquat validates the near-duplicate detector algorithm itself
// against pairs modeled on the real published attack pattern, using a
// constructed two-entry corpus rather than the live registry.
func BacktestTyposquat() []TyposquatBacktestResult {
	results := make([]TyposquatBacktestResult, 0, len(typosquatBacktestPairs))
	for _, pair := range typosquatBacktestPairs {
		target := ServerRecord{Name: "candidate", Packages: []Package{{Identifier: pair.Typosquat}}}
		corpus := []ServerRecord{
			target,
			{Name: "registered", Packages: []Package{{Identifier: pair.Registered}}},
		}
		sig := typosquatSignal(target, corpus)
		results = append(results, TyposquatBacktestResult{
			Pair:     pair,
			Detected: sig.Available && sig.Impact < 0,
			Distance: levenshtein(pair.Registered, pair.Typosquat),
		})
	}
	return results
}
