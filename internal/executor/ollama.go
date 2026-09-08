// SPDX-License-Identifier: MIT

package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"sync"
	"time"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/sandbox"
	"github.com/ankit373/hydra/internal/util"
)

// OllamaExecutor calls the Ollama native /api/generate endpoint.
// Before dispatching it health-checks the server and attempts auto-start.
// Ports dispatch/ollama.sh natively in Go.
type OllamaExecutor struct {
	client *http.Client
}

func (e *OllamaExecutor) httpClient() *http.Client {
	if e.client != nil {
		return e.client
	}
	return http.DefaultClient
}

type ollamaGenerateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

type ollamaGenerateResponse struct {
	Response           string `json:"response"`
	Model              string `json:"model"`
	Done               bool   `json:"done"`
	PromptEvalCount    int    `json:"prompt_eval_count"`
	EvalCount          int    `json:"eval_count"`
	PromptEvalDuration int64  `json:"prompt_eval_duration"`
	EvalDuration       int64  `json:"eval_duration"`
	TotalDuration      int64  `json:"total_duration"`
}

func (e *OllamaExecutor) Execute(ctx context.Context, req Request) (*Response, error) {
	host := ollamaHost()

	if err := e.ensureRunning(host); err != nil {
		return nil, fmt.Errorf("ollama executor: %w", err)
	}

	modelFlag := req.Head.Meta["model_flag"]
	if modelFlag == "" {
		modelFlag = req.Head.ID
	}

	body := ollamaGenerateRequest{
		Model:  modelFlag,
		Prompt: req.Prompt,
		Stream: false,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/api/generate", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := e.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama exec %s: %w", req.Head.ID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, httpStatusError(req.Head.ID, resp)
	}

	// Safety net: cap response body so a runaway/adversarial server can't OOM us.
	// If the body exceeds the limit the JSON decoder returns an error, which is
	// the right outcome, Ollama should never emit a 33 MB JSON response.
	limited := io.LimitReader(resp.Body, int64(util.DefaultMaxBytes)+1)

	var out ollamaGenerateResponse
	if err := json.NewDecoder(limited).Decode(&out); err != nil {
		return nil, fmt.Errorf("ollama exec %s: decode: %w", req.Head.ID, err)
	}
	if out.Response == "" {
		return nil, fmt.Errorf("ollama exec %s: empty response", req.Head.ID)
	}

	writeTokenSidecar(modelFlag, "ollama", "real", out.PromptEvalCount, out.EvalCount)

	return &Response{
		Output:       out.Response,
		Duration:     time.Since(start),
		Model:        firstNonEmpty(out.Model, modelFlag),
		InputTokens:  out.PromptEvalCount,
		OutputTokens: out.EvalCount,
		// Prompt-eval is the work before the first output token, so it is
		// exactly TTFT. Ollama has always reported it and Hydra parsed it into
		// a field nothing read.
		TTFT: time.Duration(out.PromptEvalDuration),
	}, nil
}

// ollamaServeOnce ensures at most one `ollama serve` process is started by Hydra.
var ollamaServeOnce sync.Once

// serveTarget returns the host:port Hydra should bind when it starts Ollama
// itself, and false when host is not one it should start a server for.
//
// It parses the address rather than matching a prefix, the same reasoning as
// provider.isLoopback: "127.0.0.1.evil.com" is a valid hostname someone else
// controls, and a prefix check passes it.
func serveTarget(host string) (string, bool) {
	u, err := url.Parse(host)
	if err != nil || u.Host == "" {
		return "", false
	}
	if h := u.Hostname(); h != "localhost" {
		if ip := net.ParseIP(h); ip == nil || !ip.IsLoopback() {
			return "", false
		}
	}
	return u.Host, true
}

// ensureRunning checks Ollama health and attempts auto-start if down.
func (e *OllamaExecutor) ensureRunning(host string) error {
	if e.isHealthy(host) {
		return nil
	}

	// Only ever start a server Hydra will itself talk to. A non-loopback host
	// is another machine: a local daemon would neither reach it nor be what
	// the operator asked for.
	bind, ok := serveTarget(host)
	if !ok {
		return fmt.Errorf("ollama at %s is not responding, and Hydra only auto-starts a server on loopback", host)
	}

	var startErr error
	ollamaServeOnce.Do(func() {
		cmd := sandbox.Harden(exec.Command("ollama", "serve"))
		// The child reads $OLLAMA_HOST itself. Inheriting the environment let
		// OLLAMA_HOST=0.0.0.0 bind the model server to every interface while
		// provider.OllamaHost had already refused that value for Hydra's own
		// requests and fallen back to loopback: Hydra would start a
		// network-exposed server believing it was talking to localhost.
		cmd.Env = append(
			sandbox.WithVars("OLLAMA_MODELS", "OLLAMA_KEEP_ALIVE", "OLLAMA_NUM_PARALLEL", "OLLAMA_FLASH_ATTENTION"),
			"OLLAMA_HOST="+bind)
		cmd.Stdout = nil
		cmd.Stderr = nil
		if err := cmd.Start(); err != nil {
			startErr = fmt.Errorf("ollama not running and could not start: %w", err)
			return
		}
		// Reap the child when it exits so it doesn't become a zombie.
		go func() { _ = cmd.Wait() }()
	})
	if startErr != nil {
		return startErr
	}

	// Wait up to 3 seconds for it to become healthy.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		if e.isHealthy(host) {
			return nil
		}
	}
	return fmt.Errorf("ollama started but did not respond within 3s, is it installed?")
}

// isHealthy returns true if Ollama's root endpoint responds with 200.
func (e *OllamaExecutor) isHealthy(host string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+"/", nil)
	if err != nil {
		return false
	}
	resp, err := e.httpClient().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 400
}

// ollamaHost delegates to provider.OllamaHost so discovery and execution can
// never resolve to different addresses again (#282). Kept as a wrapper rather
// than replacing every call site, since the name reads better here.
func ollamaHost() string { return provider.OllamaHost() }
