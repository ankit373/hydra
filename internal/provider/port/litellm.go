// SPDX-License-Identifier: MIT

package port

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/ankit373/hydra/internal/capabilities"
	"github.com/ankit373/hydra/internal/provider"
)

// DefaultLiteLLMHost is where a LiteLLM proxy listens unless told otherwise.
// LITELLM_PROXY_URL and LITELLM_PROXY_API_KEY are the variables LiteLLM's own
// client reads, so a machine already configured for it needs nothing new.
const DefaultLiteLLMHost = "http://localhost:4000"

const litellmPort = 4000

// litellmLocalUpstreams are the upstream providers that are servers the
// operator runs. Read from what the proxy reports per model, never inferred
// from its address: a LiteLLM proxy on localhost forwarding to OpenAI is the
// normal deployment, and calling that local would rank a paid head at the free
// tier and hand the local-only PII policy a head that leaves the machine (#717).
//
// Deliberately short. Triton and Xinference are self-hosted too, but leaving
// them out costs a tier, while wrongly including something costs the guarantee.
var litellmLocalUpstreams = map[string]bool{
	"ollama":      true,
	"ollama_chat": true,
	"vllm":        true,
	"hosted_vllm": true,
	"lm_studio":   true,
	"llamafile":   true,
}

// litellmService discovers the models a LiteLLM proxy fronts, one head each.
type litellmService struct {
	base string
	key  string
}

func newLiteLLMService() *litellmService {
	return &litellmService{
		base: provider.HostFromEnv("LITELLM_PROXY_URL", DefaultLiteLLMHost),
		key:  strings.TrimSpace(os.Getenv("LITELLM_PROXY_API_KEY")),
	}
}

func (s *litellmService) addr() string { return hostPort(s.base, litellmPort) }

func (s *litellmService) probe(ctx context.Context, caps *capabilities.DB) ([]provider.Head, error) {
	// /health/liveliness answers without a key, so the question "is this a
	// LiteLLM proxy" is settled before the question "can Hydra read it",
	// and a key-protected proxy is still reported rather than looking absent.
	if err := s.alive(ctx); err != nil {
		return nil, err
	}

	models, err := s.models(ctx)
	if err != nil {
		return []provider.Head{s.unreadableHead(err)}, nil
	}

	heads := make([]provider.Head, 0, len(models))
	for _, m := range models {
		heads = append(heads, s.head(m, caps))
	}
	return heads, nil
}

func (s *litellmService) alive(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+"/health/liveliness", nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("litellm probe: liveliness returned %d", resp.StatusCode)
	}
	return nil
}

// litellmModel is the part of /model/info Hydra reads. The upstream provider
// and the upstream model id are what make this worth calling at all: /v1/models
// reports every model as owned_by "openai" whatever it actually fronts, so it
// cannot answer the locality question.
type litellmModel struct {
	ModelName    string `json:"model_name"`
	LiteLLMParam struct {
		Model   string `json:"model"`
		APIBase string `json:"api_base"`
	} `json:"litellm_params"`
	ModelInfo struct {
		Provider       string `json:"litellm_provider"`
		MaxInputTokens int    `json:"max_input_tokens"`
		Mode           string `json:"mode"`
	} `json:"model_info"`
}

func (s *litellmService) models(ctx context.Context) ([]litellmModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+"/model/info", nil)
	if err != nil {
		return nil, err
	}
	if s.key != "" {
		req.Header.Set("Authorization", "Bearer "+s.key)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Measured against 1.80: no key is a 500 and a wrong key a 400, so the
		// status cannot be read as "unauthorized" the way a 401 could be.
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var payload struct {
		Data []litellmModel `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Data, nil
}

// unreadableHead reports a proxy that is running but whose model list Hydra
// cannot read. One head, never routable, so the proxy appears in `hyctl probe`
// with the reason instead of being invisible, which is the difference between
// "I have nothing configured" and "my key is missing" (#248).
func (s *litellmService) unreadableHead(err error) provider.Head {
	reason := "LiteLLM is running here but its model list could not be read (" + err.Error() +
		"); set LITELLM_PROXY_API_KEY"
	if s.key != "" {
		reason = "LiteLLM is running here but rejected LITELLM_PROXY_API_KEY (" + err.Error() + ")"
	}
	return provider.Head{
		ID:       "litellm/unreadable",
		Name:     "LiteLLM (" + s.addr() + ")",
		Provider: "litellm",
		Source:   "port",
		Endpoint: s.base,
		Meta:     map[string]string{"unroutable_reason": reason},
	}
}

func (s *litellmService) head(m litellmModel, caps *capabilities.DB) provider.Head {
	upstream := m.ModelInfo.Provider
	local := litellmLocalUpstreams[upstream]

	meta := map[string]string{
		// The alias the proxy routes on, which is what a request must name.
		// Also what keeps these heads distinct through rank's per-provider
		// dedup, the same mechanism the OpenRouter allowlist uses (#752).
		"model": m.ModelName,
	}
	if upstream != "" {
		meta["litellm_upstream"] = upstream
	}
	if u := m.LiteLLMParam.Model; u != "" {
		meta["litellm_upstream_model"] = u
	}
	if m.ModelInfo.MaxInputTokens > 0 {
		meta["model_ctx_max"] = strconv.Itoa(m.ModelInfo.MaxInputTokens)
	}
	if upstream == "" {
		// Says why it is not local, so a head sitting a tier lower than the
		// operator expects has a readable cause rather than looking arbitrary.
		meta["litellm_upstream"] = "unreported"
	}
	if m.ModelInfo.Mode != "" && m.ModelInfo.Mode != "chat" && m.ModelInfo.Mode != "completion" {
		// An embedding or rerank model behind the proxy fails every dispatch,
		// the same way an embedding-only Ollama model does (#532).
		meta["embedding_only"] = "true"
	}

	return provider.Head{
		ID:       "litellm/" + m.ModelName,
		Name:     m.ModelName + " (LiteLLM)",
		Provider: "litellm",
		Source:   "port",
		Endpoint: s.base,
		// Scored on the model the proxy actually calls, not on the alias: an
		// alias is whatever the operator typed, so scoring it would give every
		// model behind a proxy the same default.
		CapScore:  caps.ScoreOllama(upstreamModelID(m)),
		LocalOnly: local,
		AuthReady: true,
		Meta:      meta,
	}
}

// upstreamModelID is the model id without its provider prefix, which is the
// spelling the capability data is keyed on ("gpt-4o-mini", not
// "openai/gpt-4o-mini"). Falls back to the alias when nothing is reported.
func upstreamModelID(m litellmModel) string {
	id := m.LiteLLMParam.Model
	if id == "" {
		return m.ModelName
	}
	if _, rest, found := strings.Cut(id, "/"); found {
		return rest
	}
	return id
}
