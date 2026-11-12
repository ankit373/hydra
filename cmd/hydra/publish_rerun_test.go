// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// npm and PyPI never take a version twice, so a re-run of publish.yml for a
// tag that is partly out has to treat work already done as done. Before these,
// "Re-run failed jobs" went red on every channel that had already worked.

type publishStep struct {
	ID   string         `yaml:"id"`
	Name string         `yaml:"name"`
	Run  string         `yaml:"run"`
	Uses string         `yaml:"uses"`
	With map[string]any `yaml:"with"`
}

func publishSteps(t *testing.T, file, job string) []publishStep {
	t.Helper()
	var wf struct {
		Jobs map[string]struct {
			Steps []publishStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(workflowFile(t, file)), &wf); err != nil {
		t.Fatal(err)
	}
	steps := wf.Jobs[job].Steps
	if len(steps) == 0 {
		t.Fatalf("%s has no job %q; this test no longer checks what it thinks", file, job)
	}
	return steps
}

func runOf(t *testing.T, file, job, contains string) string {
	t.Helper()
	for _, s := range publishSteps(t, file, job) {
		if strings.Contains(s.Run, contains) {
			return s.Run
		}
	}
	t.Fatalf("%s job %s has no step running %q", file, job, contains)
	return ""
}

// Counting only .tar.gz called a release with no zips or SBOMs complete, and
// a re-run never uploaded them. checksums.txt names every format.
func TestPublish_SkipCheckUsesTheChecksumManifest(t *testing.T) {
	var check string
	for _, s := range publishSteps(t, "publish.yml", "binaries") {
		if s.ID == "check" {
			check = s.Run
		}
	}
	if check == "" {
		t.Fatal("publish.yml binaries has no `check` step; this test no longer checks what it thinks")
	}
	if !strings.Contains(check, "checksums.txt") {
		t.Error("the GoReleaser skip check does not compare against checksums.txt, so a half-uploaded release reads as complete")
	}
	for _, ext := range []string{`endswith(".tar.gz")`, `endswith(".zip")`} {
		if strings.Contains(check, ext) {
			t.Errorf("the GoReleaser skip check filters on %s, which misses every other archive format", ext)
		}
	}
}

func TestPublish_PyPIUploadSkipsFilesAlreadyThere(t *testing.T) {
	if run := runOf(t, "publish.yml", "pip", "twine upload"); !strings.Contains(run, "--skip-existing") {
		t.Error("twine upload has no --skip-existing, so a re-run fails on the files PyPI already has")
	}
}

func TestPublish_NpmTreatsAPublishedVersionAsDone(t *testing.T) {
	run := runOf(t, "publish.yml", "npm", "npm publish")
	if !strings.Contains(run, `npm view "hyctl@$VERSION" version`) {
		t.Error("the npm job does not check whether the version is already on npm, so a re-run fails")
	}
	if !strings.Contains(run, "pipefail") {
		t.Error("the npm job pipes npm publish through tee without pipefail, so a failed publish reads green")
	}
}

// git commit with nothing staged exits 1, and the script runs under set -e.
func TestTapScript_ExitsCleanlyWhenTheFormulaIsUnchanged(t *testing.T) {
	src := repoFile(t, "scripts", "update-tap-formula.sh")
	guard := strings.Index(src, "diff --cached --quiet")
	commit := strings.Index(src, " commit -m")
	if guard < 0 || commit < 0 || guard > commit {
		t.Error("update-tap-formula.sh commits without first checking that the formula changed, so a re-run fails")
	}
}

// Without a ref, a re-publish of an old tag builds the dispatching branch's
// head and uploads it under that tag.
func TestPublish_DesktopBuildsTheTagItUploadsTo(t *testing.T) {
	var wf struct {
		Jobs map[string]struct {
			Uses string         `yaml:"uses"`
			With map[string]any `yaml:"with"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(workflowFile(t, "publish.yml")), &wf); err != nil {
		t.Fatal(err)
	}
	for id, j := range wf.Jobs {
		if strings.HasSuffix(j.Uses, "/desktop-build.yml") {
			if ref, _ := j.With["ref"].(string); ref != "${{ inputs.tag }}" {
				t.Errorf("publish.yml job %s builds desktop from ref %v, want ${{ inputs.tag }}", id, j.With["ref"])
			}
			return
		}
	}
	t.Error("publish.yml never calls desktop-build.yml; this test no longer checks what it thinks")
}
