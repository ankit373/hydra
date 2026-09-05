// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"encoding/json"
	"github.com/ankit373/hydra/internal/config"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// Each provider adapter builds a request in that vendor's wire format and picks
// the answer back out of that vendor's response shape. Both halves are pure
// convention — a wrong header name is a 401 the user reads as "my key is bad",
// and a wrong response field is an empty string that Hydra reports as a
// successful, empty answer. Nothing but a test catches the second one.
//
// The adapters address their vendors by hardcoded URL, so tests redirect them
// with a transport rather than by making the URLs configurable for tests alone.

// redirect returns an executor whose requests all land on srv, keeping the
// original host/path visible to the handler through r.Host and r.URL.Path.
func redirect(srv *httptest.Server) *HTTPExecutor {
	target, _ := url.Parse(srv.URL)
	return &HTTPExecutor{client: &http.Client{
		Transport: rewriteTransport{host: target.Host, base: http.DefaultTransport},
	}}
}

type rewriteTransport struct {
	host string
	base http.RoundTripper
}

func (t rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = t.host
	return t.base.RoundTrip(clone)
}

// capture records what the adapter actually put on the wire.
type capture struct {
	path    string // decoded
	rawPath string // as it went over the wire
	query   string
	host    string
	headers http.Header
	body    map[string]any
}

