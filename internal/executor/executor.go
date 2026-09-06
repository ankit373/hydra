// SPDX-License-Identifier: MIT

// Package executor runs prompts against Hydra Heads.
// Two strategies: CLI (subprocess) and HTTP (OpenAI-compatible REST).
package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	Truncated    bool // true when output exceeded the accumulator cap

	// TokensEstimated is true when InputTokens/OutputTokens were derived by
	// Hydra (e.g. agy's char/4 heuristic) rather than reported by the provider.
	// HTTP and Ollama executors parse real usage and leave this false; the agy
	// executor sets it true. Consumers must not present estimated tokens as
	// measured spend.
	TokensEstimated bool
}

// EstimateTokens approximates a token count from text length, for the CLI
// heads that report no usage of their own.
//
// Never returns 0 for text that exists. len(s)/4 truncated a reply of "OK" to
// zero tokens, so a call that answered was reported as producing nothing and
// its cost rounded to $0.000 (#696).
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	if n := len(s) / 4; n > 0 {
		return n
	}
	return 1
}

// Executor runs a prompt and returns a response.
type Executor interface {
	Execute(ctx context.Context, req Request) (*Response, error)
}

// Unroutable explains why a discovered head cannot be dispatched to, or returns
// "" when it can.
//
// This is the primary check and Supports delegates to it, so a surface that
// lists heads and a surface that routes to them cannot disagree. They did:
// `hyctl probe` advertised the PATH-discovered Ollama binary while
// `hyctl dispatch --local` refused and told the user to go look at `hyctl probe`
// (#248). Discovery finding a head and Hydra being able to drive it are
// different questions, and only one function should answer the second.
func Unroutable(h provider.Head) string {
	switch {
	case h.Meta["embedding_only"] == "true":
		// Discovered and shown, never dispatched: it has no completion API,
		// so every dispatch to it would fail (#532).
		return "embeddings only, never routed"
	case h.Source == "registry":
		// Every registry head is driven by AgyExecutor, which execs `agy`.
		// Calling them routable without it turned one missing binary into a
		// chain of eight identical failures per dispatch (#688).
		if h.Executable == "" {
			return "the agy CLI is not on PATH, so nothing can drive this head"
		}
		return ""
	case h.Source == "port" || h.Source == "env" || h.Endpoint != "":
		if SupportsHTTP(h) {
			return ""
		}
		return "no API key or default model configured for " + h.Provider
	}
	if _, ok := cliTemplates[h.Provider]; ok {
		return ""
	}
	if _, ok := cliTemplates[h.ID]; ok {
		return ""
	}
	// ollama and llamafile are found on $PATH but driven over HTTP, so the
	// binary alone is not routable, the port provider registers the real heads
	// once a server answers. Say that, rather than "no executor", because the
	// user can act on it.
	if h.LocalOnly {
		return "binary only, start its local server (e.g. `ollama serve`) to route to its models"
	}
	return "no executor for provider " + h.Provider
}

// Supports reports whether Hydra can execute a discovered head today.
func Supports(h provider.Head) bool { return Unroutable(h) == "" }

// For selects the correct Executor for a given Head.
//   - registry source (agy tiers): AgyExecutor → native (execs the `agy` binary)
//   - ollama source: OllamaExecutor → native /api/generate
//   - env source / port source / explicit endpoint: HTTPExecutor → per-provider REST
//   - everything else: CLIExecutor → subprocess
//
// env-key heads (Source=="env") carry no Executable, they are API providers
// (anthropic, openai, groq, …). HTTPExecutor.Execute dispatches on Head.Provider
// and already has an adapter for each, so env heads route to HTTP, not CLI.
func For(h provider.Head) Executor {
	if h.Source == "registry" {
		return &AgyExecutor{}
	}
	if h.Source == "ollama" || h.Provider == "ollama" {
		return &OllamaExecutor{}
	}
	if h.Source == "port" || h.Source == "env" || h.Endpoint != "" {
		return &HTTPExecutor{}
	}
	return &CLIExecutor{}
}

type tokenSidecar struct {
	Model          string `json:"model"`
	Executor       string `json:"executor"`
	Source         string `json:"source"`
	PromptTokens   int    `json:"prompt_tokens"`
	ResponseTokens int    `json:"response_tokens"`
}

// writeTokenSidecar writes token usage to HYDRA_TOKEN_SIDECAR if set.
func writeTokenSidecar(model, executorName, source string, prompt, response int) {
	raw := os.Getenv("HYDRA_TOKEN_SIDECAR")
	if raw == "" {
		return
	}
	// Reject paths with traversal components or path separators in the basename.
	clean := filepath.Clean(raw)
	if !filepath.IsAbs(clean) || strings.Contains(filepath.Base(clean), "..") {
		return
	}
	// Only allow writes inside os.TempDir to prevent arbitrary file writes.
	tmpDir := filepath.Clean(os.TempDir())
	if !strings.HasPrefix(clean, tmpDir+string(filepath.Separator)) {
		return
	}
	data, _ := json.Marshal(tokenSidecar{
		Model:          model,
		Executor:       executorName,
		Source:         source,
		PromptTokens:   prompt,
		ResponseTokens: response,
	})
	_ = os.WriteFile(clean, data, 0o600)
}
