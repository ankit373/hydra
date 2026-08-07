// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A gate that cannot go red is not a gate. Every check here is driven to both
// verdicts against real files on disk.

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A profile with a mix of covered and uncovered blocks must yield the same
// number `go tool cover -func` reports: covered statements over total.
func TestParseProfile_ComputesPerPackageCoverage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cover.out")
	writeFile(t, path, strings.Join([]string{
		"mode: set",
		// internal/a: 6 of 8 statements covered → 75%
		"github.com/ankit373/hydra/internal/a/one.go:1.1,3.2 4 1",
		"github.com/ankit373/hydra/internal/a/one.go:5.1,7.2 2 1",
		"github.com/ankit373/hydra/internal/a/two.go:1.1,2.2 2 0",
		// internal/b: nothing covered → 0%
		"github.com/ankit373/hydra/internal/b/x.go:1.1,2.2 5 0",
		// cmd/hydra: all covered → 100%
		"github.com/ankit373/hydra/cmd/hydra/main.go:1.1,2.2 3 7",
		"",
	}, "\n"))

	got, err := parseProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{
		"internal/a": 75,
		"internal/b": 0,
		"cmd/hydra":  100,
	}
	for pkg, w := range want {
		if g, ok := got[pkg]; !ok {
			t.Errorf("%s is missing from the parsed profile", pkg)
		} else if diff := g - w; diff > 0.001 || diff < -0.001 {
			t.Errorf("%s = %.2f%%, want %.2f%%", pkg, g, w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("parsed %d packages, want %d: %v", len(got), len(want), got)
	}
}

// A truncated or noisy profile must not be read as full coverage. Silently
// treating a broken profile as "everything passes" is the one failure mode the
// gate cannot have.
func TestParseProfile_IgnoresMalformedLinesRatherThanCounting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cover.out")
	writeFile(t, path, strings.Join([]string{
		"mode: set",
		"not a profile line at all",
		"github.com/ankit373/hydra/internal/a/one.go:1.1,3.2 notanumber 1",
		"github.com/ankit373/hydra/internal/a/one.go:1.1,3.2 4 0",
		"",
	}, "\n"))

	got, err := parseProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got["internal/a"] != 0 {
		t.Errorf("internal/a = %.1f%%, want 0 — the malformed line must not count "+
			"as covered", got["internal/a"])
	}

	if _, err := parseProfile(filepath.Join(dir, "absent.out")); err == nil {
		t.Error("a missing profile was read as success; the gate would pass with no data")
	}
}

func TestLoadConfig_RejectsWhatItCannotEnforce(t *testing.T) {
	dir := t.TempDir()

	good := filepath.Join(dir, "good.txt")
	writeFile(t, good, "# a comment\n\nfloor internal/a 80\nno-tests internal/b because reasons\nskip-budget 5\n")
	cfg, err := loadConfig(good)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Floors["internal/a"] != 80 {
		t.Errorf("floor = %v", cfg.Floors["internal/a"])
	}
	if cfg.NoTests["internal/b"] != "because reasons" {
		t.Errorf("reason = %q", cfg.NoTests["internal/b"])
	}
	if cfg.SkipBudget != 5 {
		t.Errorf("skip budget = %d", cfg.SkipBudget)
	}

	bad := map[string]string{
		// A no-tests line with no reason is how the allow-list grows by habit.
		"no-tests with no reason":    "skip-budget 1\nno-tests internal/b\n",
		"floor with no percent":      "skip-budget 1\nfloor internal/a\n",
		"floor that is not a number": "skip-budget 1\nfloor internal/a lots\n",
		"unknown directive":          "skip-budget 1\nfence internal/a 80\n",
		// Without a budget the skip check silently does nothing.
		"no skip budget": "floor internal/a 80\n",
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".txt")
			writeFile(t, p, body)
			if cfg, err := loadConfig(p); err == nil {
				t.Errorf("loadConfig accepted %s: %+v", name, cfg)
			}
		})
	}
}

// The floor check must go red when breached, and must say what to do about it.
func TestCheck_FloorBreachIsReportedWithTheNumbers(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "cover.out")
	writeFile(t, profile, "mode: set\ngithub.com/ankit373/hydra/internal/a/x.go:1.1,2.2 4 0\n"+
		"github.com/ankit373/hydra/internal/a/x.go:3.1,4.2 6 1\n") // 60%

	// Above the floor: no problem.
	cfg := &Config{Floors: map[string]float64{"internal/a": 50}, NoTests: map[string]string{}, SkipBudget: 100}
	problems, _, err := check(cfg, profile, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("a package above its floor was reported: %v", problems)
	}

	// Below it: exactly one problem, naming both numbers and the file to edit.
	cfg.Floors["internal/a"] = 90
	problems, _, err = check(cfg, profile, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 {
		t.Fatalf("got %d problems, want 1: %v", len(problems), problems)
	}
	msg := problems[0]
	for _, want := range []string{"internal/a", "60.0", "90", "coverage-floors.txt"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not mention %q: %s", want, msg)
		}
	}
}

// A floor for a package that produced no coverage at all must fail. Otherwise
// deleting a package's tests silently removes it from the gate.
func TestCheck_AFloorWithNoMeasurementFails(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "cover.out")
	writeFile(t, profile, "mode: set\n")

	cfg := &Config{Floors: map[string]float64{"internal/gone": 80}, NoTests: map[string]string{}, SkipBudget: 100}
	problems, _, err := check(cfg, profile, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) == 0 {
		t.Fatal("a floor with no coverage behind it passed; deleting a package's " +
			"tests would silently remove it from the gate")
	}
	if !strings.Contains(problems[0], "internal/gone") {
		t.Errorf("the message does not name the package: %s", problems[0])
	}
}

