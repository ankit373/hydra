// SPDX-License-Identifier: MIT

//go:build !windows

package workflow

import (
	"os"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// The store is written with restrictive permissions: a workflow carries prompts
// and model output, which is the user's work.
//
// Build-tagged rather than skipped at runtime. Windows does not implement the
// mode bits (os.Chmod there only toggles read-only, so the file reads back
// -rw-rw-rw- however it was created), and a test that exists only to skip on
// two of three platforms is decorative.
func TestSave_FileIsNotWorldReadable(t *testing.T) {
	testutil.NewSandbox(t)
	saved(t, "wf1", []Step{{Prompt: "a"}})
	p, err := Path("wf1")
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("mode = %v, want no group/other access", perm)
	}
}
