// SPDX-License-Identifier: MIT

package embed

import (
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/provider"
)

// Enabled reports whether vector capture was opted into.
func Enabled(cfg *config.Config) bool { return cfg != nil && cfg.CaptureEmbeddings }

// Budget resolves the store's byte budget from config.
func Budget(cfg *config.Config) int64 {
	if cfg != nil && cfg.EmbedBudgetMB > 0 {
		return int64(cfg.EmbedBudgetMB) << 20
	}
	return DefaultBudgetBytes
}

// Open resolves an embedder and its store from config and discovered heads.
//
// Three outcomes, and a caller must tell them apart: capture off, capture on
// with no model available, and ready. The first two both mean "record nothing"
// and mean it for different reasons, so the surfaces can say which.
func Open(cfg *config.Config, heads []provider.Head) (Embedder, *Store, error) {
	if !Enabled(cfg) {
		return Unavailable{}, nil, nil
	}
	model := ""
	if cfg != nil {
		model = cfg.EmbedModel
	}
	e := Resolve(heads, model)
	if !e.Available() {
		return e, nil, nil
	}
	st, err := OpenStore(Dir(), e.Model())
	if err != nil {
		return e, nil, err
	}
	st.SetBudget(Budget(cfg))
	return e, st, nil
}
