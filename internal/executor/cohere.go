// SPDX-License-Identifier: MIT

package executor

import (
	"encoding/json"
	"fmt"
	"strings"
)

// cohereToolCalls decodes the calls on a streamed delta, accepting either one
// call as an object or several as an array.
//
// Cohere documents its streaming tool events as Python object reprs rather than
// raw JSON, so which of the two the wire carries is not stated anywhere
// readable. Accepting both is the honest answer to that, and costs a few lines;
// guessing one would mean a stub that agrees with the guess and a head that
// silently never calls a tool.
type cohereToolCalls []ToolCall

func (c *cohereToolCalls) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var many []ToolCall
		if err := json.Unmarshal(b, &many); err != nil {
			return err
		}
		*c = many
		return nil
	}
	var one ToolCall
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	*c = []ToolCall{one}
	return nil
}

// cohereToolChoice translates OpenAI's tool_choice. Cohere accepts REQUIRED and
// NONE only: omitting it is how the model is left to decide, and there is no
// way to force a named tool, so that request is refused rather than downgraded
// into "any tool", which would call something the caller did not ask for.
func cohereToolChoice(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		switch strings.ToLower(name) {
		case "auto":
			return "", nil
		case "required":
			return "REQUIRED", nil
		case "none":
			return "NONE", nil
		}
		return "", fmt.Errorf("cohere: tool_choice %q has no equivalent", name)
	}
	var fn struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &fn); err == nil && fn.Type == "function" {
		return "", fmt.Errorf(
			"cohere: tool_choice cannot name a tool (%s); it accepts REQUIRED or NONE only",
			fn.Function.Name)
	}
	return "", fmt.Errorf("cohere: tool_choice %s has no equivalent", raw)
}

// cohereFinish translates a finish_reason into the vocabulary
// Response.FinishReason carries, which is OpenAI's.
//
// ERROR and TIMEOUT are not answers. They arrive on a call that otherwise looks
// complete, and reporting either as "stop" hands the caller a truncated answer
// as a whole one, which is how every other dialect's mid-stream error used to
// read before it was given its own branch.
func cohereFinish(reason string) (string, error) {
	switch strings.ToUpper(reason) {
	case "":
		return "", nil
	case "TOOL_CALL":
		return "tool_calls", nil
	case "MAX_TOKENS":
		return "length", nil
	case "ERROR", "TIMEOUT":
		return "", fmt.Errorf("cohere: the model stopped with %s, so what arrived is not a whole answer",
			strings.ToUpper(reason))
	default:
		// COMPLETE, STOP_SEQUENCE, and anything added later.
		return "stop", nil
	}
}
