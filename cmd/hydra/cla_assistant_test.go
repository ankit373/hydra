// SPDX-License-Identifier: MIT

package main

import (
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The CLA is signed once through CLA Assistant, which posts the license/cla status.
// cla.yml mirrors it as "Contributor has signed" until main's Mergify reads license/cla
// (step 2 of #1380). These pin the bridge and that the checkbox gate is gone.

const claCondition = "check-success=Contributor has signed"

type ghWorkflow struct {
	On          map[string]any `yaml:"on"`
	Permissions map[string]any `yaml:"permissions"`
	Jobs        map[string]struct {
		Name        string            `yaml:"name"`
		If          string            `yaml:"if"`
		Permissions map[string]string `yaml:"permissions"`
		Steps       []struct {
			Uses string `yaml:"uses"`
			Run  string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func parseWorkflow(t *testing.T, name string) ghWorkflow {
	t.Helper()
	var w ghWorkflow
	if err := yaml.Unmarshal([]byte(workflowFile(t, name)), &w); err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
	if len(w.Jobs) == 0 {
		t.Fatalf("%s has no jobs; this test no longer checks what it thinks", name)
	}
	return w
}

// checkNarrow fails on anything beyond {} at the top, the allowed job grants, a
// checkout, or an expression interpolated into run:.
func checkNarrow(t *testing.T, name string, w ghWorkflow, allowed map[string]string) {
	t.Helper()
	if w.Permissions == nil || len(w.Permissions) != 0 {
		t.Errorf("%s: top-level permissions = %v, want {}", name, w.Permissions)
	}
	for id, job := range w.Jobs {
		for k, v := range job.Permissions {
			if allowed[k] != v {
				t.Errorf("%s job %s grants %s: %s; only %v is needed", name, id, k, v, allowed)
			}
		}
		for _, s := range job.Steps {
			if strings.HasPrefix(s.Uses, "actions/checkout") {
				t.Errorf("%s job %s checks out code; it only calls the API", name, id)
			}
			if strings.Contains(s.Run, "${{") {
				t.Errorf("%s job %s interpolates an expression into run:; pass it through env", name, id)
			}
		}
	}
}

// Both contributor queues require the CLA in queue_conditions: merge_conditions run
// on Mergify's draft PR, which mergify[bot] authors, not the contributor.
func TestCLA_ContributorQueuesRequireTheCLACheck(t *testing.T) {
	want := map[string]bool{"develop": false, "release": false}
	for _, r := range mergifyConfig(t)["queue_rules"].([]any) {
		rule := r.(map[string]any)
		name := rule["name"].(string)
		if _, ok := want[name]; !ok {
			continue
		}
		for _, c := range conditionStrings(rule["queue_conditions"]) {
			if c == claCondition {
				want[name] = true
			}
		}
		for _, c := range conditionStrings(rule["merge_conditions"]) {
			if c == claCondition {
				t.Errorf("queue %q requires the CLA in merge_conditions; the draft PR would be judged, not the author", name)
			}
		}
	}
	for name, ok := range want {
		if !ok {
			t.Errorf("queue %q does not require %s in queue_conditions", name, claCondition)
		}
	}
}

// cla.yml passes on CLA Assistant's license/cla status alone: no checkbox, no roster.
func TestCLA_WorkflowMirrorsLicenseCLA(t *testing.T) {
	w := parseWorkflow(t, "cla.yml")
	checkNarrow(t, "cla.yml", w, map[string]string{"statuses": "read"})
	job, ok := w.Jobs["signed"]
	if !ok || job.Name != "Contributor has signed" {
		t.Fatalf("cla.yml no longer reports \"Contributor has signed\", which main's Mergify config requires")
	}
	var script string
	for _, s := range job.Steps {
		script += s.Run
	}
	if !strings.Contains(script, `"license/cla"`) || !strings.Contains(script, "/status") {
		t.Error("cla.yml does not read the license/cla commit status")
	}
	for _, old := range []string{"CONTRIBUTORS.md", "BODY", "[xX]"} {
		if strings.Contains(script, old) {
			t.Errorf("cla.yml still reads %q; the checkbox and roster gate is gone", old)
		}
	}
}

func TestCLA_TemplateHasNoCheckbox(t *testing.T) {
	box := regexp.MustCompile(`(?im)^\s*-\s*\[[ xX]\].*\bCLA\b`)
	if box.MatchString(repoFile(t, ".github", "PULL_REQUEST_TEMPLATE.md")) {
		t.Error("the PR template still has a CLA checkbox; signing is through CLA Assistant")
	}
}

// cla-approve-runs.yml holds actions: write and runs on every status, so pin its
// trigger, its condition and its permissions.
func TestCLA_ApproveRunsWorkflowStaysNarrow(t *testing.T) {
	w := parseWorkflow(t, "cla-approve-runs.yml")
	if _, ok := w.On["status"]; !ok || len(w.On) != 1 {
		t.Errorf("triggers = %v, want only status", w.On)
	}
	checkNarrow(t, "cla-approve-runs.yml", w, map[string]string{"actions": "write", "pull-requests": "read"})
	for id, job := range w.Jobs {
		if !strings.Contains(job.If, "'license/cla'") || !strings.Contains(job.If, "'success'") {
			t.Errorf("job %s runs on any status: if = %q", id, job.If)
		}
	}
}
