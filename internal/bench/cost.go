// SPDX-License-Identifier: MIT

package bench

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/pricing"
)

// Price is one dated row, copied into run.json so a later refresh cannot change
// an old run's numbers.
type Price struct {
	Model     string    `json:"model"` // the id sent to the provider
	Ref       string    `json:"ref"`   // pricing.DB key used, or "override"
	InPerM    float64   `json:"in_per_mtok"`
	OutPerM   float64   `json:"out_per_mtok"`
	Source    string    `json:"source"`     // "openrouter" | "config:<url>"
	FetchedAt time.Time `json:"fetched_at"` // the pricing cache's fetch time, or the override's date
}

// PriceSnapshot is every model a run can call, keyed by the id sent.
type PriceSnapshot map[string]Price

// PriceLookup is pricing.DB.ModelPrice, as a seam that needs no pricing cache.
type PriceLookup func(model string) (pricing.ModelPrice, bool)

// Snapshot resolves every model before the run starts. A model it cannot price is
// an error, so a run never starts that it cannot account for.
func Snapshot(lookup PriceLookup, fetchedAt time.Time, models []string, refs map[string]string,
	overrides map[string]Price) (PriceSnapshot, error) {
	out := PriceSnapshot{}
	for _, m := range models {
		if p, ok := overrides[m]; ok {
			p.Model, p.Ref = m, "override"
			out[m] = p
			continue
		}
		ref := refs[m]
		if ref == "" {
			ref = m
		}
		mp, ok := lookup(ref)
		if !ok {
			return nil, fmt.Errorf("bench: cannot price %s (pricing key %q): add a [price_refs] or [prices] entry", m, ref)
		}
		if fetchedAt.IsZero() {
			return nil, fmt.Errorf("bench: the price of %s has no fetch date; run `hyctl pricing refresh`", m)
		}
		out[m] = Price{Model: m, Ref: ref, InPerM: mp.InputPerMillion, OutPerM: mp.OutputPerMillion,
			Source: "openrouter", FetchedAt: fetchedAt}
	}
	return out, nil
}

// Cost prices provider-reported tokens. Unlike pricing.DB it does not round.
func (s PriceSnapshot) Cost(model string, in, out int) (float64, error) {
	p, ok := s[model]
	if !ok {
		return 0, fmt.Errorf("bench: %s is not in the run's price snapshot", model)
	}
	return (float64(in)*p.InPerM + float64(out)*p.OutPerM) / 1e6, nil
}

// worstCase is one call's ceiling: len/3 input tokens, above dispatch's len/4 so the
// guard errs toward refusing, and every allowed output token.
func (s PriceSnapshot) worstCase(model string, req executor.Request) (float64, error) {
	in := (len(req.System) + len(req.Prompt)) / 3
	return s.Cost(model, in+1, req.MaxTokens)
}

// ErrBudget is a refusal before the call that could cross --max-usd.
var ErrBudget = errors.New("bench: the next call could exceed --max-usd")

// Budget enforces --max-usd before each call, starting from what the run already
// spent so a resumed run cannot spend the cap twice.
type Budget struct {
	mu         sync.Mutex
	max, spent float64
}

func NewBudget(maxUSD, alreadySpent float64) *Budget {
	return &Budget{max: maxUSD, spent: alreadySpent}
}

// Reserve holds worstCase against the cap, or refuses with ErrBudget.
func (b *Budget) Reserve(worstCase float64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.spent+worstCase > b.max {
		return fmt.Errorf("%w: spent $%.6f of $%.6f, the next call needs up to $%.6f", ErrBudget, b.spent, b.max, worstCase)
	}
	b.spent += worstCase
	return nil
}

// Settle replaces a reservation with what the call actually cost.
func (b *Budget) Settle(reserved, actual float64) {
	b.mu.Lock()
	b.spent += actual - reserved
	b.mu.Unlock()
}

func (b *Budget) Spent() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent
}