// A package with no test files must fail unless allow-listed, and a stale
// allow-list entry must fail too.
func TestCheck_ZeroTestPackages(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "cover.out")
	writeFile(t, profile, "mode: set\n")

	writeFile(t, filepath.Join(dir, "internal", "untested", "x.go"), "package untested\n")
	writeFile(t, filepath.Join(dir, "internal", "tested", "x.go"), "package tested\n")
	writeFile(t, filepath.Join(dir, "internal", "tested", "x_test.go"), "package tested\n")

	cfg := &Config{Floors: map[string]float64{}, NoTests: map[string]string{}, SkipBudget: 100}
	problems, _, err := check(cfg, profile, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "internal/untested") {
		t.Fatalf("got %v, want one problem naming internal/untested", problems)
	}

	// Allow-listed: silent.
	cfg.NoTests["internal/untested"] = "a reason"
	problems, _, err = check(cfg, profile, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("an allow-listed package was reported: %v", problems)
	}

	// Allow-listing a package that *does* have tests is stale, and a stale
	// entry exempts it again the moment its tests are deleted.
	cfg.NoTests["internal/tested"] = "out of date"
	problems, _, err = check(cfg, profile, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "internal/tested") {
		t.Errorf("got %v, want the stale allow-list entry reported", problems)
	}
}

// The skip budget must go red when exceeded, and a skip with no reason must be
// reported wherever it is.
func TestCheck_SkipBudgetAndReasons(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "cover.out")
	writeFile(t, profile, "mode: set\n")

	writeFile(t, filepath.Join(dir, "pkg", "a_test.go"), strings.Join([]string{
		"package a",
		"func TestOne(t *testing.T) {",
		`	t.Skip("no git on this machine")`,
		"}",
		"func TestTwo(t *testing.T) {",
		`	t.Skipf("no %s here", "tsc")`,
		"}",
		"func TestThree(t *testing.T) {",
		"	t.Skip()",
		"}",
		"// t.Skip( in a comment must not count",
		"",
	}, "\n"))

	cfg := &Config{Floors: map[string]float64{}, NoTests: map[string]string{}, SkipBudget: 3}
	problems, _, err := check(cfg, profile, dir)
	if err != nil {
		t.Fatal(err)
	}
	// Three real skips, within budget — but one has no reason.
	if len(problems) != 1 {
		t.Fatalf("got %v, want only the reasonless skip reported", problems)
	}
	if !strings.Contains(problems[0], "a_test.go:9") {
		t.Errorf("the reasonless skip was not located: %s", problems[0])
	}

	// Over budget: an additional problem.
	cfg.SkipBudget = 2
	problems, _, err = check(cfg, profile, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 2 {
		t.Fatalf("got %v, want the budget breach reported too", problems)
	}
	var sawBudget bool
	for _, p := range problems {
		if strings.Contains(p, "budget of 2") {
			sawBudget = true
		}
	}
	if !sawBudget {
		t.Errorf("the budget breach does not state the budget: %v", problems)
	}
}

func TestSkipCall(t *testing.T) {
	tests := []struct {
		line string
		args string
		ok   bool
	}{
		{`	t.Skip("a reason")`, `"a reason"`, true},
		{`	t.Skipf("no %s", x)`, `"no %s", x`, true},
		{`	t.Skip()`, ``, true},
		{`	// t.Skip("commented out")`, ``, false},
		{`	t.SkipNow()`, ``, false},
		{`	fmt.Println("nothing to do with skipping")`, ``, false},
		{``, ``, false},
	}
	for _, tt := range tests {
		args, ok := skipCall(tt.line)
		if ok != tt.ok {
			t.Errorf("skipCall(%q) ok = %v, want %v", tt.line, ok, tt.ok)
			continue
		}
		if ok && args != tt.args {
			t.Errorf("skipCall(%q) = %q, want %q", tt.line, args, tt.args)
		}
	}
}

func TestPackageOf(t *testing.T) {
	tests := map[string]string{
		"github.com/ankit373/hydra/internal/a/one.go": "internal/a",
		"github.com/ankit373/hydra/cmd/hydra/main.go": "cmd/hydra",
		"github.com/ankit373/hydra/registry/x.go":     "registry",
	}
	for in, want := range tests {
		if got := packageOf(in); got != want {
			t.Errorf("packageOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// The checked-in config must be loadable and internally consistent, or the gate
// fails for its own reasons rather than the repo's.
func TestCheckedInConfig_IsValid(t *testing.T) {
	cfg, err := loadConfig(filepath.Join("..", "coverage-floors.txt"))
	if err != nil {
		t.Fatalf("the checked-in config does not load: %v", err)
	}
	if len(cfg.Floors) == 0 {
		t.Error("no floors are set, so the gate enforces nothing")
	}
	for pkg, floor := range cfg.Floors {
		if floor < 0 || floor > 100 {
			t.Errorf("%s has a floor of %v, outside 0–100", pkg, floor)
		}
	}
	// A package cannot both have a floor and be allow-listed as untested — one
	// of the two is wrong, and the gate would enforce the weaker.
	for pkg := range cfg.NoTests {
		if _, hasFloor := cfg.Floors[pkg]; hasFloor {
			t.Errorf("%s has both a coverage floor and a no-tests exemption", pkg)
		}
	}
	if cfg.SkipBudget <= 0 {
		t.Errorf("skip budget = %d; a budget of zero or less disables the check",
			cfg.SkipBudget)
	}
}
