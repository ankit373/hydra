// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/ankit373/hydra/internal/executor"
)

// The fallback chain exists for a head that failed. Measured before this
// check, one malformed tool call walked all seven heads on the machine, every
// one of them a real request, and each failure parked a healthy head on the
// breaker. A conversation no head can be asked has to be refused before any of
// them runs.
func TestDispatch_AnUnaskableRequestNeverReachesAHead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HYDRA_HOME", home)

	bad := executor.ToolCall{ID: "c1", Type: "function"}
	bad.Function.Name = "f"
	bad.Function.Arguments = "not json"

	d := newTestDispatcher()
	_, err := d.Dispatch(context.Background(), "hi", Options{
		Messages: []executor.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", ToolCalls: []executor.ToolCall{bad}},
		},
	})
	if err == nil {
		t.Fatal("a malformed tool call was dispatched")
	}
	if !errors.Is(err, executor.ErrUnaskable) {
		t.Fatalf("the refusal is not marked unaskable, so a caller cannot tell it from a head failure: %v", err)
	}
	// A bare test Dispatcher has no policy, no heads and no ledger. Reaching
	// any of them would panic, so getting a clean refusal is itself the proof
	// that nothing downstream ran.
}
