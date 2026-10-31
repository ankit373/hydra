// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/trust"
)

// #785 built UnreadableSourceKey and left in place the message that produces
// exactly what it detects: the refusal told the reader to record under
// `--source model:<id>`, and "model:" is the first prefix on that list. Doing
// as instructed filled a cell nothing reads and earned the same refusal again.
//
// Anchored on the detector rather than on the old string, so the two cannot
// drift apart again whichever one someone edits.
func TestNoEvidenceError_SuggestsASourceTheRouterCanLookUp(t *testing.T) {
	for _, c := range []struct {
		name  string
		heads []string
	}{
		{"heads known", []string{"ollama/qwen3:4b", "agy/claude-sonnet"}},
		{"no heads known", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			msg := noEvidenceError("go", c.heads).Error()
			src := suggestedSource(t, msg)
			if reason, bad := trust.UnreadableSourceKey(src); bad {
				t.Errorf("the refusal tells the reader to record under %q: %s", src, reason)
			}
		})
	}
}

// A placeholder passes the detector trivially, so the populated case has to
// assert the stronger thing: the key named is a head from the run that was
// just refused. Without this the guard above holds on a fixture that cannot
// tell a real id from a shape description.
func TestNoEvidenceError_NamesAHeadFromTheRefusedRun(t *testing.T) {
	heads := []string{"ollama/qwen3:4b", "agy/claude-sonnet"}
	msg := noEvidenceError("go", heads).Error()

	src := suggestedSource(t, msg)
	for _, h := range heads {
		if src == h {
			return
		}
	}
	t.Errorf("suggested --source %q is not one of the heads this run would sample %v;\n%s",
		src, heads, msg)
}

// suggestedSource returns the --source value from the copy-pasteable
// `hyctl trust record` line, which is the whole point of the message.
func suggestedSource(t *testing.T, msg string) string {
	t.Helper()
	for _, line := range strings.Split(msg, "\n") {
		if !strings.Contains(line, "hyctl trust record") {
			continue
		}
		i := strings.Index(line, "--source ")
		if i < 0 {
			t.Fatalf("the trust record line names no --source: %q", line)
		}
		return strings.Fields(line[i+len("--source "):])[0]
	}
	t.Fatalf("no `hyctl trust record` line to follow:\n%s", msg)
	return ""
}
