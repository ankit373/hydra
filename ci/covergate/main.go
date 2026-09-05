// SPDX-License-Identifier: MIT

// Command covergate enforces three things CI cannot otherwise see:
//
//  1. per-package coverage against a checked-in floor,
//  2. that no package has zero test files unless it is allow-listed with a
//     reason,
//  3. that the number of t.Skip calls has not grown past a checked-in budget.
//
// All three exist because the failure they catch is invisible: coverage slides
// down one PR at a time, a new package ships with no tests at all, and a
// cross-platform suite fills with `t.Skip("not on windows")` until the
// three-OS matrix is decorative. None of those turn CI red on their own.
//
// Usage:
//
//	go test ./... -coverprofile=cover.out
//	go run ./ci/covergate -profile cover.out -config ci/coverage-floors.txt
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func main() {
	var (
		profile = flag.String("profile", "cover.out", "coverage profile from `go test -coverprofile`")
		config  = flag.String("config", "ci/coverage-floors.txt", "floors, allow-list and skip budget")
		root    = flag.String("root", ".", "module root to scan for packages and skips")
	)
	flag.Parse()

	cfg, err := loadConfig(*config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "covergate: %v\n", err)
		os.Exit(2)
	}

	problems, skipsUsed, err := check(cfg, *profile, *root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "covergate: %v\n", err)
		os.Exit(2)
	}

	if len(problems) == 0 {
		fmt.Printf("covergate: %d floors met, %d allow-listed packages, %d/%d skips used\n",
			len(cfg.Floors), len(cfg.NoTests), skipsUsed, cfg.SkipBudget)
		return
	}
	for _, p := range problems {
		// ::error:: is GitHub Actions' annotation prefix, so each problem lands
		// on the PR rather than only in the log.
		fmt.Printf("::error::%s\n", p)
	}
	fmt.Fprintf(os.Stderr, "\ncovergate: %d problem(s)\n", len(problems))
	os.Exit(1)
}

// Config is the checked-in contract.
type Config struct {
	Floors     map[string]float64
	NoTests    map[string]string // package → reason
	SkipBudget int
}

func loadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		Floors:     map[string]float64{},
		NoTests:    map[string]string{},
		SkipBudget: -1,
	}

	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		switch fields[0] {
		case "floor":
			if len(fields) != 3 {
				return nil, fmt.Errorf("%s:%d: floor needs a package and a percent", path, i+1)
			}
			pct, err := strconv.ParseFloat(fields[2], 64)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %q is not a percent", path, i+1, fields[2])
			}
			cfg.Floors[fields[1]] = pct
		case "no-tests":
			if len(fields) < 3 {
				// A package with no tests is not a package with nothing to
				// test. Requiring the reason is what stops the list growing by
				// habit.
				return nil, fmt.Errorf("%s:%d: no-tests needs a reason", path, i+1)
			}
			cfg.NoTests[fields[1]] = strings.Join(fields[2:], " ")
		case "skip-budget":
			if len(fields) != 2 {
				return nil, fmt.Errorf("%s:%d: skip-budget needs a number", path, i+1)
			}
			n, err := strconv.Atoi(fields[1])
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %q is not a number", path, i+1, fields[1])
			}
			cfg.SkipBudget = n
		default:
			return nil, fmt.Errorf("%s:%d: unknown directive %q", path, i+1, fields[0])
		}
	}
	if cfg.SkipBudget < 0 {
		return nil, fmt.Errorf("%s: no skip-budget set", path)
	}
	return cfg, nil
}

