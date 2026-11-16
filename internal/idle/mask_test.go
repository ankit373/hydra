// SPDX-License-Identifier: MIT

package idle

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestMask_KeepsUserTurnsAndTail(t *testing.T) {
	in := agentLoop()
	got := Mask(in, 4)
	for i, turn := range got.Turns {
		whole := i >= 36 || turn.Role == "system" || turn.Role == "user"
		if whole != (turn.Text == in.Turns[i].Text) {
			t.Errorf("turn %d (%s): want kept whole = %v", i, turn.Role, whole)
		}
		if turn.Seq != in.Turns[i].Seq || turn.Ref != in.Turns[i].Ref || turn.Role != in.Turns[i].Role {
			t.Errorf("turn %d lost its role, seq or ref", i)
		}
	}
	if in.Turns[2].Text == got.Turns[2].Text {
		t.Fatal("Mask changed the input it was given, or masked nothing")
	}
	if !slices.Equal(ExtractConstraints(got.Turns), ExtractConstraints(in.Turns)) {
		t.Fatal("masking dropped a constraint line")
	}
}

func TestMask_PlaceholderCarriesRef(t *testing.T) {
	in := agentLoop()
	got := Mask(in, 4)
	want := fmt.Sprintf("[assistant output masked: %d bytes, ref r2]", len(in.Turns[2].Text))
	if got.Turns[2].Text != want {
		t.Fatalf("placeholder %q, want %q", got.Turns[2].Text, want)
	}
}

func TestMask_ByteStable(t *testing.T) {
	render := func(ts Turns) string {
		var b strings.Builder
		for _, turn := range ts.Turns {
			b.WriteString(turn.Role + ": " + turn.Text + "\n")
		}
		return b.String()
	}
	a, b := render(Mask(agentLoop(), 4)), render(Mask(agentLoop(), 4))
	if a != b {
		t.Fatal("masking the same turns twice gave different bytes")
	}
}
