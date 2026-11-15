// SPDX-License-Identifier: MIT

package checkgen

import (
	"github.com/ankit373/hydra/internal/egress"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
)

// PickGenerator picks the cheapest measured head of another family, else the cheapest of
// another family, else the cheapest with sameFamily set. A prompt held local gets a local head or none.
func PickGenerator(heads []provider.Head, scores map[string]rank.Score, answering provider.Head,
	parts []egress.Part, localOnly bool) (h provider.Head, sameFamily, ok bool) {
	// Unlike dispatch, egress.strict = false never lets secret content out here:
	// a check is optional, so there is no answer to save by leaking it.
	local := localOnly || len(egress.Origins(parts)) > 0
	fam := provider.Family(answering)
	measured, other, same, self := -1, -1, -1, -1
	for i, c := range heads {
		if local && !c.LocalOnly {
			continue
		}
		switch {
		case c.ID == answering.ID:
			self = i
		case provider.Family(c) != fam:
			if scores[c.ID].Measured() && cheaper(c, heads, measured, scores) {
				measured = i
			}
			if cheaper(c, heads, other, scores) {
				other = i
			}
		default:
			if cheaper(c, heads, same, scores) {
				same = i
			}
		}
	}
	for _, i := range []int{measured, other} {
		if i >= 0 {
			return heads[i], false, true
		}
	}
	// The answering head comes last: its own blind spots are the ones to avoid.
	for _, i := range []int{same, self} {
		if i >= 0 {
			return heads[i], true, true
		}
	}
	return provider.Head{}, false, false
}

// cheaper says whether c beats heads[best] on nominal cost, then ID; best < 0 is no pick yet.
func cheaper(c provider.Head, heads []provider.Head, best int, scores map[string]rank.Score) bool {
	if best < 0 {
		return true
	}
	b := heads[best]
	if cc, bc := scores[c.ID].CostUSD, scores[b.ID].CostUSD; cc != bc {
		return cc < bc
	}
	return c.ID < b.ID
}
