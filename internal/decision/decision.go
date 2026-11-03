// SPDX-License-Identifier: MIT

// Package decision is what a decision source is asked and what it answers: a
// question over a fixed set of outcomes, and a probability for each of them.
package decision

import (
	"errors"
	"fmt"
	"math"
)

// Kind is the shape of answer a question asks for.
type Kind string

const (
	// Choice picks one option from a fixed list.
	Choice Kind = "choice"
	// Predicate asks how likely a statement is to be true.
	Predicate Kind = "predicate"
	// Score rates against ordered levels, lowest first.
	Score Kind = "score"
)

const (
	// tolerance is how far from 1 a distribution may sum before it is refused.
	// Rounding in a wire format gets this far; a broken source does not.
	tolerance = 1e-6

	// The bounds every supported dialect accepts, so a question valid here is
	// valid on whichever source the router picks.
	minOptions = 2
	maxChoices = 255
	maxLevels  = 10
)

// predicateOutcomes are what a predicate's probabilities are reported over, in order.
var predicateOutcomes = []Option{
	{Name: "true"},
	{Name: "false"},
}

// Option is one allowed answer: a choice, or a score level.
type Option struct {
	Name        string
	Description string
}

// Question is one thing asked about the input.
type Question struct {
	// Name is the caller's label for the question, and may be empty.
	Name         string
	Kind         Kind
	Instructions string
	// Options are the choices, or the score levels lowest first. A predicate has none.
	Options []Option
}

// Request is one input and every question asked about it.
type Request struct {
	Input string
	// Image is a data: URL or an https: URL, or empty for text alone.
	Image     string
	Questions []Question
}

// Answer is one question's distribution, index-aligned with its Outcomes.
type Answer struct {
	Probabilities []float64
	// Confidence is the source's own figure for a choice or score, zero for a predicate.
	Confidence float64
}

// Result is a source's answers, index-aligned with the request's questions.
type Result struct {
	Model   string
	Answers []Answer
	// InputTokens is what the source billed; decision sources charge nothing for output.
	InputTokens int
}

// Outcomes are what a question's probabilities are reported over. Callers must not modify it.
func (q Question) Outcomes() []Option {
	if q.Kind == Predicate {
		return predicateOutcomes
	}
	return q.Options
}

// Top is the index of the most probable outcome; a tie goes to the earlier one.
func (a Answer) Top() int {
	top := 0
	for i, p := range a.Probabilities {
		if p > a.Probabilities[top] {
			top = i
		}
	}
	return top
}

// Level is a score answer's probability-weighted mean level, which can fall between levels.
func (a Answer) Level() float64 {
	level := 0.0
	for i, p := range a.Probabilities {
		level += float64(i) * p
	}
	return level
}

// Validate refuses a request some supported dialect would reject, before any source is paid.
func (r Request) Validate() error {
	if len(r.Questions) == 0 {
		return errors.New("no questions asked")
	}
	for i, q := range r.Questions {
		if err := q.validate(); err != nil {
			return fmt.Errorf("question %d: %w", i, err)
		}
	}
	return nil
}

func (q Question) validate() error {
	limit := maxChoices
	switch q.Kind {
	case Predicate:
		if len(q.Options) > 0 {
			return errors.New("a predicate takes no options")
		}
		return nil
	case Score:
		limit = maxLevels
	case Choice:
	default:
		return fmt.Errorf("unknown kind %q", q.Kind)
	}
	if n := len(q.Options); n < minOptions || n > limit {
		return fmt.Errorf("a %s takes %d to %d options, got %d", q.Kind, minOptions, limit, n)
	}
	seen := make(map[string]bool, len(q.Options))
	for _, o := range q.Options {
		if o.Name == "" {
			return errors.New("an option has no name")
		}
		if seen[o.Name] {
			return fmt.Errorf("option %q is listed twice", o.Name)
		}
		seen[o.Name] = true
	}
	return nil
}

// CheckResult refuses a result that does not give every question a distribution over its outcomes.
func (r Request) CheckResult(res Result) error {
	if len(res.Answers) != len(r.Questions) {
		return fmt.Errorf("decision source answered %d of %d questions", len(res.Answers), len(r.Questions))
	}
	for i, q := range r.Questions {
		if err := Check(res.Answers[i].Probabilities, len(q.Outcomes())); err != nil {
			return fmt.Errorf("question %d: %w", i, err)
		}
	}
	return nil
}

// Check refuses rather than normalises: a distribution with the wrong length or
// mass is a broken source, and anything drawn from it is false evidence.
func Check(probs []float64, want int) error {
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
	if math.Abs(sum-1) > tolerance {
		return fmt.Errorf("probabilities sum to %v, not 1", sum)
	}
	return nil
}
