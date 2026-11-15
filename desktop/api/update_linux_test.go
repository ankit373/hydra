// SPDX-License-Identifier: MIT

package api

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// linuxApp serves a desktop release holding "new" and returns an installed
// app holding "old".
func linuxApp(t *testing.T, withSums bool) string {
	t.Helper()
	name := "hydra-desktop_v9.9.9_linux_" + runtime.GOARCH + ".tar.gz"
	archive := testutil.TarGz(t, "Hydra", []byte("new"))
	files := map[string][]byte{name: archive}
	if withSums {
		files[name+".sha256"] = []byte(testutil.SumLine(name, archive))
	}
	fakeAppRelease(t, files)
	exe := filepath.Join(t.TempDir(), "hydra-desktop")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	withAppExecutable(t, exe)
	return exe
}

func TestTriggerUpgrade_ReplacesTheRunningApp(t *testing.T) {
	assertUpgrade(t, linuxApp(t, true), true, "new")
}

func TestTriggerUpgrade_NoChecksumInstallsNothing(t *testing.T) {
	assertUpgrade(t, linuxApp(t, false), false, "old")
}
