// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"fmt"
	"strings"

	"github.com/ankit373/hydra/internal/cache"
	"github.com/ankit373/hydra/internal/classify"
	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/signals"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/registry"
)

// ErrBlocked reports a dispatch a rule refused. Distinct from a routing
// failure: nothing was tried and nothing would have helped.
type ErrBlocked struct {
	Rule   string
	Reason string
}

func (e *ErrBlocked) Error() string {
	return fmt.Sprintf("blocked by rule %q: %s", e.Rule, e.Reason)
}

// loadRules reads signals.yaml, validating the values only this package can
// resolve. A rules file that will not load stops the dispatch rather than
// being papered over, the same posture routing.yaml already takes (#720): a
// rule that was meant to keep secrets local must not be skipped silently.
func loadRules(home string) (*signals.Engine, error) {
	return signals.Load(home, func(a signals.Action) error {
		if a.Enum != "" {
			tiers, err := registry.EnumTiers(home)
			if err != nil {
				return err
			}
			if _, ok := tiers[strings.ToUpper(a.Enum)]; !ok {
				return fmt.Errorf("enum %q is not in routing.yaml", a.Enum)
			}
		}
		if a.Tier != "" {
			if _, err := ResolveTier(a.Tier); err != nil {
				return fmt.Errorf("tier %q: %w", a.Tier, err)
			}
		}
		return nil
	})
}

// Decide evaluates the routing rules once for this dispatch.
//
// Once is the contract: the result is carried on Options so every fallback
// candidate sees the same decision. Recomputing per candidate would let a rule
// answer differently for two attempts at one task.
//
// blastRadius is the caller's, because the caller is the one that already
// loaded the code graph for --file; nil leaves the signal absent rather than
// zero, and zero dependents is a real reading.
func (d *Dispatcher) Decide(ctx context.Context, prompt, domain string, blastRadius *int) signals.Decision {
	in := signals.Input{Prompt: prompt, BlastRadius: blastRadius}
	if d == nil {
		return (*signals.Engine)(nil).Evaluate(in)
	}
	if d.cal != nil {
		cal := d.calibratedIn(domain)
		in.Calibrated = &cal
	}
	// Only when a rule asks. The lookup costs an embedding call and a pass over
	// the corpus, and #750 removed a per-dispatch HTTP call for good reason; an
	// empty signals.yaml must still route byte-identically.
	if d.rules.ReadsAny(signals.CorpusSignals...) {
		in.CorpusPassRate, in.CorpusSupport = d.corpusEvidence(ctx, prompt)
	}
	return d.rules.Evaluate(in)
}

// corpusEvidence asks internal/classify what the verified examples say about
// work like this. Both results nil every way it cannot answer, since a zero
// pass rate means work like this always failed and must not be invented.
func (d *Dispatcher) corpusEvidence(ctx context.Context, prompt string) (*float64, *int) {
	c := d.corpus()
	if c == nil {
		return nil, nil
	}
	emb := d.Embedder()
	if emb == nil || !emb.Available() {
		return nil, nil
	}
	// cache.Normalize, the same derivation the corpus was written with, or the
	// query vector is not comparable to the stored ones.
	vec, err := emb.Embed(ctx, cache.Normalize(prompt))
	if err != nil || len(vec) == 0 {
		return nil, nil
	}
	n, err := c.Near(vec, classify.DefaultK, -1)
	if err != nil {
		return nil, nil
	}
	rate, support := n.PassRate, n.Size
	return &rate, &support
}

// corpus loads the eval set once per Dispatcher. Once per dispatch would read
// the whole file on every task, which is the cost this signal is opt-in to
// avoid in the first place.
func (d *Dispatcher) corpus() *classify.Corpus {
	d.corpusLoad.Do(func() {
		all, err := evalset.Load(evalset.DefaultPath())
		if err != nil {
			return
		}
		c, err := classify.Load(all)
		if err != nil {
			return
		}
		d.corpusData = c
	})
	return d.corpusData
}

// calibratedIn reports whether internal/trust holds any real observation for
// this domain. The prior alone is not evidence, so N must be above zero.
func (d *Dispatcher) calibratedIn(domain string) bool {
	if domain == "" {
		domain = trust.DefaultDomain
	}
	for _, s := range d.cal.Report() {
		if s.Domain == domain && s.N > 0 {
			return true
		}
	}
	return false
}

// applyDecision folds a rule's action into the options this dispatch runs with.
//
// route and block are dispatch's to apply. require_confidence is not: the
// stopping rule lives where --confidence is read, so that action is applied by
// the caller and is a no-op here rather than being silently dropped.
func applyDecision(dec signals.Decision, opts *Options) error {
	switch dec.Action.Type {
	case signals.ActionBlock:
		return &ErrBlocked{Rule: dec.Rule, Reason: dec.Action.Reason}
	case signals.ActionRoute:
		if dec.Action.LocalOnly {
			opts.LocalOnly = true
		}
		// An explicit flag wins over a rule: the rule is a default for the
		// dispatches nobody spoke about, not an override of the ones they did.
		if dec.Action.Tier != "" && opts.TierHint == "" {
			opts.TierHint = dec.Action.Tier
		}
		if dec.Action.Enum != "" && opts.Enum == "" {
			opts.Enum = dec.Action.Enum
		}
	}
	return nil
}
