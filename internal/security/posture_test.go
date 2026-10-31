// SPDX-License-Identifier: MIT

package security

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
)

// Every verdict must name the condition that produced it, a top line nobody
// can interrogate is just a mood.
func TestAssessPosture_TriggersAreNamed(t *testing.T) {
	cases := []struct {
		name    string
		report  *Report
		chain   ledger.ChainResult
		want    Verdict
		mustSay string
	}{
		{"truncated ledger", &Report{}, ledger.ChainResult{Chained: 3, Truncated: true},
			VerdictActNow, "truncated"},
		{"confirmed leak", &Report{Exposures: []Exposure{{Remote: true, Known: true}}},
			ledger.ChainResult{Intact: true}, VerdictActNow, "remote head"},
		{"critical incident", &Report{Incidents: []Incident{{Severity: SeverityCritical, Narrative: "n"}}},
			ledger.ChainResult{Intact: true}, VerdictActNow, "critical incident"},
		{"inert control", &Report{Controls: []Control{{Declared: true, Wired: false}}},
			ledger.ChainResult{Intact: true}, VerdictAttention, "cannot fire"},
		{"clean", &Report{}, ledger.ChainResult{Intact: true}, VerdictOK, "no condition fired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := AssessPosture(tc.report, tc.chain)
			if p.Verdict != tc.want {
				t.Errorf("verdict = %q, want %q", p.Verdict, tc.want)
			}
			if !strings.Contains(p.Trigger, tc.mustSay) {
				t.Errorf("trigger = %q, want it to mention %q", p.Trigger, tc.mustSay)
			}
		})
	}
}

// A fail-open default on a machine denying nothing is a default, not a
// finding, otherwise every fresh install cries wolf on first run.
func TestAssessPosture_FailOpenOnlyMattersWithDenials(t *testing.T) {
	quiet := AssessPosture(&Report{PolicyAudit: PolicyAudit{FailOpen: true, Rules: []RuleStat{{}}}},
		ledger.ChainResult{Intact: true})
	if quiet.Verdict != VerdictOK {
		t.Errorf("verdict = %q on a quiet machine, want ok", quiet.Verdict)
	}

	busy := AssessPosture(&Report{
		PolicyAudit: PolicyAudit{FailOpen: true, Rules: []RuleStat{{}}},
		Ledger:      LedgerPanel{Denied: 3},
	}, ledger.ChainResult{Intact: true})
	if busy.Verdict != VerdictAttention {
		t.Errorf("verdict = %q with denials occurring, want attention", busy.Verdict)
	}
}

// An OK verdict has to state what it actually checked, so it is not read as
// "everything imaginable is fine".
func TestAssessPosture_OKStatesItsScope(t *testing.T) {
	p := AssessPosture(&Report{}, ledger.ChainResult{Intact: true})
	if len(p.Checked) == 0 {
		t.Fatal("an ok verdict listed nothing it checked")
	}
}
