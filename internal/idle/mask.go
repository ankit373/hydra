// SPDX-License-Identifier: MIT

package idle

import (
	"fmt"
	"slices"
)

// Mask is the baseline a pack must beat (arXiv 2508.21433): no model, old
// assistant and tool output cut to one line naming its size and ref. System
// and user turns, where every rule lives, and the last keepLast turns stay whole.
func Mask(t Turns, keepLast int) Turns {
	out := Turns{Turns: slices.Clone(t.Turns), Files: t.Files}
	tail := len(out.Turns) - keepLast
	for i, turn := range out.Turns {
		if i >= tail || turn.Role == "system" || turn.Role == "developer" || turn.Role == "user" {
			continue
		}
		out.Turns[i].Text = fmt.Sprintf("[%s output masked: %d bytes, ref %s]", turn.Role, len(turn.Text), turn.Ref)
	}
	return out
}
