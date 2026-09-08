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

// prereleaseWorkflows are the channels built from a branch, with no tag on
// HEAD. The stable release is tagged, so it never had this problem.
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
				t.Errorf("%s does not skip validate, and HEAD carries no tag here, so "+
					"GoReleaser will refuse the build: %s", name, args)
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
