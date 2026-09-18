// SPDX-License-Identifier: MIT

package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// geminiChunk is one SSE payload of streamGenerateContent. usageMetadata
// repeats on every chunk carrying the running totals, so the last one reported
// is the whole call and an early one is a fraction of it (#845).
type geminiChunk struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
	ModelVersion string `json:"modelVersion"`
	Error        *struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"error"`
}

func (e *HTTPExecutor) streamGemini(ctx context.Context, req Request, onDelta OnDelta) (*Response, error) {
	model := defaultModelFor("google")
	raw, err := json.Marshal(geminiBody(req))
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		geminiURL(model, "streamGenerateContent", true), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	setGeminiHeaders(httpReq)
	httpReq.Header.Set("Accept", "text/event-stream")

	gotModel := model
	var inTok, outTok int

	sink, start, err := e.sseStream(ctx, req.Head.ID, httpReq, onDelta, func(payload string, sink *deltaSink) error {
		var chunk geminiChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return fmt.Errorf("decode chunk: %w", err)
		}
		// Arrives on a 200 mid-stream, so without this a quota refusal reads as
		// a short answer rather than as the failure it is.
		if chunk.Error != nil {
			return fmt.Errorf("gemini: %s: %s", chunk.Error.Status, chunk.Error.Message)
		}
		gotModel = firstNonEmpty(chunk.ModelVersion, gotModel)
		// Last wins, not first and not largest: these are running totals, and a
		// chunk that omits them entirely must not zero what the last one said.
		if chunk.UsageMetadata != nil {
			inTok = chunk.UsageMetadata.PromptTokenCount
			outTok = chunk.UsageMetadata.CandidatesTokenCount
		}
		for _, c := range chunk.Candidates {
			for _, p := range c.Content.Parts {
				sink.write(p.Text)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	answer := httpResponse(req, sink.output(), gotModel, inTok, outTok, start)
	answer.Truncated = sink.truncated()
	answer.TTFT = sink.firstTokenAt()
	return answer, nil
}
