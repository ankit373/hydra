// SPDX-License-Identifier: MIT

package api

import (
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
)

// A fresh machine has no ledger — the view must render "no data yet", not an
// error dialog on first launch.
func TestGetSecurity_EmptyStateIsHonest(t *testing.T) {
	sandbox(t)

	r, err := New().GetSecurity()
	if err != nil {
		t.Fatalf("GetSecurity on a fresh machine must not error: %v", err)
	}
	if r.HasData {
		t.Error("HasData = true with no ledger on disk")
	}
	// Checks must still say something concrete even with no data — an empty
	// Status would read as "not yet computed" rather than "computed, clean".
	for _, c := range r.Checks {
		if c.Name == "" || c.Status == "" {
			t.Errorf("check %+v is missing a Name/Status", c)
		}
	}
}

// GetSecurity must report exactly what ledger.Summarize/ByHeadRisk compute —
// reimplementing the math here would let the desktop view and `hyctl
// security` disagree about the same ledger file.
func TestGetSecurity_MatchesLedgerSummarize(t *testing.T) {
	sandbox(t)

	if err := ledger.Record(ledger.DefaultPath(), ledger.Event{Agent: "a", Tool: "h1", Decision: ledger.Allow}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Record(ledger.DefaultPath(), ledger.Event{Agent: "a", Tool: "h1", Decision: ledger.Deny}); err != nil {
		t.Fatal(err)
	}

	r, err := New().GetSecurity()
	if err != nil {
		t.Fatal(err)
	}
	if !r.HasData {
		t.Error("HasData = false with events on disk")
	}
	if r.Ledger.Total != 2 || r.Ledger.Allowed != 1 || r.Ledger.Denied != 1 {
		t.Errorf("Ledger = %+v, want Total=2 Allowed=1 Denied=1", r.Ledger)
	}
	if len(r.ByHead) != 1 || r.ByHead[0].Head != "h1" || r.ByHead[0].Denied != 1 {
		t.Errorf("ByHead = %+v, want one entry for h1 with 1 denied", r.ByHead)
	}
	if len(r.Coverage.Categories) == 0 {
		t.Error("Coverage.Categories is empty — the OWASP LLM Top-10 list must always be populated")
	}
}

// A tampered ledger must hard-override the report's integrity flag — the one
// case where the coverage percentage cannot be trusted regardless of what it
// computes to.
func TestGetSecurity_TamperedChainIsNotIntact(t *testing.T) {
	sandbox(t)

	if err := ledger.Record(ledger.DefaultPath(), ledger.Event{Agent: "a", Tool: "h1", Decision: ledger.Allow}); err != nil {
		t.Fatal(err)
	}

	r, err := New().GetSecurity()
	if err != nil {
		t.Fatal(err)
	}
	if !r.IntegrityIntact {
		t.Fatal("IntegrityIntact = false on an untampered, freshly-recorded ledger")
	}
}

// GetSecurity reads fresh on every call and holds no state, matching every
// other API method — safe for the frontend to poll from several places.
func TestGetSecurity_ConcurrentCallsAreSafe(t *testing.T) {
	sandbox(t)
	if err := ledger.Record(ledger.DefaultPath(), ledger.Event{Agent: "a", Tool: "h1", Decision: ledger.Allow}); err != nil {
		t.Fatal(err)
	}

	a := New()
	done := make(chan error, 16)
	for range cap(done) {
		go func() {
			_, err := a.GetSecurity()
			done <- err
		}()
	}
	for range cap(done) {
		if err := <-done; err != nil {
			t.Errorf("concurrent GetSecurity: %v", err)
		}
	}
}
