// SPDX-License-Identifier: MIT

package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// cohereEvent is one SSE payload of the v2 chat stream. Answer text sits three
// levels down on content-delta, and a reader that stops one level short streams
// empty strings from a response that otherwise looks fine (#860).
type cohereEvent struct {
	Type string `json:"type"`
	// The call's position, carried on the event rather than inside the delta.
	Index *int `json:"index"`
	Delta *struct {
		// Reported on message-end. ERROR and TIMEOUT arrive here on a call that
		// otherwise looks complete.
		FinishReason string `json:"finish_reason"`
		Message      *struct {
			Content *struct {
				Text string `json:"text"`
			} `json:"content"`
			// The model's plan for what it will call, which is not the answer.
			ToolPlan  string          `json:"tool_plan"`
			ToolCalls cohereToolCalls `json:"tool_calls"`
		} `json:"message"`
		// Reported once, on message-end. billed_units sits beside this and is
		// a different number; the buffered path reports tokens, so this does
		// too, or one call reports two answers depending on who watched it.
		Usage *struct {
			Tokens struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"tokens"`
		} `json:"usage"`
	} `json:"delta"`
}

func (e *HTTPExecutor) streamCohere(ctx context.Context, req Request, onDelta OnDelta) (*Response, error) {
	model := defaultModelFor("cohere")
	body, err := cohereBody(req, model, true)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cohereChatURL(), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	setCohereHeaders(httpReq)
	httpReq.Header.Set("Accept", "text/event-stream")

	var inTok, outTok int
	var finish string
	var tools toolCallStream

	sink, start, err := e.sseStream(ctx, req.Head.ID, httpReq, onDelta, func(payload string, sink *deltaSink) error {
		var ev cohereEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			return fmt.Errorf("decode event: %w", err)
		}
		if ev.Delta == nil {
			return nil
		}
		switch ev.Type {
		case "content-delta":
			if m := ev.Delta.Message; m != nil && m.Content != nil {
				sink.write(m.Content.Text)
			}
		case "tool-call-start", "tool-call-delta":
			// The start names the call and the deltas carry its arguments as
			// partial JSON, both under the same index.
			m := ev.Delta.Message
			if m == nil {
				return nil
			}
			for i, c := range m.ToolCalls {
				c.Index = cohereIndex(ev.Index, len(tools.calls)+i)
				if c.Type == "" && c.Function.Name != "" {
					c.Type = "function"
				}
				tools.add(c)
			}
			if len(m.ToolCalls) > 0 {
				sink.noteStructured()
			}
		case "message-end":
			if u := ev.Delta.Usage; u != nil {
				inTok, outTok = u.Tokens.InputTokens, u.Tokens.OutputTokens
			}
			done, err := cohereFinish(ev.Delta.FinishReason)
			if err != nil {
				return err
			}
			finish = done
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	answer := httpResponse(req, sink.output(), model, inTok, outTok, start)
	answer.Truncated = sink.truncated()
	answer.TTFT = sink.firstTokenAt()
	answer.ToolCalls = tools.done()
	answer.FinishReason = finish
	return answer, nil
}

// cohereIndex prefers the index on the event and falls back to the call's
// position, so a stream that omits it still keeps two calls apart.
func cohereIndex(got *int, nth int) int {
	if got != nil {
		return *got
	}
	return nth
}
