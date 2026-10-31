// SPDX-License-Identifier: MIT

package tui

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// The bug: runInstallCmd ran `sh -lc`, and stock Windows has no sh, so the
// entire guided-install path died with an opaque exec error. Whatever shell is
// chosen for this OS must actually exist on it, a test that only checked the
// argv shape would have passed on the broken version too.
func TestShellFor_ShellExistsOnThisOS(t *testing.T) {
	argv := shellFor("echo hi")
	if len(argv) < 2 {
		t.Fatalf("shellFor returned %v, want a shell plus at least one flag", argv)
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		t.Errorf("shellFor picked %q on %s, but it is not on PATH: %v, this is exactly the "+
			"`sh -lc` failure (#259), just with a different shell", argv[0], runtime.GOOS, err)
	}
	if argv[len(argv)-1] != "echo hi" {
		t.Errorf("command is not the last argument: %v", argv)
	}
}

// End to end: the chosen shell must actually run a command line and return its
// output. This is the assertion that distinguishes "we picked a plausible
// shell" from "the install flow works".
func TestShellFor_ActuallyRunsACommand(t *testing.T) {
	argv := shellFor("echo hydra-probe")
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("running %v failed: %v\n%s", argv, err, out)
	}
	if !strings.Contains(string(out), "hydra-probe") {
		t.Errorf("output = %q, want it to contain %q", out, "hydra-probe")
	}
}

// The install command and the shell that runs it must agree, for every OS,
// checked from every OS, which is the point of taking goos as a parameter.
// The original bug only manifested on Windows, so a test that could exercise
// only the running OS would have passed everywhere except the one machine
// nobody ran it on.
func TestInstallCmdAndShellAgree_ForEveryOS(t *testing.T) {
	cases := []struct {
		goos       string
		wantShell  string
		wantInCmd  string
		bannedInfx string // a fragment that must NOT appear in the command
	}{
		// ollama.com/download/windows documents `irm … | iex` and ships no
		// winget package, so the command is PowerShell by necessity, and it
		// must therefore be run by PowerShell, not cmd.exe or sh.
		{"windows", "powershell", "iex", "| sh"},
		{"darwin", "sh", "brew", "iex"},
		{"linux", "sh", "| sh", "iex"},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			cmd := ollamaInstallCmdFor(tc.goos)
			if strings.TrimSpace(cmd) == "" {
				t.Fatalf("no install command for %s", tc.goos)
			}
			if !strings.Contains(cmd, tc.wantInCmd) {
				t.Errorf("%s install command = %q, want it to contain %q", tc.goos, cmd, tc.wantInCmd)
			}
			if strings.Contains(cmd, tc.bannedInfx) {
				t.Errorf("%s install command = %q contains %q, wrong platform's syntax",
					tc.goos, cmd, tc.bannedInfx)
			}
			shell := shellForOS(tc.goos, cmd)[0]
			if !strings.Contains(strings.ToLower(shell), tc.wantShell) {
				t.Errorf("%s: command %q would be handed to %q, want a %s shell, "+
					"this is the #259 mismatch", tc.goos, cmd, shell, tc.wantShell)
			}
		})
	}
}

// The command must always be the final argument, whatever shell was chosen,
// otherwise it is passed as a flag and silently not executed.
func TestShellForOS_CommandIsTheLastArgument(t *testing.T) {
	for _, goos := range []string{"windows", "darwin", "linux", "freebsd"} {
		argv := shellForOS(goos, "echo hi")
		if len(argv) < 2 {
			t.Errorf("%s: shellForOS returned %v", goos, argv)
			continue
		}
		if argv[len(argv)-1] != "echo hi" {
			t.Errorf("%s: command is not the last argument: %v", goos, argv)
		}
	}
}

func TestLastMeaningfulLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"only line", "only line"},
		{"first\nsecond\n", "second"},
		{"first\nreal error\n\n\n", "real error"},
		{"   \n  \n", ""},
	}
	for _, tc := range cases {
		if got := lastMeaningfulLine(tc.in); got != tc.want {
			t.Errorf("lastMeaningfulLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
