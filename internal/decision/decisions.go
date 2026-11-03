// SPDX-License-Identifier: MIT

package decision

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

type decisionsRequest struct {
	Model     string              `json:"model"`
	Input     json.RawMessage     `json:"input"`
	Questions []decisionsQuestion `json:"questions"`
}

type decisionsQuestion struct {
	Type         string            `json:"type"`
	Name         *string           `json:"name,omitempty"`
	Instructions json.RawMessage   `json:"instructions"`
	Choices      []decisionsChoice `json:"choices,omitempty"`
	Levels       []decisionsLevel  `json:"levels,omitempty"`
}

type decisionsChoice struct {
	Value       json.RawMessage `json:"value"`
	Description string          `json:"description,omitempty"`
}

type decisionsLevel struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type decisionsMessage struct {
	Role    string          `json:"role"`
	Content []decisionsPart `json:"content"`
}

type decisionsPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type decisionsResponse struct {
	Model   string            `json:"model"`
	Answers []decisionsAnswer `json:"answers"`
	Usage   decisionsUsage    `json:"usage"`
}

type decisionsAnswer struct {
	Type string `json:"type"`
	// Name is null for an unnamed question, which is how the API echoes one.
	Name          *string            `json:"name"`
	Probability   *float64           `json:"probability,omitempty"`
	Choice        json.RawMessage    `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities []decisionsOutcome `json:"probabilities,omitempty"`
}

type decisionsOutcome struct {
	Value       json.RawMessage `json:"value"`
	Label       string          `json:"label,omitempty"`
	Probability float64         `json:"probability"`
}

type decisionsUsage struct {
	InputTokens         int                   `json:"input_tokens"`
	InputTokensDetails  decisionsInputDetail  `json:"input_tokens_details"`
	OutputTokens        int                   `json:"output_tokens"`
	OutputTokensDetails decisionsOutputDetail `json:"output_tokens_details"`
	TotalTokens         int                   `json:"total_tokens"`
}

type decisionsInputDetail struct {
	CacheWriteTokens int `json:"cache_write_tokens"`
	CachedTokens     int `json:"cached_tokens"`
}

type decisionsOutputDetail struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// refusalType is the answer a Decisions source gives in place of one it declined.
const refusalType = "refusal"

// MarshalDecisions encodes r as an OpenAI Decisions request.
func MarshalDecisions(r Request, model string) ([]byte, error) {
	w := decisionsRequest{Model: model, Questions: make([]decisionsQuestion, len(r.Questions))}
	var input any = r.Input
	if r.Image != "" {
		input = []decisionsMessage{{
			Role: "user",
			Content: []decisionsPart{
				{Type: "input_text", Text: r.Input},
				{Type: "input_image", ImageURL: r.Image},
			},
		}}
	}
	var err error
	if w.Input, err = json.Marshal(input); err != nil {
		return nil, err
	}
	for i, q := range r.Questions {
		if w.Questions[i], err = decisionsQuestionFor(q); err != nil {
			return nil, err
		}
	}
	return json.Marshal(w)
}

func decisionsQuestionFor(q Question) (decisionsQuestion, error) {
	instructions, err := json.Marshal(q.Instructions)
	if err != nil {
		return decisionsQuestion{}, err
	}
	out := decisionsQuestion{Type: string(q.Kind), Instructions: instructions}
	for _, o := range q.Options {
		switch q.Kind {
		case Choice:
			var value json.RawMessage
			if value, err = json.Marshal(o.Name); err != nil {
				return decisionsQuestion{}, err
			}
			out.Choices = append(out.Choices, decisionsChoice{Value: value, Description: o.Description})
		case Score:
			out.Levels = append(out.Levels, decisionsLevel{Label: o.Name, Description: o.Description})
		}
	}
	return out, nil
}

// ParseDecisionsResponse reads a Decisions reply, whose answers come back in question order.
func ParseDecisionsResponse(body []byte, r Request) (Result, error) {
	var w decisionsResponse
	if err := json.Unmarshal(body, &w); err != nil {
		return Result{}, fmt.Errorf("decode: %w", err)
	}
	if len(w.Answers) != len(r.Questions) {
		return Result{}, fmt.Errorf("answered %d of %d questions", len(w.Answers), len(r.Questions))
	}
	res := Result{Model: w.Model, InputTokens: w.Usage.InputTokens, Answers: make([]Answer, len(r.Questions))}
	for i, q := range r.Questions {
		ans, err := w.Answers[i].answer(q)
		if err != nil {
			return Result{}, fmt.Errorf("question %d: %w", i, err)
		}
		res.Answers[i] = ans
	}
	return res, nil
}

func (a decisionsAnswer) answer(q Question) (Answer, error) {
	switch a.Type {
	case string(q.Kind):
	case refusalType:
		return Answer{}, errors.New("the source declined to answer")
	default:
		return Answer{}, fmt.Errorf("answered a %s question as %q", q.Kind, a.Type)
	}
	if q.Kind == Predicate {
		return predicateAnswer(a.Probability)
	}
	keyed := make(map[string]float64, len(a.Probabilities))
	for _, o := range a.Probabilities {
		key, err := outcomeKey(q.Kind, o.Value)
		if err != nil {
			return Answer{}, err
		}
		keyed[key] = o.Probability
	}
	keys := names(q.Options)
	if q.Kind == Score {
		keys = levelKeys(len(q.Options))
	}
	probs, err := aligned(keyed, keys)
	return Answer{Probabilities: probs, Confidence: confidenceOf(a.Confidence)}, err
}

// outcomeKey reads an outcome's value: a choice's string, or a score level's index.
func outcomeKey(k Kind, raw json.RawMessage) (string, error) {
	if k == Score {
		var level int
		if err := json.Unmarshal(raw, &level); err != nil {
			return "", fmt.Errorf("score level %s is not an integer", raw)
		}
		return strconv.Itoa(level), nil
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		return "", fmt.Errorf("choice value %s is not a string", raw)
	}
	return name, nil
}

// ParseDecisionsRequest reads a client's Decisions request. The model it names is
// not read: which source answers is the router's choice.
func ParseDecisionsRequest(body []byte) (Request, error) {
	var w decisionsRequest
	if err := json.Unmarshal(body, &w); err != nil {
		return Request{}, err
	}
	input, err := inputText(w.Input)
	if err != nil {
		return Request{}, err
	}
	r := Request{Input: input, Questions: make([]Question, len(w.Questions))}
	for i, q := range w.Questions {
		if r.Questions[i], err = q.question(); err != nil {
			return Request{}, fmt.Errorf("question %d: %w", i, err)
		}
	}
	return r, r.Validate()
}

func (w decisionsQuestion) question() (Question, error) {
	instructions, err := partsText(w.Instructions)
	if err != nil {
		return Question{}, err
	}
	q := Question{Kind: Kind(w.Type), Instructions: instructions}
	if w.Name != nil {
		q.Name = *w.Name
	}
	for _, c := range w.Choices {
		var name string
		if name, err = outcomeKey(Choice, c.Value); err != nil {
			return Question{}, err
		}
		q.Options = append(q.Options, Option{Name: name, Description: c.Description})
	}
	for _, l := range w.Levels {
		q.Options = append(q.Options, Option{Name: l.Label, Description: l.Description})
	}
	return q, nil
}

// inputText reads the input as text: a string, or messages whose parts are text or JSON.
func inputText(raw json.RawMessage) (string, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var items []struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return "", fmt.Errorf("input is neither text nor a list of messages: %w", err)
	}
	texts := make([]string, len(items))
	for i, it := range items {
		if it.Type != "" && it.Type != "message" {
			return "", fmt.Errorf("input item %d: %q is not routed yet", i, it.Type)
		}
		text, err := partsText(it.Content)
		if err != nil {
			return "", fmt.Errorf("input item %d: %w", i, err)
		}
		texts[i] = text
	}
	return joinNonEmpty(texts...), nil
}

// partsText reads content given as a string or as typed parts, keeping the text and JSON parts.
func partsText(raw json.RawMessage) (string, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var parts []struct {
		Type string          `json:"type"`
		Text string          `json:"text"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("content is neither text nor a list of parts: %w", err)
	}
	texts := make([]string, len(parts))
	for i, p := range parts {
		var err error
		switch p.Type {
		case "input_text", "output_text":
			texts[i] = p.Text
		case "input_json":
			texts[i], err = textOf(p.Data)
		case "input_image":
			err = errImageNotRouted
		default:
			err = fmt.Errorf("content part %q is not routed yet", p.Type)
		}
		if err != nil {
			return "", err
		}
	}
	return joinNonEmpty(texts...), nil
}

