// SPDX-License-Identifier: MIT

package main

import (
	"regexp"
	"strings"
	"testing"
)

// The RC and edge channels both shipped CLI archives stamped
// 0.0.0-SNAPSHOT-none, and both told people to download a filename that had
// never existed. Nothing failed; the workflows were green every time, and the
// desktop assets beside them were named correctly, which is what made it easy
// to miss (#705, #759).
//
// Two invariants, checked against the workflow files, because that is where
// they are actually decided.

// prereleaseWorkflows are the channels built from a branch, so nothing tags
// HEAD for them and they have to do it themselves. The stable release runs on
// a tag release-please already made.
var prereleaseWorkflows = []string{"rc.yml", "edge.yml"}

func workflowFile(t *testing.T, name string) string {
	t.Helper()
	return repoFile(t, ".github", "workflows", name)
}

// goreleaserArgs pulls the args line handed to goreleaser-action.
func goreleaserArgs(t *testing.T, body, name string) string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "args:") && strings.Contains(s, "release") {
			return s
		}
	}
	t.Fatalf("%s has no goreleaser `args:` line; this test no longer checks what it thinks", name)
	return ""
}

// --snapshot makes GoReleaser compute its own 0.0.0-SNAPSHOT version and
// ignore the tag, so the artifact cannot say which release it stands for.
// --skip=validate is what actually lets a build run with no tag on HEAD.
func TestPrereleaseWorkflows_DoNotBuildInSnapshotMode(t *testing.T) {
	for _, name := range prereleaseWorkflows {
		t.Run(name, func(t *testing.T) {
			args := goreleaserArgs(t, workflowFile(t, name), name)
			if strings.Contains(args, "--snapshot") {
				t.Errorf("%s builds with --snapshot, so its archives will be stamped "+
					"0.0.0-SNAPSHOT and `hyctl version` cannot report the release: %s", name, args)
			}
			if !strings.Contains(args, "validate") {
				t.Errorf("%s does not skip validate, so GoReleaser applies the git *state* "+
					"checks to a tag the workflow made a moment ago: %s", name, args)
			}
		})
	}
}

// GORELEASER_CURRENT_TAG is the only thing supplying the version once
// --snapshot is gone, so its absence is silent: the build falls back to 0.0.0.
func TestPrereleaseWorkflows_PassTheVersionToGoReleaser(t *testing.T) {
	for _, name := range prereleaseWorkflows {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(workflowFile(t, name), "GORELEASER_CURRENT_TAG:") {
				t.Errorf("%s sets no GORELEASER_CURRENT_TAG, so the build has no version "+
					"to stamp and falls back to 0.0.0", name)
			}
		})
	}
}

// edge derived its base version from `git describe`, which cannot name a
// release from develop: release tags live on main and develop's history does
// not contain them, so the most recent tag it could reach was `edge`, the one
// this workflow pushed last time. The version became "edge-edge.<sha>", not
// valid semver, and GoReleaser fell back to 0.0.0.
//
// Verified directly: on this checkout `git describe --tags --abbrev=0` returns
// an RC tag, and excluding prereleases returns nothing at all ("No tags can
// describe"), which is the same 0.0.0 by another route. The manifest is the
// only version reachable from a develop checkout.
func TestEdgeWorkflow_TakesItsBaseFromTheManifestNotGitDescribe(t *testing.T) {
	body := workflowFile(t, "edge.yml")
	// The invocation, not the word: the workflow explains in a comment why it
	// no longer uses git describe, and a bare substring match reads that as
	// the thing it warns against.
	if strings.Contains(body, "$(git describe") {
		t.Error("edge.yml derives its version from git describe, which cannot see a " +
			"release tag from develop and falls back to its own `edge` tag")
	}
	if !strings.Contains(body, ".release-please-manifest.json") {
		t.Error("edge.yml does not read .release-please-manifest.json, the version " +
			"release-please itself reads instead of tags")
	}
	// The same manifest this test suite already checks the docs against, so
	// the two cannot disagree about what the last release was.
	if v := releasedVersion(t); v == "" {
		t.Error("the manifest has no version for edge.yml to build on")
	}
}

// archiveRef matches a hydra_<version>_<os>_<arch> filename in the install
// snippet the release notes publish.
var archiveRef = regexp.MustCompile(`hydra_\$\{\{ steps\.ver\.outputs\.(\w+) \}\}_\w+_\w+\.(?:tar\.gz|zip)`)

