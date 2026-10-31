// SPDX-License-Identifier: MIT

package update

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/build"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/testutil"
)

// The update check runs at startup on every command. Its failure modes are
// asymmetric: a missed update is a minor annoyance, but a check that blocks,
// fetches on every invocation, or fires inside CI is a cost paid by everyone.

func stubReleases(t *testing.T, status int, body string) *int {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q, want GitHub's own media type", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got == "" {
			t.Error("no X-GitHub-Api-Version header; the response shape is unpinned")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	orig := ReleaseURL
	ReleaseURL = srv.URL
	t.Cleanup(func() { ReleaseURL = orig })
	return &calls
}

func TestFetchLatest_ReadsTheTagName(t *testing.T) {
	testutil.NewSandbox(t)
	stubReleases(t, 200, `{"tag_name":"v1.4.0","name":"ignored"}`)

	if got := fetchLatest(); got != "v1.4.0" {
		t.Errorf("fetchLatest() = %q, want v1.4.0", got)
	}
}

// Every failure must be silent and empty. An update check that surfaces a
// network error on a plane is worse than no update check.
func TestFetchLatest_FailuresAreSilentAndEmpty(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"rate limited", http.StatusForbidden, `{"message":"API rate limit exceeded"}`},
		{"not found", http.StatusNotFound, `{}`},
		{"server error", http.StatusInternalServerError, ``},
		{"unparsable body", http.StatusOK, `{truncated`},
		{"no tag in the payload", http.StatusOK, `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testutil.NewSandbox(t)
			stubReleases(t, tt.status, tt.body)
			if got := fetchLatest(); got != "" {
				t.Errorf("fetchLatest() = %q, want empty", got)
			}
		})
	}

	// Nothing listening at all.
	testutil.NewSandbox(t)
	orig := ReleaseURL
	ReleaseURL = "http://127.0.0.1:1/releases"
	t.Cleanup(func() { ReleaseURL = orig })
	if got := fetchLatest(); got != "" {
		t.Errorf("fetchLatest() = %q with a dead endpoint, want empty", got)
	}
}

// A GITHUB_TOKEN in the environment is used, which is what keeps a CI-adjacent
// machine from being rate-limited into never checking.
func TestFetchLatest_UsesGitHubTokenWhenPresent(t *testing.T) {
	testutil.NewSandbox(t)

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0"}`))
	}))
	defer srv.Close()
	orig := ReleaseURL
	ReleaseURL = srv.URL
	defer func() { ReleaseURL = orig }()

	t.Setenv("GITHUB_TOKEN", "ghp_test")
	fetchLatest()
	if gotAuth != "Bearer ghp_test" {
		t.Errorf("Authorization = %q, want the token as a bearer", gotAuth)
	}
}

// The 24h cache is what stops every hyctl invocation hitting GitHub.
func TestResolveLatest_CachesForTwentyFourHours(t *testing.T) {
	testutil.NewSandbox(t)
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	calls := stubReleases(t, 200, `{"tag_name":"v2.0.0"}`)

	if got := resolveLatest(); got != "v2.0.0" {
		t.Fatalf("resolveLatest() = %q", got)
	}
	if *calls != 1 {
		t.Fatalf("made %d calls on a cold cache, want 1", *calls)
	}

	// Second call must be served from the cache.
	if got := resolveLatest(); got != "v2.0.0" {
		t.Errorf("resolveLatest() = %q from cache", got)
	}
	if *calls != 1 {
		t.Errorf("made %d calls with a warm cache, every command would hit GitHub", *calls)
	}

	// A cache older than the TTL is refetched.
	stale := state{CheckedAt: time.Now().Add(-25 * time.Hour), LatestVersion: "v1.0.0"}
	raw, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.Dir(), cacheFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveLatest(); got != "v2.0.0" {
		t.Errorf("resolveLatest() = %q with a stale cache, want a refetch", got)
	}
	if *calls != 2 {
		t.Errorf("made %d calls, want a refetch after the TTL", *calls)
	}
}

