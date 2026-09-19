// SPDX-License-Identifier: MIT

package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// anthropicEvent is one SSE payload of the Messages stream. The two token
// counts arrive on different event types, so a reader that takes either one
// alone logs half of a real call as free: input_tokens on message_start,
// output_tokens on message_delta (#845).
type anthropicEvent struct {
	Type    string `json:"type"`
	Message *struct {
		Model string          `json:"model"`
		Usage *anthropicUsage `json:"usage"`
	} `json:"message"`
	// The block a tool call opens with. Its input is empty here and arrives as
	// partial_json fragments, so the identity and the arguments come from two
	// different events.
	Index        int `json:"index"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	// Carries a text_delta on content_block_delta and a stop_reason on
	// message_delta, which is why the type is read before the text.
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anthropicUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (e *HTTPExecutor) streamAnthropic(ctx context.Context, req Request, onDelta OnDelta) (*Response, error) {
	model := defaultModelFor("anthropic")
	body, err := anthropicBody(req, model, true)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicMessagesURL(), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	setAnthropicHeaders(httpReq)
	httpReq.Header.Set("Accept", "text/event-stream")

	gotModel := model
	var inTok, outTok int
	var finish string
	var tools toolCallStream

	sink, start, err := e.sseStream(ctx, req.Head.ID, httpReq, onDelta, func(payload string, sink *deltaSink) error {
		var ev anthropicEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			return fmt.Errorf("decode event: %w", err)
		}
		switch ev.Type {
		case "message_start":
			if ev.Message == nil {
				return nil
			}
			gotModel = firstNonEmpty(ev.Message.Model, gotModel)
			readAnthropicUsage(ev.Message.Usage, &inTok, &outTok)
		case "content_block_start":
			// A tool call announces itself here, with an empty input.
			if ev.ContentBlock == nil || ev.ContentBlock.Type != "tool_use" {
				return nil
			}
			tools.add(ToolCall{
				Index: ev.Index, ID: ev.ContentBlock.ID, Type: "function",
				Function: ToolCallFunction{Name: ev.ContentBlock.Name},
			})
			sink.noteStructured()
		case "content_block_delta":
			if ev.Delta == nil {
				return nil
			}
			switch ev.Delta.Type {
			case "text_delta":
				// Only text_delta is the answer. A thinking_delta is the
				// model's reasoning, and rendering it as the answer would put
				// words in the head's mouth.
				sink.write(ev.Delta.Text)
			case "input_json_delta":
				// Partial JSON, keyed by the content block's index, which is
				// how two parallel calls stay apart.
				tools.add(ToolCall{
					Index:    ev.Index,
					Function: ToolCallFunction{Arguments: ev.Delta.PartialJSON},
				})
			}
		case "message_delta":
			readAnthropicUsage(ev.Usage, &inTok, &outTok)
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				finish = anthropicFinish(ev.Delta.StopReason)
			}
		case "error":
			// Arrives on a 200 mid-stream, so without this an overload reads as
			// a short answer rather than as the failure it is.
			if ev.Error != nil {
				return fmt.Errorf("anthropic: %s: %s", ev.Error.Type, ev.Error.Message)
			}
			return fmt.Errorf("anthropic: stream error")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	answer := httpResponse(req, sink.output(), gotModel, inTok, outTok, start)
	answer.Truncated = sink.truncated()
	answer.TTFT = sink.firstTokenAt()
	answer.ToolCalls = tools.done()
	answer.FinishReason = finish
	return answer, nil
}

// readAnthropicUsage merges one event's counts into the pair. Each field is
// taken only when reported: message_delta repeats the running output count and
// carries no input count, so overwriting with a zero would throw away what
// message_start already said.
func readAnthropicUsage(u *anthropicUsage, in, out *int) {
	if u == nil {
		return
	}
	if u.InputTokens > 0 {
		*in = u.InputTokens
	}
	if u.OutputTokens > 0 {
		*out = u.OutputTokens
	}
}
