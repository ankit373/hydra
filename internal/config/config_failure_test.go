// SPDX-License-Identifier: MIT

package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	. "github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/testutil"
)

// Save's failure paths matter more than its success path: a half-written
// config.toml costs the user their whole setup, and litter left in ~/.hydra
// accumulates silently on every collision.

func TestSave_UnwritableConfigDirIsAnErrorNotSilentDataLoss(t *testing.T) {
	testutil.NewSandbox(t)

	// Dir() is a regular file, so MkdirAll cannot create the directory. This
	// happens for real when a stray `hydra` file is written by a script. The
	// sandbox pre-creates Dir() as an empty directory, so it must be removed
	// first or the file write below would fail as "is a directory".
	if err := os.RemoveAll(Dir()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Dir(), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Save(&Config{Cortex: "x"}); err == nil {
		t.Error("Save reported success with an unusable config directory, the " +
			"wizard would tell the user their setup was saved")
	}
	if Exists() {
		t.Error("Exists() = true when the config could not be written")
	}
}

func TestSave_FailedRenameLeavesNoTempFile(t *testing.T) {
	testutil.NewSandbox(t)

	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	// A directory at config.toml's path: the rename cannot replace it. This
	// stands in for the real case, which is Windows refusing a rename onto a
	// file another hyctl process has open.
	if err := os.MkdirAll(Path(), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := Save(&Config{Cortex: "x"}); err == nil {
		t.Fatal("Save succeeded onto a directory")
	}

	entries, err := os.ReadDir(Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") {
			t.Errorf("temp file %s survived a failed rename; every collision would "+
				"leave another one in ~/.hydra", e.Name())
		}
	}
}

func TestSave_ReadOnlyConfigDirIsAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows does not enforce a read-only directory mode for the owner")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	testutil.NewSandbox(t)

	// The sandbox pre-creates Dir(), so MkdirAll here would be a permission-set
	// no-op on an already-existing directory, Chmod is what actually locks it.
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(Dir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(Dir(), 0o700) })

	if err := Save(&Config{Cortex: "x"}); err == nil {
		t.Error("Save succeeded into a read-only ~/.hydra")
	}
}

// ScriptHome decides where an on-disk registry override is read from. Getting
// its precedence wrong means an operator's retuned routing.yaml is ignored, or
// worse, a stale one next to the binary silently wins over their $HYDRA_HOME.

func TestScriptHome_HydraHomeBeatsEverythingElse(t *testing.T) {
	s := testutil.NewSandbox(t)

	// Plant a registry next to the test binary too, so the walk-up branch would
	// hit if precedence were wrong.
	planted := plantRegistryBesideBinary(t)

	if got := ScriptHome(); got != s.HydraHome {
		t.Errorf("ScriptHome() = %q, want $HYDRA_HOME %q (the walk-up found %q "+
			"and took precedence)", got, s.HydraHome, planted)
	}
}

func TestScriptHome_WalksUpToARepoCheckout(t *testing.T) {
	testutil.NewSandbox(t)
	t.Setenv("HYDRA_HOME", "")

	planted := plantRegistryBesideBinary(t)

	got := ScriptHome()
	if got != planted {
		t.Errorf("ScriptHome() = %q, want the checkout %q found by walking up from "+
			"the binary, a dev build would read the embedded registry instead of "+
			"the working tree's", got, planted)
	}
}

func TestScriptHome_FallsBackToTheConfigDir(t *testing.T) {
	testutil.NewSandbox(t)
	t.Setenv("HYDRA_HOME", "")

	// No $HYDRA_HOME and no registry above the binary: this is every installed
	// binary. A miss here is normal, the embedded copy is what actually gets
	// read (#238), but the path returned must still be somewhere an operator
	// can drop an override.
	if got, want := ScriptHome(), Dir(); got != want {
		t.Errorf("ScriptHome() = %q, want %q", got, want)
	}
}

// A relative or untidy $HYDRA_HOME must be cleaned, or the same override
// resolves to two different strings and the breadcrumb changes with it.
func TestScriptHome_CleansThePath(t *testing.T) {
	testutil.NewSandbox(t)
	t.Setenv("HYDRA_HOME", "/tmp/hydra/../hydra/./registry-root/")

	if got, want := ScriptHome(), filepath.Clean("/tmp/hydra/registry-root"); got != want {
		t.Errorf("ScriptHome() = %q, want the cleaned %q", got, want)
	}
}

// plantRegistryBesideBinary creates registry/routing.yaml next to the running
// test binary, which is what ScriptHome's walk-up looks for, and removes it
// afterwards.
func plantRegistryBesideBinary(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate the test binary: %v", err)
	}
	dir := filepath.Dir(exe)
	regDir := filepath.Join(dir, "registry")
	if _, err := os.Stat(filepath.Join(regDir, "routing.yaml")); err == nil {
		return dir // already there (running from a checkout); nothing to plant
	}
	if err := os.MkdirAll(regDir, 0o700); err != nil {
		t.Skipf("cannot write beside the test binary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(regDir, "routing.yaml"), []byte("{}\n"), 0o600); err != nil {
		t.Skipf("cannot write beside the test binary: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(regDir) })
	return dir
}

// Breadcrumb ties a log entry to the routing rules in effect when it was
// written. An error from it must propagate, a caller that logs an empty
// fingerprint has recorded that the deployment is unidentifiable, which is
// indistinguishable from a deployment with no registry.
func TestBreadcrumb_MissingRegistryFileIsAnError(t *testing.T) {
	testutil.NewSandbox(t)

	orig := BreadcrumbFiles
	t.Cleanup(func() { BreadcrumbFiles = orig })
	BreadcrumbFiles = []string{"routing.yaml", "not-a-registry-file.yaml"}

	got, err := Breadcrumb()
	if err == nil {
		t.Errorf("Breadcrumb() = %q with an unreadable registry file, want an error", got)
	}
	if got != "" {
		t.Errorf("Breadcrumb() = %q alongside an error; a partial fingerprint would "+
			"be logged as if it identified the deployment", got)
	}
}
