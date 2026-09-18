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
// repeats and is cumulative, so the last one is the whole call's count and the
// first is the count of a few tokens (#851).
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
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	// Arrives on a 200 partway through, like Anthropic's error event and
	// Cohere's. A chunk of this shape carries no candidates and no feedback, so
	// every other field is skipped and the reader used to treat it as a chunk
	// carrying nothing, returning the text so far as a complete answer (#869).
	Error *struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"error"`
	ModelVersion string `json:"modelVersion"`
}

func (e *HTTPExecutor) streamGemini(ctx context.Context, req Request, onDelta OnDelta) (*Response, error) {
	model := defaultModelFor("google")
	raw, err := json.Marshal(geminiBody(req))
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiURL(model, true), bytes.NewReader(raw))
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
		// A safety filter refuses on a 200 with no candidates at all, so
		// without this a blocked prompt reads as a head that said nothing.
		if fb := chunk.PromptFeedback; fb != nil && fb.BlockReason != "" {
			return fmt.Errorf("gemini: prompt blocked: %s", fb.BlockReason)
		}
		if e := chunk.Error; e != nil {
			return fmt.Errorf("gemini: %s: %s", e.Status, e.Message)
		}
		gotModel = firstNonEmpty(chunk.ModelVersion, gotModel)
		if u := chunk.UsageMetadata; u != nil {
			if u.PromptTokenCount > 0 {
				inTok = u.PromptTokenCount
			}
			if u.CandidatesTokenCount > 0 {
				outTok = u.CandidatesTokenCount
			}
		}
		// Only the first candidate, the one the buffered path returns. With
		// candidateCount above 1 the rest are alternative answers, and
		// concatenating them would splice two of them into one.
		if len(chunk.Candidates) == 0 {
			return nil
		}
		for _, p := range chunk.Candidates[0].Content.Parts {
			sink.write(p.Text)
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
