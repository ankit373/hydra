// SPDX-License-Identifier: MIT

package executor

import (
	"encoding/json"
	"fmt"
	"strings"
)

// bedrockMessage is one Converse turn. Roles are user and assistant, and a tool
// result is a user turn carrying a toolResult block.
type bedrockMessage struct {
	Role    string         `json:"role"`
	Content []bedrockBlock `json:"content"`
}

// bedrockBlock is a union across content block types, which is what the wire is.
type bedrockBlock struct {
	Text       string             `json:"text,omitempty"`
	ToolUse    *bedrockToolUse    `json:"toolUse,omitempty"`
	ToolResult *bedrockToolResult `json:"toolResult,omitempty"`
}

type bedrockToolUse struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

// bedrockToolResult carries its result as content blocks rather than a string,
// so text is wrapped in one.
type bedrockToolResult struct {
	ToolUseID string         `json:"toolUseId"`
	Content   []bedrockBlock `json:"content"`
	Status    string         `json:"status,omitempty"`
}

type bedrockTool struct {
	ToolSpec bedrockToolSpec `json:"toolSpec"`
}

// bedrockToolSpec's inputSchema is a union whose only member here is json, so
// the schema nests one level deeper than every other dialect's.
type bedrockToolSpec struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	InputSchema bedrockInputSchema `json:"inputSchema"`
}

type bedrockInputSchema struct {
	JSON json.RawMessage `json:"json"`
}

// bedrockConversation maps the OpenAI-shaped history onto Converse's messages.
// System turns come out separately: Converse takes them as a top-level array.
func bedrockConversation(msgs []Message) (system []string, out []bedrockMessage, err error) {
	for _, m := range msgs {
		switch strings.ToLower(m.Role) {
		case "system":
			if m.Content != "" {
				system = append(system, m.Content)
			}
		case "assistant":
			blocks := make([]bedrockBlock, 0, 1+len(m.ToolCalls))
			if m.Content != "" {
				blocks = append(blocks, bedrockBlock{Text: m.Content})
			}
			for _, c := range m.ToolCalls {
				input, err := toolCallInput(c)
				if err != nil {
					return nil, nil, err
				}
				blocks = append(blocks, bedrockBlock{ToolUse: &bedrockToolUse{
					ToolUseID: c.ID, Name: c.Function.Name, Input: input,
				}})
			}
			out = appendBedrock(out, "assistant", blocks...)
		case "tool":
			out = appendBedrock(out, "user", bedrockBlock{ToolResult: &bedrockToolResult{
				ToolUseID: m.ToolCallID,
				Content:   []bedrockBlock{{Text: m.Content}},
				Status:    "success",
			}})
		default:
			if m.Content == "" {
				continue
			}
			out = appendBedrock(out, "user", bedrockBlock{Text: m.Content})
		}
	}
	return system, out, nil
}

// appendBedrock merges into the previous turn when the role is the same, so two
// tool results in a row are one user turn carrying both.
func appendBedrock(msgs []bedrockMessage, role string, blocks ...bedrockBlock) []bedrockMessage {
	if len(blocks) == 0 {
		return msgs
	}
	if n := len(msgs); n > 0 && msgs[n-1].Role == role {
		msgs[n-1].Content = append(msgs[n-1].Content, blocks...)
		return msgs
	}
	return append(msgs, bedrockMessage{Role: role, Content: blocks})
}

func bedrockTools(defs []ToolDef) []bedrockTool {
	out := make([]bedrockTool, 0, len(defs))
	for _, d := range defs {
		schema := d.Function.Parameters
		if len(schema) == 0 {
			schema = emptySchema
		}
		out = append(out, bedrockTool{ToolSpec: bedrockToolSpec{
			Name:        d.Function.Name,
			Description: d.Function.Description,
			InputSchema: bedrockInputSchema{JSON: schema},
		}})
	}
	return out
}

// bedrockToolChoice translates OpenAI's tool_choice onto Converse's union. The
// second return says whether tools should be sent at all: the union has no none
// member, so as on Anthropic that is expressed by withholding them.
//
// An unrecognised value is an error rather than a silent auto, because a
// dropped constraint yields an answer nobody asked for.
func bedrockToolChoice(raw json.RawMessage) (choice map[string]any, sendTools bool, err error) {
	if len(raw) == 0 {
		return nil, true, nil
	}
	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		switch strings.ToLower(name) {
		case "auto":
			return map[string]any{"auto": map[string]any{}}, true, nil
		case "required":
			return map[string]any{"any": map[string]any{}}, true, nil
		case "none":
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("bedrock: tool_choice %q has no equivalent", name)
	}

	var fn struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &fn); err != nil {
		return nil, false, fmt.Errorf("bedrock: tool_choice is neither a name nor a function: %w", err)
	}
	if fn.Type == "function" && fn.Function.Name != "" {
		return map[string]any{"tool": map[string]any{"name": fn.Function.Name}}, true, nil
	}
	return nil, false, fmt.Errorf("bedrock: tool_choice %s has no equivalent", raw)
}

// bedrockBlocks splits an answer's content into its text and its calls.
func bedrockBlocks(blocks []bedrockBlock) (string, []ToolCall) {
	var text []textBlock
	var calls []ToolCall
	for _, b := range blocks {
		if b.ToolUse == nil {
			text = append(text, textBlock{Text: b.Text})
			continue
		}
		calls = append(calls, ToolCall{
			ID: b.ToolUse.ToolUseID, Type: "function", Index: len(calls),
			Function: ToolCallFunction{
				Name: b.ToolUse.Name, Arguments: toolArguments(b.ToolUse.Input),
			},
		})
	}
	return joinTextBlocks(text), calls
}

// bedrockFinish translates a stopReason into the vocabulary
// Response.FinishReason carries, which is OpenAI's.
func bedrockFinish(stop string) string {
	switch stop {
	case "":
		return ""
	case "tool_use":
		return "tool_calls"
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	default:
		// end_turn, stop_sequence, the guardrail and malformed-output reasons:
		// the model stopped. A client branches on tool_calls, not on these.
		return "stop"
	}
}
