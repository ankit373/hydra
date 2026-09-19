// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"
)

// A dispatch writes nothing to disk, so the workspace verifier judged the
// repository as it already was and never saw the answer. Training calibration
// and scoring the span on that verdict was confident false evidence about every
// head that voted (#982), so the flag refuses rather than producing it.
func TestDispatch_VerifyIsRefused(t *testing.T) {
	cliSandbox(t)

	_, _, err := run(t, "dispatch", "--confidence", "0.9", "--verify", "does this hold?")
	if err == nil {
		t.Fatal("--verify was accepted; it cannot judge a dispatch's answer")
	}
	msg := err.Error()
	// The refusal has to say what to use instead, or it is a dead end for
	// someone following the old documented flow.
	for _, want := range []string{"never written to disk", "hyctl edit", "hyctl oracle verify"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not mention %q:\n%s", want, msg)
		}
	}
}

// Refused before anything routes, so nobody pays for a run whose verdict would
// mean nothing. A sandbox has no heads, so a refusal that came later would read
// as "no heads available" instead.
func TestDispatch_VerifyIsRefusedBeforeRouting(t *testing.T) {
	cliSandbox(t)

	_, _, err := run(t, "dispatch", "--confidence", "0.9", "--verify", "--enum", "SIMPLE", "q")
	if err == nil {
		t.Fatal("--verify was accepted")
	}
	if !strings.Contains(err.Error(), "--verify cannot judge a dispatch") {
		t.Errorf("failed for some other reason, so the refusal is not the early one:\n%s", err)
	}
}

// An unknown --enum is still reported first: that check exists because a typo
// silently routed to the most expensive head, and --verify must not mask it.
func TestDispatch_UnknownEnumStillBeatsTheVerifyRefusal(t *testing.T) {
	cliSandbox(t)

	_, _, err := run(t, "dispatch", "--confidence", "0.9", "--verify", "--enum", "NOPE", "q")
	if err == nil {
		t.Fatal("an unknown enum was accepted")
	}
	if !strings.Contains(err.Error(), "unknown --enum") {
		t.Errorf("err = %v, want the unknown-enum report", err)
	}
}
