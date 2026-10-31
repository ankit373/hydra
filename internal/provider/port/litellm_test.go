// SPDX-License-Identifier: MIT

package port

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/capabilities"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
)

// modelInfoFixture is what a real LiteLLM 1.80 proxy answered on /model/info,
// trimmed to the fields read here and otherwise untouched. One model whose
// upstream is this machine's Ollama, one whose upstream is OpenAI, which is the
// distinction the whole service exists to make.
const modelInfoFixture = `{
  "data": [
    {
      "model_name": "local-qwen",
      "litellm_params": {"api_base": "http://localhost:11434", "model": "ollama/qwen3:0.6b"},
      "model_info": {"max_input_tokens": 40960, "litellm_provider": "ollama", "mode": "chat"}
    },
    {
      "model_name": "cloud-gpt",
      "litellm_params": {"model": "openai/gpt-4o-mini"},
      "model_info": {"max_input_tokens": 128000, "litellm_provider": "openai", "mode": "chat"}
    }
  ]
}`

// litellmStub serves the two endpoints the probe calls. modelInfo is the body
// for /model/info, or "" to answer the way a keyless proxy really does: 500,
// not 401, measured against 1.80.
func litellmStub(t *testing.T, alive int, modelInfo string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health/liveliness", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(alive)
		_, _ = w.Write([]byte(`"I'm alive!"`))
	})
	mux.HandleFunc("/model/info", func(w http.ResponseWriter, _ *http.Request) {
		if modelInfo == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(modelInfo))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func probeStub(t *testing.T, srv *httptest.Server, key string) []provider.Head {
	t.Helper()
	caps, err := capabilities.Load("")
	if err != nil {
		t.Fatal(err)
	}
	svc := &litellmService{base: srv.URL, key: key}
	heads, err := svc.probe(context.Background(), caps)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	return heads
}

func headByID(t *testing.T, heads []provider.Head, id string) provider.Head {
	t.Helper()
	for _, h := range heads {
		if h.ID == id {
			return h
		}
	}
	t.Fatalf("no head %q in %v", id, ids(heads))
	return provider.Head{}
}

func ids(heads []provider.Head) []string {
	out := make([]string, 0, len(heads))
	for _, h := range heads {
		out = append(out, h.ID)
	}
	return out
}

// The point of the service: a proxy on localhost says nothing about where a
// given model runs, so locality is read per model from the upstream.
func TestLiteLLM_LocalityIsPerModelNotPerAddress(t *testing.T) {
	heads := probeStub(t, litellmStub(t, http.StatusOK, modelInfoFixture), "k")

	if len(heads) != 2 {
		t.Fatalf("heads = %v, want one per model", ids(heads))
	}
	if local := headByID(t, heads, "litellm/local-qwen"); !local.LocalOnly {
		t.Error("a model whose upstream is this machine's Ollama is not marked local")
	}
	cloud := headByID(t, heads, "litellm/cloud-gpt")
	if cloud.LocalOnly {
		t.Error("a model whose upstream is OpenAI is marked local: the local-only PII policy " +
			"would hand it content believing it never leaves the machine")
	}
	if got := cloud.Meta["litellm_upstream"]; got != "openai" {
		t.Errorf("upstream = %q, want openai recorded so the verdict is readable", got)
	}
}

// Being wrong about locality is only cheap in one direction, so an upstream
// that is not reported at all must not be guessed as local.
func TestLiteLLM_UnreportedUpstreamIsNotLocal(t *testing.T) {
	const noProvider = `{"data":[{"model_name":"mystery","litellm_params":{},"model_info":{}}]}`
	heads := probeStub(t, litellmStub(t, http.StatusOK, noProvider), "k")

	h := headByID(t, heads, "litellm/mystery")
	if h.LocalOnly {
		t.Error("a model with no reported upstream was assumed local")
	}
	if h.Meta["litellm_upstream"] != "unreported" {
		t.Errorf("upstream meta = %q, want it to say the proxy reported none", h.Meta["litellm_upstream"])
	}
}

// The other half of the same mistake: UITier puts any local head at 10, the
// free floor, so a proxy forwarding to a paid model must never land there.
func TestLiteLLM_AForwardingModelIsNotPricedAsFree(t *testing.T) {
	heads := probeStub(t, litellmStub(t, http.StatusOK, modelInfoFixture), "k")

	cloud := headByID(t, heads, "litellm/cloud-gpt")
	if tier := rank.UITier(cloud); tier == 10 {
		t.Errorf("tier = %d, the free local floor, for a model the proxy forwards to OpenAI", tier)
	}
	if tier := rank.UITier(headByID(t, heads, "litellm/local-qwen")); tier != 10 {
		t.Errorf("tier = %d for a genuinely local model, want the free floor 10", tier)
	}
}

// Each model is its own head all the way through ranking. Without the alias in
// Meta["model"] they collapse to one per provider, which is what happened to a
// three-model OpenRouter allowlist (#752).
//
// Two *forwarding* models, deliberately: a local head is keyed on its id
// whatever its meta says, so a fixture with one of each would survive the
// collapse for a reason that has nothing to do with the alias.
func TestLiteLLM_ForwardedModelsSurviveRanking(t *testing.T) {
	const twoCloud = `{"data":[
	  {"model_name":"gpt","litellm_params":{"model":"openai/gpt-4o-mini"},"model_info":{"litellm_provider":"openai"}},
	  {"model_name":"claude","litellm_params":{"model":"anthropic/claude-sonnet-4-5"},"model_info":{"litellm_provider":"anthropic"}}
	]}`
	heads := probeStub(t, litellmStub(t, http.StatusOK, twoCloud), "k")

	if got := headByID(t, heads, "litellm/gpt").Meta["model"]; got != "gpt" {
		t.Errorf("Meta[model] = %q, want the alias the proxy routes on", got)
	}
	ranked := rank.ByCapScore(heads)
	if len(ranked) != 2 {
		t.Errorf("ranking kept %v, want both models: they differ only by model, "+
			"so keying them on the provider loses one", ids(ranked))
	}
}

// A proxy Hydra cannot read is still a proxy that is running. Reporting
// nothing would read as "no LiteLLM here" when the truth is "no key" (#248).
func TestLiteLLM_UnreadableProxyIsReportedWithItsReason(t *testing.T) {
	heads := probeStub(t, litellmStub(t, http.StatusOK, ""), "")

	if len(heads) != 1 {
		t.Fatalf("heads = %v, want the proxy itself reported once", ids(heads))
	}
	reason := executor.Unroutable(heads[0])
	if reason == "" {
		t.Fatal("a proxy whose models could not be listed is presented as routable")
	}
	if !strings.Contains(reason, "LITELLM_PROXY_API_KEY") {
		t.Errorf("reason = %q, want it to name the variable that fixes it", reason)
	}
}

// With a key that the proxy rejected, telling the user to set the variable they
// have already set is worse than useless.
func TestLiteLLM_ARejectedKeySaysSo(t *testing.T) {
	heads := probeStub(t, litellmStub(t, http.StatusOK, ""), "wrong-key")

	reason := executor.Unroutable(heads[0])
	if !strings.Contains(reason, "rejected") {
		t.Errorf("reason = %q, want it to say the key it was given was refused", reason)
	}
}

// Something else listening on 4000 is not a LiteLLM proxy, and must not be
// reported as one.
func TestLiteLLM_AnotherServiceOnThePortIsNotAProxy(t *testing.T) {
	caps, err := capabilities.Load("")
	if err != nil {
		t.Fatal(err)
	}
	srv := litellmStub(t, http.StatusNotFound, modelInfoFixture)
	svc := &litellmService{base: srv.URL}

	if heads, err := svc.probe(context.Background(), caps); err == nil {
		t.Errorf("probe succeeded with %v against a service that serves no liveliness endpoint", ids(heads))
	}
}

// The window the proxy reports, not one Hydra invents (#764).
func TestLiteLLM_CarriesTheReportedContextWindow(t *testing.T) {
	heads := probeStub(t, litellmStub(t, http.StatusOK, modelInfoFixture), "k")

	if got := headByID(t, heads, "litellm/local-qwen").Meta["model_ctx_max"]; got != "40960" {
		t.Errorf("model_ctx_max = %q, want the 40960 the proxy reported", got)
	}
}

func TestLiteLLMHost_DefaultsAndHonoursTheClientVariable(t *testing.T) {
	t.Setenv("LITELLM_PROXY_URL", "")
	if got := newLiteLLMService().base; got != DefaultLiteLLMHost {
		t.Errorf("base = %q, want %q", got, DefaultLiteLLMHost)
	}
	t.Setenv("LITELLM_PROXY_URL", "http://localhost:4444")
	if got := newLiteLLMService().addr(); got != "localhost:4444" {
		t.Errorf("addr = %q, want the address the variable named", got)
	}
	// The cleartext rule the Ollama host resolver already applies: a prompt is
	// the user's source code and does not go to a remote host in the clear.
	t.Setenv("LITELLM_PROXY_URL", "http://proxy.example.com:4000")
	if got := newLiteLLMService().base; got != DefaultLiteLLMHost {
		t.Errorf("base = %q, want the default: plain http to a remote host is refused", got)
	}
}
