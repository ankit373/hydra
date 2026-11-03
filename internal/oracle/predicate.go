// SPDX-License-Identifier: MIT

package oracle

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/ankit373/hydra/internal/trust"
)

const (
	// The labels a predicate asks a Decider to weigh, in this order.
	optionTrue  = "true"
	optionFalse = "false"

	// distributionTolerance is how far from 1 a distribution may sum before it
	// is refused. Rounding in a wire format gets this far; a broken head does not.
	distributionTolerance = 1e-6
)

// errNoAnswerSlot reports a predicate template with no {answer}, which would
// ask the same question of every candidate and so say nothing about any of them.
var errNoAnswerSlot = errors.New("predicate template has no {answer} slot")

// Decider answers a question by putting a probability on each of a fixed set of
// options. The returned slice is index-aligned with options and sums to 1.
type Decider interface {
	Decide(ctx context.Context, question string, options []string) ([]float64, error)
}

// PredicateOracle asks a Decider a yes/no question about the candidate. Its
// verdict is weighted by calibration like any other source, so a noisy
// predicate earns little LLR without anyone switching it off.
type PredicateOracle struct {
	// Template is the question, with {answer} where the candidate goes, e.g.
	// "Does this reply cite only files that appear in the diff? {answer}".
	Template string
	// Decider is the decision source that answers the question.
	Decider Decider
	// Source is the calibration key, e.g. "verifier:predicate:cites-diff".
	Source string
}

var _ Oracle = (*PredicateOracle)(nil)

// Verify fills the template, asks the Decider, and passes when P(true) is above
// one half. A Decider failure or a malformed distribution is an error, not a verdict.
func (o *PredicateOracle) Verify(ctx context.Context, candidate string, _ trust.Task) (Verdict, error) {
	if !strings.Contains(o.Template, "{answer}") {
		return Verdict{}, fmt.Errorf("oracle %s: %w", o.Source, errNoAnswerSlot)
	}
	if o.Decider == nil {
		return Verdict{}, fmt.Errorf("oracle %s: no decision source configured", o.Source)
	}
	question := strings.ReplaceAll(o.Template, "{answer}", answerFor(candidate))
	options := []string{optionTrue, optionFalse}
	probs, err := o.Decider.Decide(ctx, question, options)
	if err != nil {
		return Verdict{}, fmt.Errorf("oracle %s: %w", o.Source, err)
	}
	if err = checkDistribution(probs, len(options)); err != nil {
		return Verdict{}, fmt.Errorf("oracle %s: %w", o.Source, err)
	}
	pTrue := probs[0]
	// A coin flip is not a yes; calibration then learns what this source's yes is worth.
	return Verdict{Passed: pTrue > 0.5, Detail: fmt.Sprintf("P(true) = %.3f", pTrue)}, nil
}

// checkDistribution refuses rather than normalises: a distribution with the
// wrong length or mass is a broken source, and a verdict drawn from it is false evidence.
func checkDistribution(probs []float64, want int) error {
	if len(probs) != want {
		return fmt.Errorf("decision source returned %d probabilities for %d options", len(probs), want)
	}
	sum := 0.0
	for i, p := range probs {
		// The negated test also catches NaN, which fails every comparison.
		if !(p >= 0 && p <= 1) {
			return fmt.Errorf("probability %d is %v, outside [0, 1]", i, p)
		}
		sum += p
	}
	if math.Abs(sum-1) > distributionTolerance {
		return fmt.Errorf("probabilities sum to %v, not 1", sum)
	}
	return nil
}
