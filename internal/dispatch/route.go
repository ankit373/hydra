// SPDX-License-Identifier: MIT

package dispatch

import (
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/runlog"
)

// gatePassed marks a candidate no gate dropped before the attempts began.
const gatePassed = "passed"

// route is the ranking a dispatch is about to try, for the run log: the
// candidates in order, then the heads gateHeads dropped, capped together.
func (d *Dispatcher) route(candidates []provider.Head, gates []Gate, scores map[string]rank.Score,
	opts Options, requirement, firstProb float64) *runlog.Route {
	r := &runlog.Route{
		Objective: string(opts.Objective), Requirement: requirement,
		FirstHead: candidates[0].ID, FirstProb: firstProb,
	}
	if len(candidates) > 1 {
		r.RunnerUp = candidates[1].ID
	}
	total := len(candidates) + len(gates)
	n := min(total, runlog.MaxRouteCandidates)
	r.Candidates, r.Omitted = make([]runlog.RouteCandidate, 0, n), total-n
	for _, h := range candidates[:min(len(candidates), n)] {
		// No score means nothing measured it, so it was ranked on its declared one.
		c := runlog.RouteCandidate{Head: h.ID, Model: h.Name, Tier: rank.UITier(h), Effective: h.CapScore, Gate: gatePassed}
		if s, ok := scores[h.ID]; ok {
			c.Effective, c.N, c.InDomain, c.Clears = s.Effective, s.N, s.InDomain, s.Clears
		}
		if d.price != nil {
			c.CostUSD, _ = d.price(h)
		}
		r.Candidates = append(r.Candidates, c)
	}
	for _, g := range gates[:n-len(r.Candidates)] {
		r.Candidates = append(r.Candidates, runlog.RouteCandidate{Head: g.Head, Gate: string(g.Code)})
	}
	return r
}
