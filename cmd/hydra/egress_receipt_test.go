// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/testutil"
)

// After a redacted dispatch the receipt names the provider and the counts, and
// never the made-up value.
func TestEgressReceipt_LastRun(t *testing.T) {
	testutil.NewSandbox(t)
	litellmHeads(t, map[string]string{"a": "Sent to <<EMAIL_1>>."}, "summary.general")
	if _, _, err := run(t, "dispatch", "--no-stream", "--redact", "reply to sam.lee@example.com"); err != nil {
		t.Fatal(err)
	}
	out, cobraOut, err := run(t, "egress", "receipt")
	if err != nil {
		t.Fatalf("receipt: %v\n%s", err, cobraOut)
	}
	for _, want := range []string{"litellm/a", "remote", "provider litellm", "EMAIL 1", "restored: 1", "chain: verified"} {
		if !strings.Contains(out, want) {
			t.Errorf("receipt lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sam.lee") {
		t.Error("the receipt printed the redacted value")
	}
}

func TestEgressReceipt_JSON(t *testing.T) {
	testutil.NewSandbox(t)
	recordExec(t, "run-old", "x")
	recordExec(t, "run-new", "y")
	out, _, err := run(t, "egress", "receipt", "run-old", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rc egressReceipt
	if err := json.Unmarshal([]byte(out), &rc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if rc.RunID != "run-old" || len(rc.Heads) != 1 || rc.Heads[0].Head != "x" || rc.Heads[0].Redacted["IBAN"] != 1 || !rc.ChainIntact {
		t.Errorf("receipt = %+v", rc)
	}
}

func TestEgressReceipt_ShowsBrokenChain(t *testing.T) {
	testutil.NewSandbox(t)
	recordExec(t, "run-1", "x")
	recordExec(t, "run-1", "y")
	raw, err := os.ReadFile(ledger.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledger.DefaultPath(), []byte(strings.Replace(string(raw), `"IBAN":1`, `"IBAN":0`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, "egress", "receipt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "BROKEN") {
		t.Errorf("an edited ledger still reads as verified:\n%s", out)
	}
}

func recordExec(t *testing.T, runID, head string) {
	t.Helper()
	if err := ledger.Record(ledger.DefaultPath(), ledger.Event{
		Agent: "hydra-dispatch", Tool: head, Action: ledger.Exec, Decision: ledger.Allow,
		Provenance: &ledger.EventProvenance{RunID: runID, Provider: "openai", Sink: "remote", Redacted: map[string]int{"IBAN": 1}},
	}); err != nil {
		t.Fatal(err)
	}
}