// A corrupt cache must be refetched, not treated as "no update".
func TestResolveLatest_CorruptCacheIsRefetched(t *testing.T) {
	testutil.NewSandbox(t)
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.Dir(), cacheFile), []byte("{truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	stubReleases(t, 200, `{"tag_name":"v3.0.0"}`)

	if got := resolveLatest(); got != "v3.0.0" {
		t.Errorf("resolveLatest() = %q with a corrupt cache", got)
	}
}

// A failed fetch must not write a cache entry, or the failure is remembered for
// 24 hours and the user is never told about a release.
func TestResolveLatest_AFailedFetchIsNotCached(t *testing.T) {
	testutil.NewSandbox(t)
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	stubReleases(t, http.StatusForbidden, `{}`)

	if got := resolveLatest(); got != "" {
		t.Errorf("resolveLatest() = %q", got)
	}
	if _, err := os.Stat(filepath.Join(config.Dir(), cacheFile)); err == nil {
		t.Error("a failed fetch wrote a cache entry, so the failure is remembered " +
			"for 24h and no update is ever reported")
	}
}

// The three environment gates run before anything touches the network. A check
// that fires inside CI is a request per job, on every job, forever.
func TestDoCheck_RefusesBeforeTouchingTheNetwork(t *testing.T) {
	tests := []struct {
		name string
		env  [2]string
	}{
		{"HYDRA_NO_UPDATE_CHECK is set", [2]string{"HYDRA_NO_UPDATE_CHECK", "1"}},
		{"running in CI", [2]string{"CI", "true"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testutil.NewSandbox(t)
			calls := stubReleases(t, 200, `{"tag_name":"v9.9.9"}`)
			t.Setenv(tt.env[0], tt.env[1])

			if got := doCheck(); got != "" {
				t.Errorf("doCheck() = %q, want empty", got)
			}
			if *calls != 0 {
				t.Errorf("made %d network calls despite the gate", *calls)
			}
		})
	}

	// A dev build has no release to compare against.
	t.Run("dev build", func(t *testing.T) {
		testutil.NewSandbox(t)
		calls := stubReleases(t, 200, `{"tag_name":"v9.9.9"}`)
		orig := build.Version
		build.Version = "dev"
		t.Cleanup(func() { build.Version = orig })

		if got := doCheck(); got != "" {
			t.Errorf("doCheck() = %q on a dev build", got)
		}
		if *calls != 0 {
			t.Errorf("a dev build made %d network calls", *calls)
		}
	})

	// Non-interactive output is the last gate: `hyctl cost --json | jq` must not
	// have an update banner appended to its stdout. Tests run with stdout piped,
	// so this is the state under test here.
	t.Run("non-interactive stdout", func(t *testing.T) {
		testutil.NewSandbox(t)
		calls := stubReleases(t, 200, `{"tag_name":"v9.9.9"}`)
		orig := build.Version
		build.Version = "v0.0.1"
		t.Cleanup(func() { build.Version = orig })

		if got := doCheck(); got != "" {
			t.Errorf("doCheck() = %q with stdout piped, the banner would land in "+
				"whatever is parsing the output", got)
		}
		if *calls != 0 {
			t.Errorf("made %d network calls with no terminal to show a banner on", *calls)
		}
	})
}

// Check memoises, so a command that consults it more than once fetches at most
// once per process.
func TestCheckAndCheckAsync_FetchAtMostOncePerProcess(t *testing.T) {
	testutil.NewSandbox(t)
	calls := stubReleases(t, 200, `{"tag_name":"v9.9.9"}`)

	first := Check()
	second := Check()
	if first != second {
		t.Errorf("Check() returned %q then %q", first, second)
	}

	got := <-CheckAsync()
	if got != first {
		t.Errorf("CheckAsync() = %q, want the same answer as Check() %q", got, first)
	}
	if *calls > 1 {
		t.Errorf("made %d network calls; the check is memoised for the process", *calls)
	}
}

