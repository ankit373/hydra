// SPDX-License-Identifier: MIT

package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/util"
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
// wire shape. The other dialects (Anthropic, Gemini, Cohere, Bedrock) each
// carry usage somewhere else in a differently-shaped event, and Replicate is a
// polling API rather than a stream, so they keep taking Execute until each is
// done deliberately rather than in a batch (#787).
func (e *HTTPExecutor) ExecuteStream(ctx context.Context, req Request, onDelta OnDelta) (*Response, error) {
	switch req.Head.Provider {
	case "azure":
		return e.streamOpenAILike(ctx, req, onDelta, azureStreamTarget)
	case "anthropic", "google", "cohere", "bedrock", "replicate":
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

	start := time.Now()
	// A whole-request timeout would cut a long answer off mid-sentence rather
	// than bound a stall, so a stream is bounded by the caller's context.
	client := *e.httpClient()
	client.Timeout = 0
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http exec %s: %w", req.Head.ID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, httpStatusError(req.Head.ID, resp)
	}

	sink := newDeltaSink(onDelta, start)
	var gotModel string
	var inTok, outTok int
	var sawUsage bool

	sc := bufio.NewScanner(io.LimitReader(resp.Body, int64(util.DefaultMaxBytes)+1))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// Comment keep-alives (":" prefixed) and the blank lines separating
		// events are part of SSE, not content.
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue // `event:` and `id:` lines carry nothing we need
		}
		payload = strings.TrimSpace(payload)
		if payload == "[DONE]" {
			break
		}
		var chunk openAIChatChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return nil, fmt.Errorf("http exec %s: decode chunk: %w", req.Head.ID, err)
		}
		if chunk.Model != "" {
			gotModel = chunk.Model
		}
		if chunk.Usage != nil {
			inTok, outTok, sawUsage = chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens, true
		}
		for _, c := range chunk.Choices {
			sink.write(c.Delta.Content)
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return nil, fmt.Errorf("http exec %s: read: %w", req.Head.ID, err)
	}

	out := sink.output()
	if out == "" {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("http exec %s: %w", req.Head.ID, ctxErr)
		}
		return nil, fmt.Errorf("http exec %s: empty response", req.Head.ID)
	}

	// A server that ignored stream_options, or a stream cut short, leaves no
	// counts. Estimated and labelled beats reporting a call that answered as
	// having cost nothing.
	estimated := false
	if !sawUsage || (inTok == 0 && outTok == 0) {
		inTok, outTok = EstimateTokens(req.Prompt), EstimateTokens(out)
		estimated = true
	}

	return &Response{
		Output:          out,
		InputTokens:     inTok,
		OutputTokens:    outTok,
		Duration:        time.Since(start),
		Model:           firstNonEmpty(gotModel, model),
		Truncated:       sink.truncated(),
		TTFT:            sink.firstTokenAt(),
		TokensEstimated: estimated,
	}, nil
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
