// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

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
		p := &oracle.PredicateOracle{
			Template: claimsJudgeAsk + util.WrapUntrusted("SOURCE", sourceOf(b.Prompt)) + "\n{answer}",
			Decider:  d, Source: s.Check(),
		}
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

// Sentence statuses. nothing_to_check is a sentence with no number, name, date or quote in it.
const (
	StatusSupported      = "supported"
	StatusNotSupported   = "not_supported"
	StatusNothingToCheck = "nothing_to_check"
)

// SentenceVerdict is one sentence's result. N counts from 1. Ground fills Unsupported, the judge Why.
type SentenceVerdict struct {
	N           int
	Text        string
	Status      string
	Unsupported []ground.Claim
	Why         string
}

// groundSentence checks one sentence with ground.Check. A sentence naming nothing checkable is skipped.
func groundSentence(prompt string) sentenceCheck {
	return func(_ context.Context, sentence string) (SentenceVerdict, error) {
		v := ground.Check(sentence, prompt)
		switch {
		case !v.Checked:
			return SentenceVerdict{}, ground.ErrNoContext
		case v.Claims == 0:
			return SentenceVerdict{Status: StatusNothingToCheck}, nil
		case v.Grounded:
			return SentenceVerdict{Status: StatusSupported}, nil
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
	all := sentences(candidate)
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

// sentences splits on . ! ? before a space or the end, and on line breaks. A
// stop inside quotes or a number does not split; a sentence with no content word is dropped.
func sentences(text string) []string {
	rs := []rune(text)
	var out []string
	var cur strings.Builder
	quoted := false
	flush := func() {
		if s := strings.TrimSpace(cur.String()); hasContentWord(s) {
			out = append(out, s)
		}
		cur.Reset()
	}
	for i, r := range rs {
		if r == '\n' {
			flush()
			quoted = false
			continue
		}
		cur.WriteRune(r)
		switch r {
		case '"':
			quoted = !quoted
		case '“':
			quoted = true
		case '”':
			quoted = false
		}
		atBreak := i+1 == len(rs) || unicode.IsSpace(rs[i+1])
		stop := strings.ContainsRune(".!?", r) ||
			(strings.ContainsRune("\"”", r) && i > 0 && strings.ContainsRune(".!?", rs[i-1]))
		if stop && !quoted && atBreak && !listMarker.MatchString(strings.TrimSpace(cur.String())) {
			flush()
		}
	}
	flush()
	return out
}

// listMarker is a numbered list item's "1." or "a)", which starts a sentence rather than ending one.
var listMarker = regexp.MustCompile(`^(\d+|[a-z])[.)]$`)

// replyWords say nothing a source could support.
var replyWords = map[string]bool{"yes": true, "yeah": true, "okay": true, "sure": true, "thanks": true, "hello": true}

// hasContentWord is a digit, or a word of three or more letters other than a reply word.
func hasContentWord(s string) bool {
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if strings.ContainsFunc(w, unicode.IsDigit) || (len([]rune(w)) >= 3 && !replyWords[w]) {
			return true
		}
	}
	return false
}
