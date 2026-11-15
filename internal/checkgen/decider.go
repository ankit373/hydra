// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ankit373/hydra/internal/decide"
	"github.com/ankit373/hydra/internal/decision"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/provider"
)

// predicateOptions are what oracle.PredicateOracle asks a Decider to weigh, P(true) first.
var predicateOptions = []string{"true", "false"}

// yesNoSystem frames a chat head as a yes/no judge for any kind of work.
const yesNoSystem = "You judge one answer against one question. Reply with a single word: yes or no."

// HeadDecider answers one yes/no question with one head. A decision head returns
// its probabilities; a chat head's strict yes or no becomes [1,0] or [0,1].
type HeadDecider struct {
	Head provider.Head
	// Router asks a decision head; its Heads are set to Head alone.
	Router decide.Router
	// Chat asks a chat head, normally (*dispatch.Dispatcher).Dispatch, which logs the call's cost.
	Chat   func(ctx context.Context, prompt string, opts dispatch.Options) (*dispatch.Result, error)
	RunID  string
	Domain string
}

var _ oracle.Decider = (*HeadDecider)(nil)

// Hard says the probabilities are a chat head's word, not a calibrated figure.
func (d *HeadDecider) Hard() bool { return !executor.IsDecisionHead(d.Head) }

// Decide asks one question per call, so each rubric item is judged on its own.
func (d *HeadDecider) Decide(ctx context.Context, question string, options []string) ([]float64, error) {
	if !slices.Equal(options, predicateOptions) {
		return nil, fmt.Errorf("head decider: only a true/false predicate is supported, got %v", options)
	}
	if executor.IsDecisionHead(d.Head) {
		return d.decide(ctx, question)
	}
	return d.chat(ctx, question)
}

func (d *HeadDecider) decide(ctx context.Context, question string) ([]float64, error) {
	r := d.Router
	if r.Ask == nil || r.Price == nil {
		return nil, errors.New("head decider: decision router has no asker or price")
	}
	r.Heads = []provider.Head{d.Head}
	out, err := r.Decide(ctx, decide.Call{
		Request: decision.Request{Input: question, Questions: []decision.Question{{
			Kind: decision.Predicate, Instructions: "Is the answer to the question in the input yes?",
		}}},
		Domain: d.Domain, RunID: d.RunID,
	})
	if err != nil {
		return nil, err
	}
	if len(out.Result.Answers) != 1 {
		return nil, fmt.Errorf("head decider: %s gave %d answers to one question", d.Head.ID, len(out.Result.Answers))
	}
	return out.Result.Answers[0].Probabilities, nil
}

func (d *HeadDecider) chat(ctx context.Context, question string) ([]float64, error) {
	if d.Chat == nil {
		return nil, errors.New("head decider: no chat dispatcher configured")
	}
	res, err := d.Chat(ctx, question+"\n\nReply yes or no.", dispatch.Options{
		Head: d.Head.ID, System: yesNoSystem, Domain: d.Domain, RunID: d.RunID,
		// The prompt embeds the answer judged, so a stored reply would be about another answer.
		NoCache: true,
	})
	if err != nil {
		return nil, err
	}
	first := ""
	if words := strings.Fields(res.Output); len(words) > 0 {
		first = strings.ToLower(strings.TrimRight(words[0], ".,!"))
	}
	switch first {
	case "yes":
		return []float64{1, 0}, nil
	case "no":
		return []float64{0, 1}, nil
	}
	return nil, fmt.Errorf("head decider: %s did not answer yes or no: %.80q", d.Head.ID, res.Output)
}
