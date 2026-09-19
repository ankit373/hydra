// SPDX-License-Identifier: MIT

package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func (e *HTTPExecutor) streamBedrock(ctx context.Context, req Request, onDelta OnDelta) (*Response, error) {
	model := defaultModelFor("bedrock")
	body, err := bedrockBody(req)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, bedrockURL(model, true), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/vnd.amazon.eventstream")
	// Signed last: the signature covers the headers it lists, so a header set
	// after it would not be the one the service verifies.
	if err := signAWSRequest(httpReq, raw, bedrockRegion(), "bedrock"); err != nil {
		return nil, fmt.Errorf("http exec %s: %w", req.Head.ID, err)
	}

	var inTok, outTok int
	var finish string
	var tools toolCallStream

	sink, start, err := e.streamRequest(ctx, req.Head.ID, httpReq, onDelta, func(body io.Reader, sink *deltaSink) error {
		return scanEventStream(body, func(f eventFrame) error {
			// An exception arrives inside a 200, as its own frame, so without
			// this a throttled call reads as a short answer.
			if f.Headers[":message-type"] == "exception" {
				return fmt.Errorf("bedrock: %s: %s", f.Headers[":exception-type"], bedrockErrorMessage(f.Payload))
			}
			switch f.Headers[":event-type"] {
			case "contentBlockStart":
				// A tool call announces its identity here; its input follows as
				// partial JSON on the deltas for the same block index.
				var ev struct {
					Start struct {
						ToolUse *struct {
							ToolUseID string `json:"toolUseId"`
							Name      string `json:"name"`
						} `json:"toolUse"`
					} `json:"start"`
					Index int `json:"contentBlockIndex"`
				}
				if err := json.Unmarshal(f.Payload, &ev); err != nil {
					return fmt.Errorf("decode contentBlockStart: %w", err)
				}
				if ev.Start.ToolUse == nil {
					return nil
				}
				tools.add(ToolCall{
					Index: ev.Index, ID: ev.Start.ToolUse.ToolUseID, Type: "function",
					Function: ToolCallFunction{Name: ev.Start.ToolUse.Name},
				})
				sink.noteStructured()
			case "contentBlockDelta":
				var ev struct {
					Delta struct {
						Text    string `json:"text"`
						ToolUse *struct {
							Input string `json:"input"`
						} `json:"toolUse"`
					} `json:"delta"`
					Index int `json:"contentBlockIndex"`
				}
				if err := json.Unmarshal(f.Payload, &ev); err != nil {
					return fmt.Errorf("decode contentBlockDelta: %w", err)
				}
				// delta.reasoningContent rides the same event and is not the
				// answer, so it is still skipped.
				if tu := ev.Delta.ToolUse; tu != nil {
					tools.add(ToolCall{
						Index:    ev.Index,
						Function: ToolCallFunction{Arguments: tu.Input},
					})
					return nil
				}
				sink.write(ev.Delta.Text)
			case "messageStop":
				var ev struct {
					StopReason string `json:"stopReason"`
				}
				if err := json.Unmarshal(f.Payload, &ev); err != nil {
					return fmt.Errorf("decode messageStop: %w", err)
				}
				finish = bedrockFinish(ev.StopReason)
			case "metadata":
				var ev struct {
					Usage struct {
						InputTokens  int `json:"inputTokens"`
						OutputTokens int `json:"outputTokens"`
					} `json:"usage"`
				}
				if err := json.Unmarshal(f.Payload, &ev); err != nil {
					return fmt.Errorf("decode metadata: %w", err)
				}
				inTok, outTok = ev.Usage.InputTokens, ev.Usage.OutputTokens
			}
			return nil
		})
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

// bedrockErrorMessage reads an exception frame's own message, falling back to
// the raw payload so a shape this does not know still reaches the user.
func bedrockErrorMessage(payload []byte) string {
	var body struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(payload, &body); err == nil && body.Message != "" {
		return body.Message
	}
	return string(payload)
}
