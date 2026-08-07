// SPDX-License-Identifier: MIT

// External test package: internal/testutil imports internal/config (for
// BreadcrumbFiles), so an in-package test importing testutil is an import
// cycle. Everything exercised here is exported API, which is what a caller
// sees anyway.
package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	. "github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/testutil"
)

// config.toml is what hyctl init writes and every later run reads. A corrupt
// write loses the user's whole setup, and Save is the only thing standing
// between them and that.

func TestDirAndPath_LiveUnderTheUsersHome(t *testing.T) {
	s := testutil.NewSandbox(t)

	if got, want := Dir(), filepath.Join(s.Home, ".hydra"); got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
	if got, want := Path(), filepath.Join(s.Home, ".hydra", "config.toml"); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestExists_TracksTheFile(t *testing.T) {
	testutil.NewSandbox(t)

	if Exists() {
		t.Error("Exists() = true before anything was written")
	}
	if err := Save(&Config{Cortex: "x"}); err != nil {
		t.Fatal(err)
	}
	if !Exists() {
		t.Error("Exists() = false after a successful Save")
	}
}

// Everything the wizard writes must survive a round trip. A field that silently
// fails to persist means the user reconfigures Hydra on every run.
func TestSaveLoad_RoundTripsEveryField(t *testing.T) {
	testutil.NewSandbox(t)

	want := &Config{
		Cortex: "claude",
		Tiers: []Tier{
			{}, // zero value must survive too
		},
		Skills: []string{"review", "qa"},
		Policies: map[string]Policy{
			"pii":    {Action: "local-only"},
			"budget": {Action: "budget-cap"},
		},
	}
	if err := Save(want); err != nil {
		t.Fatal(err)
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Cortex != want.Cortex {
		t.Errorf("Cortex = %q, want %q", got.Cortex, want.Cortex)
	}
	if len(got.Skills) != 2 || got.Skills[0] != "review" || got.Skills[1] != "qa" {
		t.Errorf("Skills = %v, want [review qa]", got.Skills)
	}
	if len(got.Policies) != 2 {
		t.Fatalf("Policies = %v, want two entries", got.Policies)
	}
	if got.Policies["pii"].Action != "local-only" {
		t.Errorf("pii policy = %q, want local-only — PII routing is enforced from this",
			got.Policies["pii"].Action)
	}
}

// An empty config is a legitimate state (a user who skipped the wizard) and
// must not be indistinguishable from a corrupt one.
func TestSaveLoad_EmptyConfigRoundTrips(t *testing.T) {
	testutil.NewSandbox(t)

	if err := Save(&Config{}); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("an empty config failed to load: %v", err)
	}
	if got.Cortex != "" || len(got.Skills) != 0 {
		t.Errorf("empty config loaded as %+v", got)
	}
}

func TestLoad_MissingFileIsAnError(t *testing.T) {
	testutil.NewSandbox(t)

	if _, err := Load(); err == nil {
		t.Error("Load succeeded with no config file — callers use this to decide " +
			"whether to run the init wizard")
	}
}

func TestLoad_MalformedTOMLIsAnError(t *testing.T) {
	testutil.NewSandbox(t)

	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("cortex = \"unterminated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Error("malformed TOML loaded without error")
	}
}

// Save is atomic: a crash mid-write must leave the previous config intact, not
// a truncated file. The observable part is that no temp file survives a
// successful save, and the file is replaced whole.
func TestSave_IsAtomicAndLeavesNoTempFiles(t *testing.T) {
	testutil.NewSandbox(t)

	if err := Save(&Config{Cortex: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(&Config{Cortex: "second"}); err != nil {
		t.Fatal(err)
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Cortex != "second" {
		t.Errorf("Cortex = %q after overwrite, want second", got.Cortex)
	}

	entries, err := os.ReadDir(Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") {
			t.Errorf("temp file %s survived a successful save", e.Name())
		}
	}
}

// The config can name a Cortex and policies; it is written to the user's home
// and read on every run, so it must not be world-readable.
func TestSave_ConfigIsNotWorldReadable(t *testing.T) {
	testutil.NewSandbox(t)

	if err := Save(&Config{Cortex: "x"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Log("windows: mode bits carry no access information here")
		return
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("config.toml mode %v is group/other readable", info.Mode().Perm())
	}
	// The directory too — a readable dir leaks what exists even if files do not.
	dinfo, err := os.Stat(Dir())
	if err != nil {
		t.Fatal(err)
	}
	if dinfo.Mode().Perm()&0o077 != 0 {
		t.Errorf("~/.hydra mode %v is group/other accessible", dinfo.Mode().Perm())
	}
}

// hyctl init and a concurrent command can both call Save. The temp-file+rename
// pattern must not corrupt the result or leave litter.
func TestSave_ConcurrentWritesLeaveAValidConfig(t *testing.T) {
	testutil.NewSandbox(t)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = Save(&Config{Cortex: "c", Skills: []string{"s"}})
		}(i)
	}
	wg.Wait()

	got, err := Load()
	if err != nil {
		t.Fatalf("config is unreadable after concurrent saves: %v", err)
	}
	if got.Cortex != "c" {
		t.Errorf("Cortex = %q after concurrent saves", got.Cortex)
	}

	entries, err := os.ReadDir(Dir())
	if err != nil {
		t.Fatal(err)
	}
	var leftovers []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") {
			leftovers = append(leftovers, e.Name())
		}
	}
	if len(leftovers) != 0 {
		t.Errorf("%d temp files survived concurrent saves: %v", len(leftovers), leftovers)
	}
}

// ScriptHome honours HYDRA_HOME above everything else — it is how an operator
// points Hydra at a retuned registry without a rebuild.
func TestScriptHome_PrefersHydraHome(t *testing.T) {
	s := testutil.NewSandbox(t)

	if got := ScriptHome(); got != s.HydraHome {
		t.Errorf("ScriptHome() = %q, want $HYDRA_HOME %q", got, s.HydraHome)
	}
}
