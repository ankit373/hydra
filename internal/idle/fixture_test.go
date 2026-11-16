// SPDX-License-Identifier: MIT

package idle

import (
	"fmt"
	"strings"
)

// agentLoop is a 40-turn tool-heavy transcript: a system prompt, then rounds
// of user ask, assistant call, tool result.
func agentLoop() Turns {
	t := Turns{Turns: []Turn{{Role: "system", Text: "You file expense reports.\nNever submit without a receipt.", Ref: "s"}}}
	for len(t.Turns) < 40 {
		n := len(t.Turns)
		role := []string{"user", "assistant", "tool"}[(n-1)%3]
		text := fmt.Sprintf("%s turn %d: %s", role, n, strings.Repeat("receipt line ", 20))
		if role == "user" {
			text = fmt.Sprintf("Add the taxi from trip %d. Only use the company card.", n)
		}
		t.Turns = append(t.Turns, Turn{Role: role, Text: text, Ref: fmt.Sprintf("r%d", n), Seq: n})
	}
	return t
}
