// SPDX-License-Identifier: MIT

package budget

import (
	"strconv"

	"github.com/ankit373/hydra/internal/provider"
)

// WindowsForHeads returns the context window to budget each discovered head
// against, keyed by head ID, which is what Registry.Record is called with.
// Keying on registry/models.yaml ids alone did not work: those only partly
// overlap head ids, so every local head missed, fell through to fallbackCloud,
// and a 4096-token model was budgeted at 200000 with the 70/75/80% bands past
// a ceiling it could never reach (#764).
//
// Where the truth lives differs by head, so the rule does too:
//
//   - A local head's window is decided by its server at runtime, and the
//     registry cannot know it. Declarations are ignored rather than merely
//     absent (#777): models.yaml already carries 32768 for a model measured at
//     4096, and honouring that would undo #764 for it.
//   - A cloud head reports nothing, so its declaration is the best evidence
//     there is, resolved by declarations.windowFor.
//
// A reported ceiling caps either, because a ceiling is a hard bound.
func WindowsForHeads(home string, heads []provider.Head) map[string]int {
	decls := loadDeclarations(home)

	// Only discovered heads go in. Record is called with a head id and nothing
	// else, so seeding the registry-local ids too added keys nothing could look
	// up, and one of them (qwen-grunt, 32768) is a local declaration that must
	// never apply.
	out := make(map[string]int, len(heads))
	for _, h := range heads {
		w := localDefaultCtx
		if !h.LocalOnly {
			w = decls.windowFor(h)
		}
		if ceil, ok := ContextCeiling(h); ok && ceil < w {
			w = ceil
		}
		out[h.ID] = w
	}
	return out
}

// declarations indexes models.yaml the three ways a head can name an entry.
// Kept separate so id beats model_flag beats name deterministically, rather
// than collapsing into one map where load order decides.
type declarations struct {
	byID   map[string]int
	byFlag map[string]int
	byName map[string]int
}

// windowFor resolves a cloud head's declared window, falling back to
// fallbackCloud when no entry names it.
//
// id, then provider/model_flag, then display name: the same three keys
// internal/cost/canonical.go already bridges these two id namespaces with.
// The head id is `claude` while the entry declaring 200000 is `claude-core`,
// so keying on id alone missed and landed on fallbackCloud, which is also
// 200000. Right answer, no mechanism: changing the declaration changed
// nothing (#777).
func (d declarations) windowFor(h provider.Head) int {
	if w, ok := d.byID[h.ID]; ok {
		return w
	}
	if w, ok := d.byFlag[h.ID]; ok {
		return w
	}
	if w, ok := d.byName[h.Name]; ok {
		return w
	}
	return fallbackCloud
}

// ContextCeiling is the architectural maximum a provider reported for a head,
// and whether it reported one at all. Not the effective window: a model whose
// ceiling is 40960 still runs at the server's 4096 default, so this caps a
// window rather than replacing it.
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
