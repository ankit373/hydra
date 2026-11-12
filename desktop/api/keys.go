// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/health"
	"github.com/ankit373/hydra/internal/keystore"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/provider/env"
)

// keyTestTimeout bounds the one call a key test makes. A cold provider can take
// several seconds to answer; past this the person is better told than kept waiting.
const keyTestTimeout = 30 * time.Second

// providerInfo is what a person needs to go and get a key: a name they
// recognise and the page that issues one.
type providerInfo struct{ label, keyURL string }

var providerInfos = map[string]providerInfo{
	"anthropic":  {"Anthropic (Claude)", "https://console.anthropic.com/settings/keys"},
	"openai":     {"OpenAI (ChatGPT)", "https://platform.openai.com/api-keys"},
	"openrouter": {"OpenRouter (many models, one key)", "https://openrouter.ai/keys"},
	"google":     {"Google Gemini", "https://aistudio.google.com/apikey"},
	"xai":        {"xAI (Grok)", "https://console.x.ai"},
	"groq":       {"Groq", "https://console.groq.com/keys"},
	"together":   {"Together AI", "https://api.together.ai/settings/api-keys"},
	"fireworks":  {"Fireworks AI", "https://fireworks.ai/account/api-keys"},
	"mistral":    {"Mistral", "https://console.mistral.ai/api-keys"},
	"deepseek":   {"DeepSeek", "https://platform.deepseek.com/api_keys"},
	"perplexity": {"Perplexity", "https://www.perplexity.ai/settings/api"},
	"cohere":     {"Cohere", "https://dashboard.cohere.com/api-keys"},
	"replicate":  {"Replicate", "https://replicate.com/account/api-tokens"},
}

// ProviderKey is one provider a key can be added for. The key itself never
// crosses to the frontend: Hint is its last four characters, and only for a
// key long enough that four characters give nothing away.
type ProviderKey struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
	KeyURL   string `json:"keyUrl"`
	EnvVar   string `json:"envVar"`

	// Source is the key in use: "stored" (saved here), "environment"
	// (exported outside Hydra, which wins over a stored one) or "" (none).
	Source string `json:"source"`
	Hint   string `json:"hint,omitempty"`
	// Saved is whether the store holds a key, in use or not, so one shadowed
	// by an export can still be removed.
	Saved bool `json:"saved"`
}

// ProviderKeys is the Providers panel.
type ProviderKeys struct {
	Keys []ProviderKey `json:"keys"`
	// Store says where saved keys live, so "stored" has a concrete meaning.
	Store string `json:"store"`
	Error string `json:"error,omitempty"`
}

// KeyTest is the outcome of one cheap call made with a provider's key.
type KeyTest struct {
	OK bool `json:"ok"`
	// Accepted is true when the provider took the key, including when the
	// account then refused the request for credit or rate (402, 429).
	Accepted bool   `json:"accepted"`
	Model    string `json:"model,omitempty"`
	Reply    string `json:"reply,omitempty"`
	Error    string `json:"error,omitempty"`
}

// GetProviderKeys lists every provider a pasted key can enable.
func (a *API) GetProviderKeys() ProviderKeys {
	out := ProviderKeys{Keys: []ProviderKey{}, Store: keystore.Backend()}
	stored := map[string]bool{}
	names, err := keystore.Names()
	if err != nil {
		out.Error = "Saved keys could not be read: " + err.Error()
	}
	for _, n := range names {
		stored[n] = true
	}
	for _, kv := range env.KeyVars() {
		info := providerInfos[kv.Provider]
		k := ProviderKey{Provider: kv.Provider, Label: info.label, KeyURL: info.keyURL, EnvVar: kv.Var, Saved: stored[kv.Var]}
		// Read what this process loaded, never the store: listing must not
		// open the keychain once per row.
		inUse := keystore.Lookup(kv.Vars...)
		switch {
		case exportedByUser(kv):
			k.Source, k.Hint = "environment", hint(inUse)
		case k.Saved && inUse == "":
			k.Source = "stored"
			out.Error = fmt.Sprintf("Hydra could not read the saved %s key. Replace it, or remove it and add it again.", info.label)
		case k.Saved:
			k.Source, k.Hint = "stored", hint(inUse)
		}
		out.Keys = append(out.Keys, k)
	}
	return out
}

