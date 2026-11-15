// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/decide"
	"github.com/ankit373/hydra/internal/decision"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

var (
	chatHead     = provider.Head{ID: "openai/gpt-x", Provider: "openai"}
	decisionHead = provider.Head{ID: "decision/typesafe", Provider: "typesafe"}
)

// chatSays stubs a chat head that replies reply and records each prompt and option set.
func chatSays(reply string, prompts *[]string, opts *[]dispatch.Options) func(context.Context, string, dispatch.Options) (*dispatch.Result, error) {
	return func(_ context.Context, prompt string, o dispatch.Options) (*dispatch.Result, error) {
		*prompts = append(*prompts, prompt)
		*opts = append(*opts, o)
		return &dispatch.Result{Output: reply}, nil
	}
}

type stubAsker struct {
	probs []float64
	asked []decision.Request
}

func (s *stubAsker) Execute(_ context.Context, _ provider.Head, r decision.Request) (decision.Result, error) {
	s.asked = append(s.asked, r)
	return decision.Result{Model: "jev", Answers: []decision.Answer{{Probabilities: s.probs}}}, nil
}

func decisionDecider(t *testing.T, ask decide.Asker) *HeadDecider {
	t.Helper()
	cal, err := trust.New(filepath.Join(t.TempDir(), "calibration.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return &HeadDecider{Head: decisionHead, Router: decide.Router{
		Price: func(provider.Head, decision.Request) float64 { return 0 },
		Ask:   ask, Cal: cal, Target: trust.Target{Confidence: 0.95},
	}}
}

func TestHeadDecider_ChatYesNo(t *testing.T) {
	for reply, want := range map[string][]float64{
		"yes": {1, 0}, "Yes.": {1, 0}, "NO": {0, 1}, "no, it never mentions the refund": {0, 1},
	} {
		var prompts []string
		var opts []dispatch.Options
		d := &HeadDecider{Head: chatHead, Chat: chatSays(reply, &prompts, &opts), RunID: "r1"}
		got, err := d.Decide(context.Background(), "Is the reply polite?", []string{"true", "false"})
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("reply %q: got %v, %v; want %v", reply, got, err, want)
			continue
		}
		if len(prompts) != 1 || !strings.HasPrefix(prompts[0], "Is the reply polite?") {
			t.Errorf("prompts = %q, want the one question once", prompts)
		}
		if o := opts[0]; o.Head != chatHead.ID || !o.NoCache || o.RunID != "r1" {
			t.Errorf("options = %+v, want the head pinned, uncached, in the run", o)
		}
	}
}

func TestHeadDecider_ChatOtherTextIsAnError(t *testing.T) {
	for _, reply := range []string{"", "maybe", "Yesterday it was", "The answer is yes", "true"} {
		var prompts []string
		var opts []dispatch.Options
		d := &HeadDecider{Head: chatHead, Chat: chatSays(reply, &prompts, &opts)}
		if got, err := d.Decide(context.Background(), "Is it polite?", []string{"true", "false"}); err == nil {
			t.Errorf("reply %q read as %v, want an error", reply, got)
		}
	}
	d := &HeadDecider{Head: chatHead}
	if _, err := d.Decide(context.Background(), "q", []string{"true", "false"}); err == nil {
		t.Error("no chat dispatcher, and no error")
	}
	if _, err := d.Decide(context.Background(), "q", []string{"a", "b", "c"}); err == nil {
		t.Error("a three-way question took the yes/no path")
	}
}

func TestHeadDecider_DecisionHeadPassesProbabilitiesThrough(t *testing.T) {
	testutil.NewSandbox(t)
	ask := &stubAsker{probs: []float64{0.62, 0.38}}
	d := decisionDecider(t, ask)
	got, err := d.Decide(context.Background(), "Does the summary name the vendor?", []string{"true", "false"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []float64{0.62, 0.38}) {
		t.Errorf("probabilities = %v, want them as the head gave them", got)
	}
	if len(ask.asked) != 1 || len(ask.asked[0].Questions) != 1 || ask.asked[0].Questions[0].Kind != decision.Predicate {
		t.Errorf("asked = %+v, want one predicate", ask.asked)
	}
	if d.Hard() {
		t.Error("a decision head's probabilities were called hard")
	}
	if _, err := (&HeadDecider{Head: decisionHead}).Decide(context.Background(), "q", []string{"true", "false"}); err == nil {
		t.Error("an unconfigured decision router answered")
	}
}

func TestPredicateOracle_WithHeadDecider(t *testing.T) {
	testutil.NewSandbox(t)
	var prompts []string
	var opts []dispatch.Options
	for _, tc := range []struct {
		name   string
		d      *HeadDecider
		passed bool
		hard   bool
	}{
		{"chat yes", &HeadDecider{Head: chatHead, Chat: chatSays("yes", &prompts, &opts)}, true, true},
		{"chat no", &HeadDecider{Head: chatHead, Chat: chatSays("no", &prompts, &opts)}, false, true},
		{"decision", decisionDecider(t, &stubAsker{probs: []float64{0.8, 0.2}}), true, false},
	} {
		o := &oracle.PredicateOracle{Template: "Does this reply mention the refund? {answer}", Decider: tc.d, Source: "gen:rubric"}
		v, err := o.Verify(context.Background(), "Your refund is on its way.", trust.Task{})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if v.Passed != tc.passed || strings.Contains(v.Detail, "hard yes or no") != tc.hard {
			t.Errorf("%s: verdict = %+v, want passed=%v hard=%v", tc.name, v, tc.passed, tc.hard)
		}
	}
	bad := &oracle.PredicateOracle{Template: "{answer}", Decider: &HeadDecider{Head: chatHead, Chat: chatSays("perhaps", &prompts, &opts)}}
	if v, err := bad.Verify(context.Background(), "x", trust.Task{}); err == nil {
		t.Errorf("a malformed reply became a verdict: %+v", v)
	}
}
