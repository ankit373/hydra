// SPDX-License-Identifier: MIT

package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/testutil"
)

// Check is the security boundary: it decides whether Hydra may write to a file
// at all. It had 0% coverage. Everything below builds paths with filepath.Join
// and t.TempDir so the tests are platform-native — which is the point, since
// this package compares paths with string operations.

// scopeRegistry builds a registry rooted at a real temp directory, so path
// comparisons are exercised in the OS's own syntax rather than a hardcoded
// POSIX string.
func scopeRegistry(t *testing.T, allowed, denied []string) (*Registry, string) {
	t.Helper()
	root := t.TempDir()
	return &Registry{
		workspaces: []Workspace{{
			Name: "ws", Root: filepath.Clean(root), Git: "false",
			AllowedGlobs: allowed, DeniedGlobs: denied,
		}},
		validators: map[string]string{"go": "gofmt -l {file}", "ts": ""},
	}, root
}

func TestCheck_AllowsAFileMatchingAnAllowedGlob(t *testing.T) {
	testutil.NewSandbox(t)
	r, root := scopeRegistry(t, []string{"src/**"}, nil)

	got, err := r.Check(filepath.Join(root, "src", "main.go"))
	if err != nil {
		t.Fatalf("a file under an allowed glob was rejected: %v", err)
	}
	if got != "ws" {
		t.Errorf("workspace = %q, want %q", got, "ws")
	}
}

func TestCheck_RejectsAFileOutsideEveryWorkspace(t *testing.T) {
	testutil.NewSandbox(t)
	r, _ := scopeRegistry(t, []string{"**"}, nil)

	other := t.TempDir()
	if _, err := r.Check(filepath.Join(other, "x.go")); err == nil {
		t.Error("a file outside every workspace root was accepted")
	}
}

// A sibling directory whose name merely starts with the root's name must not be
// treated as inside it — the classic prefix-matching escape.
func TestCheck_RejectsASiblingWithTheRootAsANamePrefix(t *testing.T) {
	testutil.NewSandbox(t)
	root := t.TempDir()
	r := &Registry{workspaces: []Workspace{{
		Name: "ws", Root: filepath.Clean(root), AllowedGlobs: []string{"**"},
	}}}

	// e.g. /tmp/T123 is the root; /tmp/T123-evil must not be inside it.
	sibling := filepath.Clean(root) + "-evil"
	if _, err := r.Check(filepath.Join(sibling, "x.go")); err == nil {
		t.Errorf("%s was accepted as inside %s — prefix matching, not path containment",
			filepath.Join(sibling, "x.go"), root)
	}
}

// Traversal must be refused on the path itself, not left to the glob list to
// catch by accident. A workspace whose allowed_globs contain "**" is entirely
// reasonable, and it must still not permit escaping the root.
func TestCheck_RejectsDotDotTraversalEvenWhenGlobsAreWideOpen(t *testing.T) {
	testutil.NewSandbox(t)
	r, root := scopeRegistry(t, []string{"**"}, nil)

	escapes := []string{
		filepath.Join(root, "..", "outside.go"),
		filepath.Join(root, "src", "..", "..", "outside.go"),
		filepath.Join(root, "..", "..", "etc", "passwd"),
	}
	for _, p := range escapes {
		// filepath.Join cleans, so build the dirty form explicitly too.
		dirty := filepath.Clean(root) + string(filepath.Separator) + ".." +
			string(filepath.Separator) + "outside.go"
		for _, candidate := range []string{p, dirty} {
			if _, err := r.Check(candidate); err == nil {
				t.Errorf("traversal accepted: %s escapes root %s", candidate, root)
			}
		}
	}
}

