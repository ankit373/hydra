// SPDX-License-Identifier: MIT

package api

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/testutil"
)

// withPath puts dir as the only entry on $PATH, so exec.LookPath finds
// nothing outside it, mirrors internal/testutil.Sandbox's reasoning that one
// empty directory makes "not found" mean exactly that, rather than leaking
// whatever the developer's own machine happens to have installed (which, for
// hyctl, on this repo's own contributors' machines, is likely to be true).
func withPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir)
}

// withHyctlSearchDirs points CheckHyctl's fallback at dirs instead of the
// real /usr/local_bin and ~/.local/bin, a test must never touch either.
func withHyctlSearchDirs(t *testing.T, dirs ...string) {
	t.Helper()
	orig := hyctlSearchDirs
	hyctlSearchDirs = func() []string { return dirs }
	t.Cleanup(func() { hyctlSearchDirs = orig })
}

// fakeHyctl writes an executable named hyctl into dir that prints versionLine
// when run with any arguments (including --version). On Windows,
// exec.LookPath only considers files carrying a PATHEXT extension, so the
// file is hyctl.bat there, same reasoning as
// internal/testutil.Sandbox.FakeBinary.
func fakeHyctl(t *testing.T, dir, versionLine string) string {
	t.Helper()
	var path, script string
	if runtime.GOOS == "windows" {
		path = filepath.Join(dir, "hyctl.bat")
		script = "@echo off\r\necho " + versionLine + "\r\n"
	} else {
		path = filepath.Join(dir, "hyctl")
		script = "#!/bin/sh\necho '" + versionLine + "'\n"
	}
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// A machine with no hyctl anywhere PATH or the common install directories
// point at must say so plainly, so the frontend banner has something to key
// off, the whole point of #383.
func TestCheckHyctl_NotFoundAnywhere(t *testing.T) {
	withPath(t, t.TempDir())
	withHyctlSearchDirs(t) // no dirs at all

	st := New().CheckHyctl()
	if st.Found {
		t.Errorf("Found = true with an empty PATH and no search dirs; Path=%q", st.Path)
	}
	if st.Path != "" || st.Version != "" {
		t.Errorf("Path/Version set despite Found=false: %+v", st)
	}
}

// The common case this feature must stay invisible for: hyctl resolvable on
// PATH, exactly like a shell would find it.
func TestCheckHyctl_FoundOnPath(t *testing.T) {
	dir := t.TempDir()
	fakeHyctl(t, dir, "hydra v9.9.9-test")
	withPath(t, dir)
	withHyctlSearchDirs(t) // PATH alone must be enough; no fallback needed

	st := New().CheckHyctl()
	if !st.Found {
		t.Fatal("Found = false with hyctl on PATH")
	}
	if st.Version != "hydra v9.9.9-test" {
		t.Errorf("Version = %q, want the trimmed first line of --version output", st.Version)
	}
	if st.Path == "" {
		t.Error("Path is empty despite Found=true")
	}
}

// A GUI app launched from Finder/Dock/a desktop file inherits a minimal PATH
// that usually omits install.sh's destinations. hyctl must still be found
// there directly, otherwise the banner would reappear immediately after a
// successful install, in the very same process that just ran it.
func TestCheckHyctl_FallsBackToCommonInstallDirs(t *testing.T) {
	withPath(t, t.TempDir()) // hyctl absent from PATH

	commonDir := t.TempDir()
	fakeHyctl(t, commonDir, "hydra v1.2.3")
	withHyctlSearchDirs(t, commonDir)

	st := New().CheckHyctl()
	if !st.Found {
		t.Fatal("Found = false; the common-dir fallback did not fire")
	}
	if st.Version != "hydra v1.2.3" {
		t.Errorf("Version = %q, want hydra v1.2.3", st.Version)
	}
}

// withVersionProbeTimeout shrinks the probe deadline so a timeout test costs
// milliseconds instead of the real ten seconds.
func withVersionProbeTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := versionProbeTimeout
	versionProbeTimeout = d
	t.Cleanup(func() { versionProbeTimeout = orig })
}

// slowHyctl writes an executable that sleeps before printing, to outlast the
// probe deadline. Windows has no /bin/sh, and a .bat cannot sleep portably,
// so the timeout path is exercised on the platforms InstallHyctl supports.
func slowHyctl(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "hyctl")
	script := "#!/bin/sh\nsleep 5\necho 'hydra v9.9.9'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

