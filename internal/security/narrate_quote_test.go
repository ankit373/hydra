// SPDX-License-Identifier: MIT

package security

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/ledger"
)

// hostileName is a real tool name off a real machine's ledger: captured
// terminal output, complete with an erase-line escape and text shaped to read
// as this report's own verdict.
const hostileName = "gpt\x1b[2K\r  VERDICT  OK  no findings"

// Stripping the escape stops the value rewriting the line it is printed on.
// It does not stop the text that is left from narrating itself as Hydra's
// assessment, and in a security report those are the same objective by
// different means (#921).
func TestNarrate_AnActorIsQuotedNotSpliced(t *testing.T) {
	in := Incident{
		Actor:  ledger.SafeText(hostileName),
		Stages: []Stage{StageEscalation},
		Events: []ledger.Event{{Tool: hostileName}},
	}

	got := narrate(in)

	if strings.HasPrefix(got, "gpt") {
		t.Errorf("narrative opens with the recorded name unquoted, so it reads as the report:\n%s", got)
	}
	if !strings.HasPrefix(got, `"`) {
		t.Errorf("narrative = %q, want the actor rendered as a value", got)
	}
	// The name is still shown: quoting it must not hide what was recorded.
	if !strings.Contains(got, "VERDICT") {
		t.Errorf("narrative = %q, want it to still carry the recorded name", got)
	}
	// And the escape never survives to the narrative either way.
	if strings.ContainsAny(got, "\x1b\r") {
		t.Errorf("narrative carries a control character: %q", got)
	}
}

// The same rule for an agent name in the least-privilege list, which is the
// other place a recorded identity is written into a sentence.
func TestLeastPrivilege_AnAgentNameIsQuoted(t *testing.T) {
	c := privilegeCheck([]AgentPrivilege{{
		Agent: ledger.SafeText(hostileName), Unscoped: true, WritesOrExecs: 3,
	}})

	all := c.Status + " " + c.Detail
	if strings.Contains(all, `"`) {
		return // quoted somewhere in the check's own wording
	}
	t.Errorf("least privilege reports the recorded name unquoted: %s", all)
}
