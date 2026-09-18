// SPDX-License-Identifier: MIT

package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// streamOptions asks an OpenAI-compatible server to report usage on a streamed
// response. Without it there is no usage block at all, and the dispatch is
// logged with zero tokens and therefore zero cost (#787).
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// openAIChatChunk is one SSE `data:` payload. Usage arrives on a final chunk
// whose Choices array is empty, so a reader that requires a choice per chunk
// throws the counts away.
type openAIChatChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// ExecuteStream streams the OpenAI-compatible and Azure paths, which share a
// wire shape, and Anthropic, which does not (#845). The dialects left each
// carry usage in a differently-shaped event, and Replicate is a polling API
// rather than a stream, so they keep taking Execute until each is done
// deliberately rather than in a batch (#787).
func (e *HTTPExecutor) ExecuteStream(ctx context.Context, req Request, onDelta OnDelta) (*Response, error) {
	switch req.Head.Provider {
	case "azure":
		return e.streamOpenAILike(ctx, req, onDelta, azureStreamTarget)
	case "anthropic":
		return e.streamAnthropic(ctx, req, onDelta)
	case "google", "cohere", "bedrock", "replicate":
		// Dialects this does not stream yet. Falling back rather than failing:
		// the caller asked for output, not specifically for a stream, and
		// executor.Stream already delivers a non-streamed answer as one delta.
		return e.Execute(ctx, req)
	default:
		cfg, err := openAICompatConfigFor(req.Head)
		if err != nil {
			return nil, fmt.Errorf("http exec %s: %w", req.Head.ID, err)
		}
		return e.streamOpenAILike(ctx, req, onDelta, func(Request) (string, string, map[string]string, error) {
			return strings.TrimRight(cfg.BaseURL, "/") + "/v1/chat/completions", cfg.Model, cfg.Headers, nil
		})
	}
}

// streamTarget resolves the endpoint, model and headers for one dialect.
type streamTarget func(req Request) (endpoint, model string, headers map[string]string, err error)

func (e *HTTPExecutor) streamOpenAILike(ctx context.Context, req Request, onDelta OnDelta, target streamTarget) (*Response, error) {
	endpoint, model, headers, err := target(req)
	if err != nil {
		return nil, err
	}

	raw, err := json.Marshal(struct {
		openAIChatRequest
		StreamOptions *streamOptions `json:"stream_options,omitempty"`
	}{
		openAIChatRequest: openAIChatRequest{
			Model:     model,
			Messages:  buildMessages(req),
			MaxTokens: req.MaxTokens,
			Stream:    true,
		},
		StreamOptions: &streamOptions{IncludeUsage: true},
	})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	var gotModel string
	var inTok, outTok int

	sink, start, err := e.sseStream(ctx, req.Head.ID, httpReq, onDelta, func(payload string, sink *deltaSink) error {
		var chunk openAIChatChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return fmt.Errorf("decode chunk: %w", err)
		}
		if chunk.Model != "" {
			gotModel = chunk.Model
		}
		if chunk.Usage != nil {
			inTok, outTok = chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens
		}
		for _, c := range chunk.Choices {
			sink.write(c.Delta.Content)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// A server that ignored stream_options, or a stream cut short, leaves no
	// counts. httpResponse estimates them, the same way the buffered dialects
	// do, so the two cannot disagree about what a call with no usage cost.
	answer := httpResponse(req, sink.output(), firstNonEmpty(gotModel, model), inTok, outTok, start)
	answer.Truncated = sink.truncated()
	answer.TTFT = sink.firstTokenAt()
	return answer, nil
}

// sseStream is the transport half every SSE dialect shares: issue the request
// without a whole-request timeout, fail on a status, fold the events through
// onData, and keep what a cancelled stream had already delivered.
//
// It returns the sink rather than a Response because each dialect carries its
// model and its token counts in events of its own shape, which is the whole of
// what differs between them.
func (e *HTTPExecutor) sseStream(ctx context.Context, headID string, httpReq *http.Request, onDelta OnDelta,
	onData func(payload string, sink *deltaSink) error) (*deltaSink, time.Time, error) {

	// A whole-request timeout would cut a long answer off mid-sentence rather
	// than bound a stall, so a stream is bounded by the caller's context.
	client := *e.httpClient()
	client.Timeout = 0

	start := time.Now()
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, start, fmt.Errorf("http exec %s: %w", headID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, start, httpStatusError(headID, resp)
	}

	sink := newDeltaSink(onDelta, start)
	err = scanSSE(resp.Body, func(payload string) error { return onData(payload, sink) })
	// A cancelled stream keeps whatever arrived: the caller stopped it, and the
	// partial is what a surface has already rendered.
	if err != nil && ctx.Err() == nil {
		return nil, start, fmt.Errorf("http exec %s: %w", headID, err)
	}

	if sink.output() == "" {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, start, fmt.Errorf("http exec %s: %w", headID, ctxErr)
		}
		return nil, start, fmt.Errorf("http exec %s: empty response", headID)
	}
	return sink, start, nil
}

// azureStreamTarget mirrors executeAzureOpenAI's endpoint construction. Azure
// puts the deployment in the path and the model nowhere, so the request body
// carries no model at all.
func azureStreamTarget(Request) (string, string, map[string]string, error) {
	base := strings.TrimRight(azureEndpoint(), "/")
	u := fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s",
		base, url.PathEscape(azureDeployment()), url.QueryEscape(azureAPIVersion()))
	return u, "", map[string]string{"api-key": apiKeyFor("azure")}, nil
}

var _ StreamingExecutor = (*HTTPExecutor)(nil)
