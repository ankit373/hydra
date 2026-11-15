// SPDX-License-Identifier: MIT

//go:build !windows

package main

import (
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// The verifier runs the candidate, so it sees no credential.
func TestJudge_SeesNoCredential(t *testing.T) {
	probe := testutil.LeakProbe(t)
	if passed, detail := judge([]string{probe}, nil); !passed {
		t.Fatalf("a credential reached the verifier: %s", detail)
	}
}
