// SPDX-License-Identifier: MIT

package verify

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// chdir moves into dir for the test, restoring afterwards.
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

// A Go repo verifies itself, whatever file the work touched.
func TestCommand_GoRepoUsesItsOwnSuite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)

	argv, label := Command("anything.go")
	if len(argv) == 0 {
		t.Fatal("a Go repo resolved no verifier")
	}
	if argv[0] != "go" || label != "go test ./..." {
		t.Errorf("argv = %v, label = %q; want the repo's own test suite", argv, label)
	}
	// It does not depend on being told a file: a confidence run need not name one.
	if a, _ := Command(""); len(a) != len(argv) {
		t.Errorf("Command(\"\") = %v, want the same suite", a)
	}
}

// Walking up must stop at the repo root, or a non-Go repo vendoring a go.mod
// somewhere above would claim `go test` as its verifier.
func TestGoModDir_StopsAtTheRepoRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module outer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(root, "inner")
	if err := os.MkdirAll(filepath.Join(inner, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, inner)

	if got := GoModDir(); got != "" {
		t.Errorf("GoModDir() = %q, want empty: .git above the go.mod means a different repo", got)
	}
}

// Nothing configured is not a pass. Callers must be able to tell the two apart,
// so an unresolvable verifier returns empty rather than something plausible.
func TestCommand_NoVerifierIsEmptyNotAGuess(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	t.Setenv("HYDRA_HOME", t.TempDir())

	for _, file := range []string{"", "x.rs", "README"} {
		if argv, label := Command(file); len(argv) != 0 || label != "" {
			t.Errorf("Command(%q) = (%v, %q), want empty with no validator configured", file, argv, label)
		}
	}
}

// withValidators points HYDRA_HOME at a registry declaring ext → command, the
// path a non-Go repo takes.
func withValidators(t *testing.T, yaml string) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "registry"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "version: \"1.0\"\nworkspaces: {}\nvalidators:\n" + yaml
	if err := os.WriteFile(filepath.Join(home, "registry", "workspace.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HYDRA_HOME", home)

	// A repo that is not Go, so resolution falls through to the validators.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
}

// {file} substitutes the real path as ONE argv element, so a path containing
// spaces reaches the verifier intact instead of splitting into two arguments.
func TestCommand_FileSubstitutesAsOneArgument(t *testing.T) {
	withValidators(t, "  ts: \"npx tsc --noEmit {file} --pretty\"\n")

	argv, label := Command("/tmp/my project/app.ts")
	want := []string{"npx", "tsc", "--noEmit", "/tmp/my project/app.ts", "--pretty"}
	if len(argv) != len(want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv = %q, want %q", argv, want)
		}
	}
	// The label is for a human, so it shows the basename, not the temp path.
	if label != "npx tsc --noEmit app.ts --pretty" {
		t.Errorf("label = %q", label)
	}
}

// A validator with no {file} is run as-is, the way a whole-project check is.
func TestCommand_ValidatorWithoutAPlaceholder(t *testing.T) {
	withValidators(t, "  py: \"pytest -q\"\n")

	argv, label := Command("src/app.py")
	if len(argv) != 2 || argv[0] != "pytest" || argv[1] != "-q" {
		t.Errorf("argv = %q, want [pytest -q]", argv)
	}
	if label != "pytest -q" {
		t.Errorf("label = %q", label)
	}
}

// An extension with no validator is unjudged, not judged by another language's.
func TestCommand_UnknownExtensionResolvesNothing(t *testing.T) {
	withValidators(t, "  ts: \"tsc {file}\"\n")

	if argv, label := Command("main.rb"); len(argv) != 0 || label != "" {
		t.Errorf("Command(main.rb) = (%v, %q), want empty", argv, label)
	}
}

// An empty template is a declared-but-unset validator, which must read as no
// verifier rather than as an empty command that would "pass" instantly.
func TestCommand_EmptyValidatorIsNotAVerifier(t *testing.T) {
	withValidators(t, "  go: \"\"\n  ts: \"   \"\n")

	for _, f := range []string{"x.go", "x.ts"} {
		if argv, _ := Command(f); len(argv) != 0 {
			t.Errorf("Command(%q) = %v, want empty for a blank template", f, argv)
		}
	}
}

// Walking up from a directory with no go.mod and no .git anywhere must stop at
// the filesystem root rather than looping, and report no Go repo.
func TestGoModDir_TerminatesAtTheFilesystemRoot(t *testing.T) {
	dir := t.TempDir() // a temp dir has neither marker above it that we own
	chdir(t, dir)

	done := make(chan string, 1)
	go func() { done <- GoModDir() }()
	select {
	case got := <-done:
		if got != "" {
			t.Errorf("GoModDir() = %q, want empty with no go.mod above", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GoModDir did not terminate: the parent==dir guard is the only thing stopping it")
	}
}

// The nearest go.mod wins, so a module nested inside another is verified by
// its own suite.
func TestGoModDir_PicksTheNearestModule(t *testing.T) {
	outer := t.TempDir()
	if err := os.WriteFile(filepath.Join(outer, "go.mod"), []byte("module outer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(outer, "nested")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "go.mod"), []byte("module inner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	chdir(t, inner)

	got, err := filepath.EvalSymlinks(GoModDir())
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(inner)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("GoModDir() = %q, want the nearest module %q", got, want)
	}
}
