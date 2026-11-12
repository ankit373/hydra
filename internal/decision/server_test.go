// SPDX-License-Identifier: MIT

package decision

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

// billingResult answers billingRequest with billingProbabilities, as a router hands it back.
func billingResult(model string, tokens int) Result {
	return Result{
		Model:       model,
		InputTokens: tokens,
		Answers: []Answer{
			{Probabilities: billingProbabilities[0]},
			{Probabilities: billingProbabilities[1], Confidence: 0.7},
			{Probabilities: billingProbabilities[2], Confidence: 0.55},
		},
	}
}

// withNames is r with each question named, as System One keys them.
func withNames(r Request, names ...string) Request {
	r.Questions = append([]Question(nil), r.Questions...)
	for i := range r.Questions {
		r.Questions[i].Name = names[i]
	}
	return r
}

// A client's request parses to what the client side encodes it from, and the
// router's answer goes back in the reply shape the client side parses.
func TestServerCodecs_EachDialectRoundTripsItsFixture(t *testing.T) {
	cases := []struct {
		name     string
		request  string
		response string
		parse    func([]byte) (Request, error)
		marshal  func(Request, Result) ([]byte, error)
		want     Request
		model    string
		tokens   int
	}{
		{
			name:     "openai",
			request:  "openai_request.json",
			response: "openai_response.json",
			parse:    ParseDecisionsRequest,
			marshal:  MarshalDecisionsResponse,
			want:     billingRequest,
			model:    "gpt-6-luna",
			tokens:   42,
		},
		// The hosted TypeSafe reply also bills output tokens, which a router has
		// none of, so the reply is checked against the same-shaped local fixture.
		{
			name:     "typesafe",
			request:  "typesafe_request.json",
			response: "sglang_response.json",
			parse:    ParseSystemOneRequest,
			marshal:  MarshalSystemOneResponse,
			want:     withNames(billingRequest, "0", "1", "2"),
			model:    "Cloudflare/clef",
			tokens:   96,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := c.parse(fixture(t, c.request))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(r, c.want) {
				t.Errorf("parsed request = %+v, want %+v", r, c.want)
			}
			body, err := c.marshal(r, billingResult(c.model, c.tokens))
			if err != nil {
				t.Fatal(err)
			}
			if !sameJSON(t, body, fixture(t, c.response)) {
				t.Errorf("encoded reply differs from %s:\n%s", c.response, body)
			}
		})
	}
}

// kindPredicate, kindChoice and kindScore are one question of each kind, answered by kindAnswers.
var (
	kindPredicate = Question{
		Name: "billing",
		Kind: Predicate,
	}
	kindChoice = Question{
		Name: "tone",
		Kind: Choice,
		Options: []Option{
			{Name: "angry"},
			{Name: "calm"},
		},
	}
	kindScore = Question{
		Name: "urgency",
		Kind: Score,
		Options: []Option{
			{Name: "low", Description: "Can wait"},
			{Name: "high"},
		},
	}
	kindAnswers = map[Kind]Answer{
		Predicate: {Probabilities: []float64{0.25, 0.75}},
		Choice:    {Probabilities: []float64{0.1, 0.9}, Confidence: 0.6},
		Score:     {Probabilities: []float64{0.25, 0.75}, Confidence: 0.4},
	}
)

