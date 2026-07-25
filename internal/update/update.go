// SPDX-License-Identifier: MIT

// Package update checks for newer Hydra releases and notifies the user.
// Matches the pattern used by github.com/cli/cli/internal/update:
//   - Results cached in ~/.hydra/update_state.json for 24 h
//   - Only shown in interactive TTY sessions
//   - Disabled by HYDRA_NO_UPDATE_CHECK=1 or CI=true or dev build
package update

import (
	"encoding/json"
	"fmt"
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
	releaseURL = "https://api.github.com/repos/ankit373/hydra/releases/latest"
	cacheFile  = "update_state.json"
	cacheTTL   = 24 * time.Hour
)

type state struct {
	CheckedAt     time.Time `json:"checked_at"`
	LatestVersion string    `json:"latest_version"`
}

var (
	checkOnce   sync.Once
	checkResult string
)

// Check returns the latest version if an update is available, empty otherwise.
// Safe to call multiple times — the network fetch runs at most once per process.
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
	req, err := http.NewRequest(http.MethodGet, releaseURL, nil)
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

// semverGT reports whether a > b (both "vX.Y.Z[-pre]" format).
func semverGT(a, b string) bool {
	ap, bp := parseVer(a), parseVer(b)
	for i := range ap {
		if ap[i] != bp[i] {
			return ap[i] > bp[i]
		}
	}
	return false
}

func parseVer(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	if idx := strings.IndexAny(v, "-+"); idx != -1 {
		v = v[:idx]
	}
	parts := strings.SplitN(v, ".", 3)
	var out [3]int
	for i := 0; i < len(parts) && i < 3; i++ {
		fmt.Sscanf(parts[i], "%d", &out[i])
	}
	return out
}
