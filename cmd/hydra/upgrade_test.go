// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"strings"
	"testing"
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

// A Homebrew install must not run install.sh at all — that would overwrite
// Homebrew's symlink with a bare binary and desync `brew`'s bookkeeping.
// `brew upgrade hyctl` is pointed at instead, printed rather than run since
// hyctl has no business invoking `brew` on the user's behalf.
func TestRunUpgrade_HomebrewInstallSkipsTheScript(t *testing.T) {
	origExe := executablePath
	executablePath = func() (string, error) { return "/opt/homebrew/Cellar/hydra/1.2.0/bin/hyctl", nil }
	t.Cleanup(func() { executablePath = origExe })

	origCmd := installScriptCommand
	installScriptCommand = "echo THIS-MUST-NOT-RUN; exit 1"
	t.Cleanup(func() { installScriptCommand = origCmd })

	var buf bytes.Buffer
	if err := runUpgrade(&buf); err != nil {
		t.Fatalf("runUpgrade() = %v, want nil (the brew path is a no-op success)", err)
	}
	if strings.Contains(buf.String(), "THIS-MUST-NOT-RUN") {
		t.Error("install.sh ran despite being a Homebrew install")
	}
	if !strings.Contains(buf.String(), "brew upgrade hyctl") {
		t.Errorf("output = %q, want it to point at `brew upgrade hyctl`", buf.String())
	}
}

// A non-Homebrew install runs install.sh and reports its output.
func TestRunUpgrade_NonHomebrewRunsTheScript(t *testing.T) {
	origExe := executablePath
	executablePath = func() (string, error) { return "/usr/local/bin/hyctl", nil }
	t.Cleanup(func() { executablePath = origExe })

	origCmd := installScriptCommand
	installScriptCommand = "echo installed-ok"
	t.Cleanup(func() { installScriptCommand = origCmd })

	var buf bytes.Buffer
	if err := runUpgrade(&buf); err != nil {
		t.Fatalf("runUpgrade() = %v", err)
	}
	if !strings.Contains(buf.String(), "installed-ok") {
		t.Errorf("output = %q, want the script's stdout", buf.String())
	}
}

// A failing installer's exit status must propagate — a silent failure here
// would make `hyctl upgrade` exit 0 having installed nothing.
func TestRunUpgrade_ScriptFailurePropagates(t *testing.T) {
	origExe := executablePath
	executablePath = func() (string, error) { return "/usr/local/bin/hyctl", nil }
	t.Cleanup(func() { executablePath = origExe })

	origCmd := installScriptCommand
	installScriptCommand = "exit 1"
	t.Cleanup(func() { installScriptCommand = origCmd })

	var buf bytes.Buffer
	if err := runUpgrade(&buf); err == nil {
		t.Error("runUpgrade() = nil, want an error when install.sh exits non-zero")
	}
}

// `hyctl upgrade` must be reachable through the real command tree, with help
// text that does not panic — the same contract every other subcommand meets
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