// SaveProviderKey stores key for provider and makes it live in this process.
// An exported key keeps winning (keystore.Lookup).
func (a *API) SaveProviderKey(providerID, key string) ProviderKeys {
	kv, ok := keyVarFor(providerID)
	if !ok {
		return a.keysWithError("unknown provider: " + providerID)
	}
	if err := keystore.Set(kv.Var, key); err != nil {
		return a.keysWithError("The key was not saved: " + redact(err.Error(), key))
	}
	keystore.Load(kv.Var, strings.TrimSpace(key))
	dispatch.ForgetProbe()
	return a.GetProviderKeys()
}

// RemoveProviderKey deletes the stored key and withdraws it from this
// process. A key the user exported is outside Hydra and stays.
func (a *API) RemoveProviderKey(providerID string) ProviderKeys {
	kv, ok := keyVarFor(providerID)
	if !ok {
		return a.keysWithError("unknown provider: " + providerID)
	}
	if err := keystore.Delete(kv.Var); err != nil {
		return a.keysWithError("The key was not removed: " + err.Error())
	}
	keystore.Unload(kv.Var)
	dispatch.ForgetProbe()
	return a.GetProviderKeys()
}

// TestProviderKey sends one tiny request with the key in use for provider.
// It is a real call, so it costs a fraction of a cent, and it is not logged as
// a dispatch: it tests a credential, not a route.
func (a *API) TestProviderKey(providerID string) KeyTest {
	kv, ok := keyVarFor(providerID)
	if !ok {
		return KeyTest{Error: "unknown provider: " + providerID}
	}
	key := keystore.Lookup(kv.Vars...)
	if key == "" {
		return KeyTest{Error: "Add a key first."}
	}
	head := provider.Head{ID: "env/" + providerID, Provider: providerID, Source: "env", AuthReady: true}
	if !executor.SupportsHTTP(head) {
		return KeyTest{Error: "This provider needs a model name as well as a key, which the app cannot set yet."}
	}

	ctx, cancel := context.WithTimeout(context.Background(), keyTestTimeout)
	defer cancel()
	res, err := (&executor.HTTPExecutor{}).Execute(ctx, executor.Request{
		Head:      head,
		Prompt:    "Reply with one word: ready",
		MaxTokens: 16,
	})
	if err != nil {
		var se *executor.StatusError
		accepted := errors.As(err, &se) && (se.Code == 402 || se.Code == 429)
		return KeyTest{Accepted: accepted, Error: redact(keyTestError(err), key)}
	}
	// A head parked for failing with an earlier, wrong key must not stay
	// parked now that this one has answered.
	hs := health.Open(health.DefaultPath())
	hs.Pass(head.ID)
	_ = hs.Flush()
	return KeyTest{OK: true, Accepted: true, Model: res.Model, Reply: redact(strings.TrimSpace(res.Output), key)}
}

// keyTestError puts the provider's answer in words a person can act on. The
// raw detail stays appended, because "rejected" alone cannot be searched for.
func keyTestError(err error) string {
	var se *executor.StatusError
	if !errors.As(err, &se) {
		if errors.Is(err, context.DeadlineExceeded) {
			return "The provider did not answer in time. Check your internet connection and try again."
		}
		return "The test could not reach the provider: " + err.Error()
	}
	switch se.Code {
	case 401, 403:
		return "The provider rejected this key. Check that you copied all of it, and that it has not been revoked."
	case 402, 429:
		return fmt.Sprintf("The key works, but the account cannot take requests right now (out of credit or rate-limited, HTTP %d).", se.Code)
	}
	return "The provider answered with an error: " + err.Error()
}

func (a *API) keysWithError(msg string) ProviderKeys {
	out := a.GetProviderKeys()
	out.Error = msg
	return out
}

// exportedByUser reports a key for kv set outside Hydra, under any name
// discovery accepts for it.
func exportedByUser(kv env.KeyVar) bool {
	for _, v := range kv.Vars {
		if strings.TrimSpace(os.Getenv(v)) != "" {
			return true
		}
	}
	return false
}

func keyVarFor(providerID string) (env.KeyVar, bool) {
	for _, kv := range env.KeyVars() {
		if kv.Provider == providerID {
			return kv, true
		}
	}
	return env.KeyVar{}, false
}

// hint is the last four characters, and nothing for a value so short that
// four of its characters would be a real fraction of it.
func hint(key string) string {
	if len(key) < 16 {
		return ""
	}
	return key[len(key)-4:]
}

// redact keeps a key out of a message even when a provider echoes it back.
func redact(msg, key string) string {
	if key = strings.TrimSpace(key); key == "" {
		return msg
	}
	return strings.ReplaceAll(msg, key, "[your key]")
}
