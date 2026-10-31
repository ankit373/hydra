// SPDX-License-Identifier: MIT

// Package glob implements the single path-glob dialect Hydra uses everywhere:
// workspace allow/deny rules and MCP ledger resource rules.
//
// It exists because those two had different dialects, and one of them changed
// meaning by operating system.
//
//   - internal/workspace matched "**" across path segments.
//   - internal/ledger used filepath.Match, which has no "**" at all, so a
//     "**/secrets/**" rule copied from workspace.yaml into mcp_policy.json
//     matched nothing below the first level: a deny that did not deny (#310).
//   - Worse, filepath.Match is separator-aware, and the separator is "\" on
//     Windows. "/repo/*" therefore matched only one level on Unix but matched
//     arbitrarily deep on Windows, because "/" was not a separator there. An
//     access gate whose rules widen on one platform is not a gate.
//
// Patterns are always "/"-separated regardless of host, because they are
// written in config files that are shared across machines.
package glob

import (
	"path"
	"strings"
	"sync"
)

// Match reports whether the "/"-separated path matches pattern.
//
// Semantics:
//   - "**" matches zero or more whole segments
//   - "*" and "?" match within a single segment and never cross "/"
//   - an empty pattern, or "*" alone at the top level, matches anything,
//     the "no constraint" case both callers rely on
func Match(pattern, p string) bool {
	if pattern == "" || pattern == "*" || pattern == "**" {
		return true
	}
	p = strings.TrimPrefix(p, "./")
	return matchSegments(strings.Split(p, "/"), splitPattern(pattern))
}

var (
	splitCacheMu sync.Mutex
	splitCache   = map[string][]string{}
)

// splitPattern caches a pattern's "/"-split segments. Patterns come from a
// small, fixed, operator-controlled set (policy/workspace config rules) that
// Match is called against repeatedly, once per rule, per resource checked,
// while the path side genuinely differs on every call, so only the pattern
// side is worth memoizing. The returned slice is never mutated by callers.
func splitPattern(pattern string) []string {
	splitCacheMu.Lock()
	if segs, ok := splitCache[pattern]; ok {
		splitCacheMu.Unlock()
		return segs
	}
	splitCacheMu.Unlock()

	segs := strings.Split(pattern, "/")

	splitCacheMu.Lock()
	splitCache[pattern] = segs
	splitCacheMu.Unlock()
	return segs
}

// matchSegments is memoized on (path index, pattern index).
//
// Without it each "**" branches over every remaining split point and matching
// is exponential in the number of "**" segments: "**/**/**/…/nomatch" against a
// long path never returns. That is a denial of service on whatever the pattern
// gates, found by fuzzing the workspace matcher (#303).
func matchSegments(pathSegs, pat []string) bool {
	type state struct{ i, j int }
	memo := make(map[state]bool)

	var match func(i, j int) bool
	match = func(i, j int) bool {
		key := state{i, j}
		if cached, ok := memo[key]; ok {
			return cached
		}
		result := func() bool {
			if j == len(pat) {
				return i == len(pathSegs)
			}
			if pat[j] == "**" {
				for k := i; k <= len(pathSegs); k++ {
					if match(k, j+1) {
						return true
					}
				}
				return false
			}
			if i == len(pathSegs) {
				return false
			}
			// path.Match, not filepath.Match: this operates on one already-split
			// segment, and path.Match's separator is always "/" on every host.
			if ok, _ := path.Match(pat[j], pathSegs[i]); !ok {
				return false
			}
			return match(i+1, j+1)
		}()
		memo[key] = result
		return result
	}
	return match(0, 0)
}
