// SPDX-License-Identifier: MIT

package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scripts/ci-scope.sh decides which PRs skip the heavy CI jobs. It must say
// full for anything that is not plainly docs, and for no files at all (#1940).
func TestCIScope_OnlyDocsSkipTheHeavyJobs(t *testing.T) {
	script, err := filepath.Abs("../../scripts/ci-scope.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		files []string
		want  string
	}{
		{"docs pages", []string{"docs/index.html", "docs/docs.html", "docs/llms.txt"}, "full=false"},
		{"markdown anywhere", []string{"README.md", "bench/aver/RESULTS.md", "CLAUDE.md"}, "full=false"},
		{"registry yaml", []string{"registry/models.yaml"}, "full=false"},
		{"docs and Go", []string{"docs/docs.html", "cmd/hydra/main.go"}, "full=true"},
		{"Go only", []string{"internal/decide/cascade.go"}, "full=true"},
		{"a workflow", []string{".github/workflows/ci.yml"}, "full=true"},
		{"desktop code", []string{"desktop/api/app.go"}, "full=true"},
		{"registry Go file", []string{"registry/registry.go"}, "full=true"},
		{"a nested yaml", []string{"registry/sub/x.yaml"}, "full=true"},
		{"no files", nil, "full=true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("bash", script)
			cmd.Stdin = strings.NewReader(strings.Join(tc.files, "\n") + "\n")
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("ci-scope.sh: %v", err)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Errorf("%v: got %q, want %q", tc.files, got, tc.want)
			}
		})
	}
}

// The merge queue and every non-main base keep the full matrix, and each heavy
// job waits on the scope job rather than deciding for itself.
func TestCIScope_QueueAndOtherBranchesRunEverything(t *testing.T) {
	body := workflowFile(t, "ci.yml")
	for _, want := range []string{"mergify/merge-queue/*", `"$BASE_REF" != main`, "needs: changes"} {
		if !strings.Contains(body, want) {
			t.Errorf("ci.yml no longer carries %q, so the scope rule can skip a build it should run", want)
		}
	}
}
