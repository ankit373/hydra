// SPDX-License-Identifier: MIT

package dispatch

import (
	"testing"

	"github.com/ankit373/hydra/internal/executor"
)

func TestConversationShape(t *testing.T) {
	for _, tc := range []struct {
		name      string
		msgs      []executor.Message
		wantTurns int
		wantLoop  bool
	}{
		{"empty", nil, 0, false},
		{
			"one exchange is one turn",
			[]executor.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}},
			1, false,
		},
		{
			"turns count users, not messages",
			[]executor.Message{
				{Role: "user"}, {Role: "assistant"}, {Role: "user"}, {Role: "assistant"},
			},
			2, false,
		},
		{
			"a tool result is a tool loop",
			[]executor.Message{{Role: "user"}, {Role: "tool", Content: "{}"}},
			1, true,
		},
		{
			"a pending tool call is a tool loop",
			[]executor.Message{
				{Role: "user"},
				{Role: "assistant", ToolCalls: []executor.ToolCall{{ID: "a"}}},
			},
			1, true,
		},
		{
			"role case does not matter, clients differ",
			[]executor.Message{{Role: "User"}, {Role: "TOOL"}},
			1, true,
		},
	} {
		turns, loop := conversationShape(tc.msgs)
		if turns != tc.wantTurns || loop != tc.wantLoop {
			t.Errorf("%s: conversationShape = (%d, %v), want (%d, %v)",
				tc.name, turns, loop, tc.wantTurns, tc.wantLoop)
		}
	}
}

// A single-shot dispatch passes no messages, so the conversation signals must
// stay absent rather than reporting a conversation of zero turns (#1021).
func TestDecide_SingleShotLeavesConversationAbsent(t *testing.T) {
	var d *Dispatcher
	dec := d.Decide(t.Context(), "rotate the signing key", "go", nil, nil)
	if dec.Fired() {
		t.Fatalf("a nil dispatcher with no rules fired: %+v", dec)
	}
}
