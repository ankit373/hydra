// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ankit373/hydra/internal/provider"
)

const (
	// sglangHostEnv moves the local decision server off SGLang's default address.
	sglangHostEnv     = "HYDRA_SGLANG_HOST"
	defaultSGLangHost = "http://127.0.0.1:30000"

	// decisionProbeTimeout bounds the info call, so a dead local port costs discovery little.
	decisionProbeTimeout = 2 * time.Second
)

// hostedDecisionSource is a decision API reached with an API key.
type hostedDecisionSource struct {
	provider string
	baseURL  string
	model    string
	// modelEnvs override model, in the variables the vendor's own SDK reads.
	modelEnvs []string
}

// hostedDecisionSources is the declared order, which routing asks in until calibration says otherwise.
var hostedDecisionSources = []hostedDecisionSource{
	{
		provider:  "typesafe",
		baseURL:   "https://api.typesafe.ai",
		model:     "jev-latest",
		modelEnvs: []string{"TYPESAFE_DEFAULT_MODEL"},
	},
	{
		provider: "openai",
		baseURL:  "https://api.openai.com",
		// The only model the Decisions API accepts.
		model: "gpt-6-luna",
	},
}

// DecisionHeads finds every decision source on this machine: a local server
// first, since it costs nothing, then each hosted API whose key is set.
func DecisionHeads(ctx context.Context) []provider.Head {
	var heads []provider.Head
	if h, ok := localDecisionHead(ctx, provider.HostFromEnv(sglangHostEnv, defaultSGLangHost)); ok {
		heads = append(heads, h)
	}
	for _, src := range hostedDecisionSources {
		if apiKeyFor(src.provider) == "" {
			continue
		}
		heads = append(heads, provider.Head{
			ID:        decisionKind + "/" + src.provider,
			Name:      src.provider + " decisions",
			Provider:  src.provider,
			Source:    "env",
			Endpoint:  src.baseURL,
			AuthReady: true,
			Meta:      map[string]string{"model": firstNonEmpty(firstEnv(src.modelEnvs...), src.model)},
		})
	}
	return heads
}

// localDecisionHead identifies an SGLang server by what its info endpoint says,
// never by its port: something else listening there must not become a head.
func localDecisionHead(ctx context.Context, base string) (provider.Head, bool) {
	ctx, cancel := context.WithTimeout(ctx, decisionProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/get_model_info", nil)
	if err != nil {
		return provider.Head{}, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return provider.Head{}, false
	}
	defer resp.Body.Close()
	var info struct {
		ModelPath string `json:"model_path"`
	}
	body := io.LimitReader(resp.Body, 1<<20)
	if resp.StatusCode != http.StatusOK || json.NewDecoder(body).Decode(&info) != nil || info.ModelPath == "" {
		return provider.Head{}, false
	}
	return provider.Head{
		ID:        fmt.Sprintf("%s/sglang/%s", decisionKind, info.ModelPath),
		Name:      info.ModelPath + " (SGLang decisions)",
		Provider:  "sglang",
		Source:    "port",
		Endpoint:  base,
		LocalOnly: true,
		AuthReady: true,
		Meta:      map[string]string{"model": info.ModelPath},
	}, true
}
