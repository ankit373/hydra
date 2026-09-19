// SPDX-License-Identifier: MIT

package executor

import (
	"encoding/json"
	"fmt"
	"strings"
)

// geminiPart is one part of a turn. The fields are a union across part kinds,
// which is what the wire is.
type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

// geminiFunctionCall carries Args as an object, where OpenAI's Arguments is a
// JSON string. The two are converted, never passed through.
type geminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// geminiFunctionResponse identifies the call it answers by **name**. Gemini has
// no tool-call id at all, which is the one place this mapping is lossy: two
// concurrent calls to the same function cannot be told apart on the way back.
type geminiFunctionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiFunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// geminiConversation maps the OpenAI-shaped history onto Gemini's.
//
// System turns come out separately, because Gemini takes them as
// system_instruction rather than as a turn. The assistant role is called
// "model" here, and a tool result is a *user* turn, so consecutive turns of the
// same role are merged: two results in a row would otherwise be two user turns
// with a model turn missing between them.
func geminiConversation(msgs []Message) (system string, out []geminiContent, err error) {
	var systems []string
	for _, m := range msgs {
		switch strings.ToLower(m.Role) {
		case "system":
			if m.Content != "" {
				systems = append(systems, m.Content)
			}
		case "tool":
			name, ok := geminiCallName(msgs, m.ToolCallID)
			if !ok {
				return "", nil, fmt.Errorf("gemini: tool result %q answers a call not in this conversation, "+
					"and the dialect identifies a result by function name rather than by id", m.ToolCallID)
			}
			out = appendGemini(out, "user", geminiPart{FunctionResponse: &geminiFunctionResponse{
				Name: name, Response: geminiResponseObject(m.Content),
			}})
		case "assistant":
			parts, err := geminiModelParts(m)
			if err != nil {
				return "", nil, err
			}
			out = appendGemini(out, "model", parts...)
		default:
			if m.Content == "" {
				continue
			}
			out = appendGemini(out, "user", geminiPart{Text: m.Content})
		}
	}
	return strings.Join(systems, "\n\n"), out, nil
}

func geminiModelParts(m Message) ([]geminiPart, error) {
	parts := make([]geminiPart, 0, 1+len(m.ToolCalls))
	if m.Content != "" {
		parts = append(parts, geminiPart{Text: m.Content})
	}
	for _, c := range m.ToolCalls {
		args, err := toolArgsObject("gemini", c)
		if err != nil {
			return nil, err
		}
		parts = append(parts, geminiPart{FunctionCall: &geminiFunctionCall{
			Name: c.Function.Name, Args: args,
		}})
	}
	return parts, nil
}

// geminiCallName finds the name of the call an id refers to, searching
// backwards so the most recent call wins once an id has been reused.
func geminiCallName(msgs []Message, id string) (string, bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		for _, c := range msgs[i].ToolCalls {
			if c.ID == id {
				return c.Function.Name, true
			}
		}
	}
	return "", false
}

// geminiResponseObject wraps a tool's reply, which OpenAI sends as an arbitrary
// string, in the object Gemini requires.
//
// A reply that is already a JSON object is passed through, so a tool returning
// structured data reaches the model as that structure rather than as a string
// of it. Anything else is carried under "result", the key Google's own examples
// use, because the alternative is refusing a tool whose reply is plain text.
func geminiResponseObject(content string) json.RawMessage {
	trimmed := strings.TrimSpace(content)
	if trimmed != "" {
		var probe map[string]any
		if json.Unmarshal([]byte(trimmed), &probe) == nil {
			return json.RawMessage(trimmed)
		}
	}
	wrapped, err := json.Marshal(map[string]string{"result": content})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return wrapped
}

func appendGemini(contents []geminiContent, role string, parts ...geminiPart) []geminiContent {
	if len(parts) == 0 {
		return contents
	}
	if n := len(contents); n > 0 && contents[n-1].Role == role {
		contents[n-1].Parts = append(contents[n-1].Parts, parts...)
		return contents
	}
	return append(contents, geminiContent{Role: role, Parts: parts})
}

// geminiToolDeclarations wraps the declarations in the single object the field
// takes. Gemini's `tools` is a list of *groupings*, not a list of functions,
// and a bare list is rejected.
func geminiToolDeclarations(defs []ToolDef) []map[string]any {
	if len(defs) == 0 {
		return nil
	}
	decls := make([]geminiFunctionDeclaration, 0, len(defs))
	for _, d := range defs {
		decls = append(decls, geminiFunctionDeclaration{
			Name:        d.Function.Name,
			Description: d.Function.Description,
			Parameters:  d.Function.Parameters,
		})
	}
	return []map[string]any{{"functionDeclarations": decls}}
}

// geminiToolCalls reads the calls out of a candidate's parts.
//
// The id is synthesised, since Gemini sends none: it is what the client will
// echo back as tool_call_id, and geminiCallName resolves it against this same
// conversation. An empty id would leave the client nothing to key its result on.
func geminiToolCalls(parts []geminiPart) []ToolCall {
	var calls []ToolCall
	for _, p := range parts {
		if p.FunctionCall == nil {
			continue
		}
		args := "{}"
		if len(p.FunctionCall.Args) > 0 {
			args = string(p.FunctionCall.Args)
		}
		calls = append(calls, ToolCall{
			ID:    fmt.Sprintf("call_%d_%s", len(calls), p.FunctionCall.Name),
			Type:  "function",
			Index: len(calls),
			Function: ToolCallFunction{
				Name:      p.FunctionCall.Name,
				Arguments: args,
			},
		})
	}
	return calls
}

// geminiAnswerText joins the text parts, leaving the calls to geminiToolCalls.
func geminiAnswerText(parts []geminiPart) string {
	texts := make([]textBlock, 0, len(parts))
	for _, p := range parts {
		if p.Text != "" {
			texts = append(texts, textBlock{Text: p.Text})
		}
	}
	return joinTextBlocks(texts)
}
