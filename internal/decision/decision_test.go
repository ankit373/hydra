// SPDX-License-Identifier: MIT

package decision

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	cases := []struct {
		name  string
		probs []float64
		want  string
	}{
		{name: "sums to one", probs: []float64{0.15, 0.8, 0.05}},
		{name: "within rounding", probs: []float64{0.5, 0.5 + 5e-7, 0}},
		{name: "wrong length", probs: []float64{1}, want: "returned 1 probabilities for 3 options"},
		{name: "short mass", probs: []float64{0.5, 0.3, 0.1}, want: "not 1"},
		{name: "past the tolerance", probs: []float64{0.5, 0.5 + 2e-6, 0}, want: "not 1"},
		{name: "negative", probs: []float64{1.2, -0.2, 0}, want: "outside [0, 1]"},
		{name: "NaN", probs: []float64{math.NaN(), 0.5, 0.5}, want: "outside [0, 1]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Check(c.probs, 3)
			if c.want == "" {
				if err != nil {
					t.Errorf("Check = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Check = %v, want one saying %q", err, c.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	two := []Option{
		{Name: "a"},
		{Name: "b"},
	}
	cases := []struct {
		name string
		q    Question
		want string
	}{
		{name: "predicate", q: Question{Kind: Predicate}},
		{name: "choice", q: Question{Kind: Choice, Options: two}},
		{name: "predicate with options", q: Question{Kind: Predicate, Options: two}, want: "takes no options"},
		{name: "unknown kind", q: Question{Kind: "box"}, want: `unknown kind "box"`},
		{name: "one option", q: Question{Kind: Choice, Options: two[:1]}, want: "takes 2 to 255 options, got 1"},
		{name: "eleven levels", q: Question{Kind: Score, Options: make([]Option, 11)}, want: "takes 2 to 10"},
		{name: "unnamed option", q: Question{Kind: Choice, Options: make([]Option, 2)}, want: "has no name"},
		{name: "duplicate", q: Question{Kind: Choice, Options: []Option{two[0], two[0]}}, want: "listed twice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Request{Questions: []Question{c.q}}.Validate()
			if c.want == "" {
				if err != nil {
					t.Errorf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Validate = %v, want one saying %q", err, c.want)
			}
		})
	}
	if err := (Request{}).Validate(); err == nil {
		t.Error("a request with no questions validated")
	}
}

func TestAnswer_TopAndLevel(t *testing.T) {
	a := Answer{Probabilities: []float64{0.1, 0.1, 0.8}}
	if got := a.Top(); got != 2 {
		t.Errorf("Top = %d, want 2", got)
	}
	// The documented score example: 0.1, 0.1, 0.8 over levels 0..2 is 1.7.
	if got := a.Level(); math.Abs(got-1.7) > 1e-12 {
		t.Errorf("Level = %v, want 1.7", got)
	}
	tie := Answer{Probabilities: []float64{0.5, 0.5}}
	if got := tie.Top(); got != 0 {
		t.Errorf("Top on a tie = %d, want the earlier option", got)
	}
}

// pngDataURL is a one-pixel png, small enough to inline.
const pngDataURL = "data:image/png;base64,iVBORw0KGgo="

var imageRequest = Request{
	Input: "Is the screen broken?",
	Image: pngDataURL,
	Questions: []Question{
		{Kind: Predicate, Instructions: "The screen is cracked."},
	},
}

func TestMarshalSystemOne_HostedImageIsAnAssetReferencedFromTheState(t *testing.T) {
	body, err := MarshalSystemOne(imageRequest, "jev-latest", ImageAsAsset)
	if err != nil {
		t.Fatal(err)
	}
	var w systemOneRequest
	if err = json.Unmarshal(body, &w); err != nil {
		t.Fatal(err)
	}
	asset, ok := w.Assets[imageRef]
	if !ok || asset.Format != "png" || asset.DataBase64 != "iVBORw0KGgo=" {
		t.Errorf("assets = %+v, want the png under %s", w.Assets, imageRef)
	}
	// Every asset must be referenced, or the hosted API answers 400.
	if !strings.Contains(string(w.State), imageRef) {
		t.Errorf("state %s does not reference the asset", w.State)
	}
	if len(w.Images) != 0 {
		t.Error("a hosted request also carried the local images list")
	}
}

func TestMarshalSystemOne_HostedImageMustBeInline(t *testing.T) {
	r := imageRequest
	r.Image = "https://example.com/screen.png"
	if _, err := MarshalSystemOne(r, "jev-latest", ImageAsAsset); err == nil {
		t.Error("a remote image was accepted where only an inline one can be sent")
	}
}

func TestMarshalSystemOne_LocalImageGoesInTheList(t *testing.T) {
	body, err := MarshalSystemOne(imageRequest, "clef", ImageInList)
	if err != nil {
		t.Fatal(err)
	}
	var w systemOneRequest
	if err = json.Unmarshal(body, &w); err != nil {
		t.Fatal(err)
	}
	if len(w.Images) != 1 || w.Images[0].URL != pngDataURL || len(w.Assets) != 0 {
		t.Errorf("images = %+v, assets = %+v; want the URL in images only", w.Images, w.Assets)
	}
}

func TestMarshalDecisions_ImageIsAPartOfOneUserMessage(t *testing.T) {
	body, err := MarshalDecisions(imageRequest, "gpt-6-luna")
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		Input []decisionsMessage `json:"input"`
	}
	if err = json.Unmarshal(body, &w); err != nil {
		t.Fatal(err)
	}
	want := []decisionsPart{
		{Type: "input_text", Text: "Is the screen broken?"},
		{Type: "input_image", ImageURL: pngDataURL},
	}
	if len(w.Input) != 1 || w.Input[0].Role != "user" || !slices.Equal(w.Input[0].Content, want) {
		t.Errorf("input = %+v, want one user message with text then image", w.Input)
	}
}

func TestParseDecisionsResponse_ARefusalIsAnError(t *testing.T) {
	r := Request{Questions: []Question{{Kind: Predicate, Instructions: "x"}}}
	body := []byte(`{"model":"m","answers":[{"type":"refusal","name":null}],"usage":{"input_tokens":1}}`)
	if _, err := ParseDecisionsResponse(body, r); err == nil || !strings.Contains(err.Error(), "declined") {
		t.Errorf("err = %v, want the refusal reported", err)
	}
}

// Structured state is kept as text, and a noul's true and false descriptions survive the trip.
func TestParseSystemOneRequest_KeepsWhatAPredicateCannotCarryAsText(t *testing.T) {
	body := []byte(`{
  "model": "jev-latest",
  "state": {"subject": "Duplicate charge", "message": "Please help."},
  "questions": {
    "spam": {
      "type": "noul",
      "instructions": "Is this message spam?",
      "criteria": {"true": "Unsolicited advertising", "false": "A legitimate conversation"}
    }
  }
}`)
	r, err := ParseSystemOneRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"subject":"Duplicate charge","message":"Please help."}`; r.Input != want {
		t.Errorf("Input = %q, want the state as compact JSON %q", r.Input, want)
	}
	want := strings.Join([]string{
		"Is this message spam?",
		"True means: Unsolicited advertising",
		"False means: A legitimate conversation",
	}, "\n\n")
	if q := r.Questions[0]; q.Name != "spam" || q.Kind != Predicate || q.Instructions != want {
		t.Errorf("question = %+v, want the criteria folded into the instructions", q)
	}
}

// billingRequest is the documented support-ticket example, asked once in every dialect.
var billingRequest = Request{
	Input: "I was charged twice for my subscription this month. Please refund the extra charge.",
	Questions: []Question{
		{
			Kind:         Predicate,
			Instructions: "Is this message about billing?",
		},
		{
			Kind:         Choice,
			Instructions: "What is the tone of this message?",
			Options: []Option{
				{Name: "angry", Description: "An upset or hostile message"},
				{Name: "calm", Description: "A neutral or polite message"},
				{Name: "excited", Description: "An enthusiastic or eager message"},
			},
		},
		{
			Kind:         Score,
			Instructions: "How urgent is this message?",
			Options: []Option{
				{Name: "Can wait"},
				{Name: "Needs attention this week"},
				{Name: "Needs attention today"},
			},
		},
	},
}

// billingProbabilities is what every fixture answers, per question.
var billingProbabilities = [][]float64{
	{0.98, 0.02},
	{0.15, 0.8, 0.05},
	{0.1, 0.1, 0.8},
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sameJSON compares two bodies as values, so key order and spacing do not matter.
func sameJSON(t *testing.T, got, want []byte) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("encoded body is not JSON: %v", err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("fixture is not JSON: %v", err)
	}
	return near(g, w)
}

// near is equality on decoded JSON with numbers within float rounding, since a
// computed score level writes 1.7 as 1.7000000000000002.
func near(got, want any) bool {
	switch w := want.(type) {
	case float64:
		g, ok := got.(float64)
		return ok && math.Abs(g-w) <= 1e-12
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k, wv := range w {
			if gv, found := g[k]; !found || !near(gv, wv) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !near(g[i], w[i]) {
				return false
			}
		}
		return true
	}
	return got == want
}

// closeTo compares distributions to within float rounding, since 1 - P(true) is computed.
func closeTo(got, want []float64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			return false
		}
	}
	return true
}

func TestCodecs_EachDialectRoundTripsItsFixture(t *testing.T) {
	hosted := func(r Request, model string) ([]byte, error) {
		return MarshalSystemOne(r, model, ImageAsAsset)
	}
	local := func(r Request, model string) ([]byte, error) {
		return MarshalSystemOne(r, model, ImageInList)
	}
	cases := []struct {
		name    string
		model   string
		marshal func(Request, string) ([]byte, error)
		parse   func([]byte, Request) (Result, error)
		tokens  int
	}{
		{
			name:    "typesafe",
			model:   "jev-latest",
			marshal: hosted,
			parse:   ParseSystemOneResponse,
			tokens:  120,
		},
		{
			name:    "sglang",
			model:   "Cloudflare/clef",
			marshal: local,
			parse:   ParseSystemOneResponse,
			tokens:  96,
		},
		{
			name:    "openai",
			model:   "gpt-6-luna",
			marshal: MarshalDecisions,
			parse:   ParseDecisionsResponse,
			tokens:  42,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, err := c.marshal(billingRequest, c.model)
			if err != nil {
				t.Fatal(err)
			}
			if !sameJSON(t, body, fixture(t, c.name+"_request.json")) {
				t.Errorf("encoded body differs from %s_request.json:\n%s", c.name, body)
			}
			res, err := c.parse(fixture(t, c.name+"_response.json"), billingRequest)
			if err != nil {
				t.Fatal(err)
			}
			if err = billingRequest.CheckResult(res); err != nil {
				t.Errorf("CheckResult = %v, want the fixture accepted", err)
			}
			for i, want := range billingProbabilities {
				if got := res.Answers[i].Probabilities; !closeTo(got, want) {
					t.Errorf("question %d probabilities = %v, want %v", i, got, want)
				}
			}
			if got := res.Answers[1].Confidence; got != 0.7 {
				t.Errorf("choice confidence = %v, want 0.7", got)
			}
			if res.Model != c.model || res.InputTokens != c.tokens {
				t.Errorf("model, tokens = %q, %d; want %q, %d", res.Model, res.InputTokens, c.model, c.tokens)
			}
		})
	}
}

// ab is one choice between a and b, which most malformed replies below answer.
var ab = Request{
	Questions: []Question{
		{
			Kind: Choice,
			Options: []Option{
				{Name: "a"},
				{Name: "b"},
			},
		},
	},
}

// A repeated outcome must be refused: a later value would overwrite the
// earlier one and hide mass that Check needs to see.
func TestParse_RefusesARepeatedOutcome(t *testing.T) {
	cases := []struct {
		name  string
		parse func([]byte, Request) (Result, error)
		body  string
	}{
		{
			name:  "system one",
			parse: ParseSystemOneResponse,
			body:  `{"answers":{"0":{"type":"choice","probabilities":{"a":0.3,"a":0.7,"b":0.3}}}}`,
		},
		{
			name:  "decisions",
			parse: ParseDecisionsResponse,
			body: `{"answers":[{"type":"choice","probabilities":[{"value":"a","probability":0.3},` +
				`{"value":"a","probability":0.7},{"value":"b","probability":0.3}]}]}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := c.parse([]byte(c.body), ab)
			if err == nil || !strings.Contains(err.Error(), `gave "a" more than one probability`) {
				t.Errorf("parse = %v, %v; want the repeated outcome refused", res, err)
			}
		})
	}
}

