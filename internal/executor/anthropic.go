// SPDX-License-Identifier: MIT

package executor

import (
	"encoding/json"
	"fmt"
	"strings"
)

// anthropicMessage is one turn in Anthropic's shape. Content is always blocks,
// never the bare string the API also accepts, so one encoding covers a turn
// carrying text, a tool call and a tool result alike.
type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

// anthropicBlock is one content block. The fields are a union across block
// types, which is what the wire is.
type anthropicBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

// anthropicTool is a function definition. input_schema is required, so a tool
// declared with no parameters still needs an empty object schema.
type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

var emptySchema = json.RawMessage(`{"type":"object","properties":{}}`)

// anthropicConversation maps the OpenAI-shaped history onto Anthropic's.
//
// System turns come out separately because Anthropic takes them as a top-level
// field rather than a message, and consecutive same-role turns are merged
// because Anthropic requires roles to alternate: a tool result is a *user*
// turn there, so two results in a row would otherwise be two user messages and
// the request would be refused outright.
func anthropicConversation(msgs []Message) (system string, out []anthropicMessage, err error) {
	var systems []string
	for _, m := range msgs {
		switch strings.ToLower(m.Role) {
		case "system":
			if m.Content != "" {
				systems = append(systems, m.Content)
			}
		case "tool":
			out = appendAnthropic(out, "user", anthropicBlock{
				Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content,
			})
		case "assistant":
			blocks, err := assistantBlocks(m)
			if err != nil {
				return "", nil, err
			}
			out = appendAnthropic(out, "assistant", blocks...)
		default:
			if m.Content == "" {
				continue
			}
			out = appendAnthropic(out, "user", anthropicBlock{Type: "text", Text: m.Content})
		}
	}
	return strings.Join(systems, "\n\n"), out, nil
}

func assistantBlocks(m Message) ([]anthropicBlock, error) {
	blocks := make([]anthropicBlock, 0, 1+len(m.ToolCalls))
	if m.Content != "" {
		blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
	}
	for _, c := range m.ToolCalls {
		input, err := toolInput(c)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, anthropicBlock{
			Type: "tool_use", ID: c.ID, Name: c.Function.Name, Input: input,
		})
	}
	return blocks, nil
}

// toolInput turns OpenAI's arguments, a JSON string, into Anthropic's input, an
// object.
//
// Arguments that do not parse are refused rather than replaced with an empty
// object: sending `{}` would be a call the model reads as "no arguments", which
// is a wrong answer, where the refusal names the call that cannot be expressed.
func toolInput(c ToolCall) (json.RawMessage, error) {
	args := strings.TrimSpace(c.Function.Arguments)
	if args == "" {
		return json.RawMessage(`{}`), nil
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(args), &probe); err != nil {
		return nil, fmt.Errorf("anthropic: tool call %s (%s) has arguments that are not a JSON object: %w",
			c.ID, c.Function.Name, err)
	}
	return json.RawMessage(args), nil
}

// appendAnthropic adds blocks to the last message when it has the same role,
// which is what keeps the roles alternating.
func appendAnthropic(msgs []anthropicMessage, role string, blocks ...anthropicBlock) []anthropicMessage {
	if len(blocks) == 0 {
		return msgs
	}
	if n := len(msgs); n > 0 && msgs[n-1].Role == role {
		msgs[n-1].Content = append(msgs[n-1].Content, blocks...)
		return msgs
	}
	return append(msgs, anthropicMessage{Role: role, Content: blocks})
}

func anthropicTools(defs []ToolDef) []anthropicTool {
	out := make([]anthropicTool, 0, len(defs))
	for _, d := range defs {
		schema := d.Function.Parameters
		if len(schema) == 0 {
			schema = emptySchema
		}
		out = append(out, anthropicTool{
			Name: d.Function.Name, Description: d.Function.Description, InputSchema: schema,
		})
	}
	return out
}

// anthropicToolChoice translates OpenAI's tool_choice. The second return says
// whether tools should be sent at all: "none" is expressed by withholding them,
// which every version of the API honours.
//
// An unrecognised value is an error rather than a silent "auto". tool_choice is
// a constraint the caller asked for, and dropping a constraint yields an answer
// nobody asked for, which is exactly the class of failure a tool-using client
// cannot detect.
func anthropicToolChoice(raw json.RawMessage) (choice map[string]any, sendTools bool, err error) {
	if len(raw) == 0 {
		return nil, true, nil
	}
	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		switch strings.ToLower(name) {
		case "auto":
			return map[string]any{"type": "auto"}, true, nil
		case "required":
			return map[string]any{"type": "any"}, true, nil
		case "none":
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("anthropic: tool_choice %q has no equivalent", name)
	}

	var fn struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &fn); err != nil {
		return nil, false, fmt.Errorf("anthropic: tool_choice is neither a name nor a function: %w", err)
	}
	if fn.Type == "function" && fn.Function.Name != "" {
		return map[string]any{"type": "tool", "name": fn.Function.Name}, true, nil
	}
	return nil, false, fmt.Errorf("anthropic: tool_choice %s has no equivalent", raw)
}

// anthropicFinish translates a stop_reason into the vocabulary
// Response.FinishReason carries, which is OpenAI's, because that is what an
// OpenAI-shaped client branches on. Translated once here rather than at every
// reader.
func anthropicFinish(stop string) string {
	switch stop {
	case "":
		return ""
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	default:
		// end_turn, stop_sequence, and anything added later: the model stopped.
		return "stop"
	}
}