// CheckIgnoringTTY exists for exactly one reason: a caller with no controlling
// terminal (the desktop app) still needs an answer. go test's own stdout is
// never a TTY, so doCheck() returning "" here is the gap this closes.
func TestCheckIgnoringTTY_SkipsTheTTYGate(t *testing.T) {
	testutil.NewSandbox(t)
	calls := stubReleases(t, 200, `{"tag_name":"v9.9.9"}`)
	orig := build.Version
	build.Version = "v0.0.1"
	t.Cleanup(func() { build.Version = orig })

	if got := doCheck(); got != "" {
		t.Fatalf("doCheck() = %q, want empty under go test (no TTY), precondition for this test", got)
	}
	if got := CheckIgnoringTTY(); got != "v9.9.9" {
		t.Errorf("CheckIgnoringTTY() = %q, want v9.9.9 despite no TTY", got)
	}
	if *calls != 1 {
		t.Errorf("made %d network calls, want 1", *calls)
	}
}

// The env-var and dev-build gates still apply, only the TTY check is skipped.
func TestCheckIgnoringTTY_StillRefusesBeforeTouchingTheNetwork(t *testing.T) {
	tests := []struct {
		name string
		env  [2]string
	}{
		{"HYDRA_NO_UPDATE_CHECK is set", [2]string{"HYDRA_NO_UPDATE_CHECK", "1"}},
		{"running in CI", [2]string{"CI", "true"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testutil.NewSandbox(t)
			calls := stubReleases(t, 200, `{"tag_name":"v9.9.9"}`)
			t.Setenv(tt.env[0], tt.env[1])

			if got := CheckIgnoringTTY(); got != "" {
				t.Errorf("CheckIgnoringTTY() = %q, want empty", got)
			}
			if *calls != 0 {
				t.Errorf("made %d network calls despite the gate", *calls)
			}
		})
	}

	t.Run("dev build", func(t *testing.T) {
		testutil.NewSandbox(t)
		calls := stubReleases(t, 200, `{"tag_name":"v9.9.9"}`)
		orig := build.Version
		build.Version = "dev"
		t.Cleanup(func() { build.Version = orig })

		if got := CheckIgnoringTTY(); got != "" {
			t.Errorf("CheckIgnoringTTY() = %q on a dev build", got)
		}
		if *calls != 0 {
			t.Errorf("a dev build made %d network calls", *calls)
		}
	})
}

// No update is offered to a caller already on the latest version.
func TestCheckIgnoringTTY_NoUpdateWhenCurrent(t *testing.T) {
	testutil.NewSandbox(t)
	stubReleases(t, 200, `{"tag_name":"v1.0.0"}`)
	orig := build.Version
	build.Version = "v1.0.0"
	t.Cleanup(func() { build.Version = orig })

	if got := CheckIgnoringTTY(); got != "" {
		t.Errorf("CheckIgnoringTTY() = %q, want empty when already current", got)
	}
}

// Unlike Check, CheckIgnoringTTY must not freeze its answer for the life of
// the process via sync.Once, the desktop app is long-running and expected to
// poll it. The on-disk cache in resolveLatest is what bounds the network
// calls, not an in-process memo.
func TestCheckIgnoringTTY_DoesNotMemoiseInProcess(t *testing.T) {
	testutil.NewSandbox(t)
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	calls := stubReleases(t, 200, `{"tag_name":"v2.0.0"}`)
	orig := build.Version
	build.Version = "v1.0.0"
	t.Cleanup(func() { build.Version = orig })

	first := CheckIgnoringTTY()
	second := CheckIgnoringTTY()
	if first != "v2.0.0" || second != "v2.0.0" {
		t.Fatalf("CheckIgnoringTTY() = %q then %q, want v2.0.0 both times", first, second)
	}
	if *calls != 1 {
		t.Errorf("made %d network calls, want 1 (bounded by the 24h disk cache, not a process memo)", *calls)
	}
}