func TestMarshalDecisionsResponse_EachKind(t *testing.T) {
	cases := []struct {
		name string
		q    Question
		want string
	}{
		{
			name: "predicate",
			q:    kindPredicate,
			want: `{"type":"predicate","name":"billing","probability":0.25}`,
		},
		{
			name: "choice",
			q:    kindChoice,
			want: `{"type":"choice","name":"tone","choice":"calm","confidence":0.6,"probabilities":[` +
				`{"value":"angry","probability":0.1},{"value":"calm","probability":0.9}]}`,
		},
		{
			name: "score",
			q:    kindScore,
			want: `{"type":"score","name":"urgency","score":0.75,"confidence":0.4,"probabilities":[` +
				`{"value":0,"label":"low","probability":0.25},{"value":1,"label":"high","probability":0.75}]}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Request{Questions: []Question{c.q}}
			res := Result{Model: "m", InputTokens: 7, Answers: []Answer{kindAnswers[c.q.Kind]}}
			body, err := MarshalDecisionsResponse(r, res)
			if err != nil {
				t.Fatal(err)
			}
			var w struct {
				Model   string            `json:"model"`
				Answers []json.RawMessage `json:"answers"`
				Usage   decisionsUsage    `json:"usage"`
			}
			if err = json.Unmarshal(body, &w); err != nil {
				t.Fatal(err)
			}
			if len(w.Answers) != 1 || !sameJSON(t, w.Answers[0], []byte(c.want)) {
				t.Errorf("answers = %s, want [%s]", w.Answers, c.want)
			}
			if w.Model != "m" || w.Usage.InputTokens != 7 || w.Usage.TotalTokens != 7 {
				t.Errorf("model, usage = %q, %+v; want m billed 7 input tokens in total", w.Model, w.Usage)
			}
		})
	}
}

func TestMarshalSystemOneResponse_EachKind(t *testing.T) {
	cases := []struct {
		name string
		q    Question
		want string
	}{
		{
			name: "predicate",
			q:    kindPredicate,
			want: `{"type":"noul","noul":0.25}`,
		},
		{
			name: "choice",
			q:    kindChoice,
			want: `{"type":"choice","choice":"calm","confidence":0.6,"probabilities":{"angry":0.1,"calm":0.9}}`,
		},
		// A level's description has no field of its own in a legend, so it joins the name.
		{
			name: "score",
			q:    kindScore,
			want: `{"type":"score","score":0.75,"confidence":0.4,` +
				`"legend":{"0":"low: Can wait","1":"high"},"probabilities":{"0":0.25,"1":0.75}}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Request{Questions: []Question{c.q}}
			res := Result{Model: "m", InputTokens: 7, Answers: []Answer{kindAnswers[c.q.Kind]}}
			body, err := MarshalSystemOneResponse(r, res)
			if err != nil {
				t.Fatal(err)
			}
			var w struct {
				Model   string                     `json:"model"`
				Answers map[string]json.RawMessage `json:"answers"`
				Usage   systemOneUsage             `json:"usage"`
			}
			if err = json.Unmarshal(body, &w); err != nil {
				t.Fatal(err)
			}
			got, ok := w.Answers[c.q.Name]
			if len(w.Answers) != 1 || !ok || !sameJSON(t, got, []byte(c.want)) {
				t.Errorf("answers = %s, want %s under %q", body, c.want, c.q.Name)
			}
			if w.Model != "m" || w.Usage.InputTokens != 7 {
				t.Errorf("model, usage = %q, %+v; want m billed 7 input tokens", w.Model, w.Usage)
			}
		})
	}
}

// Probabilities are written in option order, which a map-backed object would not keep.
func TestMarshalSystemOneResponse_KeepsOptionOrder(t *testing.T) {
	q := Question{
		Name: "pick",
		Kind: Choice,
		Options: []Option{
			{Name: "zeta"},
			{Name: "alpha"},
			{Name: "mid"},
		},
	}
	res := Result{Answers: []Answer{{Probabilities: []float64{0.5, 0.2, 0.3}}}}
	body, err := MarshalSystemOneResponse(Request{Questions: []Question{q}}, res)
	if err != nil {
		t.Fatal(err)
	}
	want := `"probabilities":{"zeta":0.5,"alpha":0.2,"mid":0.3}`
	if !strings.Contains(string(body), want) {
		t.Errorf("body = %s, want %s", body, want)
	}
}

// A NaN has no JSON form, so the reply is refused rather than written broken.
func TestMarshalResponses_RefuseANaN(t *testing.T) {
	cases := []struct {
		name    string
		marshal func(Request, Result) ([]byte, error)
		q       Question
	}{
		{
			name:    "decisions predicate",
			marshal: MarshalDecisionsResponse,
			q:       kindPredicate,
		},
		{
			name:    "system one choice",
			marshal: MarshalSystemOneResponse,
			q:       kindChoice,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := Result{Answers: []Answer{{Probabilities: []float64{math.NaN(), 0.5}}}}
			if body, err := c.marshal(Request{Questions: []Question{c.q}}, res); err == nil {
				t.Errorf("marshal = %s, want an error for NaN", body)
			}
		})
	}
}

// Message input keeps its text and JSON parts, in order, one paragraph each.
func TestParseDecisionsRequest_ReadsMessageInput(t *testing.T) {
	body := []byte(`{
  "model": "ignored",
  "input": [
    {
      "type": "message",
      "role": "user",
      "content": "Plain text."
    },
    {
      "role": "user",
      "content": [
        {"type": "input_text", "text": "Typed text."},
        {"type": "input_json", "data": {"order": 42}}
      ]
    },
    {
      "type": "message",
      "role": "assistant",
      "content": [{"type": "output_text", "text": "Earlier reply."}]
    }
  ],
  "questions": [
    {
      "type": "predicate",
      "name": "refund",
      "instructions": [{"type": "input_text", "text": "Is a refund asked for?"}]
    }
  ]
}`)
	r, err := ParseDecisionsRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	want := Request{
		Input: strings.Join([]string{
			"Plain text.",
			"Typed text.\n\n" + `{"order":42}`,
			"Earlier reply.",
		}, "\n\n"),
		Questions: []Question{
			{
				Name:         "refund",
				Kind:         Predicate,
				Instructions: "Is a refund asked for?",
			},
		},
	}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("request = %+v, want %+v", r, want)
	}
}