// MarshalDecisionsResponse answers a client in the Decisions shape, in its question order.
func MarshalDecisionsResponse(r Request, res Result) ([]byte, error) {
	w := decisionsResponse{
		Model:   res.Model,
		Answers: make([]decisionsAnswer, len(r.Questions)),
		Usage:   decisionsUsage{InputTokens: res.InputTokens, TotalTokens: res.InputTokens},
	}
	var err error
	for i, q := range r.Questions {
		if w.Answers[i], err = decisionsAnswerFor(q, res.Answers[i]); err != nil {
			return nil, err
		}
	}
	return json.Marshal(w)
}

func decisionsAnswerFor(q Question, a Answer) (decisionsAnswer, error) {
	out := decisionsAnswer{Type: string(q.Kind)}
	if q.Name != "" {
		out.Name = &q.Name
	}
	if q.Kind == Predicate {
		out.Probability = &a.Probabilities[0]
		return out, nil
	}
	out.Confidence = &a.Confidence
	out.Probabilities = make([]decisionsOutcome, len(q.Options))
	var err error
	for i, o := range q.Options {
		outcome := decisionsOutcome{Probability: a.Probabilities[i]}
		if q.Kind == Choice {
			outcome.Value, err = json.Marshal(o.Name)
		} else {
			outcome.Label = o.Name
			outcome.Value, err = json.Marshal(i)
		}
		if err != nil {
			return out, err
		}
		out.Probabilities[i] = outcome
	}
	if q.Kind == Choice {
		out.Choice = out.Probabilities[a.Top()].Value
		return out, nil
	}
	level := a.Level()
	out.Score = &level
	return out, nil
}
