// SPDX-License-Identifier: MIT

package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// bundle writes dir/Hydra.app holding body as its executable and returns it.
func bundle(t *testing.T, dir, body string) string {
	t.Helper()
	exe := filepath.Join(dir, "Hydra.app", "Contents", "MacOS", "Hydra")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

// macApp serves a release zip made by ditto, as CI makes it, and returns an
// installed bundle's executable holding "old".
func macApp(t *testing.T, withSums bool) string {
	t.Helper()
	src := t.TempDir()
	bundle(t, src, "new")
	zip := filepath.Join(t.TempDir(), "app.zip")
	cmd := exec.Command("/usr/bin/ditto", "-c", "-k", "--sequesterRsrc", "--keepParent", filepath.Join(src, "Hydra.app"), zip)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ditto: %v: %s", err, out)
	}
	data, err := os.ReadFile(zip)
	if err != nil {
		t.Fatal(err)
	}
	name := "hydra-desktop_v9.9.9_darwin_universal.zip"
	files := map[string][]byte{name: data}
	if withSums {
		files[name+".sha256"] = []byte(testutil.SumLine(name, data))
	}
	fakeAppRelease(t, files)
	exe := bundle(t, t.TempDir(), "old")
	withAppExecutable(t, exe)
	return exe
}

func TestTriggerUpgrade_ReplacesTheRunningApp(t *testing.T) {
	exe := macApp(t, true)
	assertUpgrade(t, exe, true, "new")
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(exe)))), ".hydra-upgrade-*")); len(left) != 0 {
		t.Errorf("temp dirs left behind: %v", left)
	}
}

func TestTriggerUpgrade_NoChecksumInstallsNothing(t *testing.T) {
	assertUpgrade(t, macApp(t, false), false, "old")
}