func TestParse_RefusesAMalformedReply(t *testing.T) {
	predicate := Request{
		Questions: []Question{
			{Kind: Predicate},
		},
	}
	score := Request{
		Questions: []Question{
			{
				Kind: Score,
				Options: []Option{
					{Name: "low"},
					{Name: "high"},
				},
			},
		},
	}
	cases := []struct {
		name  string
		parse func([]byte, Request) (Result, error)
		r     Request
		body  string
		want  string
	}{
		{
			name:  "system one not JSON",
			parse: ParseSystemOneResponse,
			r:     ab,
			body:  `{`,
			want:  "decode",
		},
		{
			name:  "decisions not JSON",
			parse: ParseDecisionsResponse,
			r:     ab,
			body:  `{`,
			want:  "decode",
		},
		{
			name:  "system one missing answer",
			parse: ParseSystemOneResponse,
			r:     ab,
			body:  `{"answers":{}}`,
			want:  "question 0: no answer",
		},
		{
			name:  "decisions missing answer",
			parse: ParseDecisionsResponse,
			r:     ab,
			body:  `{"answers":[]}`,
			want:  "answered 0 of 1 questions",
		},
		{
			name:  "system one wrong kind",
			parse: ParseSystemOneResponse,
			r:     ab,
			body:  `{"answers":{"0":{"type":"noul","noul":0.5}}}`,
			want:  `answered a choice question as "noul"`,
		},
		{
			name:  "decisions wrong kind",
			parse: ParseDecisionsResponse,
			r:     ab,
			body:  `{"answers":[{"type":"score"}]}`,
			want:  `answered a choice question as "score"`,
		},
		{
			name:  "probabilities not an object",
			parse: ParseSystemOneResponse,
			r:     ab,
			body:  `{"answers":{"0":{"type":"choice","probabilities":[0.5,0.5]}}}`,
			want:  "are not an object",
		},
		{
			name:  "probability not a number",
			parse: ParseSystemOneResponse,
			r:     ab,
			body:  `{"answers":{"0":{"type":"choice","probabilities":{"a":"half","b":0.5}}}}`,
			want:  "probability for a",
		},
		{
			name:  "an outcome nobody asked about",
			parse: ParseSystemOneResponse,
			r:     ab,
			body:  `{"answers":{"0":{"type":"choice","probabilities":{"a":0.5,"c":0.5}}}}`,
			want:  `gave no probability for "b"`,
		},
		{
			name:  "an extra outcome",
			parse: ParseSystemOneResponse,
			r:     ab,
			body:  `{"answers":{"0":{"type":"choice","probabilities":{"a":0.5,"b":0.3,"c":0.2}}}}`,
			want:  "answered 3 outcomes for 2 asked",
		},
		{
			name:  "choice value not a string",
			parse: ParseDecisionsResponse,
			r:     ab,
			body:  `{"answers":[{"type":"choice","probabilities":[{"value":1,"probability":1}]}]}`,
			want:  "choice value 1 is not a string",
		},
		{
			name:  "score level not an integer",
			parse: ParseDecisionsResponse,
			r:     score,
			body:  `{"answers":[{"type":"score","probabilities":[{"value":"low","probability":1}]}]}`,
			want:  `score level "low" is not an integer`,
		},
		{
			name:  "predicate with no probability",
			parse: ParseDecisionsResponse,
			r:     predicate,
			body:  `{"answers":[{"type":"predicate"}]}`,
			want:  "gave no probability of true",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.parse([]byte(c.body), c.r)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("parse = %v, want one saying %q", err, c.want)
			}
		})
	}
}

func TestParseSystemOneResponse_AlignsToOptionOrder(t *testing.T) {
	body := `{"answers":{"0":{"type":"choice","probabilities":{"b":0.9,"a":0.1}}}}`
	res, err := ParseSystemOneResponse([]byte(body), ab)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := res.Answers[0].Probabilities, []float64{0.1, 0.9}; !slices.Equal(got, want) {
		t.Errorf("probabilities = %v, want %v in option order", got, want)
	}
}

func TestCheckResult(t *testing.T) {
	short := Result{
		Answers: []Answer{
			{Probabilities: []float64{0.5, 0.4}},
		},
	}
	if err := ab.CheckResult(short); err == nil || !strings.Contains(err.Error(), "question 0") {
		t.Errorf("CheckResult = %v, want the short mass refused on question 0", err)
	}
	if err := ab.CheckResult(Result{}); err == nil || !strings.Contains(err.Error(), "answered 0 of 1") {
		t.Errorf("CheckResult = %v, want the missing answer refused", err)
	}
}
