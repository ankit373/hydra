// SPDX-License-Identifier: MIT

package executor

import (
	"encoding/json"

	"github.com/ankit373/hydra/internal/provider"
)

// Message is one turn of a conversation.
//
// Request.Prompt and Request.System describe a single turn and cannot carry a
// tool result, which is exactly what an agent loop sends back on its next call.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// ToolDef is a function a head may ask to call.
type ToolDef struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction keeps Parameters raw: it is a JSON Schema the caller authored,
// and re-encoding it through a Go shape would quietly drop what Hydra has no
// type for.
type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ToolCall is a head asking for one call. Arguments is a JSON string rather
// than an object, which is what the wire format specifies.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Index    int              `json:"index,omitempty"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// CanUseTools reports whether a head can be sent function definitions and
// answer with structured calls.
//
// Only the OpenAI-compatible path carries them. Anthropic, Gemini, Cohere and
// Bedrock each shape tools differently, and Replicate polls rather than chats.
// The predicate exists so a dispatch can skip a head that cannot, because a
// silently dropped tool array is not a degraded answer: the caller's agent loop
// never terminates, and every round looks like the model simply declining.
func CanUseTools(h provider.Head) bool {
	if _, ok := For(h).(*HTTPExecutor); !ok {
		return false
	}
	switch h.Provider {
	case "anthropic", "google", "cohere", "bedrock", "replicate":
		return false
	}
	_, err := openAICompatConfigFor(h)
	return err == nil
}

// toolCallStream reassembles tool calls that arrive in fragments, which is how
// every streaming dialect sends them: the name comes once, the arguments are a
// JSON string split over as many chunks as the model took to write it.
type toolCallStream struct {
	calls []ToolCall
}

// add folds one fragment in, keyed by the index the wire uses to interleave
// parallel calls.
//
// A fragment naming a different id at the same index starts a new call instead
// of appending: a server that sends each call whole has no reason to send an
// index at all, so every one of its calls arrives as index 0 and they would
// otherwise concatenate into one call with unparseable arguments.
func (t *toolCallStream) add(frag ToolCall) {
	at := t.find(frag)
	if at < 0 {
		t.calls = append(t.calls, frag)
		return
	}
	c := &t.calls[at]
	if frag.ID != "" {
		c.ID = frag.ID
	}
	if frag.Type != "" {
		c.Type = frag.Type
	}
	if frag.Function.Name != "" {
		c.Function.Name = frag.Function.Name
	}
	c.Function.Arguments += frag.Function.Arguments
}

// find returns the call frag continues, searching backwards so the newest call
// at an index wins once that index has been reused.
func (t *toolCallStream) find(frag ToolCall) int {
	for i := len(t.calls) - 1; i >= 0; i-- {
		if t.calls[i].Index != frag.Index {
			continue
		}
		if frag.ID != "" && t.calls[i].ID != "" && frag.ID != t.calls[i].ID {
			return -1
		}
		return i
	}
	return -1
}

// done returns the calls in the order their first fragment arrived. A slice
// rather than a map keyed by index, so two calls cannot order themselves
// differently on two runs of the same stream.
func (t *toolCallStream) done() []ToolCall {
	return t.calls
}
