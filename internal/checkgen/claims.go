// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ankit373/hydra/internal/ground"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/util"
)

// maxClaimSentences caps the sentences checked; past it the verdict is partly checked, never a pass.
const maxClaimSentences = 40

const claimsJudgeAsk = "Is the sentence in the answer below fully supported by the source? " +
	"Judge that one sentence only, against the source only.\n"

// claimsBody is the request as the writer saw it: ground checks against all of it.
type claimsBody struct {
	Prompt string `json:"prompt"`
}

// claimsGen checks an answer over documents one sentence at a time, twice:
// variant ground with ground.Check, variant judge with a yes/no question per sentence.
type claimsGen struct{}

func init() {
	for _, work := range []string{"answer", "summary", "analysis"} {
		if err := Register(work, claimsGen{}); err != nil {
			panic(err)
		}
	}
}

func (claimsGen) Kind() string { return "claims" }

// Generate needs no model. A request with no fenced source has nothing to check against.
func (claimsGen) Generate(_ context.Context, task Task) ([]Spec, error) {
	if sourceOf(task.Text) == "" {
		return nil, nil
	}
	var out []Spec
	for _, v := range []string{"ground", "judge"} {
		s, err := NewSpec("claims", v, claimsBody{Prompt: task.Text}, task, task.Writer)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (claimsGen) Oracle(s Spec, env Env) (oracle.Oracle, error) {
	var b claimsBody
	if err := json.Unmarshal(s.Body, &b); err != nil {
		return nil, fmt.Errorf("checkgen claims: spec %s: %w", s.ID, err)
	}
	switch s.Variant {
	case "ground":
		return &claimsOracle{check: groundSentence(b.Prompt)}, nil
	case "judge":
		d, err := env.decider(s.Generator)
		if err != nil {
			return nil, err
		}
		p := &oracle.PredicateOracle{Template: claimsJudgeTemplate(sourceOf(b.Prompt)), Decider: d, Source: s.Check()}
		return &claimsOracle{check: func(ctx context.Context, sentence string) (SentenceVerdict, error) {
			v, err := p.Verify(ctx, sentence, trust.Task{})
			if err != nil {
				return SentenceVerdict{}, err
			}
			if v.Passed {
				return SentenceVerdict{Status: StatusSupported}, nil
			}
			return SentenceVerdict{Status: StatusNotSupported, Why: "the judge answered no: " + v.Detail}, nil
		}}, nil
	}
	return nil, fmt.Errorf("checkgen claims: spec %s has unknown variant %q", s.ID, s.Variant)
}

// claimsJudgeTemplate is the judge's question about one sentence, {answer} where the sentence goes.
func claimsJudgeTemplate(source string) string {
	return claimsJudgeAsk + util.WrapUntrusted("SOURCE", source) + "\n{answer}"
}

// ClaimsJudgeChat is the system prompt and question the judge variant sends a chat head
// about one sentence, built by the same oracle, so the bench asks what dispatch asks.
func ClaimsJudgeChat(source, sentence string) (system, prompt string, err error) {
	var q recordQuestion
	p := &oracle.PredicateOracle{Template: claimsJudgeTemplate(source), Decider: &q, Source: "bench"}
	if _, err := p.Verify(context.Background(), sentence, trust.Task{}); err != nil {
		return "", "", err
	}
	return yesNoSystem, chatQuestion(string(q)), nil
}

// recordQuestion is a Decider that keeps the question it was asked and answers yes.
type recordQuestion string

func (q *recordQuestion) Decide(_ context.Context, question string, _ []string) ([]float64, error) {
	*q = recordQuestion(question)
	return []float64{1, 0}, nil
}

// GroundSentence is the ground variant's verdict on one sentence of an answer to prompt.
func GroundSentence(ctx context.Context, prompt, sentence string) (SentenceVerdict, error) {
	return groundSentence(prompt)(ctx, sentence)
}

// Sentence statuses. nothing_to_check is a sentence with no number, name, date or quote in it.
const (
	StatusSupported      = "supported"
	StatusNotSupported   = "not_supported"
	StatusNothingToCheck = "nothing_to_check"
)

// SentenceVerdict is one sentence's result. N counts from 1. Ground fills Unsupported, the judge Why.
// Sources are where a sentence ground passed sits in the request; the judge gives none.
type SentenceVerdict struct {
	N           int
	Text        string
	Status      string
	Unsupported []ground.Claim
	Why         string
	Sources     []ground.Span
}

// groundSentence checks one sentence with ground.Check. A sentence naming nothing checkable is skipped.
func groundSentence(prompt string) sentenceCheck {
	var index *ground.Index // built on the first sentence, once per check
	return func(_ context.Context, sentence string) (SentenceVerdict, error) {
		v := ground.Check(sentence, prompt)
		switch {
		case !v.Checked:
			return SentenceVerdict{}, ground.ErrNoContext
		case v.Claims == 0:
			return SentenceVerdict{Status: StatusNothingToCheck}, nil
		case v.Grounded:
			if index == nil {
				index = ground.NewIndex(prompt)
			}
			var spans []ground.Span
			for _, c := range v.Supported {
				spans = append(spans, index.Locate(c)...)
			}
			return SentenceVerdict{Status: StatusSupported, Sources: ground.MergeSpans(spans)}, nil
		}
		return SentenceVerdict{Status: StatusNotSupported, Unsupported: v.Unsupported, Why: v.Detail()}, nil
	}
}

// sentenceCheck judges one sentence; Verify fills in N and Text.
type sentenceCheck func(ctx context.Context, sentence string) (SentenceVerdict, error)

// claimsOracle keeps Last, the verdict of each sentence its most recent Verify looked at.
type claimsOracle struct {
	check sentenceCheck
	Last  []SentenceVerdict
}

// Verify passes only when every sentence it checked passed and it checked them all.
// Any error leaves the whole answer not checked rather than judged on part of it.
func (o *claimsOracle) Verify(ctx context.Context, candidate string, _ trust.Task) (oracle.Verdict, error) {
	all := ground.Sentences(candidate)
	checked := all[:min(len(all), maxClaimSentences)]
	o.Last = make([]SentenceVerdict, 0, len(checked))
	var failed []string
	ran := 0
	for i, s := range checked {
		sv, err := o.check(ctx, s)
		if err != nil {
			return oracle.Verdict{}, fmt.Errorf("sentence %d: %w", i+1, err)
		}
		sv.N, sv.Text = i+1, s
		o.Last = append(o.Last, sv)
		if sv.Status == StatusNothingToCheck {
			continue
		}
		ran++
		if sv.Status == StatusNotSupported {
			failed = append(failed, failedDetail(sv))
		}
	}
	if len(failed) > 0 {
		return oracle.Verdict{Detail: "sentences not supported: " + strings.Join(failed, "; ")}, nil
	}
	if ran == 0 {
		return oracle.Verdict{}, ground.ErrNoClaims
	}
	if len(checked) < len(all) {
		return oracle.Verdict{}, fmt.Errorf("partly checked: the first %d of %d sentences passed", len(checked), len(all))
	}
	return oracle.Verdict{Passed: true, Detail: fmt.Sprintf("all %d sentences checked passed", ran)}, nil
}

// failedDetail names a failed sentence, with the spans ground could not find. The judge's Why stays out of it.
func failedDetail(sv SentenceVerdict) string {
	f := fmt.Sprintf("%d %q", sv.N, sv.Text)
	if len(sv.Unsupported) > 0 {
		f += " (" + sv.Why + ")"
	}
	return f
}
