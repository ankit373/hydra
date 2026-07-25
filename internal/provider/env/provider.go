// SPDX-License-Identifier: MIT

// Package env discovers AI heads via API keys present in the environment.
// To add a new provider: add one entry to the knownKeys table.
package env

import (
	"context"
	"os"

	"github.com/ankit373/hydra/internal/capabilities"
	"github.com/ankit373/hydra/internal/provider"
)

func init() { provider.Register(&Provider{}) }

type Provider struct{}

func (p *Provider) ID() string { return "env" }

func (p *Provider) Discover(_ context.Context) ([]provider.Head, error) {
	caps, err := capabilities.Load(capabilities.DefaultOverlayPath())
	if err != nil {
		return nil, err
	}

	var heads []provider.Head
	for _, k := range knownKeys {
		if !k.detected() {
			continue
		}
		id := "env/" + k.providerID
		heads = append(heads, provider.Head{
			ID:        id,
			Name:      caps.Name(id),
			Provider:  k.providerID,
			Source:    "env",
			CapScore:  caps.Score(id),
			AuthReady: true,
		})
	}
	return heads, nil
}

type keySpec struct {
	envVars    []string // all must be non-empty (AND) unless anyOf is true
	anyOf      bool     // at least one env var must be set
	providerID string
}

func (k keySpec) detected() bool {
	if k.anyOf {
		for _, v := range k.envVars {
			if os.Getenv(v) != "" {
				return true
			}
		}
		return false
	}
	for _, v := range k.envVars {
		if os.Getenv(v) == "" {
			return false
		}
	}
	return true
}

// knownKeys — add new API-key-based providers here.
var knownKeys = []keySpec{
	{envVars: []string{"ANTHROPIC_API_KEY"},                         providerID: "anthropic"},
	{envVars: []string{"OPENAI_API_KEY"},                            providerID: "openai"},
	{envVars: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, anyOf: true, providerID: "google"},
	{envVars: []string{"XAI_API_KEY"},                               providerID: "xai"},
	{envVars: []string{"GROQ_API_KEY"},                              providerID: "groq"},
	{envVars: []string{"TOGETHER_API_KEY"},                          providerID: "together"},
	{envVars: []string{"FIREWORKS_API_KEY"},                         providerID: "fireworks"},
	{envVars: []string{"MISTRAL_API_KEY"},                           providerID: "mistral"},
	{envVars: []string{"DEEPSEEK_API_KEY"},                          providerID: "deepseek"},
	{envVars: []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}, providerID: "bedrock"},
	{envVars: []string{"AZURE_OPENAI_API_KEY", "AZURE_OPENAI_ENDPOINT"}, providerID: "azure"},
	{envVars: []string{"PERPLEXITY_API_KEY"},                        providerID: "perplexity"},
	{envVars: []string{"COHERE_API_KEY"},                            providerID: "cohere"},
	{envVars: []string{"REPLICATE_API_TOKEN"},                       providerID: "replicate"},
}