// Denied globs win over allowed ones. This is what keeps .env files and
// credential JSON out of reach even inside an allowed tree.
func TestCheck_DeniedGlobsBeatAllowedGlobs(t *testing.T) {
	testutil.NewSandbox(t)
	r, root := scopeRegistry(t, []string{"**"}, []string{"**/.env*", "**/secrets/**"})

	for _, rel := range []string{
		".env",
		filepath.Join("src", ".env.local"),
		filepath.Join("src", "secrets", "key.pem"),
	} {
		if _, err := r.Check(filepath.Join(root, rel)); err == nil {
			t.Errorf("%s was allowed despite a denied glob", rel)
		}
	}
	// …and an ordinary file in the same tree is still allowed, or the deny rule
	// is simply blocking everything.
	if _, err := r.Check(filepath.Join(root, "src", "main.go")); err != nil {
		t.Errorf("an ordinary file was blocked: %v", err)
	}
}

// Matching a glob is required, not optional: a file inside the root but outside
// every allowed glob must be refused.
func TestCheck_FileInsideRootButOutsideEveryGlobIsRefused(t *testing.T) {
	testutil.NewSandbox(t)
	r, root := scopeRegistry(t, []string{"src/**"}, nil)

	if _, err := r.Check(filepath.Join(root, "vendor", "lib.go")); err == nil {
		t.Error("a file outside every allowed glob was accepted")
	}
}

func TestCheck_RelativePathsAreRefused(t *testing.T) {
	testutil.NewSandbox(t)
	r, _ := scopeRegistry(t, []string{"**"}, nil)

	for _, p := range []string{"main.go", "./main.go", filepath.Join("src", "x.go"), ""} {
		if _, err := r.Check(p); err == nil {
			t.Errorf("relative path %q was accepted", p)
		}
	}
}

// HYDRA_WORKSPACE pins the workspace. It must still refuse a path outside that
// workspace's root — an override that widened scope would be a way around the
// whole boundary.
func TestCheck_WorkspaceOverrideCannotWidenScope(t *testing.T) {
	s := testutil.NewSandbox(t)
	r, root := scopeRegistry(t, []string{"**"}, nil)
	s.SetKey(t, "HYDRA_WORKSPACE", "ws")

	if _, err := r.Check(filepath.Join(root, "ok.go")); err != nil {
		t.Fatalf("a file inside the pinned workspace was rejected: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "x.go")
	if _, err := r.Check(outside); err == nil {
		t.Error("HYDRA_WORKSPACE let a path outside its root through")
	}
}

func TestCheck_UnknownWorkspaceOverrideIsAnError(t *testing.T) {
	s := testutil.NewSandbox(t)
	r, root := scopeRegistry(t, []string{"**"}, nil)
	s.SetKey(t, "HYDRA_WORKSPACE", "does-not-exist")

	_, err := r.Check(filepath.Join(root, "ok.go"))
	if err == nil {
		t.Fatal("an undefined HYDRA_WORKSPACE was silently ignored")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error %q does not name the bad workspace", err)
	}
}

// The root itself is inside the workspace.
func TestCheck_RootItselfIsContained(t *testing.T) {
	testutil.NewSandbox(t)
	r, root := scopeRegistry(t, []string{"**", "."}, nil)

	if _, err := r.Check(filepath.Clean(root)); err != nil {
		t.Errorf("the workspace root itself was rejected: %v", err)
	}
}

// ── Resolve ───────────────────────────────────────────────────────────────────

func TestResolve_GitModes(t *testing.T) {
	testutil.NewSandbox(t)
	root := t.TempDir()

	for _, tc := range []struct{ mode, wantRoot string }{
		{"true", filepath.Clean(root)},
		{"false", ""},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			r := &Registry{workspaces: []Workspace{{
				Name: "ws", Root: filepath.Clean(root), Git: tc.mode, AllowedGlobs: []string{"**"},
			}}}
			got, err := r.Resolve(filepath.Join(root, "a.go"))
			if err != nil {
				t.Fatal(err)
			}
			if got.GitRoot != tc.wantRoot {
				t.Errorf("git=%s → GitRoot %q, want %q", tc.mode, got.GitRoot, tc.wantRoot)
			}
			if got.Workspace != "ws" || got.Root != filepath.Clean(root) {
				t.Errorf("Resolve = %+v", got)
			}
		})
	}
}

