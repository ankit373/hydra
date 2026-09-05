// SPDX-License-Identifier: MIT

package swarm

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
)

const defaultMaxHeads = 5

// HeadSelector picks which Heads to fire for a given swarm run.
// Implementations are composable: a filter wraps a base selector.
type HeadSelector interface {
	Select(all []provider.Head, opts Options) ([]provider.Head, error)
}

// resolveSelector picks the right HeadSelector from Options.
// Priority: explicit HeadIDs > TierHint (numeric or named) > top-N by CapScore.
func resolveSelector(opts Options, cfg *config.Config) HeadSelector {
	if len(opts.HeadIDs) > 0 {
		return &IDSelector{}
	}
	if opts.TierHint != "" {
		if _, err := strconv.Atoi(opts.TierHint); err == nil {
			return &NumericTierSelector{}
		}
		return &TierSelector{cfg: cfg}
	}
	return &CapScoreSelector{}
}

// ── NumericTierSelector ──────────────────────────────────────────────────────

// NumericTierSelector filters to heads at or below the requested capability
// tier (rank.UITier), mirroring dispatch.selectHeads' numeric branch so a
// numeric --tier restricts --swarm/--confidence the same way it restricts a
// plain dispatch. Before this existed a numeric TierHint matched no config
// tier name and always fell through to CapScoreSelector's top-N fan-out,
// silently ignoring the requested tier (#501).
type NumericTierSelector struct{}

func (s *NumericTierSelector) Select(all []provider.Head, opts Options) ([]provider.Head, error) {
	want, err := strconv.Atoi(opts.TierHint)
	if err != nil {
		return nil, fmt.Errorf("swarm: tier hint %q is not numeric", opts.TierHint)
	}

	var candidates []provider.Head
	for _, h := range all {
		if executable(h) && rank.UITier(h) >= want {
			candidates = append(candidates, h)
		}
	}
	if len(candidates) > 0 {
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].CapScore > candidates[j].CapScore })
		return applyFiltersAndCap(candidates, opts), nil
	}

	// Nothing at or below the requested tier, degrade to the cheapest
	// executable heads available, never the most expensive (matches
	// dispatch.selectHeads' own degrade path).
	for _, h := range all {
		if executable(h) {
			candidates = append(candidates, h)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("swarm: no executable heads found")
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].CapScore < candidates[j].CapScore })
	return applyFiltersAndCap(candidates, opts), nil
}

// ── TierSelector ─────────────────────────────────────────────────────────────

// TierSelector filters to heads assigned to a named tier in config.
// Falls back to CapScoreSelector when the tier has no live heads.
type TierSelector struct{ cfg *config.Config }

func (s *TierSelector) Select(all []provider.Head, opts Options) ([]provider.Head, error) {
	tierIDs := map[string]bool{}
	for _, t := range s.cfg.Tiers {
		if t.Name == opts.TierHint {
			for _, id := range t.Heads {
				tierIDs[id] = true
			}
		}
	}

	var candidates []provider.Head
	for _, h := range all {
		if executable(h) && tierIDs[h.ID] {
			candidates = append(candidates, h)
		}
	}

	if len(candidates) == 0 {
		// Tier has no live heads, fall back to capability score ranking.
		return (&CapScoreSelector{}).Select(all, opts)
	}

	return applyFiltersAndCap(candidates, opts), nil
}

// ── IDSelector ────────────────────────────────────────────────────────────────

// IDSelector resolves an explicit list of head IDs.
// Returns an error if any requested ID is not found in the probed set.
type IDSelector struct{}

func (s *IDSelector) Select(all []provider.Head, opts Options) ([]provider.Head, error) {
	index := make(map[string]provider.Head, len(all))
	for _, h := range all {
		index[h.ID] = h
	}

	var selected []provider.Head
	var missing []string
	for _, id := range opts.HeadIDs {
		h, ok := index[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		if !executable(h) {
			continue
		}
		selected = append(selected, h)
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("swarm: head IDs not found or not executable: %v", missing)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("swarm: no executable heads in the requested ID list")
	}

	return applyFiltersAndCap(selected, opts), nil
}

// ── CapScoreSelector ─────────────────────────────────────────────────────────

// CapScoreSelector picks the top-N executable heads ranked by CapScore descending.
// Used as default when neither HeadIDs nor TierHint is set, and as fallback
// when a TierSelector finds no live heads.
type CapScoreSelector struct{}

func (s *CapScoreSelector) Select(all []provider.Head, opts Options) ([]provider.Head, error) {
	var candidates []provider.Head
	for _, h := range all {
		if executable(h) {
			candidates = append(candidates, h)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("swarm: no executable heads found")
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].CapScore > candidates[j].CapScore
	})

	return applyFiltersAndCap(candidates, opts), nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func executable(h provider.Head) bool {
	return executor.Supports(h)
}

// applyFiltersAndCap applies MinCapScore filter and MaxHeads cap in one pass.
func applyFiltersAndCap(heads []provider.Head, opts Options) []provider.Head {
	maxHeads := opts.MaxHeads
	if maxHeads <= 0 {
		// An explicit HeadIDs list is the caller's stated intent, so the
		// *default* cap must not silently trim it, pinning 7 heads and
		// receiving 5 is exactly the source-diversity problem --swarm-heads
		// exists to solve. An explicitly set MaxHeads still applies.
		if len(opts.HeadIDs) > 0 {
			maxHeads = len(heads)
		} else {
			maxHeads = defaultMaxHeads
		}
	}

	var out []provider.Head
	for _, h := range heads {
		if opts.LocalOnly && !h.LocalOnly {
			continue
		}
		if opts.MinCapScore > 0 && h.CapScore < opts.MinCapScore {
			continue
		}
		out = append(out, h)
		if len(out) >= maxHeads {
			break
		}
	}
	return out
}
