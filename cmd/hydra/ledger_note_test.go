// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/trust"
)

// Λ AFTER is the leading answer's total recomputed over every vote, and the LLR
// beside it is one source's contribution to whichever answer led at that
// moment. Read as a running sum they contradict each other, which is how a run
// showing two negative LLRs and a rising Λ reads as broken arithmetic (#997).
func TestLedgerNote_SaysSoOnlyWhenTheColumnDoesNotAddUp(t *testing.T) {
	t.Run("an ordinary run says nothing", func(t *testing.T) {
		got := ledgerNote([]trust.Evidence{
			{Source: "a", LLR: 1.0, LambdaAfter: 1.0, Candidate: "X"},
			{Source: "b", LLR: 0.5, LambdaAfter: 1.5, Candidate: "X"},
		})
		if got != "" {
			t.Errorf("note on a ledger that adds up: %q", got)
		}
	})

	t.Run("a discounted repeat is named as the reason", func(t *testing.T) {
		got := ledgerNote([]trust.Evidence{
			{Source: "a", LLR: 1.312, LambdaAfter: 1.312, Candidate: "X"},
			{Source: "b", LLR: 1.312, LambdaAfter: 1.968, Candidate: "X"},
		})
		if !strings.Contains(got, "family") {
			t.Errorf("note does not name the discount: %q", got)
		}
	})

	t.Run("a leader change is named as the reason", func(t *testing.T) {
		got := ledgerNote([]trust.Evidence{
			{Source: "a", LLR: 1.0, LambdaAfter: 1.0, Candidate: "X"},
			{Source: "b", LLR: -2.0, LambdaAfter: 1.4, Candidate: "Y"},
		})
		if !strings.Contains(got, "leader changed") {
			t.Errorf("note does not name the leader change: %q", got)
		}
	})

	t.Run("an empty ledger says nothing", func(t *testing.T) {
		if got := ledgerNote(nil); got != "" {
			t.Errorf("note on an empty ledger: %q", got)
		}
	})
}
