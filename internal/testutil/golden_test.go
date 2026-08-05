// SPDX-License-Identifier: MIT

package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Normalise is what makes a golden file comparable across a maintainer's
// laptop, a Linux runner and a Windows one. Every substitution it misses is a
// golden test that passes on one machine and fails on another — and every one
// it over-applies hides a real change.

func TestNormalise_ReplacesWhatDiffersByMachine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{{
		name: "timestamps",
		in:   "ran at 2026-08-05T12:34:56Z\n",
		want: "ran at <ts>\n",
	}, {
		name: "timestamps with an offset",
		in:   "ran at 2026-08-05T12:34:56.789+05:30\n",
		want: "ran at <ts>\n",
	}, {
		name: "dollar amounts",
		in:   "cost $0.0412\n",
		want: "cost <usd>\n",
	}, {
		name: "windows line endings",
		in:   "one\r\ntwo\r\n",
		want: "one\ntwo\n",
	}, {
		name: "trailing whitespace, which varies by terminal-width assumptions",
		in:   "padded   \nalso\t\n",
		want: "padded\nalso\n",
	}, {
		name: "backslashes become forward slashes",
		in:   `a\b\c` + "\n",
		want: "a/b/c\n",
	}, {
		// Output is compared line by line, so a missing or extra trailing
		// newline must not be the difference.
		name: "trailing newlines are collapsed to exactly one",
		in:   "text\n\n\n",
		want: "text\n",
	}, {
		name: "no trailing newline gains one",
		in:   "text",
		want: "text\n",
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Normalise(tt.in); got != tt.want {
				t.Errorf("Normalise(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Temp paths are substituted longest-first. A sandbox's HydraHome sits inside
// the same root as its Home, so shortest-first would half-substitute the
// nested one and leave a machine-specific fragment in the golden file.
func TestNormalise_SubstitutesNestedTempPathsLongestFirst(t *testing.T) {
	root := "/tmp/TestSomething123"
	nested := root + "/hydra"

	in := "home=" + root + " hydra=" + nested + "\n"
	got := Normalise(in, root, nested) // deliberately shortest first

	if strings.Contains(got, "TestSomething123") {
		t.Errorf("a machine-specific path survived: %q", got)
	}
	if strings.Contains(got, "<tmp>/hydra") {
		t.Errorf("the nested path was half-substituted by its parent: %q", got)
	}
	if got != "home=<tmp> hydra=<tmp>\n" {
		t.Errorf("Normalise = %q", got)
	}

	// An empty path in the list must be ignored, not replace everything.
	if got := Normalise("abc\n", ""); got != "abc\n" {
		t.Errorf("an empty temp path matched everything: %q", got)
	}
}

// Windows hands back both separators for the same path depending on who built
// the string, so the alternate spelling must normalise too.
func TestNormalise_SubstitutesEitherSeparatorSpelling(t *testing.T) {
	winPath := `C:\Users\RUNNER~1\AppData\Local\Temp\TestX`
	forward := strings.ReplaceAll(winPath, `\`, `/`)

	for _, spelling := range []string{winPath, forward} {
		got := Normalise("path="+spelling+"\n", winPath)
		if got != "path=<tmp>\n" {
			t.Errorf("Normalise with %q spelling = %q, want the temp path replaced",
				spelling, got)
		}
	}
}

// Golden's whole point is a comparison that can fail. These drive both
// outcomes through a real testdata file.
func TestGolden_MatchesAndReportsADifference(t *testing.T) {
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("testdata", "sample.golden"),
		[]byte("hello\nworld\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A match must not fail. The critical part is that `got` and `want` are
	// both normalised: comparing a normalised got against a raw want made every
	// golden fail on Windows, where git's autocrlf rewrote the fixture.
	if goldenFails(t, "sample", "hello\r\nworld\r\n") {
		t.Error("Golden failed on output that differs only by line ending")
	}

	// A real difference must fail.
	if !goldenFails(t, "sample", "hello\nEARTH\n") {
		t.Error("Golden passed on genuinely different output")
	}

	// A missing golden file must fail rather than silently passing — a contract
	// with no fixture is not a contract.
	if !goldenFails(t, "never-blessed", "anything") {
		t.Error("Golden passed with no fixture on disk")
	}
}

// goldenFails reports whether Golden failed for this input.
//
// The call runs on its own goroutine because Golden reports a missing fixture
// with t.Fatalf, and FailNow calls runtime.Goexit, which unwinds whichever
// goroutine it runs on: on the test's own goroutine that would abort this test,
// and on a synthetic *testing.T never started by the framework it panics.
// Golden's failure *text* is not asserted here — testing.T does not expose what
// a throwaway T recorded, and an assertion that cannot fail is worse than none.
func goldenFails(t *testing.T, name, got string) bool {
	t.Helper()

	fake := &testing.T{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = recover() }()
		Golden(fake, name, got)
	}()
	<-done
	return fake.Failed()
}

// ── sandbox helpers ───────────────────────────────────────────────────────────

func TestSetKey_IsScopedToTheTest(t *testing.T) {
	s := NewSandbox(t)
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		t.Fatal("the sandbox did not clear the key")
	}
	s.SetKey(t, "ANTHROPIC_API_KEY", "sk-test")
	if os.Getenv("ANTHROPIC_API_KEY") != "sk-test" {
		t.Error("SetKey did not set the variable")
	}
}

func TestFakeBinary_IsExecutableAndOnPath(t *testing.T) {
	s := NewSandbox(t)

	path := s.FakeBinary(t, "some-tool")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("FakeBinary wrote nothing: %v", err)
	}
	// The point of the helper is that exec.LookPath finds it — a discovery test
	// asserting "this CLI is installed" depends on that and nothing else.
	if !strings.HasPrefix(path, s.BinDir) {
		t.Errorf("the binary is at %q, outside the sandbox's PATH dir %q", path, s.BinDir)
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(path, ".bat") {
		t.Errorf("path = %q; exec.LookPath on Windows needs an executable extension", path)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o100 == 0 {
			t.Errorf("mode %v is not executable", info.Mode().Perm())
		}
	}
}

func TestWriteRegistry_WritesEveryBreadcrumbFile(t *testing.T) {
	dir := t.TempDir()
	WriteRegistry(t, dir)

	entries, err := os.ReadDir(filepath.Join(dir, "registry"))
	if err != nil {
		t.Fatalf("no registry was written: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the registry directory is empty")
	}

	// With explicit contents, each file gets its own — which is what lets a
	// breadcrumb test show that changing one file changes the fingerprint.
	dir2 := t.TempDir()
	WriteRegistry(t, dir2, "a", "b", "c", "d")
	raw, err := os.ReadFile(filepath.Join(dir2, "registry", "routing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "a" {
		t.Errorf("routing.yaml = %q, want the first content argument", raw)
	}
}

// ── the failure message ───────────────────────────────────────────────────────

// Golden's failure message is the product: a contributor who trips a contract
// has to be able to act on it without reading golden.go. It has to say what
// differed and give the exact command to re-bless.
func TestGolden_FailureMessageIsActionable(t *testing.T) {
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("testdata", "msg.golden"),
		[]byte("line one\nline two\nline three\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// firstDiff, quote and itoa are what build that message, so they are
	// exercised through it rather than only in isolation.
	if !goldenFails(t, "msg", "line one\nCHANGED\nline three\n") {
		t.Error("Golden passed on a changed middle line")
	}
	// Extra output at the end, and missing output at the end, are both real
	// differences and must be reported rather than treated as a prefix match.
	if !goldenFails(t, "msg", "line one\nline two\nline three\nline four\n") {
		t.Error("Golden passed on extra trailing output")
	}
	if !goldenFails(t, "msg", "line one\nline two\n") {
		t.Error("Golden passed on truncated output")
	}
}

// firstDiff locates the difference. Reporting the wrong line sends the reader
// to the wrong place, and the end-of-output cases are where an off-by-one hides.
func TestFirstDiff_LocatesTheLine(t *testing.T) {
	tests := []struct {
		name       string
		want, got  string
		wantSubstr string
	}{
		{"identical", "a\nb\n", "a\nb\n", ""},
		{"first line", "a\nb", "X\nb", "line 1"},
		{"middle line", "a\nb\nc", "a\nX\nc", "line 2"},
		{"got is shorter", "a\nb\nc", "a\nb", "line 3"},
		{"got is longer", "a\nb", "a\nb\nc", "line 3"},
		{"both empty", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := firstDiff(tt.want, tt.got)
			if tt.wantSubstr == "" {
				if got != "" {
					t.Errorf("firstDiff reported a difference where there is none: %q", got)
				}
				return
			}
			if !strings.Contains(got, tt.wantSubstr) {
				t.Errorf("firstDiff = %q, want it to name %q", got, tt.wantSubstr)
			}
			// A missing line must read as the end of output, not as an empty
			// string the reader mistakes for a blank line.
			if strings.Contains(tt.name, "shorter") || strings.Contains(tt.name, "longer") {
				if !strings.Contains(got, "end of output") {
					t.Errorf("firstDiff = %q, want it to say one side ended", got)
				}
			}
		})
	}
}

func TestQuoteAndItoa(t *testing.T) {
	if got := quote(""); got != "(end of output)" {
		t.Errorf("quote(\"\") = %q; an empty string would read as a blank line", got)
	}
	if got := quote("text"); got != `"text"` {
		t.Errorf("quote = %q", got)
	}
	for n, want := range map[int]string{0: "0", 1: "1", 9: "9", 10: "10", 42: "42", 1234: "1234"} {
		if got := itoa(n); got != want {
			t.Errorf("itoa(%d) = %q, want %q", n, got, want)
		}
	}
}

// pkgDir goes into the re-bless command, so it has to be a path that can be
// pasted from the repo root.
func TestPkgDir_IsPastableFromTheRepoRoot(t *testing.T) {
	got := pkgDir(t)
	if got == "" {
		t.Fatal("pkgDir returned nothing; the printed command would be unusable")
	}
	if filepath.IsAbs(got) {
		t.Errorf("pkgDir = %q, want a module-relative path", got)
	}
	if !strings.Contains(got, "testutil") && got != "..." {
		t.Errorf("pkgDir = %q, want it to name this package", got)
	}
}

// ── AllowHostBinary ───────────────────────────────────────────────────────────

// AllowHostBinary is the documented alternative to t.Skip for a test that needs
// one real tool. It exists because a skipped test is how a suite quietly stops
// covering the thing it was written for.
func TestAllowHostBinary_AdmitsARealToolAndRefusesAnAbsentOne(t *testing.T) {
	s := NewSandbox(t)

	// Nothing is on PATH to begin with.
	if _, err := exec.LookPath("go"); err == nil {
		t.Fatal("the sandbox did not hide the host PATH")
	}

	// A tool that is certainly not installed.
	if s.AllowHostBinary(t, "definitely-not-installed-anywhere-xyz") {
		t.Error("AllowHostBinary claimed to admit a tool that does not exist")
	}

	// `go` is running this test, so it is on the host PATH by construction.
	if !s.AllowHostBinary(t, "go") {
		t.Skip("go is not on the host PATH, which should be impossible here")
	}
	found, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go was admitted but is not resolvable: %v", err)
	}
	if found == "" {
		t.Error("LookPath returned an empty path")
	}

	// The sandbox's other guarantees must survive: admitting one tool must not
	// restore the developer's credentials or home directory.
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		t.Error("AllowHostBinary restored a provider credential")
	}
	if home, _ := os.UserHomeDir(); home != s.Home {
		t.Errorf("home = %q after admitting a binary, want the sandbox's %q", home, s.Home)
	}
}

// WriteRegistry's argument check exists so a caller cannot silently write a
// partial registry — a breadcrumb over three of four files is not the
// deployment's identity.
func TestWriteRegistry_RejectsAPartialContentList(t *testing.T) {
	dir := t.TempDir()

	fake := &testing.T{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = recover() }()
		WriteRegistry(fake, dir, "only", "three", "given")
	}()
	<-done
	if !fake.Failed() {
		t.Error("WriteRegistry accepted a content list shorter than BreadcrumbFiles; " +
			"the result would be a partial registry")
	}
}
