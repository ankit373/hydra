// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Version strings are hand-maintained in several files and go stale every
// release. app.html alone carries 23 of them, so a partial update is the
// normal failure, not a rare one: the ones you miss keep pointing at the
// previous release's assets, which 404, and nothing says so.
//
// CLAUDE.md already documents that these need bumping by hand. That is what
// keeps failing. This turns "remember to update them" into a red build.

// releasedVersion is what release-please last shipped, and the version the
// asset names on the releases page actually carry.
func releasedVersion(t *testing.T) string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal([]byte(repoFile(t, ".release-please-manifest.json")), &m); err != nil {
		t.Fatalf("release-please manifest is not valid JSON: %v", err)
	}
	v, ok := m["."]
	if !ok || v == "" {
		t.Fatal(`release-please manifest has no "." entry; there is no version to check against`)
	}
	return v
}

// Every version reference in the docs must name one version. A file naming two
// is mid-update, which is exactly the state that ships broken download links.
func TestDocs_VersionReferencesAgree(t *testing.T) {
	files := map[string]string{
		"docs/app.html":   repoFile(t, "docs", "app.html"),
		"docs/index.html": repoFile(t, "docs", "index.html"),
		"README.md":       repoFile(t, "README.md"),
	}

	// Only lines that are actually about the Hydra release: a page mentions
	// plenty of other version-shaped numbers (Wails 2.15.0, schema versions).
	rx := regexp.MustCompile(`(?:HYDRA_VERSION=v|releases/download/v|hydra-desktop_v|"softwareVersion":\s*"|Release&nbsp;: <span class="g">v)(\d+\.\d+\.\d+)`)

	seen := map[string][]string{} // version -> files naming it
	for name, src := range files {
		for _, m := range rx.FindAllStringSubmatch(src, -1) {
			v := m[1]
			if !contains(seen[v], name) {
				seen[v] = append(seen[v], name)
			}
		}
	}
	if len(seen) == 0 {
		t.Fatal("no Hydra version references found at all; this test has stopped testing anything")
	}
	if len(seen) > 1 {
		t.Errorf("the docs name %d different Hydra versions; a partial bump ships download links that 404:", len(seen))
		for v, where := range seen {
			t.Errorf("  %s in %v", v, where)
		}
	}
}

// The docs must not fall behind what was actually released, or every download
// link points at the previous release's asset names.
//
// Ahead is allowed and is the normal pre-release state: the docs are bumped in
// the PR that prepares a release, and release-please only writes the manifest
// when that release actually lands. Requiring equality would make preparing a
// release fail this test, which is the opposite of the point.
func TestDocs_VersionIsNotBehindTheRelease(t *testing.T) {
	want := releasedVersion(t)
	src := repoFile(t, "docs", "app.html")

	rx := regexp.MustCompile(`releases/download/v(\d+\.\d+\.\d+)/`)
	found := map[string]bool{}
	for _, m := range rx.FindAllStringSubmatch(src, -1) {
		found[m[1]] = true
	}
	if len(found) == 0 {
		t.Fatal("app.html has no release download links; this test has stopped testing anything")
	}
	for v := range found {
		if cmpSemver(v, want) < 0 {
			t.Errorf("app.html links to v%s but %s was released since.\n"+
				"Desktop asset names embed their version, so /releases/latest/download/ "+
				"cannot address them and these go stale every release. Bump them, or the "+
				"downloads 404.", v, want)
		}
	}
}

// cmpSemver compares two dotted numeric versions. Reports -1, 0 or 1.
// Deliberately not a dependency: these are always plain X.Y.Z here, and the
// regexes above have already refused anything else.
func cmpSemver(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// An asset URL names the version twice, in the tag and in the filename. They
// have drifted apart before, and a mismatched pair 404s while looking right.
func TestDocs_AssetNamesMatchTheirTag(t *testing.T) {
	src := repoFile(t, "docs", "app.html")
	rx := regexp.MustCompile(`releases/download/v(\d+\.\d+\.\d+)/hydra-desktop_v(\d+\.\d+\.\d+)_`)

	ms := rx.FindAllStringSubmatch(src, -1)
	if len(ms) == 0 {
		t.Fatal("no desktop asset links found; this test has stopped testing anything")
	}
	for _, m := range ms {
		if m[1] != m[2] {
			t.Errorf("asset link uses tag v%s but filename v%s; one of them 404s", m[1], m[2])
		}
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
