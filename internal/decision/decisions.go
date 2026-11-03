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
	Type          string             `json:"type"`
	Probability   *float64           `json:"probability,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities []decisionsOutcome `json:"probabilities,omitempty"`
}

type decisionsOutcome struct {
	Value       json.RawMessage `json:"value"`
	Probability float64         `json:"probability"`
}

type decisionsUsage struct {
	InputTokens int `json:"input_tokens"`
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
	keyed := make([]keyedProb, len(a.Probabilities))
	for i, o := range a.Probabilities {
		key, err := outcomeKey(q.Kind, o.Value)
		if err != nil {
			return Answer{}, err
		}
		keyed[i] = keyedProb{key: key, p: o.Probability}
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
