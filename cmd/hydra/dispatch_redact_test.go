// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// The hosted stub never sees the made-up email; the printed answer has it back.
func TestDispatchRedact_HostedHeadSeesPlaceholdersOnly(t *testing.T) {
	testutil.NewSandbox(t)
	f := litellmHeads(t, map[string]string{"a": "Sent to <<EMAIL_1>>."}, "summary.general")

	out, cobraOut, err := run(t, "dispatch", "--no-stream", "--redact", "reply to sam.lee@example.com about the refund")
	if err != nil {
		t.Fatalf("dispatch: %v\n%s%s", err, out, cobraOut)
	}
	if sent := f.prompt("a"); strings.Contains(sent, "sam.lee@example.com") || !strings.Contains(sent, "<<EMAIL_1>>") {
		t.Errorf("the hosted head was sent %q", sent)
	}
	if !strings.Contains(out, "Sent to sam.lee@example.com.") || !strings.Contains(out, "redacted:") {
		t.Errorf("printed answer is not restored or says nothing of redaction:\n%s", out)
	}
}

func TestDispatchRedact_RefusesAnEnsemble(t *testing.T) {
	testutil.NewSandbox(t)
	litellmHeads(t, map[string]string{"a": "ok"}, "summary.general")
	if _, _, err := run(t, "dispatch", "--redact", "--swarm", "x"); err == nil ||
		!strings.Contains(err.Error(), "--redact runs a single dispatch") {
		t.Errorf("--redact --swarm = %v, want a refusal", err)
	}
}
