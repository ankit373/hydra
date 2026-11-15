// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/trust"
)

// labelTask is a classification request whose writer always replies reply.
func labelTask(text, reply string, asked *string) Task {
	return Task{
		Text: text, Kind: "classification.support",
		Writer: GenInfo{Head: "or/claude", Family: "anthropic"},
		Ask: func(_ context.Context, prompt string) (string, error) {
			if asked != nil {
				*asked = prompt
			}
			return reply, nil
		},
	}
}

func labelsOf(t *testing.T, s Spec) []string {
	t.Helper()
	var got []string
	if err := json.Unmarshal(s.Body, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestLabels_ReadsSetFromRequest(t *testing.T) {
	const req = "Tag this ticket as one of: Billing, Refund, Bug. Ticket: I was charged twice."
	var asked string
	specs, err := labelsGen{}.Generate(context.Background(), labelTask(req, "```json\n[\"Billing\", \"Refund\", \"Bug\"]\n```", &asked))
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Kind != "labels" || specs[0].Generator.Family != "anthropic" {
		t.Fatalf("specs = %+v, want one labels spec from the writer", specs)
	}
	if got := labelsOf(t, specs[0]); !reflect.DeepEqual(got, []string{"Billing", "Refund", "Bug"}) {
		t.Errorf("labels = %v", got)
	}
	if !strings.Contains(asked, "untrusted data") || !strings.Contains(asked, req) {
		t.Errorf("the request did not reach the writer fenced: %q", asked)
	}
	o, err := labelsGen{}.Oracle(specs[0])
	if err != nil {
		t.Fatal(err)
	}
	for answer, want := range map[string]bool{"refund": true, " **Bug**.": true, "shipping": false} {
		v, err := o.Verify(context.Background(), answer, trust.Task{})
		if err != nil || v.Passed != want {
			t.Errorf("answer %q: passed=%v err=%v, want %v", answer, v.Passed, err, want)
		}
	}
	if gs := For("classification.support"); len(gs) == 0 || gs[0].Kind() != "labels" {
		t.Errorf("classification has generators %v, want the labels one", gs)
	}
}

func TestLabels_DropsInventedLabel(t *testing.T) {
	const req = "Is this message spam, ham or phishing? Message: debug your account now"
	specs, err := labelsGen{}.Generate(context.Background(),
		labelTask(req, `["spam", "Ham", "phishing", "scam", "bug", "spam"]`, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 {
		t.Fatalf("specs = %+v, want one", specs)
	}
	// scam is invented, bug only sits inside "debug", and the second spam is a repeat.
	if got := labelsOf(t, specs[0]); !reflect.DeepEqual(got, []string{"spam", "Ham", "phishing"}) {
		t.Errorf("labels = %v, want only the request's own", got)
	}
}

func TestLabels_NoSetIsNotChecked(t *testing.T) {
	for _, tc := range []struct{ req, reply string }{
		{"What is the sentiment of this review? Great food.", "[]"},
		{"Classify as urgent. Server down.", `["urgent"]`},
		{"Pick a team: sales or support.", `["marketing", "legal"]`},
	} {
		specs, err := labelsGen{}.Generate(context.Background(), labelTask(tc.req, tc.reply, nil))
		if err != nil || len(specs) != 0 {
			t.Errorf("%q with %s: specs=%v err=%v, want no check", tc.req, tc.reply, specs, err)
		}
		if Label(nil) != LabelNotChecked {
			t.Error("no specs did not read as not checked")
		}
	}
	for _, task := range []Task{
		labelTask("one of: a, b", "sure, a and b", nil),
		{Text: "one of: a, b"},
		{Text: "x", Ask: func(context.Context, string) (string, error) { return "", errors.New("down") }},
	} {
		if specs, err := (labelsGen{}).Generate(context.Background(), task); err == nil {
			t.Errorf("task %q gave specs %v with no error", task.Text, specs)
		}
	}
	if _, err := (labelsGen{}).Oracle(Spec{Body: []byte(`{`)}); err == nil {
		t.Error("a corrupt spec body built an oracle")
	}
}
