// SPDX-License-Identifier: MIT

package executor

import (
	"encoding/json"
	"fmt"
	"strings"
)

// geminiContent is one turn. Gemini's roles are user and model, and a tool
// result is a user turn carrying a functionResponse part.
type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

// geminiPart is a union across part types, which is what the wire is.
type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	Name string `json:"name"`
	// An object, where a tool result is text, so text is wrapped once.
	Response json.RawMessage `json:"response"`
}

// geminiTool holds every declaration in one entry, which is the shape the API
// takes: a list of tools each holding a list of functions, not one tool each.
type geminiTool struct {
	FunctionDeclarations []geminiFunctionDecl `json:"functionDeclarations"`
}

type geminiFunctionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// geminiConversation maps the OpenAI-shaped history onto Gemini's contents.
//
// A functionResponse is matched by function **name**: Gemini's calls carry no
// id at all, so the id an OpenAI client sends back is one Hydra minted, and has
// to be resolved against the calls earlier in this same conversation. That
// resolution is also what lets a conversation survive a fallback to a different
// dialect mid-loop, since it re-derives from the history rather than trusting
// the id's shape.
func geminiConversation(msgs []Message) (system string, out []geminiContent, err error) {
	var systems []string
	names := map[string]string{} // tool call id -> function name

	for _, m := range msgs {
		switch strings.ToLower(m.Role) {
		case "system":
			if m.Content != "" {
				systems = append(systems, m.Content)
			}
		case "assistant":
			parts := make([]geminiPart, 0, 1+len(m.ToolCalls))
			if m.Content != "" {
				parts = append(parts, geminiPart{Text: m.Content})
			}
			for _, c := range m.ToolCalls {
				args, err := toolCallInput(c)
				if err != nil {
					return "", nil, err
				}
				names[c.ID] = c.Function.Name
				parts = append(parts, geminiPart{
					FunctionCall: &geminiFunctionCall{Name: c.Function.Name, Args: args},
				})
			}
			out = appendGemini(out, "model", parts...)
		case "tool":
			name := firstNonEmpty(names[m.ToolCallID], m.Name)
			if name == "" {
				return "", nil, fmt.Errorf(
					"gemini: tool result %q names no call in this conversation, and the dialect matches results by function name",
					m.ToolCallID)
			}
			out = appendGemini(out, "user", geminiPart{
				FunctionResponse: &geminiFunctionResponse{Name: name, Response: toolResultObject(m.Content)},
			})
		default:
			if m.Content == "" {
				continue
			}
			out = appendGemini(out, "user", geminiPart{Text: m.Content})
		}
	}
	return strings.Join(systems, "\n\n"), out, nil
}

func appendGemini(out []geminiContent, role string, parts ...geminiPart) []geminiContent {
	if len(parts) == 0 {
		return out
	}
	if n := len(out); n > 0 && out[n-1].Role == role {
		out[n-1].Parts = append(out[n-1].Parts, parts...)
		return out
	}
	return append(out, geminiContent{Role: role, Parts: parts})
}

// toolResultObject renders a tool result as the object functionResponse wants.
// A result that is already a JSON object is sent as itself; anything else is
// wrapped once rather than re-encoded into something the model has to unpick.
func toolResultObject(content string) json.RawMessage {
	trimmed := strings.TrimSpace(content)
	if trimmed != "" {
		var probe map[string]any
		if err := json.Unmarshal([]byte(trimmed), &probe); err == nil {
			return json.RawMessage(trimmed)
		}
	}
	wrapped, err := json.Marshal(map[string]string{"result": content})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return wrapped
}

func geminiTools(defs []ToolDef) []geminiTool {
	decls := make([]geminiFunctionDecl, 0, len(defs))
	for _, d := range defs {
		decls = append(decls, geminiFunctionDecl{
			Name:        d.Function.Name,
			Description: d.Function.Description,
			Parameters:  d.Function.Parameters,
		})
	}
	return []geminiTool{{FunctionDeclarations: decls}}
}

// geminiToolConfig translates OpenAI's tool_choice onto functionCallingConfig.
// Unlike Anthropic, Gemini has a NONE mode of its own, so "none" is said rather
// than expressed by withholding the tools.
//
// An unrecognised value is an error rather than a silent AUTO: dropping a
// constraint the caller asked for yields an answer nobody asked for.
func geminiToolConfig(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	cfg := func(mode string, allowed ...string) map[string]any {
		inner := map[string]any{"mode": mode}
		if len(allowed) > 0 {
			inner["allowedFunctionNames"] = allowed
		}
		return map[string]any{"functionCallingConfig": inner}
	}

	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		switch strings.ToLower(name) {
		case "auto":
			return cfg("AUTO"), nil
		case "required":
			return cfg("ANY"), nil
		case "none":
			return cfg("NONE"), nil
		}
		return nil, fmt.Errorf("gemini: tool_choice %q has no equivalent", name)
	}

	var fn struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &fn); err != nil {
		return nil, fmt.Errorf("gemini: tool_choice is neither a name nor a function: %w", err)
	}
	if fn.Type == "function" && fn.Function.Name != "" {
		return cfg("ANY", fn.Function.Name), nil
	}
	return nil, fmt.Errorf("gemini: tool_choice %s has no equivalent", raw)
}

// geminiCallID mints the id an OpenAI client needs to send a result back.
// Gemini issues none, so this is Hydra's, derived from the call's name and its
// position in the answer so that one answer reads the same twice.
func geminiCallID(name string, nth int) string {
	return fmt.Sprintf("call_%s_%d", name, nth)
}

// geminiFinish translates a finishReason.
//
// Gemini reports STOP whether or not it called a function, so a tool call is
// the only evidence that the turn ended for one. Every other dialect says so
// itself, and a client branches on this rather than on the content.
func geminiFinish(reason string, calls int) string {
	if calls > 0 {
		return "tool_calls"
	}
	switch reason {
	case "":
		return ""
	case "MAX_TOKENS":
		return "length"
	default:
		return "stop"
	}
}

// geminiParts splits an answer's parts into the text and the calls it asked
// for. A call arrives whole, unlike Anthropic's, so there is nothing to
// reassemble: what there is to do is mint the id the dialect never gave it.
func geminiParts(parts []geminiPart) (string, []ToolCall) {
	var text []textBlock
	var calls []ToolCall
	for _, p := range parts {
		if p.FunctionCall == nil {
			text = append(text, textBlock{Text: p.Text})
			continue
		}
		name := p.FunctionCall.Name
		calls = append(calls, ToolCall{
			ID: geminiCallID(name, len(calls)), Type: "function", Index: len(calls),
			Function: ToolCallFunction{Name: name, Arguments: toolArguments(p.FunctionCall.Args)},
		})
	}
	return joinTextBlocks(text), calls
}
