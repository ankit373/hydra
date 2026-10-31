// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/egress"
)

// The orchestration protocol's next step after reading a response is to apply
// it to disk, so a response carrying a credential has to say so on the way
// past, not only in the ledger (#740).
func TestPrintOutputWarning(t *testing.T) {
	secret := egress.Part{
		Source: egress.SourceHead, Origin: "ollama:qwen3",
		Sens: egress.Secret, Reasons: []string{"content:aws access key id"},
	}

	out := captureStdout(t, func() { printOutputWarning(secret) })
	if !strings.Contains(out, "aws access key id") {
		t.Errorf("the warning does not name what fired, so it cannot be judged:\n%s", out)
	}
	if !strings.Contains(out, "Review before applying") {
		t.Errorf("the warning does not say what to do about it:\n%s", out)
	}

	// Silence is the common case; a warning on every answer is one nobody reads.
	quiet := captureStdout(t, func() {
		printOutputWarning(egress.Part{Source: egress.SourceHead, Origin: "x"})
	})
	if strings.TrimSpace(quiet) != "" {
		t.Errorf("an ordinary response printed a warning:\n%s", quiet)
	}
}