func TestParseDecisionsRequest_Refuses(t *testing.T) {
	// question wraps one question in an otherwise valid request.
	question := func(q string) string {
		return `{"input":"x","questions":[` + q + `]}`
	}
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "not JSON",
			body: `{`,
			want: "unexpected end of JSON input",
		},
		{
			name: "input neither text nor messages",
			body: `{"input":5,"questions":[]}`,
			want: "input is neither text nor a list of messages",
		},
		{
			name: "input item that is not a message",
			body: `{"input":[{"type":"function_call"}],"questions":[]}`,
			want: `input item 0: "function_call" is not routed yet`,
		},
		{
			name: "message content neither text nor parts",
			body: `{"input":[{"content":5}],"questions":[]}`,
			want: "input item 0: content is neither text nor a list of parts",
		},
		{
			name: "image in the input",
			body: `{"input":[{"content":[{"type":"input_image","image_url":"https://x/y.png"}]}],"questions":[]}`,
			want: "images are not routed yet",
		},
		{
			name: "unknown content part",
			body: `{"input":[{"content":[{"type":"input_file"}]}],"questions":[]}`,
			want: `content part "input_file" is not routed yet`,
		},
		{
			name: "instructions neither text nor parts",
			body: question(`{"type":"predicate","instructions":5}`),
			want: "question 0: content is neither text nor a list of parts",
		},
		{
			name: "choice value not a string",
			body: question(`{"type":"choice","instructions":"x","choices":[{"value":1},{"value":2}]}`),
			want: "question 0: choice value 1 is not a string",
		},
		{
			name: "unsupported question type",
			body: question(`{"type":"ranking","instructions":"x"}`),
			want: `question 0: unknown kind "ranking"`,
		},
		{
			name: "one choice",
			body: question(`{"type":"choice","instructions":"x","choices":[{"value":"a"}]}`),
			want: "a choice takes 2 to 255 options, got 1",
		},
		{
			name: "no questions",
			body: `{"input":"x","questions":[]}`,
			want: "no questions asked",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := ParseDecisionsRequest([]byte(c.body))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("parse = %+v, %v; want an error saying %q", r, err, c.want)
			}
		})
	}
}

// Questions come out sorted by key and choices by name, so one request always routes the same way.
func TestParseSystemOneRequest_OrdersWhatAMapLeavesUnordered(t *testing.T) {
	body := []byte(`{
  "state": "Where is my order?",
  "questions": {
    "urgency": {
      "type": "score",
      "criteria": [
        "Can wait",
        {"level": "today"}
      ]
    },
    "tone": {
      "type": "choice",
      "instructions": {"ask": "tone"},
      "criteria": {
        "calm": null,
        "angry": "Hostile"
      }
    }
  }
}`)
	r, err := ParseSystemOneRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	want := Request{
		Input: "Where is my order?",
		Questions: []Question{
			{
				Name:         "tone",
				Kind:         Choice,
				Instructions: `{"ask":"tone"}`,
				Options: []Option{
					{Name: "angry", Description: "Hostile"},
					{Name: "calm"},
				},
			},
			{
				Name: "urgency",
				Kind: Score,
				Options: []Option{
					{Name: "Can wait"},
					{Name: `{"level":"today"}`},
				},
			},
		},
	}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("request = %+v, want %+v", r, want)
	}
}

func TestParseSystemOneRequest_Refuses(t *testing.T) {
	// question wraps one question named q in an otherwise valid request.
	question := func(q string) string {
		return `{"state":"x","questions":{"q":` + q + `}}`
	}
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "not JSON",
			body: `{`,
			want: "unexpected end of JSON input",
		},
		{
			name: "an asset",
			body: `{"state":"x","assets":{"img":{"type":"image","format":"png","data_base64":"AA=="}}}`,
			want: "images are not routed yet",
		},
		{
			name: "an image list",
			body: `{"state":"x","images":[{"url":"https://x/y.png"}]}`,
			want: "images are not routed yet",
		},
		{
			name: "unsupported question type",
			body: question(`{"type":"ranking"}`),
			want: `question "q": question type "ranking" is not routed`,
		},
		{
			name: "noul criteria not an object",
			body: question(`{"type":"noul","criteria":[1]}`),
			want: `question "q": json: cannot unmarshal array`,
		},
		{
			name: "choice criteria not an object",
			body: question(`{"type":"choice","criteria":["a","b"]}`),
			want: `question "q": json: cannot unmarshal array`,
		},
		{
			name: "score criteria not a list",
			body: question(`{"type":"score","criteria":{"low":"x"}}`),
			want: `question "q": json: cannot unmarshal object`,
		},
		{
			name: "one choice",
			body: question(`{"type":"choice","criteria":{"a":null}}`),
			want: "a choice takes 2 to 255 options, got 1",
		},
		{
			name: "no questions",
			body: `{"state":"x","questions":{}}`,
			want: "no questions asked",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := ParseSystemOneRequest([]byte(c.body))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("parse = %+v, %v; want an error saying %q", r, err, c.want)
			}
		})
	}
}
