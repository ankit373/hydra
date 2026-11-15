// SPDX-License-Identifier: MIT

//go:build unix

package update

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// withGh installs a fake gh that exits with code for every command.
func withGh(t *testing.T, code int) {
	t.Helper()
	gh := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nexit "+strconv.Itoa(code)+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	lookGh = func() string { return gh }
}

func goodSums(name string, a []byte) string { return testutil.SumLine(name, a) }

func TestInstallCLI_ProvenanceFailureRefuses(t *testing.T) {
	cliRelease(t, []byte("new"), goodSums)
	withGh(t, 1)
	dest, _, err := installOver(t)
	if err == nil || !strings.Contains(err.Error(), "provenance") {
		t.Fatalf("err = %v, want a provenance refusal", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "old" {
		t.Errorf("dest was changed to %q", got)
	}
}

func TestInstallCLI_ProvenanceVerifiedOrSkippedWhenSignedOut(t *testing.T) {
	for code, want := range map[int]string{0: "Provenance: verified", 4: "gh is not signed in"} {
		cliRelease(t, []byte("new"), goodSums)
		withGh(t, code)
		_, log, err := installOver(t)
		if err != nil || !strings.Contains(log, want) {
			t.Errorf("gh exit %d: err = %v, log:\n%s", code, err, log)
		}
	}
}

// A directory that cannot be written leaves no partial file and reports why.
func TestInstallMember_UnwritableDirFails(t *testing.T) {
	if os.Geteuid() == 0 {
		return // root writes anywhere; nothing to prove
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := InstallMember(testutil.TarGz(t, "hyctl", []byte("x")), "hyctl", filepath.Join(dir, "hyctl")); err == nil {
		t.Error("InstallMember succeeded in a read-only directory")
	}
}