func serve(t *testing.T, status int, response string) (*httptest.Server, *capture) {
	t.Helper()
	got := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.query, got.host = r.URL.Path, r.URL.RawQuery, r.Host
		got.rawPath = r.URL.EscapedPath()
		got.headers = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func head(providerID string) provider.Head {
	return provider.Head{ID: providerID + "-head", Provider: providerID, Source: "env", AuthReady: true}
}

func TestExecuteAnthropic_WireFormatAndParsing(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("ANTHROPIC_MODEL", "claude-test-1")

	srv, got := serve(t, 200, `{"model":"claude-test-1","content":[
		{"type":"text","text":"first block"},{"type":"text","text":"second block"}],
		"usage":{"input_tokens":31,"output_tokens":12}}`)

	resp, err := redirect(srv).Execute(context.Background(), Request{
		Prompt: "hello", System: "be terse", MaxTokens: 256, Head: head("anthropic"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if got.host != "api.anthropic.com" || got.path != "/v1/messages" {
		t.Errorf("addressed %s%s, want api.anthropic.com/v1/messages", got.host, got.path)
	}
	// Anthropic authenticates on x-api-key, not Authorization; the version
	// header is mandatory and the API rejects the call without it.
	if k := got.headers.Get("x-api-key"); k != "sk-ant-test" {
		t.Errorf("x-api-key = %q, want the configured key", k)
	}
	if v := got.headers.Get("anthropic-version"); v == "" {
		t.Error("no anthropic-version header; the API rejects the request without one")
	}
	if got.body["system"] != "be terse" {
		t.Errorf("system = %v, want the system prompt as a top-level field, not a message",
			got.body["system"])
	}
	if got.body["max_tokens"] != float64(256) {
		t.Errorf("max_tokens = %v, want 256 — Anthropic requires it", got.body["max_tokens"])
	}
	if got.body["model"] != "claude-test-1" {
		t.Errorf("model = %v, want the env override", got.body["model"])
	}

	// Multiple content blocks must be joined, not silently truncated to the first.
	if !strings.Contains(resp.Output, "first block") || !strings.Contains(resp.Output, "second block") {
		t.Errorf("Output = %q, want both content blocks", resp.Output)
	}
	if resp.InputTokens != 31 || resp.OutputTokens != 12 {
		t.Errorf("tokens = %d/%d, want 31/12 from the provider's own usage block",
			resp.InputTokens, resp.OutputTokens)
	}
	if resp.TokensEstimated {
		t.Error("TokensEstimated = true though the provider reported real usage")
	}
}

func TestExecuteAnthropic_MaxTokensDefaultsRatherThanSendingZero(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "ANTHROPIC_API_KEY", "k")

	srv, got := serve(t, 200, `{"content":[{"type":"text","text":"ok"}]}`)
	if _, err := redirect(srv).Execute(context.Background(), Request{
		Prompt: "hi", Head: head("anthropic"),
	}); err != nil {
		t.Fatal(err)
	}
	if got.body["max_tokens"] == float64(0) || got.body["max_tokens"] == nil {
		t.Errorf("max_tokens = %v with none requested; Anthropic rejects a request "+
			"without a positive value", got.body["max_tokens"])
	}
}

func TestExecuteGemini_WireFormatAndParsing(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "GEMINI_API_KEY", "goog-test")
	t.Setenv("GEMINI_MODEL", "gemini-test-pro")

	srv, got := serve(t, 200, `{"candidates":[{"content":{"parts":[
		{"text":"part one"},{"text":"part two"}]}}],
		"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":4},
		"modelVersion":"gemini-test-pro-002"}`)

	resp, err := redirect(srv).Execute(context.Background(), Request{
		Prompt: "hello", System: "be terse", MaxTokens: 128, Head: head("google"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Gemini names the model in the path, so a model with a slash or colon must
	// be escaped rather than splitting the route.
	if !strings.Contains(got.path, "gemini-test-pro") || !strings.HasSuffix(got.path, ":generateContent") {
		t.Errorf("path = %q, want the model and :generateContent", got.path)
	}
	if k := got.headers.Get("x-goog-api-key"); k != "goog-test" {
		t.Errorf("x-goog-api-key = %q", k)
	}
	if _, ok := got.body["system_instruction"]; !ok {
		t.Error("the system prompt was dropped; Gemini takes it as system_instruction, " +
			"not as a message")
	}
	if _, ok := got.body["generationConfig"]; !ok {
		t.Error("max tokens were dropped; Gemini takes them under generationConfig")
	}

	if !strings.Contains(resp.Output, "part one") || !strings.Contains(resp.Output, "part two") {
		t.Errorf("Output = %q, want both parts joined", resp.Output)
	}
	if resp.InputTokens != 9 || resp.OutputTokens != 4 {
		t.Errorf("tokens = %d/%d, want 9/4", resp.InputTokens, resp.OutputTokens)
	}
	if resp.Model != "gemini-test-pro-002" {
		t.Errorf("Model = %q, want the modelVersion the API actually served", resp.Model)
	}
}

// Gemini returns 200 with no candidates when the prompt is filtered. That is
// not a successful empty answer — reporting it as one would log a paid call
// that produced nothing and hand the caller "".
func TestExecuteGemini_NoCandidatesIsAnError(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "GEMINI_API_KEY", "k")

	srv, _ := serve(t, 200, `{"candidates":[],"promptFeedback":{"blockReason":"SAFETY"}}`)
	if resp, err := redirect(srv).Execute(context.Background(), Request{
		Prompt: "hi", Head: head("google"),
	}); err == nil {
		t.Errorf("a filtered response was reported as success: %+v", resp)
	}
}

func TestExecuteCohere_WireFormatAndParsing(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "COHERE_API_KEY", "co-test")
	t.Setenv("COHERE_MODEL", "command-test")

	srv, got := serve(t, 200, `{"id":"x","message":{"content":[
		{"type":"text","text":"cohere says hi"}]},
		"usage":{"tokens":{"input_tokens":5,"output_tokens":3}}}`)

	resp, err := redirect(srv).Execute(context.Background(), Request{
		Prompt: "hello", MaxTokens: 64, Head: head("cohere"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if got.host != "api.cohere.ai" || got.path != "/v2/chat" {
		t.Errorf("addressed %s%s, want api.cohere.ai/v2/chat", got.host, got.path)
	}
	if a := got.headers.Get("Authorization"); a != "Bearer co-test" {
		t.Errorf("Authorization = %q", a)
	}
	if resp.Output != "cohere says hi" {
		t.Errorf("Output = %q", resp.Output)
	}
	// Cohere nests usage one level deeper than everyone else; reading it at the
	// top level yields a silent 0/0 and free-looking spend.
	if resp.InputTokens != 5 || resp.OutputTokens != 3 {
		t.Errorf("tokens = %d/%d, want 5/3 from usage.tokens", resp.InputTokens, resp.OutputTokens)
	}
}

func TestExecuteAzure_BuildsTheDeploymentURL(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "AZURE_OPENAI_API_KEY", "az-test")
	// A trailing slash on the endpoint is what a user copies out of the portal.
	t.Setenv("AZURE_OPENAI_ENDPOINT", "https://my-resource.openai.azure.com/")
	t.Setenv("AZURE_OPENAI_DEPLOYMENT", "my deployment")

	srv, got := serve(t, 200, okChatBody)

	resp, err := redirect(srv).Execute(context.Background(), Request{
		Prompt: "hello", Head: head("azure"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(got.path, "//") {
		t.Errorf("path = %q — the endpoint's trailing slash was not trimmed", got.path)
	}
	if !strings.Contains(got.rawPath, "my%20deployment") {
		t.Errorf("wire path = %q, want the deployment name escaped; a space would "+
			"otherwise break the route", got.rawPath)
	}
	if !strings.Contains(got.query, "api-version=") {
		t.Errorf("query = %q, want an api-version — Azure rejects the call without one", got.query)
	}
	if k := got.headers.Get("api-key"); k != "az-test" {
		t.Errorf("api-key = %q; Azure does not use Authorization", k)
	}
	if resp.Output != "hello from the stub" {
		t.Errorf("Output = %q", resp.Output)
	}
}

func TestExecuteAzure_EmptyChoicesIsAnError(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "AZURE_OPENAI_API_KEY", "k")
	t.Setenv("AZURE_OPENAI_ENDPOINT", "https://r.openai.azure.com")
	t.Setenv("AZURE_OPENAI_DEPLOYMENT", "d")

	srv, _ := serve(t, 200, `{"choices":[]}`)
	if resp, err := redirect(srv).Execute(context.Background(), Request{
		Prompt: "hi", Head: head("azure"),
	}); err == nil {
		t.Errorf("an empty choices array was reported as success: %+v", resp)
	}
}

// Bedrock is the only provider that signs rather than sending a bearer token.
// An unsigned request is a 403 the user cannot diagnose from Hydra's output.
func TestExecuteBedrock_SignsTheRequest(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	s.SetKey(t, "AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_REGION", "eu-west-1")

	srv, got := serve(t, 200, okChatBody)

	resp, err := redirect(srv).Execute(context.Background(), Request{
		Prompt: "hello", Head: head("bedrock"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(got.host, "eu-west-1") {
		t.Errorf("host = %q, want the configured region", got.host)
	}
	auth := got.headers.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		t.Fatalf("Authorization = %q, want a SigV4 signature", auth)
	}
	for _, want := range []string{"AKIDEXAMPLE", "eu-west-1/bedrock/aws4_request", "Signature="} {
		if !strings.Contains(auth, want) {
			t.Errorf("signature is missing %q: %s", want, auth)
		}
	}
	// SigV4 signs the payload hash, so it must be sent as a header for the
	// service to verify what it received.
	if got.headers.Get("X-Amz-Content-Sha256") == "" {
		t.Error("no X-Amz-Content-Sha256; the service cannot verify the payload")
	}
	if resp.Output != "hello from the stub" {
		t.Errorf("Output = %q", resp.Output)
	}
}

// Replicate is asynchronous: the first response is usually "starting", and the
// answer only exists after polling. Returning that first response as the answer
// would hand the caller a status object.
func TestExecuteReplicate_PollsUntilTheAnswerExists(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "REPLICATE_API_TOKEN", "r8-test")
	t.Setenv("REPLICATE_MODEL", "meta/llama-test")

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == http.MethodPost {
			// The poll URL must be on Replicate's own host — the executor
			// refuses anything else, which is what stops a hostile response
			// redirecting the authenticated poll somewhere arbitrary.
			_, _ = w.Write([]byte(`{"status":"processing","urls":{"get":"https://api.replicate.com/v1/predictions/abc"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"succeeded","output":["chunk one ","chunk two"]}`))
	}))
	defer srv.Close()

	resp, err := redirect(srv).Execute(context.Background(), Request{
		Prompt: "hello", System: "be terse", Head: head("replicate"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls < 2 {
		t.Errorf("made %d calls; a processing prediction was returned without polling", calls)
	}
	// Replicate streams output as an array of fragments that must be joined,
	// not rendered as a Go slice.
	if resp.Output != "chunk one chunk two" {
		t.Errorf("Output = %q, want the fragments joined", resp.Output)
	}
}

// A poll URL pointing anywhere but Replicate must be refused. This is the guard
// against a response steering an authenticated request at another host.
func TestGetReplicatePrediction_RefusesAForeignPollURL(t *testing.T) {
	testutil.NewSandbox(t)

	_, err := (&HTTPExecutor{}).getReplicatePrediction(
		context.Background(), "h", "https://attacker.example/v1/predictions/abc")
	if err == nil {
		t.Fatal("a poll URL on another host was accepted")
	}
	if !strings.Contains(err.Error(), "unexpected Replicate poll URL") {
		t.Errorf("error = %v, want it to name the rejected URL", err)
	}
}

// A prediction that ends in failure must surface Replicate's own message.
func TestExecuteReplicate_FailedPredictionReportsItsError(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "REPLICATE_API_TOKEN", "r8")

	srv, _ := serve(t, 200, `{"status":"failed","error":"model out of memory"}`)
	_, err := redirect(srv).Execute(context.Background(), Request{Prompt: "hi", Head: head("replicate")})
	if err == nil {
		t.Fatal("a failed prediction was reported as success")
	}
	if !strings.Contains(err.Error(), "model out of memory") {
		t.Errorf("error = %v, want Replicate's own message", err)
	}

	srv2, _ := serve(t, 200, `{"status":"canceled"}`)
	_, err = redirect(srv2).Execute(context.Background(), Request{Prompt: "hi", Head: head("replicate")})
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Errorf("a canceled prediction gave %v, want the status named", err)
	}
}

// Every adapter must turn an HTTP error into an error, not into an empty
// successful response. This is the same defect class as #260 and #310: a
// silent empty answer reads as a real one.
func TestEveryProvider_HTTPErrorIsAnError(t *testing.T) {
	providers := []string{"anthropic", "google", "cohere", "azure", "bedrock", "replicate", "openai"}

	for _, p := range providers {
		t.Run(p, func(t *testing.T) {
			s := testutil.NewSandbox(t)
			for _, kv := range [][2]string{
				{"ANTHROPIC_API_KEY", "k"}, {"GEMINI_API_KEY", "k"}, {"COHERE_API_KEY", "k"},
				{"AZURE_OPENAI_API_KEY", "k"}, {"REPLICATE_API_TOKEN", "k"}, {"OPENAI_API_KEY", "k"},
				{"AWS_ACCESS_KEY_ID", "k"}, {"AWS_SECRET_ACCESS_KEY", "k"},
			} {
				s.SetKey(t, kv[0], kv[1])
			}
			t.Setenv("AWS_REGION", "us-east-1")
			t.Setenv("AZURE_OPENAI_ENDPOINT", "https://r.openai.azure.com")
			t.Setenv("AZURE_OPENAI_DEPLOYMENT", "d")

			srv, _ := serve(t, http.StatusTooManyRequests, `{"error":{"message":"rate limited"}}`)
			resp, err := redirect(srv).Execute(context.Background(), Request{Prompt: "hi", Head: head(p)})
			if err == nil {
				t.Fatalf("HTTP 429 was reported as success: %+v", resp)
			}
			if !strings.Contains(err.Error(), "429") {
				t.Errorf("error = %v, want the status code so the user can act on it", err)
			}
		})
	}
}

// SupportsHTTP is what stops dispatch routing to a head it cannot drive. Each
// provider has its own set of required settings, and a head that reports as
// supported without them fails at execution time instead of at selection time.
func TestSupportsHTTP_RequiresEveryProvidersOwnSettings(t *testing.T) {
	tests := []struct {
		provider string
		env      map[string]string
	}{
		{"anthropic", map[string]string{"ANTHROPIC_API_KEY": "k"}},
		{"google", map[string]string{"GEMINI_API_KEY": "k"}},
		{"cohere", map[string]string{"COHERE_API_KEY": "k"}},
		{"azure", map[string]string{
			"AZURE_OPENAI_API_KEY": "k", "AZURE_OPENAI_ENDPOINT": "https://r.openai.azure.com",
			"AZURE_OPENAI_DEPLOYMENT": "d",
		}},
		{"bedrock", map[string]string{
			"AWS_ACCESS_KEY_ID": "k", "AWS_SECRET_ACCESS_KEY": "s", "AWS_REGION": "us-east-1",
		}},
		{"replicate", map[string]string{"REPLICATE_API_TOKEN": "k", "REPLICATE_MODEL": "m/n"}},
		{"openai", map[string]string{"OPENAI_API_KEY": "k"}},
	}

	for _, tt := range tests {
		t.Run(tt.provider+" unconfigured", func(t *testing.T) {
			testutil.NewSandbox(t)
			if SupportsHTTP(head(tt.provider)) {
				t.Errorf("%s reports as supported with nothing configured; dispatch "+
					"would route to it and fail at execution", tt.provider)
			}
		})
		t.Run(tt.provider+" configured", func(t *testing.T) {
			testutil.NewSandbox(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			if !SupportsHTTP(head(tt.provider)) {
				t.Errorf("%s reports as unsupported with %v set", tt.provider, tt.env)
			}
		})
	}

	// Azure needs three settings, so any one missing must disqualify it — this
	// is where a partial config silently produces a broken URL.
	for _, missing := range []string{"AZURE_OPENAI_API_KEY", "AZURE_OPENAI_ENDPOINT", "AZURE_OPENAI_DEPLOYMENT"} {
		t.Run("azure without "+missing, func(t *testing.T) {
			testutil.NewSandbox(t)
			for _, k := range []string{"AZURE_OPENAI_API_KEY", "AZURE_OPENAI_ENDPOINT", "AZURE_OPENAI_DEPLOYMENT"} {
				if k != missing {
					t.Setenv(k, "set")
				}
			}
			if SupportsHTTP(head("azure")) {
				t.Errorf("azure reports as supported without %s", missing)
			}
		})
	}

	testutil.NewSandbox(t)
	if SupportsHTTP(provider.Head{ID: "x", Provider: "some-vendor-we-do-not-speak"}) {
		t.Error("an unknown provider reports as supported")
	}
}

// The Azure API version has a default because the service requires one, but an
// operator pinning an older one must win.
func TestAzureAPIVersion_DefaultsButIsOverridable(t *testing.T) {
	testutil.NewSandbox(t)
	if got := azureAPIVersion(); got == "" {
		t.Error("azureAPIVersion() is empty; Azure rejects a request without it")
	}
	t.Setenv("AZURE_OPENAI_API_VERSION", "2023-05-15")
	if got := azureAPIVersion(); got != "2023-05-15" {
		t.Errorf("azureAPIVersion() = %q, want the operator's pin", got)
	}
}

// Region and credential lookups accept the standard AWS variable names, in the
// order the AWS SDKs themselves resolve them.
func TestAWSEnvLookups(t *testing.T) {
	testutil.NewSandbox(t)
	if bedrockRegion() != "" || awsAccessKeyID() != "" || awsSecretAccessKey() != "" || awsSessionToken() != "" {
		t.Fatal("the sandbox did not clear the AWS environment")
	}

	t.Setenv("AWS_DEFAULT_REGION", "ap-south-1")
	if got := bedrockRegion(); got != "ap-south-1" {
		t.Errorf("bedrockRegion() = %q, want the AWS_DEFAULT_REGION fallback", got)
	}
	t.Setenv("AWS_REGION", "us-west-2")
	if got := bedrockRegion(); got != "us-west-2" {
		t.Errorf("bedrockRegion() = %q, want AWS_REGION to win", got)
	}

	t.Setenv("AWS_SESSION_TOKEN", "tok")
	if got := awsSessionToken(); got != "tok" {
		t.Errorf("awsSessionToken() = %q", got)
	}
}

// defaultModelFor resolves a model without a rebuild. A provider with no
// fallback (Replicate, where the model is the identity of what runs) must
// return empty rather than a guess.
func TestDefaultModelFor_EnvOverridesAndReplicateHasNoDefault(t *testing.T) {
	testutil.NewSandbox(t)

	if got := defaultModelFor("anthropic"); got == "" {
		t.Error("anthropic has no default model")
	}
	if got := defaultModelFor("replicate"); got != "" {
		t.Errorf("defaultModelFor(replicate) = %q; there is no sensible default — "+
			"the model is what the user is paying to run", got)
	}
	if got := defaultModelFor("not-a-provider"); got != "" {
		t.Errorf("defaultModelFor(unknown) = %q", got)
	}

	// The vendor-specific variable and Hydra's own both work, vendor first.
	t.Setenv("HYDRA_MODEL_ANTHROPIC", "hydra-pinned")
	if got := defaultModelFor("anthropic"); got != "hydra-pinned" {
		t.Errorf("defaultModelFor(anthropic) = %q, want the HYDRA_ override", got)
	}
	t.Setenv("ANTHROPIC_MODEL", "vendor-pinned")
	if got := defaultModelFor("anthropic"); got != "vendor-pinned" {
		t.Errorf("defaultModelFor(anthropic) = %q, want the vendor variable to win", got)
	}
}

// Replicate's output is untyped JSON: a string, an array of fragments, or a
// number. All three must render as text a human can read.
func TestStringifyAny_HandlesEveryReplicateOutputShape(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"plain string", "hello", "hello"},
		{"streamed token fragments concatenate", []any{"a ", "b"}, "a b"},
		{"number", float64(42), "42"},
		{"nil is empty, not \"<nil>\"", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stringifyAny(tt.in); got != tt.want {
				t.Errorf("stringifyAny(%#v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
	// An object falls back to JSON rather than Go's %v, which would emit
	// map[k:v] — not something a user or a downstream parser can use.
	if got := stringifyAny(map[string]any{"k": "v"}); !strings.Contains(got, `"k"`) {
		t.Errorf("stringifyAny(map) = %q, want JSON", got)
	}
}

// A context deadline must abort a poll rather than running to the five-minute
// ceiling.
func TestExecuteReplicate_HonoursContextCancellation(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "REPLICATE_API_TOKEN", "k")

	srv, _ := serve(t, 200, `{"status":"processing","urls":{"get":"https://api.replicate.com/v1/predictions/abc"}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := redirect(srv).Execute(ctx, Request{Prompt: "hi", Head: head("replicate")}); err == nil {
		t.Fatal("a cancelled poll returned success")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v to notice cancellation", elapsed)
	}
}

// The sandbox can only hide a variable it knows about. This keeps
// testutil.ModelPinVars in step with the table defaultModelFor actually reads,
// so adding a provider cannot quietly reintroduce the leak: a developer with
// the new variable exported would otherwise have every model-resolution test
// read their shell.
//
// Mirrors TestAPIKeyVars_CoversEnvProvider, which does the same for credentials.
func TestModelPinVars_CoversDefaultModelFor(t *testing.T) {
	scrubbed := map[string]bool{}
	for _, v := range testutil.ModelPinVars {
		scrubbed[v] = true
	}

	// Every provider defaultModelFor knows about, and the variables it consults
	// for each. Derived by asking it: set each candidate and see if it wins.
	providers := []string{
		"anthropic", "openai", "openrouter", "google", "xai", "groq", "together",
		"fireworks", "mistral", "deepseek", "bedrock", "perplexity", "cohere", "replicate",
	}

	for _, p := range providers {
		t.Run(p, func(t *testing.T) {
			testutil.NewSandbox(t)
			base := defaultModelFor(p)

			// Any variable that can change this provider's answer must be
			// scrubbed. Probe the two naming conventions the table uses.
			candidates := []string{
				strings.ToUpper(p) + "_MODEL",
				"HYDRA_MODEL_" + strings.ToUpper(p),
			}
			for _, env := range candidates {
				t.Setenv(env, "probe-value")
				changed := defaultModelFor(p) != base
				t.Setenv(env, "")
				if changed && !scrubbed[env] {
					t.Errorf("%s changes defaultModelFor(%s) but testutil.ModelPinVars "+
						"does not clear it — a developer with it exported gets different "+
						"test results", env, p)
				}
			}
		})
	}

	// The sandbox must actually leave them empty, whatever the developer has set.
	t.Setenv("ANTHROPIC_MODEL", "leaked-from-the-shell")
	t.Setenv("AWS_REGION", "leaked-region")
	testutil.NewSandbox(t)
	if got := defaultModelFor("anthropic"); got == "leaked-from-the-shell" {
		t.Error("the sandbox did not clear ANTHROPIC_MODEL")
	}
	if got := bedrockRegion(); got != "" {
		t.Errorf("bedrockRegion() = %q inside a sandbox, want empty", got)
	}
}

// Every adapter decodes its vendor's response shape. A 200 with a body that
// does not parse must be an error, not an empty successful answer — each
// adapter has its own decode call, so each needs its own case.
func TestEveryProvider_UnparsableBodyIsAnError(t *testing.T) {
	providers := []string{"anthropic", "google", "cohere", "azure", "bedrock", "replicate", "openai"}

	for _, p := range providers {
		t.Run(p, func(t *testing.T) {
			s := testutil.NewSandbox(t)
			for _, kv := range [][2]string{
				{"ANTHROPIC_API_KEY", "k"}, {"GEMINI_API_KEY", "k"}, {"COHERE_API_KEY", "k"},
				{"AZURE_OPENAI_API_KEY", "k"}, {"REPLICATE_API_TOKEN", "k"}, {"OPENAI_API_KEY", "k"},
				{"AWS_ACCESS_KEY_ID", "k"}, {"AWS_SECRET_ACCESS_KEY", "k"},
			} {
				s.SetKey(t, kv[0], kv[1])
			}
			t.Setenv("AWS_REGION", "us-east-1")
			t.Setenv("AZURE_OPENAI_ENDPOINT", "https://r.openai.azure.com")
			t.Setenv("AZURE_OPENAI_DEPLOYMENT", "d")
			t.Setenv("REPLICATE_MODEL", "m/n")

			srv, _ := serve(t, http.StatusOK, `{"truncated`)
			resp, err := redirect(srv).Execute(context.Background(), Request{
				Prompt: "hi", Head: head(p),
			})
			if err == nil {
				t.Fatalf("an unparsable 200 body was reported as success: %+v", resp)
			}
			if !strings.Contains(err.Error(), "decode") && !strings.Contains(err.Error(), "parse") {
				t.Errorf("error = %v, want it to say the response could not be read", err)
			}
		})
	}
}

// recoverAgySwap is best-effort by design: it must not panic or corrupt the
// file when it has become unreadable underneath it, and it must not conjure a
// settings.json that was never there.
func TestRecoverAgySwap_IsBestEffortOnBadInput(t *testing.T) {
	testutil.NewSandbox(t)

	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.json")
	// With no sentinel, recovery must not create a file that wasn't there.
	recoverAgySwap(missing)
	if _, err := os.Stat(missing); err == nil {
		t.Error("a settings.json was created from nothing")
	}

	// A sentinel with no settings file beside it: recovery writes the sentinel
	// back, which is the whole point — the settings file was lost mid-swap.
	lost := filepath.Join(dir, "lost.json")
	if err := os.WriteFile(lost+agyOrigSuffix, []byte(`{"model":"restored"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	recoverAgySwap(lost)
	raw, err := os.ReadFile(lost)
	if err != nil {
		t.Fatalf("recovery did not restore a settings file from its sentinel: %v", err)
	}
	if !strings.Contains(string(raw), "restored") {
		t.Errorf("recovered content = %q", raw)
	}
}

// writeAuthRequired is best-effort observability: an auth failure must still be
// reported to the caller even when the record cannot be written.
func TestWriteAuthRequired_UnwritableLogDirIsSilent(t *testing.T) {
	testutil.NewSandbox(t)

	// Dir() is a regular file, so logs/ cannot be created. The sandbox
	// pre-creates it as an empty directory, so remove that first.
	if err := os.RemoveAll(config.Dir()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Dir(), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The assertion is that this returns at all.
	writeAuthRequired("pool", "model", "https://example.com/auth")
}
