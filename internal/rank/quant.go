// SPDX-License-Identifier: MIT

package rank

import (
	"strconv"
	"strings"

	"github.com/ankit373/hydra/internal/provider"
)

// unknownQuantBits is the sort position of a head whose quantization nothing
// reported. It sorts after every head that reported one.
//
// Not a claim that such a head is worse. It is a fixed position, and the thing
// it replaces is an arbitrary one: a comparator that skipped the key whenever
// either side was unknown stopped being transitive (Q8 before Q4 by bits, Q4
// before an unknown by id, that unknown before Q8 by id, a cycle), and an
// intransitive comparator is how #765 happened in the first place.
const unknownQuantBits = -1

// quantBits reads bits-per-weight off a quantization label: Q4_K_M → 4,
// Q8_0 → 8, F16/BF16 → 16, IQ2_XXS → 2, MXFP4 → 4. It reports ok=false for a
// label it cannot read, which is the only honest answer for one it has not seen.
//
// A label parse, deliberately not a quality score. "More bits is not worse for
// the same base model" is well founded; any particular magnitude would not be.
func quantBits(label string) (int, bool) {
	s := strings.ToUpper(strings.TrimSpace(label))
	if s == "" {
		return 0, false
	}
	// FP first, and by search rather than by prefix: the microscale formats
	// carry a vendor prefix (MXFP4, NVFP4), so anchoring at the start misses
	// them and falling through to "F" would read MXFP4 as unparseable.
	if i := strings.Index(s, "FP"); i >= 0 {
		return digitsAt(s, i+len("FP"))
	}
	for _, p := range []string{"BF", "F"} {
		if strings.HasPrefix(s, p) {
			return digitsAt(s, len(p))
		}
	}
	// Integer quants are Q<n>_… or IQ<n>_…, where n is the nominal width. The
	// K/S/M/XXS suffixes vary the per-block layout, not the nominal width, so
	// they are not read: Q4_K_M and Q4_0 are both 4-bit, and separating them
	// on this key would claim a precision this parse does not have.
	s = strings.TrimPrefix(s, "I")
	if strings.HasPrefix(s, "Q") {
		return digitsAt(s, len("Q"))
	}
	return 0, false
}

// digitsAt parses the run of digits starting at i, and reports whether there
// was one and it was positive.
func digitsAt(s string, i int) (int, bool) {
	end := i
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == i {
		return 0, false
	}
	n, err := strconv.Atoi(s[i:end])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// quantRank is a head's position on the quantization key: its bits per weight,
// or unknownQuantBits when nothing reported one. Higher sorts first.
func quantRank(h provider.Head) int {
	if bits, ok := quantBits(h.Meta["model_quant"]); ok {
		return bits
	}
	return unknownQuantBits
}
