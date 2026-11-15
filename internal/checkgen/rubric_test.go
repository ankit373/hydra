// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/trust"
)

const supportAsk = `Reply to this customer. Mention the "refund", keep it under 120 words, ` +
	`do not say "guaranteed", do not promise a delivery date, and keep a friendly tone.`

// rubricReply is what the writer lists for supportAsk, with one item nobody asked for.
const rubricReply = `[
 {"text": "mentions the refund", "type": "must_mention", "phrase": "refund"},
 {"text": "under 120 words", "type": "max_words", "limit": 120},
 {"text": "never says guaranteed", "type": "must_not_say", "phrase": "guaranteed"},
 {"text": "The reply does not promise a delivery date.", "type": "judge"},
 {"text": "The tone is friendly.", "type": "judge"},
 {"text": "mentions the loyalty discount", "type": "must_mention", "phrase": "loyalty discount"},
 {"text": "offers a \"free upgrade\"", "type": "judge"},
 {"text": "under 50 words", "type": "max_words", "limit": 50},
 {"text": "is poetic", "type": "vibes"}
]`

func rubricSpecs(t *testing.T, reply string) []Spec {
	t.Helper()
	specs, err := rubricGen{}.Generate(context.Background(), Task{
		Text: supportAsk, Kind: "writing.support", Writer: GenInfo{Head: "local:qwen", Family: "qwen"},
		Ask: func(context.Context, string) (string, error) { return reply, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return specs
}

func itemsOf(t *testing.T, s Spec) []rubricItem {
	t.Helper()
	var items []rubricItem
	if err := json.Unmarshal(s.Body, &items); err != nil {
		t.Fatal(err)
	}
	return items
}

func TestRubric_DropsItemNotInRequest(t *testing.T) {
	specs := rubricSpecs(t, rubricReply)
	if len(specs) != 2 || specs[0].Variant != "code" || specs[1].Variant != "judge" {
		t.Fatalf("specs = %+v, want code then judge", specs)
	}
	var kept []string
	for _, s := range specs {
		for _, it := range itemsOf(t, s) {
			kept = append(kept, it.Text)
		}
	}
	want := "mentions the refund|under 120 words|never says guaranteed|" +
		"The reply does not promise a delivery date.|The tone is friendly."
	if got := strings.Join(kept, "|"); got != want {
		t.Fatalf("kept %q,\nwant %q", got, want)
	}
}

func TestRubric_CodeItemsNeedNoModel(t *testing.T) {
	o, err := rubricGen{}.Oracle(rubricSpecs(t, rubricReply)[0], Env{})
	if err != nil {
		t.Fatal(err)
	}
	for answer, want := range map[string]bool{
		"Your refund is on its way.":                  true,
		"Your REFUND is on its way, guaranteed.":      false,
		"Sorry for the wait, we are looking into it.": false,
		"Your refund " + strings.Repeat("word ", 120): false,
	} {
		v, err := o.Verify(context.Background(), answer, trust.Task{})
		if err != nil || v.Passed != want || (!v.Passed && !strings.Contains(v.Detail, "items failed: ")) {
			t.Errorf("Verify(%.40q) = %+v, %v; want passed=%v", answer, v, err, want)
		}
	}
}

func TestRubric_EachJudgeItemAskedAlone(t *testing.T) {
	d := &noIfSays{word: "Friday"}
	o, err := rubricGen{}.Oracle(rubricSpecs(t, rubricReply)[1], Env{Decider: func(GenInfo) oracle.Decider { return d }})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := o.Verify(context.Background(), "Your refund is on its way.", trust.Task{}); err != nil || !v.Passed {
		t.Fatalf("verdict %+v, %v", v, err)
	}
	if len(d.asked) != 2 || !strings.Contains(d.asked[0], "delivery date") || strings.Contains(d.asked[0], "friendly") ||
		!strings.Contains(d.asked[1], "friendly") {
		t.Fatalf("asked %q, want one question per judge item", d.asked)
	}
	if _, err := (rubricGen{}).Oracle(rubricSpecs(t, rubricReply)[1], Env{}); err == nil {
		t.Error("a judge rubric with no model was built")
	}
}

func TestRubric_TooFewItemsIsNotChecked(t *testing.T) {
	reply := `[{"text": "mentions the refund", "type": "must_mention", "phrase": "refund"},
	 {"text": "says sorry", "type": "must_mention", "phrase": "sorry"},
	 {"text": "is short", "type": "max_words", "limit": 30}]`
	if specs := rubricSpecs(t, reply); specs != nil {
		t.Fatalf("one backed item made specs %+v; want none, so the reply reads as not checked", specs)
	}
	if _, err := (rubricGen{}).Generate(context.Background(), Task{Text: supportAsk}); err == nil {
		t.Error("generated with no writer to ask")
	}
	bad := Task{Text: supportAsk, Ask: func(context.Context, string) (string, error) { return "sure!", nil }}
	if _, err := (rubricGen{}).Generate(context.Background(), bad); err == nil {
		t.Error("a reply with no JSON list was read as items")
	}
}

// The done-when: a reply that promises a date fails the judge rubric, and the verdict names the item.
func TestRubric_ReplyPromisingADateFails(t *testing.T) {
	span, _ := setup(t, &fakeGen{})
	if err := Register("writing", rubricGen{}); err != nil {
		t.Fatal(err)
	}
	specs := rubricSpecs(t, rubricReply)
	env := Env{Decider: func(GenInfo) oracle.Decider { return &noIfSays{word: "Friday"} }}
	res, err := Run(context.Background(), specs, "Your refund will arrive by Friday.", answering, span, env)
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].Verdict.Passed || res[1].Verdict.Passed ||
		!strings.Contains(res[1].Verdict.Detail, `1 "The reply does not promise a delivery date."`) {
		t.Fatalf("results = %+v", res)
	}
	if res[1].Spec.Check() != "gen:rubric-judge" || Label(res) != LabelSomeFailed {
		t.Errorf("check %q label %q", res[1].Spec.Check(), Label(res))
	}
}