// check returns every problem found, plus how many skips the suite used, so a
// green run still reports the headroom rather than only the verdict.
func check(cfg *Config, profile, root string) (problems []string, skips int, err error) {
	coverage, err := parseProfile(profile)
	if err != nil {
		return nil, 0, err
	}

	// 1. Floors.
	for _, pkg := range sortedKeys(cfg.Floors) {
		floor := cfg.Floors[pkg]
		got, measured := coverage[pkg]
		if !measured {
			problems = append(problems, fmt.Sprintf(
				"%s has a floor of %.0f%% but no coverage was measured, was it deleted, "+
					"or did its tests stop running?", pkg, floor))
			continue
		}
		if got < floor {
			problems = append(problems, fmt.Sprintf(
				"%s is at %.1f%%, below its floor of %.0f%%. Raise the coverage, or lower "+
					"the floor in ci/coverage-floors.txt, which is a change a reviewer sees.",
				pkg, got, floor))
		}
	}

	// Every measurement, printed whether or not the run passes. Ratcheting a
	// floor means knowing the current number, and the run you are looking at is
	// where it should be, not a local run on a different OS, where the same
	// package can measure ten points apart.
	fmt.Println("covergate: measured coverage")
	for _, pkg := range sortedKeys(coverage) {
		got := coverage[pkg]
		floor, hasFloor := cfg.Floors[pkg]
		switch {
		case !hasFloor:
			fmt.Printf("  %-32s %5.1f%%  (no floor)\n", pkg, got)
		case got-floor >= 10:
			fmt.Printf("  %-32s %5.1f%%  floor %.0f%%  ← consider ratcheting\n", pkg, got, floor)
		default:
			fmt.Printf("  %-32s %5.1f%%  floor %.0f%%\n", pkg, got, floor)
		}
	}
	fmt.Println()

	// 2. Packages with no test files.
	untested, err := packagesWithoutTests(root)
	if err != nil {
		return nil, 0, err
	}
	for _, pkg := range untested {
		if _, allowed := cfg.NoTests[pkg]; !allowed {
			problems = append(problems, fmt.Sprintf(
				"%s has no test files. Add some, or allow-list it in "+
					"ci/coverage-floors.txt with a reason.", pkg))
		}
	}
	// An allow-list entry for a package that now has tests is stale, and a
	// stale entry silently exempts it again if its tests are ever removed.
	for _, pkg := range sortedKeys(cfg.NoTests) {
		if !contains(untested, pkg) {
			if _, exists := os.Stat(filepath.Join(root, pkg)); exists == nil {
				problems = append(problems, fmt.Sprintf(
					"%s is allow-listed as having no tests, but it has some. Remove the "+
						"no-tests line, a stale entry exempts it again if they are deleted.", pkg))
			}
		}
	}

	// 3. Skip budget.
	skips, unexplained, err := countSkips(root)
	if err != nil {
		return nil, 0, err
	}
	if skips > cfg.SkipBudget {
		problems = append(problems, fmt.Sprintf(
			"the suite has %d t.Skip calls, over the budget of %d. A three-OS matrix "+
				"where every awkward test skips on two of them is decorative, either "+
				"make the test run, or raise the budget deliberately.", skips, cfg.SkipBudget))
	}
	for _, loc := range unexplained {
		problems = append(problems, fmt.Sprintf(
			"%s: t.Skip with no reason. The next person needs to know whether it is "+
				"still true.", loc))
	}

	return problems, skips, nil
}

