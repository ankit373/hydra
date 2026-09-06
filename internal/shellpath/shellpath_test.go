// SPDX-License-Identifier: MIT

package shellpath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sep(parts ...string) string { return strings.Join(parts, string(os.PathListSeparator)) }

// The reported case: launchd's PATH has none of the directories a CLI head
// lives in, so discovery finds nothing the same machine's terminal finds.
func TestMergePath_AddsWhatTheShellKnows(t *testing.T) {
	got := mergePath(sep("/usr/bin", "/bin"), sep("/usr/bin", "/Users/x/.local/bin", "/opt/homebrew/bin"))
	want := sep("/usr/bin", "/bin", "/Users/x/.local/bin", "/opt/homebrew/bin")
	if got != want {
		t.Errorf("mergePath =\n %q\nwant\n %q", got, want)
	}
}

// The process may hold entries the shell has never heard of. Replacing rather
// than merging would trade one missing head for another.
func TestMergePath_KeepsEntriesTheShellDoesNotHave(t *testing.T) {
	got := mergePath(sep("/opt/only-in-process"), sep("/usr/bin"))
	if !strings.Contains(got, "/opt/only-in-process") {
		t.Errorf("mergePath = %q, dropped an entry the process already had", got)
	}
}

func TestMergePath_NoDuplicates(t *testing.T) {
	got := mergePath(sep("/usr/bin", "/bin"), sep("/bin", "/usr/bin"))
	if got != sep("/usr/bin", "/bin") {
		t.Errorf("mergePath = %q, want the duplicates collapsed", got)
	}
}

// A shell that cannot be run, or is not configured, must leave PATH alone
// rather than truncating it to nothing.
func TestMergePath_NoLoginPathChangesNothing(t *testing.T) {
	if got := mergePath(sep("/usr/bin", "/bin"), ""); got != "" {
		t.Errorf("mergePath = %q, want \"\" so the caller leaves PATH untouched", got)
	}
}

// Exercised through a deliberately bare PATH, which is the only state Adopt
// acts on: with this machine's real PATH the guard returns first and the test
// would assert nothing while passing.
func TestAdopt_RecoversTheShellsPathFromABareOne(t *testing.T) {
	bare := sep("/usr/bin", "/bin", "/usr/sbin", "/sbin")
	t.Setenv("PATH", bare)
	Adopt()

	got := os.Getenv("PATH")
	if got == "" {
		t.Fatal("Adopt emptied PATH")
	}
	for _, dir := range strings.Split(bare, string(os.PathListSeparator)) {
		if !strings.Contains(got, dir) {
			t.Errorf("Adopt dropped %q from PATH", dir)
		}
	}
	if os.Getenv("SHELL") != "" && got == bare {
		t.Errorf("PATH = %q, want the shell's own entries merged in", got)
	}
}

// The guard is what makes this safe to call from every hyctl command: asking a
// shell costs about a second, and a process started from a terminal already
// knows the answer.
func TestLooksBare(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"macOS launchd", sep("/usr/bin", "/bin", "/usr/sbin", "/sbin"), true},
		// Every Debian-family default. The allowlist this replaced did not
		// know /usr/games, so a .desktop launch looked already-configured and
		// the recovery never ran.
		{"Linux .desktop", sep("/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin", "/usr/games", "/usr/local/games"), true},
		{"systemd user service", sep("/usr/local/bin", "/usr/bin", "/bin"), true},
		{"a shell profile has run", sep(filepath.Join(home, ".local", "bin"), "/usr/bin", "/bin"), false},
		{"empty", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := looksBare(c.path); got != c.want {
				t.Errorf("looksBare(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

// A terminal-started process must not pay for a subprocess it does not need.
func TestAdopt_LeavesARealPathUntouched(t *testing.T) {
	// The entry has to be under this test's HOME, because that is exactly what
	// looksBare looks for.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	real := sep(filepath.Join(home, ".local", "bin"), "/usr/bin", "/bin")
	t.Setenv("PATH", real)
	Adopt()
	if got := os.Getenv("PATH"); got != real {
		t.Errorf("PATH = %q, want it untouched at %q", got, real)
	}
}
