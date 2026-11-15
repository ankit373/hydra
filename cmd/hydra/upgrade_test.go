// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/update"
)

func TestIsHomebrewInstall(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/usr/local/Cellar/hydra/1.2.0/bin/hyctl", true},
		{"/opt/homebrew/Cellar/hydra/1.2.0/bin/hyctl", true},
		{"/home/linuxbrew/.linuxbrew/Cellar/hydra/1.2.0/bin/hyctl", true},
		{"/usr/local/bin/hyctl", false},
		{"/home/user/.local/bin/hyctl", false},
		{"/Applications/hyctl", false},
	}
	for _, tt := range tests {
		if got := isHomebrewInstall(tt.path); got != tt.want {
			t.Errorf("isHomebrewInstall(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// fakeUpgradeRelease serves a release whose hyctl archive holds "new", with
// checksums.txt only when withSums, and hides gh so provenance is skipped.
func fakeUpgradeRelease(t *testing.T, withSums bool) {
	t.Helper()
	name := fmt.Sprintf("hydra_9.9.9_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archive := testutil.TarGz(t, "hyctl", []byte("new"))
	files := map[string][]byte{name: archive}
	if withSums {
		files["checksums.txt"] = []byte(testutil.SumLine(name, archive))
	}
	api, dl := testutil.FakeRelease(t, "v9.9.9", files)
	origAPI, origDL := update.ReleaseURL, update.DownloadURL
	update.ReleaseURL, update.DownloadURL = api, dl
	t.Cleanup(func() { update.ReleaseURL, update.DownloadURL = origAPI, origDL })
	t.Setenv("PATH", t.TempDir())
}

// upgradeAt runs runUpgrade as if hyctl lived at exe.
func upgradeAt(t *testing.T, exe string) (string, error) {
	t.Helper()
	origExe := executablePath
	executablePath = func() (string, error) { return exe, nil }
	t.Cleanup(func() { executablePath = origExe })
	var buf bytes.Buffer
	err := runUpgrade(context.Background(), &buf)
	return buf.String(), err
}

func oldHyctl(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "hyctl")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

// A Homebrew install must not be touched: `brew upgrade hyctl` is printed
// instead, and nothing is fetched.
func TestRunUpgrade_HomebrewInstallFetchesNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a Homebrew install reached the network")
	}))
	t.Cleanup(srv.Close)
	orig := update.ReleaseURL
	update.ReleaseURL = srv.URL
	t.Cleanup(func() { update.ReleaseURL = orig })

	out, err := upgradeAt(t, "/opt/homebrew/Cellar/hydra/1.2.0/bin/hyctl")
	if err != nil {
		t.Fatalf("runUpgrade() = %v, want nil (the brew path is a no-op success)", err)
	}
	if !strings.Contains(out, "brew upgrade hyctl") {
		t.Errorf("output = %q, want it to point at `brew upgrade hyctl`", out)
	}
}

// The binary next to the running one is replaced from the verified archive.
func TestRunUpgrade_ReplacesTheRunningBinary(t *testing.T) {
	fakeUpgradeRelease(t, true)
	exe := oldHyctl(t)
	out, err := upgradeAt(t, exe)
	got, _ := os.ReadFile(exe)
	if runtime.GOOS == "windows" {
		if err == nil || string(got) != "old" {
			t.Errorf("Windows upgrade: err = %v, binary %q; want a refusal pointing at install.ps1", err, got)
		}
		return
	}
	if err != nil || string(got) != "new" {
		t.Fatalf("err = %v, binary %q, want the new one\n%s", err, got, out)
	}
}

// With no checksums.txt the upgrade stops and the old binary stays.
func TestRunUpgrade_NoChecksumsInstallsNothing(t *testing.T) {
	fakeUpgradeRelease(t, false)
	exe := oldHyctl(t)
	if _, err := upgradeAt(t, exe); err == nil {
		t.Error("runUpgrade() = nil with no checksums.txt published")
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Errorf("binary was replaced with %q", got)
	}
}

// `hyctl upgrade` must be reachable through the real command tree, with help
// text that does not panic, the same contract every other subcommand meets
// (see naming_test.go / cli_contract_test.go).
func TestCLI_UpgradeHasHelp(t *testing.T) {
	cliSandbox(t)
	_, out, err := run(t, "upgrade", "--help")
	if err != nil {
		t.Fatalf("hyctl upgrade --help: %v", err)
	}
	if !strings.Contains(out, "upgrade") {
		t.Errorf("help output does not mention upgrade:\n%s", out)
	}
}
