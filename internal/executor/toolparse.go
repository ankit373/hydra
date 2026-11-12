// SPDX-License-Identifier: MIT

package executor

import (
	"encoding/json"
	"strings"
)

// RecoverToolCalls reads a tool call a model wrote as ordinary text because its
// server did not structure one. Measured on this machine: Ollama reports
// `capabilities: [tools]` for every local model, and qwen3:0.6b does return
// structured calls, while Qwen2.5-Coder:7b returns
// `{"name": "get_weather", "arguments": {"city": "Paris"}}` as the message
// content with finish_reason "stop" every time. An OpenAI client then sees a
// JSON blob where a tool call should be and the agent loop stops.
//
// Deliberately narrow, because the alternative is inventing a reading:
//
//   - the whole content, trimmed, must be the call. Prose anywhere means no.
//   - the name must be one the caller actually offered. A model naming a tool
//     nobody has is confabulating, not calling (vLLM #58147 is that bug).
//   - every element must parse, or none are taken. No partial recovery.
//
// What it cannot do is tell a call from a model quoting one. Asked to "show me
// an example of the JSON for calling get_weather, do not actually call it",
// Qwen2.5-Coder answers with exactly the bytes it sends to make the call. The
// ambiguity is in the model's output, not in this function, and no reader
// human or otherwise could resolve it. Measured and recorded rather than
// papered over; a head that structures its calls has no such ambiguity, which
// is the real fix and is a routing decision.
func RecoverToolCalls(content string, offered []ToolDef) ([]ToolCall, bool) {
	s := strings.TrimSpace(content)
	if s == "" || len(offered) == 0 {
		return nil, false
	}
	if s[0] != '{' && s[0] != '[' {
		return nil, false
	}

	var raw []map[string]json.RawMessage
	if s[0] == '[' {
		if err := json.Unmarshal([]byte(s), &raw); err != nil {
			return nil, false
		}
	} else {
		var one map[string]json.RawMessage
		if err := json.Unmarshal([]byte(s), &one); err != nil {
			return nil, false
		}
		raw = []map[string]json.RawMessage{one}
	}
	if len(raw) == 0 {
		return nil, false
	}

	names := make(map[string]bool, len(offered))
	for _, t := range offered {
		names[t.Function.Name] = true
	}

	out := make([]ToolCall, 0, len(raw))
	for i, obj := range raw {
		name, ok := stringField(obj, "name")
		if !ok || !names[name] {
			return nil, false
		}
		args, ok := argumentsField(obj)
		if !ok {
			return nil, false
		}
		out = append(out, ToolCall{
			ID:       toolCallID(name, i),
			Type:     "function",
			Index:    i,
			Function: ToolCallFunction{Name: name, Arguments: args},
		})
	}
	return out, true
}

func stringField(obj map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := obj[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, s != ""
}

// argumentsField accepts both spellings models use, and both encodings: an
// object, or the JSON string of one, which is what the wire format itself
// uses. Anything else is not an argument list and refuses the whole parse.
func argumentsField(obj map[string]json.RawMessage) (string, bool) {
	raw, ok := obj["arguments"]
	if !ok {
		if raw, ok = obj["parameters"]; !ok {
			return "", false
		}
	}
	var asObject map[string]any
	if err := json.Unmarshal(raw, &asObject); err == nil {
		return string(raw), true
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err != nil {
		return "", false
	}
	if err := json.Unmarshal([]byte(asString), &asObject); err != nil {
		return "", false
	}
	return asString, true
}
