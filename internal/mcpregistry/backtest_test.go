// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The test suite stubs OSV.dev rather than depending on the network, the
// real API was hand-verified live while building this (postmark-mcp:
// MAL-2025-47604, mcp-remote: CVE-2025-6514), see KnownIncidents' doc
// comment. This test proves the plumbing (Backtest calls knownBadSignal
// correctly and reports Caught accurately), not that OSV.dev's data hasn't
// changed since, that's what re-running the real command before a launch
// decision is for.
func TestBacktest_ReportsCaughtWhenOSVHasAnAdvisory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Package struct{ Name string } `json:"package"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Package.Name == "postmark-mcp" {
			_ = json.NewEncoder(w).Encode(osvResponse{Vulns: []osvVuln{{ID: "MAL-2025-47604"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(osvResponse{})
	}))
	defer srv.Close()
	orig := osvQueryURL
	osvQueryURL = srv.URL
	defer func() { osvQueryURL = orig }()

	results := Backtest(context.Background())
	if len(results) != len(KnownIncidents) {
		t.Fatalf("got %d results, want %d", len(results), len(KnownIncidents))
	}

	byName := map[string]BacktestResult{}
	for _, r := range results {
		byName[r.Incident.Name] = r
	}
	if !byName["postmark-mcp rug-pull"].Caught {
		t.Error("postmark-mcp should be Caught when OSV.dev has an advisory for it")
	}
	if byName["mcp-remote OS command injection"].Caught {
		t.Error("mcp-remote should not be Caught when the stub returns no advisory for it")
	}
}

func TestBacktest_ReportsMissedWhenOSVHasNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(osvResponse{})
	}))
	defer srv.Close()
	orig := osvQueryURL
	osvQueryURL = srv.URL
	defer func() { osvQueryURL = orig }()

	for _, r := range Backtest(context.Background()) {
		if r.Caught {
			t.Errorf("%s: Caught=true with no advisory data available", r.Incident.Name)
		}
	}
}

func TestBacktestTyposquat_DetectsKnownPatternPairs(t *testing.T) {
	for _, r := range BacktestTyposquat() {
		if !r.Detected {
			t.Errorf("%s vs %s (%d edits): expected the typosquat detector to flag this pair", r.Pair.Registered, r.Pair.Typosquat, r.Distance)
		}
		if r.Distance > 3 {
			t.Errorf("%s vs %s: distance %d is suspiciously large for a claimed typosquat pair, check the fixture", r.Pair.Registered, r.Pair.Typosquat, r.Distance)
		}
	}
}

func TestKnownIncidents_TableIsWellFormed(t *testing.T) {
	if len(KnownIncidents) == 0 {
		t.Fatal("KnownIncidents must not be empty, an empty backtest silently reports nothing")
	}
	for _, inc := range KnownIncidents {
		if inc.Name == "" || inc.Identifier == "" || inc.RegistryType == "" || inc.Description == "" {
			t.Errorf("incomplete KnownIncident entry: %+v", inc)
		}
	}
}
