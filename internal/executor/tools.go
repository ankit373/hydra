// SPDX-License-Identifier: MIT

package executor

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
// Every chat dialect carries them now. Replicate is the exception: its
// prediction API polls rather than chats, and has no tool surface at all.
// The predicate exists so a dispatch can skip a head that cannot, because a
// silently dropped tool array is not a degraded answer: the caller's agent loop
// never terminates, and every round looks like the model simply declining.
func CanUseTools(h provider.Head) bool {
	if _, ok := For(h).(*HTTPExecutor); !ok {
		return false
	}
	switch h.Provider {
	case "anthropic", "google", "bedrock", "cohere":
		return true
	case "replicate":
		return false
	}
	_, err := openAICompatConfigFor(h)
	return err == nil
}

// ErrUnaskable marks a request no head can be asked, as opposed to a head that
// failed. The fallback chain exists for the second, so advancing it on the
// first only spends money to be told the same thing again, and marking the head
// failed parks a healthy one over the caller's mistake.
//
// Deliberately narrow. It belongs only on a refusal that holds for **every**
// dialect, which is why unparseable tool arguments carry it: the mappers refuse
// them, and on the OpenAI-compatible path the server answers 400 (measured
// against Ollama). A dialect-specific refusal must not carry it, because
// another head can express what this one cannot: Gemini rejecting a tool result
// whose id names no call is exactly that, since every other dialect carries ids.
var ErrUnaskable = errors.New("no head can be asked this request")

// CheckAskable reports whether a conversation can be expressed to any head at
// all, so a request that cannot is refused before one runs rather than after
// every one of them has been paid to say so.
//
// Checked here rather than per dialect because the OpenAI-compatible path does
// not convert the arguments at all: it forwards the string and the server
// answers 400, so the mappers' own refusals never see the commonest case. A
// property of the request belongs on the request.
func CheckAskable(msgs []Message) error {
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			if _, err := toolCallInput(c); err != nil {
				return err
			}
		}
	}
	return nil
}

// toolCallInput turns OpenAI's arguments, a JSON string, into Anthropic's input, an
// object.
//
// Anthropic calls it input and Gemini calls it args; both want an object where
// OpenAI sends a string, so one derivation serves both.
//
// Arguments that do not parse are refused rather than replaced with an empty
// object: sending `{}` would be a call the model reads as "no arguments", which
// is a wrong answer, where the refusal names the call that cannot be expressed.
func toolCallInput(c ToolCall) (json.RawMessage, error) {
	args := strings.TrimSpace(c.Function.Arguments)
	if args == "" {
		return json.RawMessage(`{}`), nil
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(args), &probe); err != nil {
		return nil, fmt.Errorf("%w: tool call %s (%s) has arguments that are not a JSON object: %w",
			ErrUnaskable, c.ID, c.Function.Name, err)
	}
	return json.RawMessage(args), nil
}

// toolArguments renders a dialect's argument object as the JSON string
// ToolCall carries, so one shape reaches every caller whatever the head spoke.
// An absent input is "{}" rather than empty: a client parses this.
func toolArguments(input json.RawMessage) string {
	if len(input) == 0 {
		return "{}"
	}
	return string(input)
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
	c.Function.Name = foldName(c.Function.Name, frag.Function.Name)
	c.Function.Arguments += frag.Function.Arguments
}

// foldName continues a name across fragments, the way the arguments beside it
// are continued, but keeps a repeat rather than doubling it.
//
// Assigning instead would only ever protect a server that restates the whole
// name on every fragment, and such a server restates the arguments too, which
// concatenate into something no client can parse. So it was not buying that
// case and was losing the split one outright: "get_wea" then "ther" arrived as
// "ther", and an agent asked for a tool its client does not have.
//
// A server that sends the name once, which is what OpenAI does, carries no name
// on its later fragments and is untouched either way.
func foldName(have, frag string) string {
	if frag == "" || frag == have {
		return have
	}
	return have + frag
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

// numbered gives calls their position, the way an assembled stream's are
// numbered, so a dialect that reports its calls whole agrees with one that
// reports them in fragments.
func numbered(calls []ToolCall) []ToolCall {
	for i := range calls {
		calls[i].Index = i
	}
	return calls
}

// done returns the calls in the order their first fragment arrived. A slice
// rather than a map keyed by index, so two calls cannot order themselves
// differently on two runs of the same stream.
func (t *toolCallStream) done() []ToolCall {
	// Renumbered by position. The wire index says how fragments were
	// interleaved and the dialects count different things (OpenAI counts
	// calls, Anthropic counts content blocks, so its first call is often 1),
	// which would make one answer read differently depending on who spoke it.
	for i := range t.calls {
		t.calls[i].Index = i
	}
	return t.calls
}
