// SPDX-License-Identifier: MIT

package decision

import (
	"encoding/json"
	"math"
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
