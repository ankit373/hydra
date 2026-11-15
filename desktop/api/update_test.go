// SPDX-License-Identifier: MIT

package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/ankit373/hydra/internal/build"
	"github.com/ankit373/hydra/internal/testutil"
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

// fakeAppRelease serves files as release v9.9.9 and hides gh.
func fakeAppRelease(t *testing.T, files map[string][]byte) {
	t.Helper()
	api, dl := testutil.FakeRelease(t, "v9.9.9", files)
	origAPI, origDL := update.ReleaseURL, update.DownloadURL
	update.ReleaseURL, update.DownloadURL = api, dl
	t.Cleanup(func() { update.ReleaseURL, update.DownloadURL = origAPI, origDL })
	t.Setenv("PATH", t.TempDir())
}

// withAppExecutable makes TriggerUpgrade treat exe as the running app.
func withAppExecutable(t *testing.T, exe string) {
	t.Helper()
	orig := appExecutable
	appExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { appExecutable = orig })
}

// assertUpgrade checks OK and that exe now holds want.
func assertUpgrade(t *testing.T, exe string, wantOK bool, want string) {
	t.Helper()
	r := New().TriggerUpgrade()
	if r.OK != wantOK {
		t.Errorf("OK = %v, want %v; output:\n%s", r.OK, wantOK, r.Output)
	}
	if r.Output == "" {
		t.Error("Output is empty; the notice has nothing to show")
	}
	if got, _ := os.ReadFile(exe); string(got) != want {
		t.Errorf("app holds %q, want %q", got, want)
	}
}
