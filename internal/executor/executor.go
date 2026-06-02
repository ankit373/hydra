// Package executor runs prompts against Hydra Heads.
// Two strategies: CLI (subprocess) and HTTP (OpenAI-compatible REST).
package executor

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/ankit373/hydra/internal/provider"
)

// Request is everything needed to execute a prompt against a Head.
type Request struct {
	Prompt    string
	Head      provider.Head
	MaxTokens int    // 0 = provider default
	System    string // optional system prompt
}

// Response is the result of a successful execution.
type Response struct {
	Output       string
	InputTokens  int
	OutputTokens int
	Duration     time.Duration
	Model        string
}

// Executor runs a prompt and returns a response.
type Executor interface {
	Execute(ctx context.Context, req Request) (*Response, error)
}

// Supports reports whether Hydra can execute a discovered head today.
func Supports(h provider.Head) bool {
	if h.Source == "registry" {
		return true // agy heads — AgyExecutor handles them
	}
	if h.Source == "port" || h.Endpoint != "" {
		return SupportsHTTP(h)
	}
	if _, ok := cliTemplates[h.Provider]; ok {
		return true
	}
	_, ok := cliTemplates[h.ID]
	return ok
}

// For selects the correct Executor for a given Head.
//   - registry source (agy tiers): AgyExecutor → calls dispatch/agy.sh
//   - ollama source: OllamaExecutor → native /api/generate
//   - port source / explicit endpoint: HTTPExecutor → OpenAI-compatible REST
//   - everything else: CLIExecutor → subprocess
func For(h provider.Head) Executor {
	if h.Source == "registry" {
		return &AgyExecutor{}
	}
	if h.Source == "ollama" || h.Provider == "ollama" {
		return &OllamaExecutor{}
	}
	if h.Source == "port" || h.Endpoint != "" {
		return &HTTPExecutor{}
	}
	return &CLIExecutor{}
}

type tokenSidecar struct {
	Model         string `json:"model"`
	Executor      string `json:"executor"`
	Source        string `json:"source"`
	PromptTokens  int    `json:"prompt_tokens"`
	ResponseTokens int   `json:"response_tokens"`
}

// writeTokenSidecar writes token usage to HYDRA_TOKEN_SIDECAR if set.
func writeTokenSidecar(model, executorName, source string, prompt, response int) {
	path := os.Getenv("HYDRA_TOKEN_SIDECAR")
	if path == "" {
		return
	}
	data, _ := json.Marshal(tokenSidecar{
		Model:          model,
		Executor:       executorName,
		Source:         source,
		PromptTokens:   prompt,
		ResponseTokens: response,
	})
	_ = os.WriteFile(path, data, 0o644)
}
