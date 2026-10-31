// SPDX-License-Identifier: MIT

package executor

import (
	"errors"
	"testing"
)

// The fallback chain exists for a head that failed. A request no head can be
// asked is not that, and retrying it spends on every remaining head to be told
// the same thing.
func TestToolCallInput_UnparseableArgumentsAreUnaskable(t *testing.T) {
	c := ToolCall{ID: "c1", Type: "function"}
	c.Function.Name = "f"
	c.Function.Arguments = "not json"

	_, err := toolCallInput(c)
	if err == nil {
		t.Fatal("unparseable arguments were accepted")
	}
	if !errors.Is(err, ErrUnaskable) {
		t.Errorf("the refusal is not marked unaskable, so the chain retries it on every head: %v", err)
	}
	// The caller still has to be told which call it was.
	if !contains(err.Error(), "f") || !contains(err.Error(), "c1") {
		t.Errorf("the refusal does not name the call: %v", err)
	}
}

// Empty arguments are a call with no parameters, which is ordinary.
func TestToolCallInput_EmptyArgumentsAreNotAnError(t *testing.T) {
	c := ToolCall{ID: "c1", Type: "function"}
	c.Function.Name = "f"

	got, err := toolCallInput(c)
	if err != nil {
		t.Fatalf("a call with no arguments was refused: %v", err)
	}
	if string(got) != "{}" {
		t.Errorf("got %s, want {}", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