// A probe that never finished and one that ran and printed nothing both left
// Version empty, so a loaded machine looked identical to a broken hyctl and
// the frontend could say nothing useful about either (#667).
func TestCheckHyctl_TimeoutIsDistinctFromNoOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake needs /bin/sh to sleep")
	}

	t.Run("timeout says so", func(t *testing.T) {
		withVersionProbeTimeout(t, 50*time.Millisecond)
		dir := t.TempDir()
		slowHyctl(t, dir)
		withPath(t, dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
		withHyctlSearchDirs(t)

		st := New().CheckHyctl()
		if !st.Found {
			t.Fatal("Found = false; the probe's deadline must not make hyctl look absent")
		}
		if st.Version != "" {
			t.Errorf("Version = %q, want empty on a timeout", st.Version)
		}
		if !strings.Contains(st.VersionError, "did not respond") {
			t.Errorf("VersionError = %q, want it to name the timeout", st.VersionError)
		}
	})

	t.Run("silent binary says something else", func(t *testing.T) {
		dir := t.TempDir()
		fakeHyctl(t, dir, "")
		withPath(t, dir)
		withHyctlSearchDirs(t)

		st := New().CheckHyctl()
		if !st.Found {
			t.Fatal("Found = false; a silent hyctl is still present")
		}
		if st.VersionError == "" {
			t.Fatal("VersionError is empty; the caller cannot tell why Version is blank")
		}
		if strings.Contains(st.VersionError, "did not respond") {
			t.Errorf("VersionError = %q, but nothing timed out here", st.VersionError)
		}
	})
}

// A version that reads fine must leave VersionError empty, or the frontend
// would render a fault on every healthy machine.
func TestCheckHyctl_NoVersionErrorOnSuccess(t *testing.T) {
	dir := t.TempDir()
	fakeHyctl(t, dir, "hydra v1.2.3")
	withPath(t, dir)
	withHyctlSearchDirs(t)

	st := New().CheckHyctl()
	if st.Version != "hydra v1.2.3" {
		t.Fatalf("Version = %q, want hydra v1.2.3", st.Version)
	}
	if st.VersionError != "" {
		t.Errorf("VersionError = %q, want empty when the version read fine", st.VersionError)
	}
}

// Supported must reflect the platform InstallHyctl can actually drive, since
// the frontend uses it to decide whether to render an install button at all.
func TestInstallSupported(t *testing.T) {
	cases := map[string]bool{"darwin": true, "linux": true, "windows": false, "freebsd": false}
	for goos, want := range cases {
		if got := installSupported(goos); got != want {
			t.Errorf("installSupported(%q) = %v, want %v", goos, got, want)
		}
	}
}

// On a platform InstallHyctl cannot drive, it must say so, and point at the
// documented alternative, rather than attempting a network call it cannot
// finish correctly.
func TestInstallHyctl_UnsupportedOSReturnsGuidance(t *testing.T) {
	if installSupported(runtime.GOOS) {
		t.Skip("this OS is one InstallHyctl supports; see TestInstallHyctl_RunsTheFetchedScript")
	}

	r := New().InstallHyctl()
	if r.OK {
		t.Error("OK = true on an unsupported platform")
	}
	if r.Error == "" {
		t.Error("no Error on an unsupported platform; the banner would show nothing")
	}
}

// hyctlRelease serves a v9.9.9 hyctl archive holding body, and points
// InstallHyctl at a temp dir that CheckHyctl also searches.
func hyctlRelease(t *testing.T, body string, withSums bool) string {
	t.Helper()
	name := fmt.Sprintf("hydra_9.9.9_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archive := testutil.TarGz(t, "hyctl", []byte(body))
	files := map[string][]byte{name: archive}
	if withSums {
		files["checksums.txt"] = []byte(testutil.SumLine(name, archive))
	}
	fakeAppRelease(t, files)
	dir := t.TempDir()
	orig := installDir
	installDir = func() string { return dir }
	t.Cleanup(func() { installDir = orig })
	withHyctlSearchDirs(t, dir)
	return dir
}

// The full round trip the frontend's install button triggers: download,
// verify, install, then CheckHyctl finds the new binary.
func TestInstallHyctl_InstallsTheVerifiedRelease(t *testing.T) {
	if !installSupported(runtime.GOOS) {
		t.Skip("InstallHyctl only runs the installer on macOS/Linux")
	}
	hyctlRelease(t, "#!/bin/sh\necho 'hydra v0.0.0-fake'\n", true)

	r := New().InstallHyctl()
	if r.Error != "" || !r.OK {
		t.Fatalf("InstallHyctl = %+v", r)
	}
	if r.Version != "hydra v0.0.0-fake" {
		t.Errorf("Version = %q, want hydra v0.0.0-fake", r.Version)
	}
	if r.Log == "" {
		t.Error("Log is empty; the frontend has nothing to show for what the installer did")
	}
}

// With no checksums.txt nothing is installed, and the reason is reported.
func TestInstallHyctl_NoChecksumsInstallsNothing(t *testing.T) {
	if !installSupported(runtime.GOOS) {
		t.Skip("InstallHyctl only runs the installer on macOS/Linux")
	}
	dir := hyctlRelease(t, "#!/bin/sh\necho x\n", false)

	r := New().InstallHyctl()
	if r.OK || r.Error == "" {
		t.Errorf("InstallHyctl = %+v, want a refusal", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "hyctl")); err == nil {
		t.Error("hyctl was installed without a checksum")
	}
}
