// SPDX-License-Identifier: MIT

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/build"
	"github.com/ankit373/hydra/internal/update"
)

// stubGitHubRelease points update.ReleaseURL at a local server for the life of
// the test, so GetUpdateStatus is exercised without ever reaching the real
// GitHub API, same reasoning as internal/update's own fetch_test.go.
func stubGitHubRelease(t *testing.T, tag string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"` + tag + `"}`))
	}))
	t.Cleanup(srv.Close)

	orig := update.ReleaseURL
	update.ReleaseURL = srv.URL
	t.Cleanup(func() { update.ReleaseURL = orig })
}

func withVersion(t *testing.T, v string) {
	t.Helper()
	orig := build.Version
	build.Version = v
	t.Cleanup(func() { build.Version = orig })
}

// GetUpdateStatus must report the running build even when no update exists,
// the version badge in the footer needs it either way.
func TestGetUpdateStatus_ReportsCurrentVersionWhenUpToDate(t *testing.T) {
	sandbox(t)
	withVersion(t, "v1.2.0")
	stubGitHubRelease(t, "v1.2.0")

	s := New().GetUpdateStatus()
	if s.Current != "v1.2.0" {
		t.Errorf("Current = %q, want v1.2.0", s.Current)
	}
	if s.Available {
		t.Errorf("Available = true when already on the latest release (Latest=%q)", s.Latest)
	}
	if s.Latest != "" {
		t.Errorf("Latest = %q, want empty when there is nothing to offer", s.Latest)
	}
}

// The whole point of the method: say so when a newer release exists.
func TestGetUpdateStatus_AvailableWhenNewer(t *testing.T) {
	sandbox(t)
	withVersion(t, "v1.0.0")
	stubGitHubRelease(t, "v1.1.0")

	s := New().GetUpdateStatus()
	if !s.Available {
		t.Fatal("Available = false with a newer release published")
	}
	if s.Latest != "v1.1.0" {
		t.Errorf("Latest = %q, want v1.1.0", s.Latest)
	}
	if s.Current != "v1.0.0" {
		t.Errorf("Current = %q, want v1.0.0", s.Current)
	}
}

// GetUpdateStatus goes through update.CheckIgnoringTTY, so it must honour the
// same HYDRA_NO_UPDATE_CHECK escape hatch hyctl's own banner does, a user who
// disabled the check should not see it resurface in the desktop app.
func TestGetUpdateStatus_RespectsNoUpdateCheckEnvVar(t *testing.T) {
	sandbox(t)
	withVersion(t, "v1.0.0")
	stubGitHubRelease(t, "v9.9.9")
	t.Setenv("HYDRA_NO_UPDATE_CHECK", "1")

	if s := New().GetUpdateStatus(); s.Available {
		t.Errorf("Available = true despite HYDRA_NO_UPDATE_CHECK; got Latest=%q", s.Latest)
	}
}

// TriggerUpgrade wires stdout+stderr into the same Accumulator every other
// subprocess capture in this codebase uses, and reports success/failure by
// the process exit code.
func TestTriggerUpgrade_ReportsSuccessAndCapturesOutput(t *testing.T) {
	orig := installAppScriptCommand
	installAppScriptCommand = "echo installed-ok"
	t.Cleanup(func() { installAppScriptCommand = orig })

	r := New().TriggerUpgrade()
	if !r.OK {
		t.Errorf("OK = false, want true for an exit-0 command; output: %q", r.Output)
	}
	if !strings.Contains(r.Output, "installed-ok") {
		t.Errorf("Output = %q, want it to contain the script's stdout", r.Output)
	}
}

func TestTriggerUpgrade_ReportsFailure(t *testing.T) {
	orig := installAppScriptCommand
	installAppScriptCommand = "echo boom >&2; exit 1"
	t.Cleanup(func() { installAppScriptCommand = orig })

	r := New().TriggerUpgrade()
	if r.OK {
		t.Error("OK = true, want false for a non-zero exit")
	}
	if !strings.Contains(r.Output, "boom") {
		t.Errorf("Output = %q, want it to contain the script's stderr", r.Output)
	}
}
