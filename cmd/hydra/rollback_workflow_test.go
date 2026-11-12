// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// rollback.yml changes what every channel installs by default, so it must only
// run when someone asks, and must not change anything unless they say so.
func TestRollback_RunsOnlyByHandAndDryByDefault(t *testing.T) {
	var wf struct {
		On map[string]struct {
			Inputs map[string]struct {
				Type    string `yaml:"type"`
				Default any    `yaml:"default"`
			} `yaml:"inputs"`
		} `yaml:"on"`
	}
	if err := yaml.Unmarshal([]byte(workflowFile(t, "rollback.yml")), &wf); err != nil {
		t.Fatal(err)
	}
	if len(wf.On) != 1 {
		t.Errorf("rollback.yml has triggers %v, want workflow_dispatch only", wf.On)
	}
	in := wf.On["workflow_dispatch"].Inputs
	if _, ok := in["tag"]; !ok {
		t.Error("rollback.yml takes no tag input")
	}
	if d := in["dry_run"]; d.Type != "boolean" || d.Default != true {
		t.Errorf("dry_run is %s defaulting to %v, want a boolean defaulting to true", d.Type, d.Default)
	}
}

// Every command that changes a channel goes through do_it, which only prints
// in a dry run. A bare call would change the channel even in a dry run.
func TestRollback_EveryChangeHonoursDryRun(t *testing.T) {
	body := workflowFile(t, "rollback.yml")
	for _, cmd := range []string{"gh release edit", "npm deprecate", "npm dist-tag add"} {
		for _, line := range strings.Split(body, "\n") {
			s := strings.TrimSpace(line)
			if strings.HasPrefix(s, cmd) {
				t.Errorf("rollback.yml runs %q outside do_it, so a dry run changes it: %s", cmd, s)
			}
		}
	}
	if !strings.Contains(body, `if [ "$DRY_RUN" = true ]; then
            echo "would run: scripts/update-tap-formula.sh`) {
		t.Error("the Homebrew job does not skip update-tap-formula.sh in a dry run")
	}
}

func TestRollback_RunbookCoversTheManualSteps(t *testing.T) {
	book := repoFile(t, "RUNBOOK-rollback.md")
	for _, want := range []string{"rollback.yml", "dry_run", "Yank", "release:blocked", "NPM_TOKEN"} {
		if !strings.Contains(book, want) {
			t.Errorf("RUNBOOK-rollback.md never mentions %s", want)
		}
	}
}