// parseProfile computes per-package coverage from a Go coverage profile.
//
// Lines look like:
//
//	github.com/ankit373/hydra/internal/x/file.go:10.2,12.16 2 1
//
// where the trailing numbers are the statement count in that block and how many
// times it ran. Package coverage is covered statements over total statements,
// the same arithmetic `go tool cover -func` reports.
func parseProfile(path string) (map[string]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	type counts struct{ total, covered int }
	byPkg := map[string]*counts{}

	scanner := bufio.NewScanner(f)
	// A profile line is short, but a long file path plus a huge module path can
	// approach the default 64 KiB token limit; raise it rather than have the
	// scanner stop silently mid-file.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		colon := strings.LastIndex(line, ":")
		if colon < 0 {
			continue
		}
		fileRef := line[:colon]
		fields := strings.Fields(line[colon+1:])
		if len(fields) != 3 {
			continue
		}
		stmts, err1 := strconv.Atoi(fields[1])
		hits, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil {
			continue
		}

		pkg := packageOf(fileRef)
		c := byPkg[pkg]
		if c == nil {
			c = &counts{}
			byPkg[pkg] = c
		}
		c.total += stmts
		if hits > 0 {
			c.covered += stmts
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	out := make(map[string]float64, len(byPkg))
	for pkg, c := range byPkg {
		if c.total == 0 {
			continue
		}
		out[pkg] = float64(c.covered) / float64(c.total) * 100
	}
	return out, nil
}

// modulePath is trimmed from profile entries so floors are written the way a
// contributor refers to a package, not as a full import path.
const modulePath = "github.com/ankit373/hydra/"

// packageOf turns a profile file reference into the package path used in the
// config file.
func packageOf(fileRef string) string {
	dir := filepath.ToSlash(filepath.Dir(fileRef))
	dir = strings.TrimPrefix(dir, modulePath)
	dir = strings.TrimPrefix(dir, strings.TrimSuffix(modulePath, "/"))
	return strings.TrimPrefix(dir, "/")
}

// packagesWithoutTests walks root for directories holding .go files but no
// _test.go, skipping anything not part of the module's own source.
func packagesWithoutTests(root string) ([]string, error) {
	var out []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
			name == "vendor" || name == "node_modules" || name == "testdata" ||
			name == "desktop" || name == "bench" || name == "ci") {
			return filepath.SkipDir
		}

		entries, rerr := os.ReadDir(path)
		if rerr != nil {
			return rerr
		}
		var hasGo, hasTest bool
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			hasGo = true
			if strings.HasSuffix(e.Name(), "_test.go") {
				hasTest = true
			}
		}
		if hasGo && !hasTest {
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// countSkips counts t.Skip/t.Skipf calls and reports the ones with no reason.
func countSkips(root string) (total int, unexplained []string, err error) {
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			name := d.Name()
			// ci/ is the gate's own tooling, not the suite it measures, and its
			// tests hold fixture text that reads as skips.
			if path != root && (strings.HasPrefix(name, ".") || name == "vendor" ||
				name == "node_modules" || name == "desktop" || name == "bench" ||
				name == "ci") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}

		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, path)
		inTest := false
		for i, line := range strings.Split(string(raw), "\n") {
			if kind, ok := enclosingFunc(line); ok {
				inTest = kind == "Test"
			}
			call, ok := skipCall(line)
			if !ok || !inTest {
				continue
			}
			total++
			if call == "" {
				unexplained = append(unexplained,
					fmt.Sprintf("%s:%d", filepath.ToSlash(rel), i+1))
			}
		}
		return nil
	})
	return total, unexplained, err
}

// enclosingFunc reports which kind of top-level test function a line opens.
//
// The distinction matters: `t.Skip()` inside an f.Fuzz body rejects a generated
// input and moves on, it is the fuzzer's own control flow, happens thousands
// of times per run, and a reason string there would be noise. A skip inside a
// Test function is the thing this budget exists to bound: a platform or
// environment exemption that quietly stops covering something.
func enclosingFunc(line string) (kind string, ok bool) {
	if !strings.HasPrefix(line, "func ") {
		return "", false
	}
	name := strings.TrimPrefix(line, "func ")
	switch {
	case strings.HasPrefix(name, "Fuzz"):
		return "Fuzz", true
	case strings.HasPrefix(name, "Test"):
		return "Test", true
	case strings.HasPrefix(name, "Benchmark"), strings.HasPrefix(name, "Example"):
		return "Other", true
	}
	// A plain helper: skips inside it belong to whatever calls it, and the
	// caller's own function line has already set the mode.
	return "", false
}

// skipCall reports whether the line calls t.Skip/t.Skipf and what it passed.
// It is deliberately textual: the alternative is parsing every test file, and
// the thing being counted is a convention, not a type.
func skipCall(line string) (args string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "//") {
		return "", false
	}
	for _, form := range []string{".Skipf(", ".Skip("} {
		idx := strings.Index(trimmed, form)
		if idx < 0 {
			continue
		}
		// .SkipNow() takes no reason by design and is a different thing.
		rest := trimmed[idx+len(form):]
		close := strings.LastIndex(rest, ")")
		if close < 0 {
			return rest, true // continued on the next line; assume it has one
		}
		return strings.TrimSpace(rest[:close]), true
	}
	return "", false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
