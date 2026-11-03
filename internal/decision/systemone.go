// SPDX-License-Identifier: MIT

package decision

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ImageCarrier is how a System One body carries an image.
type ImageCarrier int

const (
	// ImageAsAsset references the image from the state, which is the hosted API's form.
	ImageAsAsset ImageCarrier = iota
	// ImageInList sends it in a top-level images list, the local server's extension.
	ImageInList
)

// imageRef is where an asset lands in the state; long enough not to occur in real text.
const imageRef = "hydra-image-7f3a9c2e"

// noulType is System One's name for a predicate.
const noulType = "noul"

type systemOneRequest struct {
	Model     string                       `json:"model"`
	State     json.RawMessage              `json:"state"`
	Questions map[string]systemOneQuestion `json:"questions"`
	Assets    map[string]systemOneAsset    `json:"assets,omitempty"`
	Images    []imageURL                   `json:"images,omitempty"`
}

type systemOneQuestion struct {
	Type         string          `json:"type"`
	Instructions json.RawMessage `json:"instructions,omitempty"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
}

type systemOneAsset struct {
	Type       string `json:"type"`
	Format     string `json:"format"`
	DataBase64 string `json:"data_base64"`
}

type imageURL struct {
	URL string `json:"url"`
}

type systemOneResponse struct {
	Model   string                     `json:"model"`
	Answers map[string]systemOneAnswer `json:"answers"`
	Usage   systemOneUsage             `json:"usage"`
}

type systemOneAnswer struct {
	Type          string                 `json:"type"`
	Noul          *float64               `json:"noul,omitempty"`
	Confidence    *float64               `json:"confidence,omitempty"`
	Probabilities systemOneProbabilities `json:"probabilities,omitempty"`
}

// systemOneProbabilities keeps every key a reply sent, where a map would let a repeat overwrite.
type systemOneProbabilities []keyedProb

func (s *systemOneProbabilities) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return fmt.Errorf("probabilities %s are not an object", b)
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		var p float64
		if err = dec.Decode(&p); err != nil {
			return fmt.Errorf("probability for %v: %w", t, err)
		}
		// Inside an object the decoder only yields a string where a key belongs.
		*s = append(*s, keyedProb{key: t.(string), p: p})
	}
	return nil
}

type systemOneUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// wireType is a kind's System One name.
func (k Kind) wireType() string {
	if k == Predicate {
		return noulType
	}
	return string(k)
}

// MarshalSystemOne encodes r as a System One request. Questions are keyed by
// position, so answers align without trusting a caller's names to be unique.
func MarshalSystemOne(r Request, model string, carrier ImageCarrier) ([]byte, error) {
	state := r.Input
	w := systemOneRequest{Model: model, Questions: make(map[string]systemOneQuestion, len(r.Questions))}
	if r.Image != "" {
		switch carrier {
		case ImageAsAsset:
			asset, err := assetFromDataURL(r.Image)
			if err != nil {
				return nil, err
			}
			w.Assets = map[string]systemOneAsset{imageRef: asset}
			state = joinNonEmpty(state, imageRef)
		case ImageInList:
			w.Images = []imageURL{{URL: r.Image}}
		}
	}
	var err error
	if w.State, err = json.Marshal(state); err != nil {
		return nil, err
	}
	for i, q := range r.Questions {
		if w.Questions[strconv.Itoa(i)], err = systemOneQuestionFor(q); err != nil {
			return nil, err
		}
	}
	return json.Marshal(w)
}

func systemOneQuestionFor(q Question) (systemOneQuestion, error) {
	out := systemOneQuestion{Type: q.Kind.wireType()}
	var err error
	if q.Instructions != "" {
		if out.Instructions, err = json.Marshal(q.Instructions); err != nil {
			return out, err
		}
	}
	switch q.Kind {
	case Choice:
		// A choice with no description is read by its name alone, which null says.
		criteria := make(map[string]*string, len(q.Options))
		for _, o := range q.Options {
			criteria[o.Name] = nil
			if o.Description != "" {
				criteria[o.Name] = &o.Description
			}
		}
		out.Criteria, err = json.Marshal(criteria)
	case Score:
		levels := make([]string, len(q.Options))
		for i, o := range q.Options {
			levels[i] = optionText(o)
		}
		out.Criteria, err = json.Marshal(levels)
	}
	return out, err
}

// assetFromDataURL splits a base64 data URL into the asset the hosted API takes,
// which accepts inline png and jpeg only.
func assetFromDataURL(u string) (systemOneAsset, error) {
	rest, isData := strings.CutPrefix(u, "data:")
	meta, data, _ := strings.Cut(rest, ",")
	format, known := assetFormats[meta]
	if !isData || !known || data == "" {
		return systemOneAsset{}, errors.New("a System One image must be a base64 png or jpeg data URL")
	}
	return systemOneAsset{Type: "image", Format: format, DataBase64: data}, nil
}

// assetFormats maps a data URL's media type to the asset format the hosted API names it by.
var assetFormats = map[string]string{
	"image/png;base64":  "png",
	"image/jpeg;base64": "jpg",
}

// ParseSystemOneResponse reads a System One reply to the request MarshalSystemOne encoded.
func ParseSystemOneResponse(body []byte, r Request) (Result, error) {
	var w systemOneResponse
	if err := json.Unmarshal(body, &w); err != nil {
		return Result{}, fmt.Errorf("decode: %w", err)
	}
	res := Result{Model: w.Model, InputTokens: w.Usage.InputTokens, Answers: make([]Answer, len(r.Questions))}
	for i, q := range r.Questions {
		a, ok := w.Answers[strconv.Itoa(i)]
		if !ok {
			return Result{}, fmt.Errorf("question %d: no answer", i)
		}
		ans, err := a.answer(q)
		if err != nil {
			return Result{}, fmt.Errorf("question %d: %w", i, err)
		}
		res.Answers[i] = ans
	}
	return res, nil
}

func (a systemOneAnswer) answer(q Question) (Answer, error) {
	if a.Type != q.Kind.wireType() {
		return Answer{}, fmt.Errorf("answered a %s question as %q", q.Kind, a.Type)
	}
	keys := names(q.Options)
	switch q.Kind {
	case Predicate:
		return predicateAnswer(a.Noul)
	case Score:
		keys = levelKeys(len(q.Options))
	}
	probs, err := aligned(a.Probabilities, keys)
	return Answer{Probabilities: probs, Confidence: confidenceOf(a.Confidence)}, err
}
