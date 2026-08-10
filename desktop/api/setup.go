// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/util"
)

// installScriptURL is the same curl-installer documented at the repo root and
// in the README. A var, not a const, so a test can point InstallHyctl at an
// httptest server instead of GitHub — matching internal/update's releaseURL
// and internal/pricing's openRouterModelsURL.
var installScriptURL = "https://raw.githubusercontent.com/ankit373/hydra/main/install.sh"

// hyctlSearchDirs returns the fixed destinations to check when exec.LookPath
// fails. A var, not a direct call, so a test can point it at a sandboxed
// directory — the real /usr/local/bin must never be touched by a test.
var hyctlSearchDirs = defaultHyctlSearchDirs

const (
	// installFetchTimeout bounds downloading install.sh itself (a few KB).
	installFetchTimeout = 15 * time.Second
	// installRunTimeout bounds running it — it downloads a multi-MB archive
	// of its own, so it gets much longer than the fetch above.
	installRunTimeout = 2 * time.Minute
	// versionProbeTimeout bounds `hyctl --version`, a purely local call.
	versionProbeTimeout = 3 * time.Second
)

// HyctlStatus is whether hyctl is set up on this machine, for the first-run
// banner (#383). A machine that already has hyctl working must see nothing —
// Found is what the frontend branches on — so every field here is cheap to
// compute: a PATH lookup, at most one local subprocess, no network.
type HyctlStatus struct {
	Found   bool   `json:"found"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`

	// Supported is false on platforms InstallHyctl cannot drive (Windows —
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
	st.Version = hyctlVersion(path)
	return st
}

// InstallResult is the outcome of one InstallHyctl run.
type InstallResult struct {
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`

	// Log is the installer's combined stdout/stderr, capped by
	// util.Accumulator. Returned on both success and failure — "it eventually
	// worked" tells the user nothing; the installer's own progress lines do.
	Log string `json:"log"`

	Error string `json:"error,omitempty"`
}

// InstallHyctl runs the same curl-installer documented at the repo root
// (install.sh) and in the README, rather than reimplementing platform/arch
// detection and checksum verification a second time in Go — that logic
// already exists, is shellchecked in CI, and would drift the moment one copy
// changed without the other.
//
// It differs from the documented `curl -fsSL … | sh` one-liner only in
// mechanics, not behaviour: this fetches the script itself over HTTPS via
// net/http (which validates the TLS certificate exactly as curl does) and
// feeds the bytes to `sh` on stdin. That avoids building a shell command
// string at all (there is no injection surface, since no user input reaches
// the command line) and drops the assumption that curl is even installed —
// which hyctl being absent says nothing about.
//
// Scope: macOS and Linux only. Windows ships install.ps1, a different
// installer with different mechanics (irm | iex, PowerShell execution
// policy, a %LOCALAPPDATA% destination) and different PATH-refresh
// semantics; wiring a second code path for it is left as a follow-up to
// #383 rather than blocking this MVP on full platform parity.
func (a *API) InstallHyctl() InstallResult {
	if !installSupported(runtime.GOOS) {
		return InstallResult{Error: "automatic setup is only available on macOS and Linux — " +
			"on Windows, run install.ps1 or `npm install -g hyctl` (see the README)"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), installRunTimeout)
	defer cancel()

	script, err := fetchInstallScript(ctx)
	if err != nil {
		return InstallResult{Error: fmt.Sprintf("could not download the installer: %v", err)}
	}

	out := util.NewAccumulator(1 << 20) // 1 MB — install.sh's own output is a few dozen lines
	// Absolute path, not "sh" resolved off $PATH: a GUI app launched from
	// Finder/Dock/a desktop file can inherit a PATH too minimal to resolve
	// even the shell (the same gap CheckHyctl's fallback exists for), and
	// /bin/sh is guaranteed on both platforms installSupported allows.
	cmd := exec.CommandContext(ctx, "/bin/sh")
	cmd.Stdin = strings.NewReader(script)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return InstallResult{Log: out.String(), Error: fmt.Sprintf("installer exited with an error: %v", err)}
	}

	status := a.CheckHyctl()
	return InstallResult{OK: status.Found, Version: status.Version, Log: out.String()}
}

// fetchInstallScript downloads install.sh's current contents. Capped at 1 MB
// (the real script is a few KB) so a misconfigured URL cannot turn this into
// an unbounded read.
func fetchInstallScript(parent context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(parent, installFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, installScriptURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, installScriptURL)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if len(body) == 0 {
		return "", fmt.Errorf("empty response from %s", installScriptURL)
	}
	return string(body), nil
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
func findHyctlInCommonDirs() (string, error) {
	bin := "hyctl"
	if runtime.GOOS == "windows" {
		bin = "hyctl.exe"
	}
	for _, dir := range hyctlSearchDirs() {
		p := filepath.Join(dir, bin)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("hyctl not found in common install locations")
}

// hyctlVersion runs `hyctl --version` and returns its first line — the
// "  hydra vX.Y.Z" line versionText() in cmd/hydra/main.go prints — trimmed.
// Empty on any error: the caller already knows Found=true, so a version that
// could not be read is decoration lost, not a reason to report hyctl absent.
func hyctlVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), versionProbeTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(line)
}
