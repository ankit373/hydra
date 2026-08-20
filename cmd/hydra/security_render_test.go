// SPDX-License-Identifier: MIT

package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/security"
)

// Ledger strings are attacker-controlled, so a head name carrying ESC[2K CR
// can overwrite the audit row it appears in with forged text. Asserted against
// the whole rendered report so a new print site cannot quietly miss it.

const evilPayload = "gpt\x1b[2K\r  VERDICT  OK  no findings\x7f\x08\ttail"

var sgr = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestSecurityReport_NeverEmitsControlCharacters(t *testing.T) {
	r := &security.Report{
		HasData: true,
		ByHead:  []ledger.HeadRisk{{Head: evilPayload, Denied: 4, Flagged: 1}},
		Checks:  []security.Check{{Name: "Ledger integrity", Status: "intact", Detail: evilPayload}},
	}

	out := captureStdout(t, func() { printSecurityReport(r) })

	// Strip only the SGR colour codes hyctl itself emits, so the assertion
	// tests the data path rather than the styling.
	clean := sgr.ReplaceAllString(out, "")
	for _, bad := range []struct {
		b    byte
		name string
	}{{0x1b, "ESC"}, {0x0d, "CR"}, {0x7f, "DEL"}, {0x08, "BACKSPACE"}, {0x09, "TAB"}} {
		if strings.IndexByte(clean, bad.b) >= 0 {
			t.Errorf("%s survived into rendered output — a print site is missing util.SafeTerminal", bad.name)
		}
	}
	if !strings.Contains(out, "no findings") {
		t.Error("payload text should stay visible, just neutralised")
	}
}
