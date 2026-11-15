// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/update"
	"github.com/ankit373/hydra/internal/util"
)

// installDir is where InstallHyctl puts hyctl; a var so a test never
// writes to the real /usr/local/bin or ~/.local/bin.
var installDir = update.DefaultBinDir

// hyctlSearchDirs returns the fixed destinations to check when exec.LookPath
// fails. A var, not a direct call, so a test can point it at a sandboxed
// directory, the real /usr/local/bin must never be touched by a test.
var hyctlSearchDirs = defaultHyctlSearchDirs

// installRunTimeout bounds one install or upgrade, multi-MB downloads included.
const installRunTimeout = 2 * time.Minute

// versionProbeTimeout bounds `hyctl --version`. Local, but 3s was too tight:
// forking a process on a loaded machine blew it (#667). A var, not a const,
// so a test can shrink it rather than sleep for ten seconds.
var versionProbeTimeout = 10 * time.Second

// HyctlStatus is whether hyctl is set up on this machine, for the first-run
// banner (#383). A machine that already has hyctl working must see nothing,
// Found is what the frontend branches on, so every field here is cheap to
// compute: a PATH lookup, at most one local subprocess, no network.
type HyctlStatus struct {
	Found   bool   `json:"found"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`

	// VersionError says why Version is empty when hyctl was found. Without it
	// a probe that timed out and one that printed nothing are the same blank
	// field, which is what made #667 hard to read.
	VersionError string `json:"versionError,omitempty"`

	// Supported is false on platforms InstallHyctl cannot drive (Windows,
	// see its doc comment). The frontend uses this to decide between offering
	// a one-click install and pointing at the manual instructions.
	Supported bool `json:"supported"`
}

// CheckHyctl looks for hyctl the way a shell would (exec.LookPath), then
// falls back to install.sh's own destinations (/usr/local/bin, ~/.local/bin)
// directly. The fallback matters: a GUI app launched from Finder/Dock/a
// desktop file inherits a minimal PATH that usually omits both, so right
// after InstallHyctl succeeds, a PATH-only check run in the same process
// would still report "not found" even though the install worked.
func (a *API) CheckHyctl() HyctlStatus {
	st := HyctlStatus{Supported: installSupported(runtime.GOOS)}

	path, err := exec.LookPath("hyctl")
	if err != nil {
		path, err = findHyctlInCommonDirs()
	}
	if err != nil {
		return st
	}

	st.Found = true
	st.Path = path
	st.Version, st.VersionError = hyctlVersion(path)
	return st
}

// InstallResult is the outcome of one InstallHyctl run.
type InstallResult struct {
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`

	// VersionError carries CheckHyctl's reason when the post-install probe
	// could not read a version, the install still succeeded (#667).
	VersionError string `json:"versionError,omitempty"`

	// Log is the installer's combined stdout/stderr, capped by
	// util.Accumulator. Returned on both success and failure, "it eventually
	// worked" tells the user nothing; the installer's own progress lines do.
	Log string `json:"log"`

	Error string `json:"error,omitempty"`
}

// InstallHyctl installs hyctl from the latest release the way install.sh
// does, but in process: nothing is installed unless the checksum matches.
func (a *API) InstallHyctl() InstallResult {
	if !installSupported(runtime.GOOS) {
		return InstallResult{Error: "automatic setup is only available on macOS and Linux, " +
			"on Windows, run install.ps1 or `npm install -g hyctl` (see the README)"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), installRunTimeout)
	defer cancel()

	out := util.NewAccumulator(1 << 20)
	if err := update.InstallCLI(ctx, filepath.Join(installDir(), "hyctl"), out); err != nil {
		return InstallResult{Log: out.String(), Error: fmt.Sprintf("install failed: %v", err)}
	}

	status := a.CheckHyctl()
	return InstallResult{
		OK:           status.Found,
		Version:      status.Version,
		VersionError: status.VersionError,
		Log:          out.String(),
	}
}

// installSupported takes goos rather than reading runtime.GOOS directly so a
// test can exercise the Windows branch without cross-compiling.
func installSupported(goos string) bool {
	return goos == "darwin" || goos == "linux"
}

// defaultHyctlSearchDirs is hyctlSearchDirs' production value.
func defaultHyctlSearchDirs() []string {
	if runtime.GOOS == "windows" {
		if lad := os.Getenv("LOCALAPPDATA"); lad != "" {
			// install.ps1's own destination (see the README).
			return []string{filepath.Join(lad, "Programs", "hyctl")}
		}
		return nil
	}
	dirs := []string{"/usr/local/bin"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	return dirs
}

// findHyctlInCommonDirs checks hyctlSearchDirs directly with os.Stat, for the
// PATH-refresh gap CheckHyctl's doc comment describes.
//
// On Windows this tries every name PATHEXT would resolve for a bare "hyctl":
// install.ps1 produces hyctl.exe, but a real hyctl.exe binary is what this
// checks for in production, the multi-name list exists so a fake, script-based
// stand-in (a .bat, the same trick internal/testutil.Sandbox.FakeBinary and
// this package's own fakeHyctl test helper use to avoid compiling a real PE
// binary in a test) resolves too, the same way exec.LookPath's PATHEXT search
// would treat both as valid.
func findHyctlInCommonDirs() (string, error) {
	names := []string{"hyctl"}
	if runtime.GOOS == "windows" {
		names = []string{"hyctl.exe", "hyctl.bat", "hyctl.cmd"}
	}
	for _, dir := range hyctlSearchDirs() {
		for _, name := range names {
			p := filepath.Join(dir, name)
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("hyctl not found in common install locations")
}

// hyctlVersion runs `hyctl --version` and returns its first line, the
// "  hydra vX.Y.Z" line versionText() in cmd/hydra/main.go prints, trimmed.
//
// The reason is returned alongside, because "hyctl printed no version" and
// "the probe never finished" are different facts and an empty string told the
// caller neither (#667). Still never fatal: the caller already knows
// Found=true, so an unread version is decoration lost, not hyctl missing.
func hyctlVersion(path string) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), versionProbeTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		// ctx, not err: a killed process reports "signal: killed", which says
		// nothing about who killed it or why.
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Sprintf("%s did not respond within %s", path, versionProbeTimeout)
		}
		return "", fmt.Sprintf("could not run %s --version: %v", path, err)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if line = strings.TrimSpace(line); line == "" {
		return "", fmt.Sprintf("%s --version printed nothing", path)
	}
	return line, ""
}
