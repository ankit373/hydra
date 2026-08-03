// SPDX-License-Identifier: MIT

package testutil

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// updateGolden re-blesses golden files: `go test ./... -update`.
//
// Registered here rather than per-package so one flag drives every golden in
// the repo. Re-blessing is legitimate when the change to the output was
// intended; it is the bug when it was not. CONTRIBUTING.md draws that line —
// this flag is the reason it has to.
var updateGolden = flag.Bool("update", false, "rewrite golden files to match current output")

// Normaliser replaces values that legitimately vary between machines and runs
// with stable placeholders, so a golden file asserts behaviour rather than
// incidental fact. Without these, a golden pins the maintainer's home directory
// and the version they happened to build, and fails for everyone else.
//
// Ordering matters: paths are replaced before separators, or a Windows path
// would be half-normalised.
var normalisers = []struct {
	re   *regexp.Regexp
	with string
}{
	// Durations: "1.234s", "12ms", "1m30s" → <dur>
	{regexp.MustCompile(`\b\d+(\.\d+)?(ns|µs|us|ms|s|m|h)\b`), "<dur>"},
	// Semver, with or without a leading v and any pre-release/build suffix.
	{regexp.MustCompile(`\bv?\d+\.\d+\.\d+(-[0-9A-Za-z.\-]+)?(\+[0-9A-Za-z.\-]+)?\b`), "<ver>"},
	// Git short SHAs (7-12 hex). Longer runs of hex are content, not a commit.
	{regexp.MustCompile(`\b[0-9a-f]{7,12}\b`), "<sha>"},
	// RFC3339 timestamps.
	{regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}T[\d:.]+(Z|[+\-]\d{2}:\d{2})`), "<ts>"},
	// Dollar amounts.
	{regexp.MustCompile(`\$\d+\.\d+`), "<usd>"},
}

// Normalise makes output comparable across machines, platforms and runs.
// tempPaths (typically a Sandbox's directories) are replaced first, longest
// first so a nested path does not get half-substituted by its parent.
func Normalise(s string, tempPaths ...string) string {
	sorted := append([]string(nil), tempPaths...)
	// Longest first: HydraHome may sit inside the same root as Home.
	for i := range sorted {
		for j := i + 1; j < len(sorted); j++ {
			if len(sorted[j]) > len(sorted[i]) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	for _, p := range sorted {
		if p == "" {
			continue
		}
		s = strings.ReplaceAll(s, p, "<tmp>")
		// Windows hands back both separators for the same path depending on who
		// built the string, so normalise the alternate spelling too.
		s = strings.ReplaceAll(s, strings.ReplaceAll(p, `\`, `/`), "<tmp>")
	}
	// Path separators, after the temp-path substitutions above.
	s = strings.ReplaceAll(s, `\`, `/`)
	// Line endings: a Windows runner writes CRLF, everyone else LF.
	s = strings.ReplaceAll(s, "\r\n", "\n")

	for _, n := range normalisers {
		s = n.re.ReplaceAllString(s, n.with)
	}
	// Trailing whitespace per line, which differs by terminal-width assumptions.
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

// Golden compares got against testdata/<name>.golden, normalised.
//
// The failure message is the product here: a contributor who trips a contract
// must be able to act on it without reading this file. It states what differed
// and the exact command to re-bless if the change was intended.
func Golden(t *testing.T, name, got string, tempPaths ...string) {
	t.Helper()

	path := filepath.Join("testdata", name+".golden")
	norm := Normalise(got, tempPaths...)

	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(norm), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", path)
		return
	}

	wantRaw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("no golden file at %s.\n\nOutput was:\n%s\n"+
				"If that is correct, create it:\n    go test ./%s -run %s -update",
				path, indent(norm), pkgDir(t), t.Name())
		}
		t.Fatal(err)
	}
	want := string(wantRaw)
	if norm == want {
		return
	}

	t.Errorf("output does not match %s\n\n--- want (golden) ---\n%s\n--- got ---\n%s\n"+
		"%s\n"+
		"If this change is intended, re-bless the golden:\n    go test ./%s -run %s -update\n"+
		"If it is not, the behaviour changed and that is the bug.",
		path, indent(want), indent(norm), firstDiff(want, norm), pkgDir(t), t.Name())
}

// firstDiff points at the first differing line, so a large golden does not
// leave the reader diffing two walls of text by eye.
func firstDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return "first difference at line " + itoa(i+1) + ":\n" +
				"    want: " + quote(wl) + "\n" +
				"    got:  " + quote(gl)
		}
	}
	return ""
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}

func quote(s string) string {
	if s == "" {
		return "(end of output)"
	}
	return `"` + s + `"`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// pkgDir is the package path to put in the re-bless command, so the printed
// command can be pasted as-is from the repo root.
func pkgDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		return "..."
	}
	// Trim everything up to and including the module root marker.
	for dir := wd; ; {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			rel, err := filepath.Rel(dir, wd)
			if err != nil {
				break
			}
			return strings.ReplaceAll(rel, `\`, "/")
		}
		dir = parent
	}
	return "..."
}
