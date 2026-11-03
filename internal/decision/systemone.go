// SPDX-License-Identifier: MIT

package decision

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Legend        map[string]string  `json:"legend,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
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

// ParseSystemOneRequest reads a client's System One request. The model it names is
// not read: which source answers is the router's choice.
func ParseSystemOneRequest(body []byte) (Request, error) {
	var w systemOneRequest
	if err := json.Unmarshal(body, &w); err != nil {
		return Request{}, err
	}
	if len(w.Assets) > 0 || len(w.Images) > 0 {
		return Request{}, errImageNotRouted
	}
	input, err := textOf(w.State)
	if err != nil {
		return Request{}, err
	}
	// Sorted, because a map has no order and the same request must route the same way twice.
	keys := make([]string, 0, len(w.Questions))
	for k := range w.Questions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	r := Request{Input: input, Questions: make([]Question, len(keys))}
	for i, k := range keys {
		if r.Questions[i], err = w.Questions[k].question(k); err != nil {
			return Request{}, fmt.Errorf("question %q: %w", k, err)
		}
	}
	return r, r.Validate()
}

func (w systemOneQuestion) question(name string) (Question, error) {
	instructions, err := textOf(w.Instructions)
	if err != nil {
		return Question{}, err
	}
	q := Question{Name: name, Instructions: instructions}
	switch w.Type {
	case noulType:
		q.Kind = Predicate
		q.Instructions, err = withNoulCriteria(instructions, w.Criteria)
	case string(Choice):
		q.Kind = Choice
		q.Options, err = choiceOptions(w.Criteria)
	case string(Score):
		q.Kind = Score
		q.Options, err = scoreOptions(w.Criteria)
	default:
		err = fmt.Errorf("question type %q is not routed", w.Type)
	}
	return q, err
}

// withNoulCriteria folds a noul's true and false descriptions into its
// instructions, since a predicate elsewhere has no field for them.
func withNoulCriteria(instructions string, raw json.RawMessage) (string, error) {
	var c struct {
		True  json.RawMessage `json:"true"`
		False json.RawMessage `json:"false"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &c); err != nil {
			return "", err
		}
	}
	whenTrue, err := textOf(c.True)
	if err != nil {
		return "", err
	}
	whenFalse, err := textOf(c.False)
	if err != nil {
		return "", err
	}
	if whenTrue != "" {
		whenTrue = "True means: " + whenTrue
	}
	if whenFalse != "" {
		whenFalse = "False means: " + whenFalse
	}
	return joinNonEmpty(instructions, whenTrue, whenFalse), nil
}

// choiceOptions are sorted by name: a JSON object has no order to keep.
func choiceOptions(raw json.RawMessage) ([]Option, error) {
	var criteria map[string]json.RawMessage
	if err := json.Unmarshal(raw, &criteria); err != nil {
		return nil, err
	}
	opts := make([]Option, 0, len(criteria))
	for name, desc := range criteria {
		text, err := textOf(desc)
		if err != nil {
			return nil, err
		}
		opts = append(opts, Option{Name: name, Description: text})
	}
	sort.Slice(opts, func(i, j int) bool { return opts[i].Name < opts[j].Name })
	return opts, nil
}

func scoreOptions(raw json.RawMessage) ([]Option, error) {
	var levels []json.RawMessage
	if err := json.Unmarshal(raw, &levels); err != nil {
		return nil, err
	}
	opts := make([]Option, len(levels))
	for i, l := range levels {
		text, err := textOf(l)
		if err != nil {
			return nil, err
		}
		opts[i] = Option{Name: text}
	}
	return opts, nil
}

// MarshalSystemOneResponse answers a client in System One's shape, keyed by its own question names.
func MarshalSystemOneResponse(r Request, res Result) ([]byte, error) {
	w := systemOneResponse{
		Model:   res.Model,
		Answers: make(map[string]systemOneAnswer, len(r.Questions)),
		Usage:   systemOneUsage{InputTokens: res.InputTokens},
	}
	for i, q := range r.Questions {
		w.Answers[q.Name] = systemOneAnswerFor(q, res.Answers[i])
	}
	return json.Marshal(w)
}

func systemOneAnswerFor(q Question, a Answer) systemOneAnswer {
	out := systemOneAnswer{Type: q.Kind.wireType()}
	if q.Kind == Predicate {
		out.Noul = &a.Probabilities[0]
		return out
	}
	out.Confidence = &a.Confidence
	out.Probabilities = make(map[string]float64, len(q.Options))
	if q.Kind == Choice {
		out.Choice = q.Options[a.Top()].Name
		for i, o := range q.Options {
			out.Probabilities[o.Name] = a.Probabilities[i]
		}
		return out
	}
	level := a.Level()
	out.Score = &level
	out.Legend = make(map[string]string, len(q.Options))
	for i, o := range q.Options {
		key := strconv.Itoa(i)
		out.Legend[key] = optionText(o)
		out.Probabilities[key] = a.Probabilities[i]
	}
	return out
}
