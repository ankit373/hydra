package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"
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
	Response           string  `json:"response"`
	Model              string  `json:"model"`
	Done               bool    `json:"done"`
	PromptEvalCount    int     `json:"prompt_eval_count"`
	EvalCount          int     `json:"eval_count"`
	PromptEvalDuration int64   `json:"prompt_eval_duration"`
	EvalDuration       int64   `json:"eval_duration"`
	TotalDuration      int64   `json:"total_duration"`
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

	var out ollamaGenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
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
	}, nil
}

// ensureRunning checks Ollama health and attempts auto-start if down.
func (e *OllamaExecutor) ensureRunning(host string) error {
	if e.isHealthy(host) {
		return nil
	}

	// Attempt to start ollama serve in background.
	ollamaCmd := exec.Command("ollama", "serve")
	ollamaCmd.Stdout = nil
	ollamaCmd.Stderr = nil
	if err := ollamaCmd.Start(); err != nil {
		return fmt.Errorf("ollama not running and could not start: %w", err)
	}

	// Wait up to 3 seconds for it to become healthy.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		if e.isHealthy(host) {
			return nil
		}
	}
	return fmt.Errorf("ollama started but did not respond within 3s — is it installed?")
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

func ollamaHost() string {
	if h := os.Getenv("OLLAMA_HOST"); h != "" {
		return h
	}
	return "http://localhost:11434"
}
