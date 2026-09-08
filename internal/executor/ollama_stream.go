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
	"time"

	"github.com/ankit373/hydra/internal/util"
)

// ExecuteStream is Execute with `"stream": true`, which makes Ollama answer in
// NDJSON: one object per line carrying a `response` fragment, then a final one
// with `done` and the token counts.
//
// The counts are the reason the final object cannot be skipped. They arrive
// only there, so a reader that stops at the last fragment reports zero tokens,
// and the dispatch is then logged as free.
func (e *OllamaExecutor) ExecuteStream(ctx context.Context, req Request, onDelta OnDelta) (*Response, error) {
	host := ollamaHost()
	if err := e.ensureRunning(host); err != nil {
		return nil, fmt.Errorf("ollama executor: %w", err)
	}

	modelFlag := req.Head.Meta["model_flag"]
	if modelFlag == "" {
		modelFlag = req.Head.ID
	}

	raw, err := json.Marshal(ollamaGenerateRequest{
		Model:  modelFlag,
		Prompt: req.Prompt,
		Stream: true,
	})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/api/generate", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	start := time.Now()
	// The shared client's timeout is a whole-request deadline, which on a
	// stream would cut a long answer off mid-sentence rather than bound a
	// stall. Streaming is bounded by the context the caller passes instead.
	client := *e.httpClient()
	client.Timeout = 0
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama exec %s: %w", req.Head.ID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, httpStatusError(req.Head.ID, resp)
	}

	sink := newDeltaSink(onDelta, start)
	var final ollamaGenerateResponse
	var sawDone bool

	sc := bufio.NewScanner(io.LimitReader(resp.Body, int64(util.DefaultMaxBytes)+1))
	// A single NDJSON line is one fragment, but Ollama's final object carries
	// the whole context array on some versions, which overflows the default
	// 64 KB token. Growing the buffer keeps that line readable instead of
	// failing the stream at the point the token counts arrive.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var chunk ollamaGenerateResponse
		if err := json.Unmarshal(line, &chunk); err != nil {
			return nil, fmt.Errorf("ollama exec %s: decode chunk: %w", req.Head.ID, err)
		}
		sink.write(chunk.Response)
		if chunk.Done {
			final, sawDone = chunk, true
			break
		}
	}
	if err := sc.Err(); err != nil {
		// A cancelled context is the caller's own decision, so what arrived so
		// far is the answer, not an error. Anything else is a real failure.
		if ctx.Err() == nil {
			return nil, fmt.Errorf("ollama exec %s: read: %w", req.Head.ID, err)
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil && !sawDone {
		out := sink.output()
		if out == "" {
			return nil, fmt.Errorf("ollama exec %s: %w", req.Head.ID, ctxErr)
		}
		return e.streamResponse(sink, final, req.Prompt, modelFlag, start, false), nil
	}
	if sink.output() == "" {
		return nil, fmt.Errorf("ollama exec %s: empty response", req.Head.ID)
	}
	if !sawDone {
		return nil, fmt.Errorf("ollama exec %s: stream ended before done", req.Head.ID)
	}

	return e.streamResponse(sink, final, req.Prompt, modelFlag, start, true), nil
}

// streamResponse assembles the Response from a finished stream. counted says
// whether the final object arrived: without it there are no provider counts,
// so they are estimated and labelled as such rather than reported as zero.
func (e *OllamaExecutor) streamResponse(sink *deltaSink, final ollamaGenerateResponse, prompt, modelFlag string, start time.Time, counted bool) *Response {
	out := sink.output()
	in, outTok, source := final.PromptEvalCount, final.EvalCount, "real"
	estimated := false
	if !counted || (in == 0 && outTok == 0) {
		in, outTok = EstimateTokens(prompt), EstimateTokens(out)
		source, estimated = "estimate", true
	}
	writeTokenSidecar(modelFlag, "ollama", source, in, outTok)

	return &Response{
		Output:       out,
		Duration:     time.Since(start),
		Model:        firstNonEmpty(final.Model, modelFlag),
		InputTokens:  in,
		OutputTokens: outTok,
		Truncated:    sink.truncated(),
		// Measured, not the provider's prompt-eval figure: the first delta
		// landing is what TTFT means, and on a stream we watch it happen.
		TTFT:            sink.firstTokenAt(),
		TokensEstimated: estimated,
	}
}

// compile-time proof the interface is satisfied, so a signature drift is a
// build failure rather than a silent fall back to the non-streaming path.
var _ StreamingExecutor = (*OllamaExecutor)(nil)
