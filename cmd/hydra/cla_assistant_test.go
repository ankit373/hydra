// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The CLA is signed once through CLA Assistant, which posts the license/cla status.
// These pin that the queue waits on it and that the old checkbox gate is gone.

const claStatus = "check-success=license/cla"

// Both contributor queues require license/cla in queue_conditions: merge_conditions
// run on Mergify's draft PR, where the status would judge the batch, not the author.
func TestCLA_ContributorQueuesRequireLicenseCLA(t *testing.T) {
	want := map[string]bool{"develop": false, "release": false}
	for _, r := range mergifyConfig(t)["queue_rules"].([]any) {
		rule := r.(map[string]any)
		name := rule["name"].(string)
		if _, ok := want[name]; !ok {
			continue
		}
		for _, c := range conditionStrings(rule["queue_conditions"]) {
			if strings.ReplaceAll(c, " ", "") == claStatus {
				want[name] = true
			}
		}
		for _, c := range conditionStrings(rule["merge_conditions"]) {
			if strings.Contains(c, "license/cla") {
				t.Errorf("queue %q requires license/cla in merge_conditions; the draft PR would be judged, not the author", name)
			}
		}
	}
	for name, ok := range want {
		if !ok {
			t.Errorf("queue %q does not require %s in queue_conditions", name, claStatus)
		}
	}
}

// The home-grown gate is gone: no workflow, no Mergify condition and no template checkbox.
func TestCLA_NoCheckboxGateRemains(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", ".github", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".yml") && strings.Contains(workflowFile(t, e.Name()), "Contributor has signed") {
			t.Errorf("%s still reports \"Contributor has signed\"; CLA Assistant's license/cla replaced it", e.Name())
		}
	}
	if strings.Contains(repoFile(t, ".mergify.yml"), "Contributor has signed") {
		t.Error(".mergify.yml still waits on \"Contributor has signed\", which nothing reports")
	}
	box := regexp.MustCompile(`(?im)^\s*-\s*\[[ xX]\].*\bCLA\b`)
	if box.MatchString(repoFile(t, ".github", "PULL_REQUEST_TEMPLATE.md")) {
		t.Error("the PR template still has a CLA checkbox; signing is through CLA Assistant")
	}
}

// cla-approve-runs.yml holds actions: write and runs on every status, so pin its
// trigger, its permissions, and that it never checks out or interpolates PR data.
func TestCLA_ApproveRunsWorkflowStaysNarrow(t *testing.T) {
	raw := workflowFile(t, "cla-approve-runs.yml")
	var w struct {
		On          map[string]any `yaml:"on"`
		Permissions map[string]any `yaml:"permissions"`
		Jobs        map[string]struct {
			If          string            `yaml:"if"`
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				Uses string `yaml:"uses"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatalf("parsing cla-approve-runs.yml: %v", err)
	}
	if _, ok := w.On["status"]; !ok || len(w.On) != 1 {
		t.Errorf("triggers = %v, want only status", w.On)
	}
	if w.Permissions == nil || len(w.Permissions) != 0 {
		t.Errorf("top-level permissions = %v, want {}", w.Permissions)
	}
	if len(w.Jobs) == 0 {
		t.Fatal("no jobs; this test no longer checks what it thinks")
	}
	allowed := map[string]string{"actions": "write", "pull-requests": "read"}
	for id, job := range w.Jobs {
		if !strings.Contains(job.If, "'license/cla'") || !strings.Contains(job.If, "'success'") {
			t.Errorf("job %s runs on any status: if = %q", id, job.If)
		}
		for k, v := range job.Permissions {
			if allowed[k] != v {
				t.Errorf("job %s grants %s: %s; only %v is needed", id, k, v, allowed)
			}
		}
		for _, s := range job.Steps {
			if strings.HasPrefix(s.Uses, "actions/checkout") {
				t.Errorf("job %s checks out code; it only calls the API", id)
			}
			if strings.Contains(s.Run, "${{") {
				t.Errorf("job %s interpolates an expression into run:; pass it through env", id)
			}
		}
	}
}