// GoReleaser's {{ .Version }} is the semver without the leading v, while the
// tag has it. Interpolating the tag into the filename produced a curl that
// 404s on every prerelease, which is how the edge channel's documented install
// command came to name a file that never existed.
func TestPrereleaseWorkflows_InstallSnippetNamesAnAssetThatExists(t *testing.T) {
	for _, name := range prereleaseWorkflows {
		t.Run(name, func(t *testing.T) {
			body := workflowFile(t, name)
			refs := archiveRef.FindAllStringSubmatch(body, -1)
			if len(refs) == 0 {
				t.Fatalf("%s publishes no hydra_<version>_<os>_<arch> install snippet; "+
					"this test no longer checks what it thinks", name)
			}
			for _, m := range refs {
				if m[1] != "semver" {
					t.Errorf("%s names the archive with outputs.%s, which carries the "+
						"leading v that GoReleaser's filenames do not, so the download 404s: %s",
						name, m[1], m[0])
				}
			}
			// And the output it uses has to be produced, or it interpolates empty.
			if !strings.Contains(body, `echo "semver=`) {
				t.Errorf("%s uses outputs.semver but never writes it, so the filename "+
					"interpolates to hydra__darwin_arm64.tar.gz", name)
			}
		})
	}
}

// outputRef matches a ${{ steps.<id>.outputs.<name> }} reference, and
// currentTagEnv the one a workflow hands GoReleaser as its tag.
var (
	outputRef     = regexp.MustCompile(`steps\.\w+\.outputs\.(\w+)`)
	currentTagEnv = regexp.MustCompile(`GORELEASER_CURRENT_TAG:\s*\$\{\{\s*steps\.\w+\.outputs\.(\w+)\s*\}\}`)
	outputAssign  = regexp.MustCompile(`echo "(\w+)=([^"]*)" >> "\$GITHUB_OUTPUT"`)
)

// A prerelease workflow invents its version, so the tag it names exists only
// if the workflow makes one. --skip=validate does not bypass GoReleaser's read
// of the tag's contents, so from #761 every edge build failed in 0s on
// "couldn't get tag contents" while the tests above stayed green, because they
// only ever checked that a tag was named (#793, #821).
func TestPrereleaseWorkflows_CreateTheTagTheyName(t *testing.T) {
	for _, name := range prereleaseWorkflows {
		t.Run(name, func(t *testing.T) {
			body := workflowFile(t, name)
			m := currentTagEnv.FindStringSubmatch(body)
			if m == nil {
				t.Fatalf("%s hands GoReleaser no GORELEASER_CURRENT_TAG output; this "+
					"test no longer checks what it thinks", name)
			}
			want := m[1]

			// rc.yml tags outputs.tag but hands GoReleaser outputs.version, which
			// works only because one shell expression writes both. Comparing the
			// values rather than the names accepts that and still catches the two
			// drifting apart.
			value := map[string]string{}
			for _, a := range outputAssign.FindAllStringSubmatch(body, -1) {
				value[a[1]] = a[2]
			}
			isTag := func(s string) bool {
				for _, r := range outputRef.FindAllStringSubmatch(s, -1) {
					if r[1] == want || (value[want] != "" && value[r[1]] == value[want]) {
						return true
					}
				}
				return false
			}

			build := strings.Index(body, goreleaserArgs(t, body, name))
			create, push, off := -1, -1, 0
			for _, line := range strings.Split(body, "\n") {
				// The one-line `run:` form, normalised to the block form.
				s := strings.TrimPrefix(strings.TrimSpace(line), "run: ")
				switch {
				case !isTag(s):
				case strings.Contains(s, "git push"):
					push = off
				case strings.Contains(s, "tag ") && !strings.Contains(s, " -d "):
					if create < 0 {
						create = off
					}
				}
				off += len(line) + 1
			}

			switch {
			case create < 0:
				t.Errorf("%s hands GoReleaser outputs.%s but never creates that tag, so "+
					"the build fails reading the tag's contents", name, want)
			case create > build:
				t.Errorf("%s creates outputs.%s only after the build that reads it, and "+
					"steps run in file order", name, want)
			}
			if push >= 0 {
				t.Errorf("%s pushes outputs.%s, putting a prerelease tag in the branch's "+
					"own history, where git describe reaches it again (#759)", name, want)
			}
		})
	}
}

// The stable release runs from publish.yml, on a tag, so it must NOT relax
// git-state validation: refusing a dirty tree or an untagged HEAD is exactly
// what guarantees a stable release is built from its own tag. This is here so
// the fix above cannot be copied into the one workflow that must not have it.
func TestStableRelease_StillValidatesGitState(t *testing.T) {
	args := goreleaserArgs(t, workflowFile(t, "publish.yml"), "publish.yml")
	for _, bad := range []string{"validate", "--snapshot"} {
		if strings.Contains(args, bad) {
			t.Errorf("publish.yml passes %s, relaxing the checks that tie a stable "+
				"release to its own tag: %s", bad, args)
		}
	}
}
