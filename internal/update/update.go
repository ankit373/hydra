// SPDX-License-Identifier: MIT

// Package update checks for newer Hydra releases and notifies the user.
// Matches the pattern used by github.com/cli/cli/internal/update:
//   - Results cached in ~/.hydra/update_state.json for 24 h
//   - Only shown in interactive TTY sessions
//   - Disabled by HYDRA_NO_UPDATE_CHECK=1 or CI=true or dev build
package update

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ankit373/hydra/internal/build"
	"github.com/ankit373/hydra/internal/config"
	"github.com/mattn/go-isatty"
)

const (
	cacheFile = "update_state.json"
	cacheTTL  = 24 * time.Hour
)

// ReleaseURL is a var so tests can point it at an httptest server, matching
// pricing's openRouterModelsURL. The alternative is a test that either reaches
// GitHub for real or does not run at all. Exported (not just this package's
// concern) so desktop/api's tests can drive GetUpdateStatus/TriggerUpgrade
// against a stub too, rather than reaching the real GitHub API.
var ReleaseURL = "https://api.github.com/repos/ankit373/hydra/releases/latest"

type state struct {
	CheckedAt     time.Time `json:"checked_at"`
	LatestVersion string    `json:"latest_version"`
}

var (
	checkOnce   sync.Once
	checkResult string
)

// Check returns the latest version if an update is available, empty otherwise.
// Safe to call multiple times, the network fetch runs at most once per process.
func Check() string {
	checkOnce.Do(func() { checkResult = doCheck() })
	return checkResult
}

// CheckAsync fires Check in a goroutine and returns a buffered channel.
// The channel delivers exactly one value (the version string or "").
// Drain with a select/default to avoid blocking if the check hasn't returned.
func CheckAsync() <-chan string {
	ch := make(chan string, 1)
	go func() { ch <- Check() }()
	return ch
}

func doCheck() string {
	if os.Getenv("HYDRA_NO_UPDATE_CHECK") != "" || os.Getenv("CI") != "" {
		return ""
	}
	if build.Version == "dev" {
		return ""
	}
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		return ""
	}
	return checkLatest()
}

// CheckIgnoringTTY runs the same opt-out and dev-build gates as doCheck, plus
// the shared 24h-cached GitHub fetch and semver compare, but skips both the
// TTY check and the CI check. It exists for callers with no controlling
// terminal to gate on, the desktop app has no stdout at all, so doCheck (and
// therefore Check) would always silently return "" for it. The CI gate exists
// in doCheck to keep hyctl's own CLI banner quiet inside automated scripts;
// that reasoning doesn't transfer to a GUI app checking for its own update,
// and keeping it here would make a real user's update check silently no-op if
// their shell happens to export CI=1 for an unrelated reason.
//
// Unlike Check, this does not memoise with sync.Once: Check's process is
// hyctl, which runs once and exits, so "at most one fetch per process" and "at
// most one fetch per 24h" are the same guarantee. The desktop app is
// long-running and expected to call this repeatedly (e.g. on a poll timer), so
// memoising in-process would freeze the answer for the life of the window.
// resolveLatest's on-disk cache is what bounds this to one network fetch per
// 24h regardless of caller.
func CheckIgnoringTTY() string {
	if os.Getenv("HYDRA_NO_UPDATE_CHECK") != "" {
		return ""
	}
	if build.Version == "dev" {
		return ""
	}
	return checkLatest()
}

// checkLatest resolves the latest release (cache-then-fetch) and reports it
// only if newer than the running build. Shared by doCheck and CheckIgnoringTTY
// so the two entry points cannot drift on what "an update exists" means.
func checkLatest() string {
	latest := resolveLatest()
	if latest != "" && semverGT(latest, build.Version) {
		return latest
	}
	return ""
}

func resolveLatest() string {
	cachePath := filepath.Join(config.Dir(), cacheFile)
	if data, err := os.ReadFile(cachePath); err == nil {
		var s state
		if json.Unmarshal(data, &s) == nil && time.Since(s.CheckedAt) < cacheTTL {
			return s.LatestVersion
		}
	}
	latest := fetchLatest()
	if latest != "" {
		s := state{CheckedAt: time.Now(), LatestVersion: latest}
		if data, err := json.Marshal(s); err == nil {
			_ = os.WriteFile(cachePath, data, 0o600)
		}
	}
	return latest
}

func fetchLatest() string {
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequest(http.MethodGet, ReleaseURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	defer resp.Body.Close()
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return ""
	}
	return payload.TagName
}

// semverGT reports whether a > b, following SemVer 2.0.0 precedence.
//
// The pre-release identifier is part of the comparison, not something to
// discard. Stripping it, as this did, made every release equal to its own
// pre-releases, so semverGT("v1.1.0", "v1.1.0-rc.9") was false and a user
// running rc.9 was never told the final v1.1.0 existed. rc-to-rc updates were
// invisible for the same reason.
func semverGT(a, b string) bool {
	aNum, aPre := parseVer(a)
	bNum, bPre := parseVer(b)

	for i := range aNum {
		if aNum[i] != bNum[i] {
			return aNum[i] > bNum[i]
		}
	}
	// Same X.Y.Z: a pre-release has LOWER precedence than the release itself.
	if aPre == "" && bPre == "" {
		return false
	}
	if aPre == "" {
		return true // a is the release, b is a pre-release of it
	}
	if bPre == "" {
		return false
	}
	return comparePrerelease(aPre, bPre) > 0
}

// comparePrerelease orders two dot-separated pre-release strings per SemVer:
// numeric identifiers compare numerically, alphanumeric ones lexically, numeric
// sorts below alphanumeric, and a longer run of equal identifiers wins.
func comparePrerelease(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] == bs[i] {
			continue
		}
		an, aIsNum := numericIdent(as[i])
		bn, bIsNum := numericIdent(bs[i])
		switch {
		case aIsNum && bIsNum:
			if an != bn {
				return sign(an - bn)
			}
		case aIsNum:
			return -1 // numeric < alphanumeric
		case bIsNum:
			return 1
		default:
			return strings.Compare(as[i], bs[i])
		}
	}
	return sign(len(as) - len(bs))
}

func numericIdent(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}

// parseVer splits "vX.Y.Z[-pre][+build]" into its numeric core and pre-release.
// Build metadata is discarded: SemVer excludes it from precedence entirely.
func parseVer(v string) ([3]int, string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if idx := strings.IndexByte(v, '+'); idx != -1 {
		v = v[:idx] // build metadata never affects precedence
	}
	pre := ""
	if idx := strings.IndexByte(v, '-'); idx != -1 {
		pre, v = v[idx+1:], v[:idx]
	}
	parts := strings.SplitN(v, ".", 3)
	var out [3]int
	for i := 0; i < len(parts) && i < 3; i++ {
		if n, ok := numericIdent(parts[i]); ok {
			out[i] = n
		}
	}
	return out, pre
}
