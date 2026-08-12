// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/security"
)

// Every string in this report comes from the ledger, and the ledger records
// what an agent sent — a tool name, a resource path, a flag reason. All of it
// is attacker-controlled.
//
// ESC[2K erases the line it is printed on and CR returns the cursor, so a tool
// named "gpt\x1b[2K\r  VERDICT  OK" overwrites the finding it appears in with
// a forged all-clear. The hash chain cannot catch this: nothing was tampered
// with, the content that arrived was simply hostile.
//
// So this test is deliberately written against the *output*, not against any
// one printer. A new print site that forgets to sanitise fails here, which is
// the only protection that survives someone adding a column in six months.

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

	// Checked as raw bytes, not runes: a terminal consumes bytes, and 0x9b is
	// a single-byte CSI on anything decoding C1. ContainsRune would look for
	// the UTF-8 encoding of U+009B (0xc2 0x9b) and miss the byte that actually
	// does the damage.
	//
	// \n is how the report is laid out, so it is the one control byte that
	// legitimately appears. Everything else moves the cursor.
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

// stripStyles removes the SGR colour sequences lipgloss itself emits, so the
// assertion above tests the data path rather than the styling.
//
// It matches ONLY a well-formed SGR sequence — ESC [ digits and semicolons m.
// Scanning ahead for the next 'm' instead would let an injected "ESC[2K" eat
// every character up to some unrelated 'm' later in the line, silently
// swallowing the very escapes this test exists to find. That is not
// hypothetical: the greedy version reported one failure where there were five.
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
