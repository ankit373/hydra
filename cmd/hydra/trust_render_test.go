// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/swarm"
	"github.com/ankit373/hydra/internal/trust"
)

// A confidence run that never left the prior looks identical to one that met
// its target unless something says so. Observed live: five heads sampled, every
// LLR +0.000, "stopped_on_budget", 50.0% (#732).
func TestSPRTWarning_FiresOnlyWhenNoSourceCarriedEvidence(t *testing.T) {
	zero := &swarm.SPRTResult{Domain: "go", Trust: &trust.Result{Ledger: []trust.Evidence{
		{Source: "claude", LLR: 0}, {Source: "codex", LLR: 0},
	}}}
	got := sprtWarning(zero)
	if got == "" {
		t.Fatal("a ledger of all-zero LLRs is the failure this warning exists for")
	}
	for _, want := range []string{"go", "never left the prior", "oracle verify"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning does not mention %q: %s", want, got)
		}
	}

	// Real evidence, even a little, is not this failure.
	some := &swarm.SPRTResult{Domain: "go", Trust: &trust.Result{Ledger: []trust.Evidence{
		{Source: "claude", LLR: 0}, {Source: "codex", LLR: 0.272},
	}}}
	if w := sprtWarning(some); w != "" {
		t.Errorf("warned on a run that did carry evidence: %s", w)
	}
}

func TestSPRTWarning_SilentWithNothingToJudge(t *testing.T) {
	if w := sprtWarning(&swarm.SPRTResult{}); w != "" {
		t.Errorf("warned with no trust result: %s", w)
	}
	if w := sprtWarning(&swarm.SPRTResult{Trust: &trust.Result{}}); w != "" {
		t.Errorf("warned on an empty ledger: %s", w)
	}
}

// "samples saved 47%" reads as a win directly above a number saying nothing was
// learned. Both conditions are required, because a run can legitimately land
// near 50% after real evidence cancelled out.
func TestStalledConfidence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		final      float64
		autoClear  float64
		wantWarned bool
	}{
		{"never moved, nothing cleared", 0.501, 0, true},
		{"exactly the prior", 0.5, 0, true},
		{"moved and cleared", 0.847, 40, false},
		{"at the prior but some run cleared", 0.501, 5, false},
		{"pushed below the prior by real disagreement", 0.30, 0, false},
		{"pushed above the prior", 0.62, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := stalledConfidence(tc.final, tc.autoClear)
			if (got != "") != tc.wantWarned {
				t.Errorf("stalledConfidence(%v, %v) = %q, wantWarned=%v",
					tc.final, tc.autoClear, got, tc.wantWarned)
			}
		})
	}
}

// The one message whose whole job is to be actionable printed two commands
// ending in a bare `--domain ` with no value.
func TestNoEvidenceError_PrintsCommandsThatCanBeRun(t *testing.T) {
	msg := noEvidenceError(trust.DefaultDomain).Error()

	if strings.Contains(msg, `--domain "`) || strings.Contains(msg, "--domain \n") {
		t.Errorf("a suggested command names an empty domain:\n%s", msg)
	}
	for _, line := range strings.Split(msg, "\n") {
		i := strings.Index(line, "--domain ")
		if i < 0 {
			continue
		}
		rest := strings.TrimSpace(line[i+len("--domain "):])
		if rest == "" || strings.HasPrefix(rest, "-") {
			t.Errorf("`--domain` has no value in a copy-pasteable line: %q", line)
		}
	}
	if !strings.Contains(msg, trust.DefaultDomain) {
		t.Errorf("the domain is not named:\n%s", msg)
	}
}

// The refusal tests whether the heads THIS RUN would sample carry evidence, so
// the message must not answer the different question of which domains have any
// evidence at all. Asking for a domain that has evidence from another source
// was refused directly above a list containing that same domain.
func TestNoEvidenceError_DoesNotContradictItself(t *testing.T) {
	msg := noEvidenceError("gotest").Error()
	if i := strings.Index(msg, "Other domains with evidence:"); i >= 0 {
		others := msg[i:]
		if idx := strings.Index(others, "\n"); idx >= 0 {
			others = others[:idx]
		}
		if strings.Contains(others, "gotest") {
			t.Errorf("the refused domain is listed as having evidence:\n%s", msg)
		}
	}
	if !strings.Contains(msg, "no head this run would sample") {
		t.Errorf("the message still claims nothing at all can judge the domain:\n%s", msg)
	}
}
