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
	Type  string `json:"type"`
	Delta *struct {
		Message *struct {
			Content *struct {
				Text string `json:"text"`
			} `json:"content"`
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
	raw, err := json.Marshal(cohereBody(req, model, true))
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
		case "message-end":
			if u := ev.Delta.Usage; u != nil {
				inTok, outTok = u.Tokens.InputTokens, u.Tokens.OutputTokens
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	answer := httpResponse(req, sink.output(), model, inTok, outTok, start)
	answer.Truncated = sink.truncated()
	answer.TTFT = sink.firstTokenAt()
	return answer, nil
}
