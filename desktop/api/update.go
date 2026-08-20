// SPDX-License-Identifier: MIT

package api

import (
	"os/exec"

	"github.com/ankit373/hydra/internal/build"
	"github.com/ankit373/hydra/internal/update"
	"github.com/ankit373/hydra/internal/util"
)

// UpdateStatus reports whether a newer Hydra release exists.
type UpdateStatus struct {
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	Available bool   `json:"available"`
}

// GetUpdateStatus checks for a newer release the same way hyctl's own startup
// banner does — internal/update's 24h-cached GitHub fetch and semver compare —
// but through update.CheckIgnoringTTY rather than update.Check: the desktop
// app has no stdout for the CLI's TTY gate to test, so Check would always
// silently answer "no update" here.
func (a *API) GetUpdateStatus() UpdateStatus {
	s := UpdateStatus{Current: build.Version}
	if latest := update.CheckIgnoringTTY(); latest != "" {
		s.Latest = latest
		s.Available = true
	}
	return s
}

// installAppScriptCommand is a var so a test can point it at a fixture
// instead of curling raw.githubusercontent.com, matching the pattern
// internal/update's releaseURL uses to keep GitHub off the test path.
var installAppScriptCommand = "curl -fsSL https://raw.githubusercontent.com/ankit373/hydra/main/install-app.sh | sh"

// UpgradeResult is what running install-app.sh produced.
type UpgradeResult struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
}

// TriggerUpgrade runs install-app.sh — the script already documented (in its
// own header) as "resolves the newest release... and never goes stale", i.e.
// the correct way to install *or* upgrade the desktop app. It downloads the
// latest release, verifies its checksum, replaces the .app bundle on disk,
// and clears the Gatekeeper quarantine flag — exactly what a user running the
// documented curl one-liner by hand would get.
//
// This is deliberately not a silent self-updater. The app ships unsigned (see
// .github/workflows/desktop-build.yml), so having a running process download
// a new binary and swap out its own executable is exactly the class of
// mechanism code-signing/notarization exists to make safe — it is easy to get
// working on one machine and then fail, or get flagged by Gatekeeper, on a
// user's. Shelling out to the same installer a user would run by hand
// replaces the bundle *on disk*, not the pages already mapped into this
// running process, so the current session is unaffected and the user is on
// the new version next launch. The frontend is expected to say so.
func (a *API) TriggerUpgrade() UpgradeResult {
	acc := util.NewAccumulator(0)
	cmd := exec.Command("sh", "-c", installAppScriptCommand)
	cmd.Stdout = acc
	cmd.Stderr = acc
	err := cmd.Run()
	return UpgradeResult{OK: err == nil, Output: acc.String()}
}