func TestResolve_RefusesRelativeAndUnknownPaths(t *testing.T) {
	testutil.NewSandbox(t)
	r, _ := scopeRegistry(t, []string{"**"}, nil)

	if _, err := r.Resolve("relative.go"); err == nil {
		t.Error("Resolve accepted a relative path")
	}
	if _, err := r.Resolve(filepath.Join(t.TempDir(), "x.go")); err == nil {
		t.Error("Resolve accepted a path outside every workspace")
	}
}

// ── GitRoot ───────────────────────────────────────────────────────────────────

// GitRoot walks up looking for .git. It must terminate at the filesystem root
// on every platform — a loop that only stops at "/" never stops on Windows,
// where the root is "C:\".
func TestGitRoot_TerminatesAndFindsTheRepo(t *testing.T) {
	testutil.NewSandbox(t)

	repo := t.TempDir()
	deep := filepath.Join(repo, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(deep, "x.go")
	if err := os.WriteFile(file, []byte("package x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := GitRoot(file); got != filepath.Clean(repo) {
		t.Errorf("GitRoot(%s) = %q, want %q", file, got, filepath.Clean(repo))
	}
	// A directory works as well as a file.
	if got := GitRoot(deep); got != filepath.Clean(repo) {
		t.Errorf("GitRoot(dir) = %q, want %q", got, filepath.Clean(repo))
	}
}

// The termination case: no .git anywhere above. This must return "" rather than
// spin forever. It is a plain test, not a benchmark, precisely so a regression
// shows up as a hung CI job rather than a wrong answer.
func TestGitRoot_ReturnsEmptyWhenThereIsNoRepoAbove(t *testing.T) {
	testutil.NewSandbox(t)

	// A temp dir is not guaranteed to be outside a repo on every machine, so
	// only assert termination and shape, not emptiness.
	dir := t.TempDir()
	done := make(chan string, 1)
	go func() { done <- GitRoot(filepath.Join(dir, "nope", "x.go")) }()

	select {
	case got := <-done:
		if got != "" && !strings.HasPrefix(filepath.Clean(dir), got) {
			t.Errorf("GitRoot returned %q, which does not contain %q", got, dir)
		}
	case <-timeoutAfterSeconds(10):
		t.Fatalf("GitRoot did not terminate on %s/%s — the walk-up loop has no "+
			"filesystem-root stop condition for this platform", runtime.GOOS, runtime.GOARCH)
	}
}

// ── ValidatorFor ──────────────────────────────────────────────────────────────

func TestValidatorFor(t *testing.T) {
	r, _ := scopeRegistry(t, []string{"**"}, nil)

	if got := r.ValidatorFor("go"); got != "gofmt -l {file}" {
		t.Errorf("ValidatorFor(go) = %q", got)
	}
	// A leading dot is accepted, since callers hand it filepath.Ext output.
	if got := r.ValidatorFor(".go"); got != "gofmt -l {file}" {
		t.Errorf("ValidatorFor(.go) = %q — a leading dot must be tolerated", got)
	}
	// Explicit null means "no validator", distinct from "unknown extension" —
	// both return "", and both must be safe.
	if got := r.ValidatorFor("ts"); got != "" {
		t.Errorf("ValidatorFor(ts) = %q, want empty (explicitly null)", got)
	}
	if got := r.ValidatorFor("unknown-ext"); got != "" {
		t.Errorf("ValidatorFor(unknown) = %q, want empty", got)
	}
}

// ── Load ──────────────────────────────────────────────────────────────────────

// Load must not fail with no on-disk registry: the embedded copy is what every
// brew/npm/pip install actually runs with (#238).
//
// It must also not fail because an *entry* is unusable. The embedded registry
// ships POSIX roots, and filepath.IsAbs("/Users/x") is false on Windows — which
// made Load error outright there, killing the whole scope layer before it
// looked at a single path (#297). One bad entry must narrow what Hydra can
// touch, never disable it.
func TestLoad_FallsBackToTheEmbeddedRegistryOnEveryPlatform(t *testing.T) {
	testutil.NewSandbox(t)

	r, err := Load("")
	if err != nil {
		t.Fatalf("Load with no on-disk registry failed on %s: %v — this is what every "+
			"installed binary does", runtime.GOOS, err)
	}
	// Whatever survives must be usable.
	for _, ws := range r.workspaces {
		if !filepath.IsAbs(ws.Root) {
			t.Errorf("workspace %q kept a non-absolute root %q", ws.Name, ws.Root)
		}
		if len(ws.AllowedGlobs) == 0 {
			t.Errorf("workspace %q allows nothing — it can never be used", ws.Name)
		}
	}
	// Whatever was dropped must say why, or a user cannot tell "nothing
	// configured" from "your configuration was ignored".
	for _, sk := range r.Skipped() {
		if sk.Name == "" || sk.Reason == "" {
			t.Errorf("skipped entry %+v does not identify itself or say why", sk)
		}
	}
	// Every entry is accounted for exactly once.
	if got, want := len(r.workspaces)+len(r.Skipped()), 2; got != want {
		t.Errorf("embedded registry yielded %d kept + skipped, want %d", got, want)
	}
	if len(r.validators) == 0 {
		t.Error("embedded registry defines no validators")
	}
	t.Logf("%s: %d workspaces usable, %d skipped", runtime.GOOS, len(r.workspaces), len(r.Skipped()))
}

// A root that is not absolute for this platform is skipped, not fatal — and a
// skipped workspace fails closed: paths that would have resolved to it are
// refused rather than silently allowed.
func TestLoad_UnusableRootIsSkippedAndFailsClosed(t *testing.T) {
	s := testutil.NewSandbox(t)

	regDir := filepath.Join(s.HydraHome, "registry")
	if err := os.MkdirAll(regDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "version: \"1.0\"\nworkspaces:\n  bad:\n    root: relative/path\n    allowed_globs: [\"**\"]\n"
	if err := os.WriteFile(filepath.Join(regDir, "workspace.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := Load(s.HydraHome)
	if err != nil {
		t.Fatalf("one unusable entry made the whole load fail: %v", err)
	}
	if len(r.workspaces) != 0 {
		t.Errorf("a relative root was kept: %+v", r.workspaces)
	}
	if len(r.Skipped()) != 1 || r.Skipped()[0].Name != "bad" {
		t.Errorf("the skipped entry was not recorded: %+v", r.Skipped())
	}
	// Fails closed: nothing resolves into a workspace that was dropped.
	abs, err := filepath.Abs(filepath.Join("relative", "path", "x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Check(abs); err == nil {
		t.Error("a path resolved into a workspace that was skipped at load")
	}
}

func TestLoad_MalformedYAMLIsAnError(t *testing.T) {
	s := testutil.NewSandbox(t)

	regDir := filepath.Join(s.HydraHome, "registry")
	if err := os.MkdirAll(regDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(regDir, "workspace.yaml"),
		[]byte("workspaces: [this is not: valid: yaml"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(s.HydraHome); err == nil {
		t.Error("malformed workspace.yaml was accepted")
	}
}

// ── glob matching ─────────────────────────────────────────────────────────────

func TestMatchGlob_SegmentSemantics(t *testing.T) {
	cases := []struct {
		rel, pat string
		want     bool
	}{
		{"src/main.go", "src/**", true},
		{"src/a/b/main.go", "src/**", true},
		{"src", "src/**", true}, // ** matches zero segments
		{"vendor/x.go", "src/**", false},
		{".env", "**/.env*", true},
		{"a/b/.env.local", "**/.env*", true},
		{"a/node_modules/x", "**/node_modules/**", true},
		{"main.go", "*.go", true},
		{"a/main.go", "*.go", false}, // * does not cross a separator
		{"./src/main.go", "src/**", true},
		{"", "**", true},
	}
	for _, tc := range cases {
		if got := matchGlob(tc.rel, tc.pat); got != tc.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tc.rel, tc.pat, got, tc.want)
		}
	}
}

// timeoutAfterSeconds is a local helper so the GitRoot termination test does not
// pull in a package-wide dependency just for one select.
func timeoutAfterSeconds(n int) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		<-time.After(time.Duration(n) * time.Second)
		close(ch)
	}()
	return ch
}
