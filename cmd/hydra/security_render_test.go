// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/security"
)

// Ledger strings are attacker-controlled, so a tool name carrying ESC[2K CR
// can overwrite the finding it appears in with a forged all-clear. Asserted
// against the whole rendered output so a new print site cannot miss it.

const evilPayload = "gpt\x1b[2K\r  VERDICT  OK  no findings\x9b1m\x7f\x08\ttail"

// hostileReport puts the payload in every field that reaches a terminal.
func hostileReport() *security.Report {
	return &security.Report{
		HasData: true,
		Posture: security.Posture{
			Verdict: security.VerdictActNow,
			Trigger: "critical incident — " + evilPayload,
			Because: []string{"critical incident — " + evilPayload},
		},
		Incidents: []security.Incident{{
			ID: "x", Actor: evilPayload, Severity: security.SeverityCritical,
			Start: "2026-08-12T10:00:00Z", End: "2026-08-12T10:30:00Z",
			Narrative: evilPayload + ": it escalated to an exec/network action.",
			Events:    []ledger.Event{{TS: "2026-08-12T10:00:00Z", Tool: evilPayload}},
		}},
		Register: security.RiskRegister{Risks: []security.Risk{{
			ID: "R-deadbeef", Class: security.ClassIncident, Title: evilPayload,
			Detail: evilPayload, Severity: security.SeverityCritical, Status: security.StatusOpen,
		}}},
		Checks: []security.Check{{
			Name: "Correlated incidents", Status: "1 incident(s)", Detail: evilPayload,
		}},
		Controls: []security.Control{{
			Name: "Injection scanning", Detail: evilPayload,
		}},
		Exposures: []security.Exposure{{
			TS: "2026-08-12T10:00:00Z", Agent: evilPayload, Head: evilPayload,
			Resource: evilPayload, Remote: true, Known: true, PIITypes: []string{"email"},
		}},
		Threats: security.Threats{
			ByMarker:        []security.Count{{Label: evilPayload, Count: 2}},
			ProbedResources: []security.Count{{Label: evilPayload, Count: 3}},
		},
		PolicyAudit: security.PolicyAudit{
			Rules: []security.RuleStat{{Index: 0, Summary: evilPayload, Decision: "deny", Hits: 1}},
		},
		ByHead: []ledger.HeadRisk{{Head: evilPayload, Denied: 4}},
		Actions: []security.Action{{
			ID: "a1", Kind: "risk", Title: evilPayload, Detail: evilPayload,
			AgeDays: 3, Priority: security.PriorityNow,
		}},
	}
}

func TestSecurityPrinters_NeverEmitControlCharacters(t *testing.T) {
	r := hostileReport()

	// Deliberately the whole report renderer, not a hand-picked list of
	// printers. An earlier version of this test called seven printers by name
	// and passed while the per-head table and the action queue were both still
	// emitting raw escapes — the list drifted from the renderer immediately.
	out := captureStdout(t, func() { printSecurityReport(r) })

	// Raw bytes, not runes: 0x9b is a single-byte CSI and ContainsRune would
	// look for its UTF-8 form instead. \n is the one control byte that
	// legitimately appears in a laid-out report.
	stripped := stripStyles(out)
	for _, bad := range []struct {
		b    byte
		name string
	}{
		{0x1b, "ESC"}, {0x0d, "CR"}, {0x7f, "DEL"}, {0x9b, "C1 CSI"}, {0x09, "TAB"}, {0x08, "BACKSPACE"},
	} {
		if strings.IndexByte(stripped, bad.b) >= 0 {
			t.Errorf("%s survived into rendered output — a print site is missing util.SafeTerminal", bad.name)
		}
	}

	// The payload text must still be visible: sanitising is not censoring,
	// and an operator needs to see what the attacker actually sent.
	if !strings.Contains(out, "no findings") {
		t.Error("payload text was dropped entirely; it should be shown, just neutralised")
	}
}

// Strips only well-formed SGR (ESC [ digits m). Scanning to the next 'm'
// instead lets an injected "ESC[2K" swallow the escapes this test looks for —
// the greedy version reported one failure where there were five.
func stripStyles(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] == ';' || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			if j < len(s) && s[j] == 'm' {
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
