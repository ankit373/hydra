// SPDX-License-Identifier: MIT

package budget

import (
	"strconv"

	"github.com/ankit373/hydra/internal/provider"
)

// WindowsForHeads returns the context window to budget each discovered head
// against, keyed by head ID, which is what Registry.Record is called with.
//
// LoadWindows alone was not enough: it keys on registry/models.yaml ids, and
// those only partly overlap head ids, so every local head missed and fell
// through to fallbackCloud. A 4096-token model was budgeted at 200000, and the
// 70/75/80% bands landed past a ceiling it could never reach (#764).
//
// The declared window and the reported ceiling are combined with min, because
// a ceiling is a hard bound: a models.yaml entry claiming 32768 for a model
// that can only do 2048 is wrong whoever wrote it.
func WindowsForHeads(home string, heads []provider.Head) map[string]int {
	declared := LoadWindows(home)

	out := make(map[string]int, len(declared)+len(heads))
	for id, w := range declared {
		out[id] = w
	}
	for _, h := range heads {
		w, ok := declared[h.ID]
		if !ok {
			w = defaultWindowFor(h)
		}
		if ceil, ok := ContextCeiling(h); ok && ceil < w {
			w = ceil
		}
		out[h.ID] = w
	}
	return out
}

// defaultWindowFor is the window to assume for a head nothing declares. A
// local head gets the local server's default rather than a cloud-sized one:
// under-assuming makes the governor escalate early, over-assuming is what
// silenced it, and for a budget governor the safe direction is the smaller
// number.
func defaultWindowFor(h provider.Head) int {
	if h.LocalOnly {
		return ollamaDefaultCtx
	}
	return fallbackCloud
}

// ContextCeiling is the architectural maximum a provider reported for a head,
// and whether it reported one at all. Not the effective window: a model whose
// ceiling is 40960 still runs at the server's 4096 default, so this caps a
// declared window rather than replacing it.
func ContextCeiling(h provider.Head) (int, bool) {
	raw, ok := h.Meta["model_ctx_max"]
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
