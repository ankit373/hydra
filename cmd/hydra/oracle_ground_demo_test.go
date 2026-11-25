// SPDX-License-Identifier: MIT

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/util"
)

const demoDir = "../../docs/demo"

// docs/demo/ground.tape records these fixtures. If a CLI change breaks what the
// GIF shows, this fails before the GIF goes stale (#1843).
func TestDemo_GroundFixturesGiveTheRecordedVerdicts(t *testing.T) {
	testutil.NewSandbox(t)
	prompt := filepath.Join(demoDir, "prompt.txt")

	out, _, err := run(t, "oracle", "ground", "--prompt", prompt, "--candidate", filepath.Join(demoDir, "summary-bad.txt"))
	wantExit(t, err, groundUnsupported)
	for _, want := range []string{"UNSUPPORTED", "number 1.9"} {
		if !strings.Contains(out, want) {
			t.Errorf("the bad summary's output is missing %q:\n%s", want, out)
		}
	}

	out, _, err = run(t, "oracle", "ground", "--prompt", prompt, "--candidate", filepath.Join(demoDir, "summary-good.txt"))
	if err != nil {
		t.Fatalf("the good summary should exit 0: %v", err)
	}
	if !strings.Contains(out, "GROUNDED") {
		t.Errorf("the good summary was not reported grounded:\n%s", out)
	}
}

// The fence carries a digest of the report, so an edited report.txt with the old
// prompt.txt would no longer be what the demo says it is.
func TestDemo_PromptFencesTheReport(t *testing.T) {
	report := strings.TrimRight(repoFile(t, "docs", "demo", "report.txt"), "\n")
	want := "Summarise this support report in one sentence.\n\n" + util.WrapUntrusted("REPORT", report) + "\n"
	if got := repoFile(t, "docs", "demo", "prompt.txt"); got != want {
		t.Errorf("docs/demo/prompt.txt is not the fenced report.txt; it should be:\n%s", want)
	}
}
