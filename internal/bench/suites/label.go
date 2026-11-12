// SPDX-License-Identifier: MIT

// Package suites holds the benchmark datasets: how each is parsed, prompted and graded.
package suites

//go:generate go run gen_splits.go

import (
	"context"
	"fmt"
	"strings"

	"github.com/ankit373/hydra/internal/bench"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/util"
)

// maxAnswerTokens bounds every arm's answer; the longest label is under 15 tokens.
const maxAnswerTokens = 32

// Registry is every suite at checkpoint A, by name. Each call builds a fresh one,
// since a suite learns its labels when it loads.
func Registry() map[string]func() bench.Suite {
	return map[string]func() bench.Suite{
		"banking77": func() bench.Suite { return &banking77{labelSuite: labelSuite{name: "banking77"}} },
		"clinc150":  func() bench.Suite { return &clinc150{labelSuite: labelSuite{name: "clinc150"}} },
	}
}

// labelSuite is what both intent suites share: one rubric, one grader.
type labelSuite struct {
	name   string
	labels []string
	match  *oracle.MatchOracle
}

func (s *labelSuite) Name() string     { return s.name }
func (s *labelSuite) Class() string    { return "classification." + s.name }
func (s *labelSuite) Labels() []string { return s.labels }

// setLabels builds the full-label-set verifier, the one serve's match:<suite> reaches.
func (s *labelSuite) setLabels(labels []string) error {
	m, err := oracle.NewMatch(s.name, labels, "")
	if err != nil {
		return err
	}
	s.labels, s.match = labels, m
	return nil
}

// Prompt is the same request for every arm: the rubric as the system prompt and the
// utterance fenced as untrusted data, greedy and short.
func (s *labelSuite) Prompt(it bench.Item) (executor.Request, error) {
	text, err := it.Text()
	if err != nil {
		return executor.Request{}, err
	}
	zero := 0.0
	return executor.Request{
		System: "Classify the user's message into exactly one intent label.\nLabels:\n" +
			strings.Join(s.labels, "\n") +
			"\nAnswer with exactly one label from the list, and nothing else.",
		Prompt:      util.WrapUntrusted("utterance", text),
		Temperature: &zero,
		MaxTokens:   maxAnswerTokens,
	}, nil
}

// Grade passes only when the answer normalises to the gold label; an answer outside
// the label set is a format fail. Both read oracle.NormaliseLabel and nothing else.
func (s *labelSuite) Grade(ctx context.Context, it bench.Item, answer string) (bench.Grade, error) {
	if s.match == nil {
		return bench.Grade{}, fmt.Errorf("suites: %s graded before it loaded", s.name)
	}
	gold, err := it.Label()
	if err != nil {
		return bench.Grade{}, err
	}
	v, err := s.match.Verify(ctx, answer, trust.Task{})
	if err != nil {
		return bench.Grade{}, err
	}
	if !v.Passed {
		return bench.Grade{Format: true, Detail: v.Detail}, nil
	}
	if oracle.NormaliseLabel(answer) != oracle.NormaliseLabel(gold) {
		return bench.Grade{Detail: fmt.Sprintf("answered %q, gold %q", oracle.NormaliseLabel(answer), gold)}, nil
	}
	return bench.Grade{Passed: true}, nil
}

// width is the zero-padding an id needs so ids sort as their rows do.
func width(n int) int { return len(fmt.Sprint(n)) }
