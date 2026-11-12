// SPDX-License-Identifier: MIT

// Package env discovers AI heads via API keys present in the environment.
// To add a new provider: add one entry to the knownKeys table.
package env

import (
	"context"
	"errors"
	"fmt"

	"github.com/ankit373/hydra/internal/awsconf"
	"github.com/ankit373/hydra/internal/capabilities"
	"github.com/ankit373/hydra/internal/keystore"
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

	// Computed once, before the loop, so a machine with no allowlist never
	// reads the pricing catalogue during discovery.
	named := allowlistedOpenRouter(caps)

	var heads []provider.Head
	for _, k := range knownKeys {
		if !k.detected() {
			continue
		}
		// An allowlist replaces the single key-derived head rather than sitting
		// beside it. Two heads on one account, one of them routing to whatever
		// OPENROUTER_MODEL happens to say, splits the account's spend between
		// them and makes neither row its real cost.
		if k.providerID == "openrouter" && len(named) > 0 {
			heads = append(heads, named...)
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
			Meta:      map[string]string{"model_source": caps.Source(id)},
		})
	}
	return heads, nil
}

type keySpec struct {
	envVars    []string // all must be non-empty (AND) unless anyOf is true
	anyOf      bool     // at least one env var must be set
	providerID string
	// detect overrides the env-var test for a provider whose credentials do not
	// live in the environment at all. Only AWS so far, which keeps its in the
	// shared config files the way every other AWS tool expects (#867).
	detect func() bool
}

func (k keySpec) detected() bool {
	if k.detect != nil {
		return k.detect()
	}
	// keystore.Lookup, not os.Getenv: a key saved in the app counts too.
	if k.anyOf {
		return keystore.Lookup(k.envVars...) != ""
	}
	for _, v := range k.envVars {
		if keystore.Lookup(v) == "" {
			return false
		}
	}
	return true
}

// knownKeys, add new API-key-based providers here.
var knownKeys = []keySpec{
	{envVars: []string{"ANTHROPIC_API_KEY"}, providerID: "anthropic"},
	{envVars: []string{"OPENAI_API_KEY"}, providerID: "openai"},
	{envVars: []string{"OPENROUTER_API_KEY"}, providerID: "openrouter"},
	{envVars: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, anyOf: true, providerID: "google"},
	{envVars: []string{"XAI_API_KEY"}, providerID: "xai"},
	{envVars: []string{"GROQ_API_KEY"}, providerID: "groq"},
	{envVars: []string{"TOGETHER_API_KEY"}, providerID: "together"},
	{envVars: []string{"FIREWORKS_API_KEY"}, providerID: "fireworks"},
	{envVars: []string{"MISTRAL_API_KEY"}, providerID: "mistral"},
	{envVars: []string{"DEEPSEEK_API_KEY"}, providerID: "deepseek"},
	{envVars: []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}, providerID: "bedrock",
		detect: func() bool { return awsconf.Resolve().Usable() }},
	{envVars: []string{"AZURE_OPENAI_API_KEY", "AZURE_OPENAI_ENDPOINT"}, providerID: "azure"},
	{envVars: []string{"PERPLEXITY_API_KEY"}, providerID: "perplexity"},
	{envVars: []string{"COHERE_API_KEY"}, providerID: "cohere"},
	{envVars: []string{"REPLICATE_API_TOKEN"}, providerID: "replicate"},
}

// KeyVar is a provider whose whole credential is one key, which is what a
// person can paste. Bedrock and Azure need more than a key and stay env-only.
type KeyVar struct {
	Provider string
	// Var is where a pasted key is filed and exported; Vars is every variable
	// discovery accepts for it, Var first (Google reads two).
	Var  string
	Vars []string
}

// KeyVars lists them in discovery order.
func KeyVars() []KeyVar {
	var out []KeyVar
	for _, k := range knownKeys {
		if k.detect == nil && (k.anyOf || len(k.envVars) == 1) {
			out = append(out, KeyVar{Provider: k.providerID, Var: k.envVars[0], Vars: k.envVars})
		}
	}
	return out
}

// LoadStoredKeys makes each stored provider key visible to keystore.Lookup,
// which discovery and the executors read through. An exported variable still
// wins, and only KeyVars names are loaded, whatever the store holds.
func LoadStoredKeys() error {
	stored, err := keystore.Names()
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, kv := range KeyVars() {
		allowed[kv.Var] = true
	}
	var errs []error
	for _, name := range stored {
		if !allowed[name] {
			continue
		}
		v, err := keystore.Get(name)
		if errors.Is(err, keystore.ErrNotFound) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		keystore.Load(name, v)
	}
	return errors.Join(errs...)
}
