// SPDX-License-Identifier: MIT

package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// withPath puts dir as the only entry on $PATH, so exec.LookPath finds
// nothing outside it — mirrors internal/testutil.Sandbox's reasoning that one
// empty directory makes "not found" mean exactly that, rather than leaking
// whatever the developer's own machine happens to have installed (which, for
// hyctl, on this repo's own contributors' machines, is likely to be true).
func withPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir)
}

// withHyctlSearchDirs points CheckHyctl's fallback at dirs instead of the
// real /usr/local_bin and ~/.local/bin — a test must never touch either.
func withHyctlSearchDirs(t *testing.T, dirs ...string) {
	t.Helper()
	orig := hyctlSearchDirs
	hyctlSearchDirs = func() []string { return dirs }
	t.Cleanup(func() { hyctlSearchDirs = orig })
}

// fakeHyctl writes an executable named hyctl into dir that prints versionLine
// when run with any arguments (including --version). On Windows,
// exec.LookPath only considers files carrying a PATHEXT extension, so the
// file is hyctl.bat there — same reasoning as
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
// off — the whole point of #383.
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
// there directly — otherwise the banner would reappear immediately after a
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

// On a platform InstallHyctl cannot drive, it must say so — and point at the
// documented alternative — rather than attempting a network call it cannot
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

// The full round trip: fetch the (fake) installer over HTTP, run it, and
// re-check via CheckHyctl — exactly what the frontend's install button
// triggers. The fake script stands in for install.sh so the test never
// reaches GitHub or writes outside its own temp directory.
func TestInstallHyctl_RunsTheFetchedScript(t *testing.T) {
	if !installSupported(runtime.GOOS) {
		t.Skip("InstallHyctl only runs the installer on macOS/Linux")
	}

	binDir := t.TempDir()
	hyctlPath := filepath.Join(binDir, "hyctl")
	script := fmt.Sprintf(`#!/bin/sh
set -e
cat > %q <<'EOF'
#!/bin/sh
echo 'hydra v0.0.0-fake'
EOF
chmod +x %q
echo "installed to %s"
`, hyctlPath, hyctlPath, binDir)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(script))
	}))
	defer ts.Close()

	restoreURL := installScriptURL
	installScriptURL = ts.URL
	t.Cleanup(func() { installScriptURL = restoreURL })

	// binDir first so the post-install CheckHyctl finds the script's fake
	// hyctl ahead of anything real; /usr/bin and /bin stay on PATH because
	// the fake script itself shells out to cat/chmod, real external commands
	// that need resolving — unlike the other tests here, which only use sh
	// builtins (echo, exit) and can tolerate an empty PATH.
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	withHyctlSearchDirs(t)

	r := New().InstallHyctl()
	if r.Error != "" {
		t.Fatalf("InstallHyctl returned an error: %s\nlog:\n%s", r.Error, r.Log)
	}
	if !r.OK {
		t.Errorf("OK = false after a successful install; log:\n%s", r.Log)
	}
	if r.Version != "hydra v0.0.0-fake" {
		t.Errorf("Version = %q, want hydra v0.0.0-fake", r.Version)
	}
	if r.Log == "" {
		t.Error("Log is empty; the frontend has nothing to show for what the installer did")
	}
}

// A download failure (bad URL, network error, non-200) must come back as a
// result the frontend can render, not a panic or a hang.
func TestInstallHyctl_DownloadFailureIsReported(t *testing.T) {
	if !installSupported(runtime.GOOS) {
		t.Skip("InstallHyctl only runs the installer on macOS/Linux")
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	restoreURL := installScriptURL
	installScriptURL = ts.URL
	t.Cleanup(func() { installScriptURL = restoreURL })

	r := New().InstallHyctl()
	if r.OK {
		t.Error("OK = true despite the download failing")
	}
	if r.Error == "" {
		t.Error("no Error despite the download failing")
	}
}

// A script that runs but exits non-zero (the real install.sh does this on an
// unsupported OS/arch or a checksum mismatch) must be reported as a failure
// with whatever it printed before dying, not silently swallowed.
func TestInstallHyctl_ScriptFailureIsReportedWithLog(t *testing.T) {
	if !installSupported(runtime.GOOS) {
		t.Skip("InstallHyctl only runs the installer on macOS/Linux")
	}

	const script = "#!/bin/sh\necho 'about to fail'\nexit 1\n"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(script))
	}))
	defer ts.Close()

	restoreURL := installScriptURL
	installScriptURL = ts.URL
	t.Cleanup(func() { installScriptURL = restoreURL })

	withPath(t, t.TempDir())
	withHyctlSearchDirs(t)

	r := New().InstallHyctl()
	if r.OK {
		t.Error("OK = true despite the installer exiting non-zero")
	}
	if r.Error == "" {
		t.Error("no Error despite the installer exiting non-zero")
	}
	if r.Log == "" {
		t.Error("Log is empty; the installer did print before failing")
	}
}
