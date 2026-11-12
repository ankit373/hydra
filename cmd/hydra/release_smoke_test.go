// SPDX-License-Identifier: MIT

package main

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Users install hyctl from five places, and before release-smoke.yml nothing
// installed from any of them after a release. These keep the smoke test
// covering every channel and keep its failure able to stop the next release.

var smokeChannels = []string{"brew", "npm", "pip", "install.sh", "archive"}

func TestReleaseSmoke_CoversEveryChannel(t *testing.T) {
	var wf struct {
		Jobs map[string]struct {
			Strategy struct {
				Matrix struct {
					Include []map[string]string `yaml:"include"`
				} `yaml:"matrix"`
			} `yaml:"strategy"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(workflowFile(t, "release-smoke.yml")), &wf); err != nil {
		t.Fatal(err)
	}
	var covered []string
	for _, row := range wf.Jobs["install"].Strategy.Matrix.Include {
		covered = append(covered, row["channel"])
	}
	script := repoFile(t, "scripts", "release-smoke.sh")
	for _, ch := range smokeChannels {
		if !slices.Contains(covered, ch) {
			t.Errorf("release-smoke.yml never installs from %s", ch)
		}
		if !strings.Contains(script, "\n  "+ch+") smoke_") {
			t.Errorf("scripts/release-smoke.sh has no case for %s", ch)
		}
	}
}

// A smoke job that only runs when every publish job passed would skip exactly
// the half-published releases it is there to catch.
func TestReleaseSmoke_RunsAfterPublishEvenWhenAChannelFailed(t *testing.T) {
	var wf struct {
		Jobs map[string]struct {
			Needs any    `yaml:"needs"`
			If    string `yaml:"if"`
			Uses  string `yaml:"uses"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(workflowFile(t, "release-please.yml")), &wf); err != nil {
		t.Fatal(err)
	}
	for id, j := range wf.Jobs {
		if !strings.HasSuffix(j.Uses, "/release-smoke.yml") {
			continue
		}
		needs := conditionStrings(j.Needs)
		if !slices.Contains(needs, "publish") {
			t.Errorf("job %s runs release-smoke.yml without waiting for publish: needs %v", id, needs)
		}
		if !strings.Contains(j.If, "!cancelled()") {
			t.Errorf("job %s skips the smoke test when a publish job fails: if %q", id, j.If)
		}
		return
	}
	t.Error("release-please.yml never runs release-smoke.yml")
}

// The label the smoke test sets has to be one Mergify refuses to land.
func TestReleaseSmoke_HoldLabelBlocksTheQueue(t *testing.T) {
	label := regexp.MustCompile(`--add-label (\S+)`).FindStringSubmatch(workflowFile(t, "release-smoke.yml"))
	if label == nil {
		t.Fatal("release-smoke.yml labels no release PR; this test no longer checks what it thinks")
	}
	shared := mergifyConfig(t)["shared"].(map[string]any)
	if !slices.Contains(conditionStrings(shared["release_not_held"]), "label!="+label[1]) {
		t.Errorf("release-smoke.yml holds the release PR with %q, but .mergify.yml's release_not_held does not refuse it", label[1])
	}
}
