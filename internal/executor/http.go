package executor

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/provider"
)

// HTTPExecutor runs prompts against HTTP-based providers.
type HTTPExecutor struct {
	client *http.Client
}

func (e *HTTPExecutor) httpClient() *http.Client {
	if e.client != nil {
		return e.client
	}
	return http.DefaultClient
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatRequest struct {
	Model     string    `json:"model,omitempty"`
	Messages  []message `json:"messages"`
	MaxTokens int       `json:"max_tokens,omitempty"`
	Stream    bool      `json:"stream"`
}

type openAIChatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type openAICompatConfig struct {
	BaseURL string
	Model   string
	Headers map[string]string
}

var errUnsupportedHTTPProvider = errors.New("provider is discovered but not executable via Hydra's HTTP executors")

func (e *HTTPExecutor) Execute(ctx context.Context, req Request) (*Response, error) {
	switch req.Head.Provider {
	case "anthropic":
		return e.executeAnthropic(ctx, req)
	case "google":
		return e.executeGemini(ctx, req)
	case "cohere":
		return e.executeCohere(ctx, req)
	case "azure":
		return e.executeAzureOpenAI(ctx, req)
	case "bedrock":
		return e.executeBedrock(ctx, req)
	case "replicate":
		return e.executeReplicate(ctx, req)
	default:
		cfg, err := openAICompatConfigFor(req.Head)
		if err != nil {
			return nil, fmt.Errorf("http exec %s: %w", req.Head.ID, err)
		}
		return e.executeOpenAICompatible(ctx, req, cfg)
	}
}

// SupportsHTTP reports whether Hydra can execute the head over HTTP today.
func SupportsHTTP(h provider.Head) bool {
	switch h.Provider {
	case "anthropic":
		return apiKeyFor("anthropic") != "" && defaultModelFor("anthropic") != ""
	case "google":
		return apiKeyFor("google") != "" && defaultModelFor("google") != ""
	case "cohere":
		return apiKeyFor("cohere") != "" && defaultModelFor("cohere") != ""
	case "azure":
		return apiKeyFor("azure") != "" && azureEndpoint() != "" && azureDeployment() != ""
	case "bedrock":
		return awsAccessKeyID() != "" && awsSecretAccessKey() != "" && bedrockRegion() != "" && defaultModelFor("bedrock") != ""
	case "replicate":
		return apiKeyFor("replicate") != "" && defaultModelFor("replicate") != ""
	default:
		_, err := openAICompatConfigFor(h)
		return err == nil
	}
}

func (e *HTTPExecutor) executeOpenAICompatible(ctx context.Context, req Request, cfg openAICompatConfig) (*Response, error) {
	msgs := buildMessages(req)
	body := openAIChatRequest{
		Model:     cfg.Model,
		Messages:  msgs,
		MaxTokens: req.MaxTokens,
		Stream:    false,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.BaseURL, "/")+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range cfg.Headers {
		httpReq.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := e.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http exec %s: %w", req.Head.ID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, httpStatusError(req.Head.ID, resp)
	}

	var cr openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, fmt.Errorf("http exec %s: decode: %w", req.Head.ID, err)
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("http exec %s: empty response", req.Head.ID)
	}

	return &Response{
		Output:       cr.Choices[0].Message.Content,
		InputTokens:  cr.Usage.PromptTokens,
		OutputTokens: cr.Usage.CompletionTokens,
		Duration:     time.Since(start),
		Model:        firstNonEmpty(cr.Model, cfg.Model),
	}, nil
}

func (e *HTTPExecutor) executeAnthropic(ctx context.Context, req Request) (*Response, error) {
	model := defaultModelFor("anthropic")
	body := map[string]interface{}{
		"model":      model,
		"max_tokens": defaultMaxTokens(req.MaxTokens, 1024),
		"messages": []map[string]string{
			{"role": "user", "content": req.Prompt},
		},
	}
	if req.System != "" {
		body["system"] = req.System
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", apiKeyFor("anthropic"))
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	start := time.Now()
	resp, err := e.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http exec %s: %w", req.Head.ID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, httpStatusError(req.Head.ID, resp)
	}

	var out struct {
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("http exec %s: decode: %w", req.Head.ID, err)
	}

	return &Response{
		Output:       joinAnthropicBlocks(out.Content),
		InputTokens:  out.Usage.InputTokens,
		OutputTokens: out.Usage.OutputTokens,
		Duration:     time.Since(start),
		Model:        firstNonEmpty(out.Model, model),
	}, nil
}

func (e *HTTPExecutor) executeGemini(ctx context.Context, req Request) (*Response, error) {
	model := defaultModelFor("google")
	body := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"role": "user",
				"parts": []map[string]string{
					{"text": req.Prompt},
				},
			},
		},
	}
	if req.System != "" {
		body["system_instruction"] = map[string]interface{}{
			"parts": []map[string]string{{"text": req.System}},
		}
	}
	if req.MaxTokens > 0 {
		body["generationConfig"] = map[string]int{"maxOutputTokens": req.MaxTokens}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	u := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", url.PathEscape(model))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", apiKeyFor("google"))

	start := time.Now()
	resp, err := e.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http exec %s: %w", req.Head.ID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, httpStatusError(req.Head.ID, resp)
	}

	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
		ModelVersion string `json:"modelVersion"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("http exec %s: decode: %w", req.Head.ID, err)
	}
	if len(out.Candidates) == 0 {
		return nil, fmt.Errorf("http exec %s: empty response", req.Head.ID)
	}

	return &Response{
		Output:       joinGeminiParts(out.Candidates[0].Content.Parts),
		InputTokens:  out.UsageMetadata.PromptTokenCount,
		OutputTokens: out.UsageMetadata.CandidatesTokenCount,
		Duration:     time.Since(start),
		Model:        firstNonEmpty(out.ModelVersion, model),
	}, nil
}

func (e *HTTPExecutor) executeCohere(ctx context.Context, req Request) (*Response, error) {
	model := defaultModelFor("cohere")
	body := map[string]interface{}{
		"model":    model,
		"messages": buildMessages(req),
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.cohere.ai/v2/chat", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKeyFor("cohere"))

	start := time.Now()
	resp, err := e.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http exec %s: %w", req.Head.ID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, httpStatusError(req.Head.ID, resp)
	}

	var out struct {
		ID      string `json:"id"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
		Usage struct {
			Tokens struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("http exec %s: decode: %w", req.Head.ID, err)
	}

	return &Response{
		Output:       joinCohereBlocks(out.Message.Content),
		InputTokens:  out.Usage.Tokens.InputTokens,
		OutputTokens: out.Usage.Tokens.OutputTokens,
		Duration:     time.Since(start),
		Model:        model,
	}, nil
}

func (e *HTTPExecutor) executeAzureOpenAI(ctx context.Context, req Request) (*Response, error) {
	body := openAIChatRequest{
		Messages:  buildMessages(req),
		MaxTokens: req.MaxTokens,
		Stream:    false,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	endpoint := strings.TrimRight(azureEndpoint(), "/")
	u := fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s", endpoint, url.PathEscape(azureDeployment()), url.QueryEscape(azureAPIVersion()))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api-key", apiKeyFor("azure"))

	start := time.Now()
	resp, err := e.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http exec %s: %w", req.Head.ID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, httpStatusError(req.Head.ID, resp)
	}

	var out openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("http exec %s: decode: %w", req.Head.ID, err)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("http exec %s: empty response", req.Head.ID)
	}

	return &Response{
		Output:       out.Choices[0].Message.Content,
		InputTokens:  out.Usage.PromptTokens,
		OutputTokens: out.Usage.CompletionTokens,
		Duration:     time.Since(start),
		Model:        firstNonEmpty(out.Model, azureDeployment()),
	}, nil
}

func (e *HTTPExecutor) executeBedrock(ctx context.Context, req Request) (*Response, error) {
	cfg := openAICompatConfig{
		BaseURL: fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", bedrockRegion()),
		Model:   defaultModelFor("bedrock"),
	}
	msgs := buildMessages(req)
	body := openAIChatRequest{
		Model:     cfg.Model,
		Messages:  msgs,
		MaxTokens: req.MaxTokens,
		Stream:    false,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.BaseURL+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if err := signAWSRequest(httpReq, raw, bedrockRegion(), "bedrock"); err != nil {
		return nil, fmt.Errorf("http exec %s: %w", req.Head.ID, err)
	}

	start := time.Now()
	resp, err := e.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http exec %s: %w", req.Head.ID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, httpStatusError(req.Head.ID, resp)
	}

	var out openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("http exec %s: decode: %w", req.Head.ID, err)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("http exec %s: empty response", req.Head.ID)
	}

	return &Response{
		Output:       out.Choices[0].Message.Content,
		InputTokens:  out.Usage.PromptTokens,
		OutputTokens: out.Usage.CompletionTokens,
		Duration:     time.Since(start),
		Model:        firstNonEmpty(out.Model, cfg.Model),
	}, nil
}

func (e *HTTPExecutor) executeReplicate(ctx context.Context, req Request) (*Response, error) {
	model := defaultModelFor("replicate")
	body := map[string]interface{}{
		"model": model,
		"input": map[string]string{
			"prompt": req.Prompt,
		},
	}
	if req.System != "" {
		body["input"].(map[string]string)["system_prompt"] = req.System
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.replicate.com/v1/predictions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKeyFor("replicate"))
	httpReq.Header.Set("Prefer", "wait=30")

	start := time.Now()
	resp, err := e.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http exec %s: %w", req.Head.ID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, httpStatusError(req.Head.ID, resp)
	}

	var pred replicatePrediction
	if err := json.NewDecoder(resp.Body).Decode(&pred); err != nil {
		return nil, fmt.Errorf("http exec %s: decode: %w", req.Head.ID, err)
	}

	const maxPollIter = 150 // 150 × 2s = 5 min ceiling
	for i := 0; i < maxPollIter && (pred.Status == "starting" || pred.Status == "processing"); i++ {
		if pred.URLs.Get == "" {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
		pred, err = e.getReplicatePrediction(ctx, req.Head.ID, pred.URLs.Get)
		if err != nil {
			return nil, err
		}
	}

	if pred.Status != "succeeded" {
		if pred.Error != "" {
			return nil, fmt.Errorf("http exec %s: %s", req.Head.ID, pred.Error)
		}
		return nil, fmt.Errorf("http exec %s: prediction ended with status %q", req.Head.ID, pred.Status)
	}

	return &Response{
		Output:   stringifyAny(pred.Output),
		Duration: time.Since(start),
		Model:    model,
	}, nil
}

type replicatePrediction struct {
	Status  string `json:"status"`
	Error   string `json:"error"`
	Output  any    `json:"output"`
	Metrics struct {
		PredictTime float64 `json:"predict_time"`
	} `json:"metrics"`
	URLs struct {
		Get string `json:"get"`
	} `json:"urls"`
}

func (e *HTTPExecutor) getReplicatePrediction(ctx context.Context, headID, getURL string) (replicatePrediction, error) {
	if !strings.HasPrefix(getURL, "https://api.replicate.com/") {
		return replicatePrediction{}, fmt.Errorf("http exec %s: unexpected Replicate poll URL %q", headID, getURL)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
	if err != nil {
		return replicatePrediction{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKeyFor("replicate"))

	resp, err := e.httpClient().Do(httpReq)
	if err != nil {
		return replicatePrediction{}, fmt.Errorf("http exec %s: %w", headID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return replicatePrediction{}, httpStatusError(headID, resp)
	}

	var pred replicatePrediction
	if err := json.NewDecoder(resp.Body).Decode(&pred); err != nil {
		return replicatePrediction{}, fmt.Errorf("http exec %s: decode: %w", headID, err)
	}
	return pred, nil
}

func openAICompatConfigFor(h provider.Head) (openAICompatConfig, error) {
	if h.Endpoint != "" {
		return openAICompatConfig{
			BaseURL: strings.TrimRight(h.Endpoint, "/"),
			Model:   trimLocalModelID(h.ID),
		}, nil
	}

	type cfg struct {
		baseURL string
		model   string
		header  string
		key     string
	}

	configs := map[string]cfg{
		"openai":     {baseURL: "https://api.openai.com", model: defaultModelFor("openai"), header: "Authorization", key: "Bearer " + apiKeyFor("openai")},
		"xai":        {baseURL: "https://api.x.ai", model: defaultModelFor("xai"), header: "Authorization", key: "Bearer " + apiKeyFor("xai")},
		"groq":       {baseURL: "https://api.groq.com/openai", model: defaultModelFor("groq"), header: "Authorization", key: "Bearer " + apiKeyFor("groq")},
		"together":   {baseURL: "https://api.together.xyz", model: defaultModelFor("together"), header: "Authorization", key: "Bearer " + apiKeyFor("together")},
		"fireworks":  {baseURL: "https://api.fireworks.ai/inference", model: defaultModelFor("fireworks"), header: "Authorization", key: "Bearer " + apiKeyFor("fireworks")},
		"mistral":    {baseURL: "https://api.mistral.ai", model: defaultModelFor("mistral"), header: "Authorization", key: "Bearer " + apiKeyFor("mistral")},
		"deepseek":   {baseURL: "https://api.deepseek.com", model: defaultModelFor("deepseek"), header: "Authorization", key: "Bearer " + apiKeyFor("deepseek")},
		"perplexity": {baseURL: "https://api.perplexity.ai", model: defaultModelFor("perplexity"), header: "Authorization", key: "Bearer " + apiKeyFor("perplexity")},
	}
	c, ok := configs[h.Provider]
	if !ok {
		return openAICompatConfig{}, errUnsupportedHTTPProvider
	}
	if c.key == "" || c.model == "" {
		return openAICompatConfig{}, errUnsupportedHTTPProvider
	}

	return openAICompatConfig{
		BaseURL: c.baseURL,
		Model:   c.model,
		Headers: map[string]string{c.header: c.key},
	}, nil
}

func buildMessages(req Request) []message {
	msgs := make([]message, 0, 2)
	if req.System != "" {
		msgs = append(msgs, message{Role: "system", Content: req.System})
	}
	msgs = append(msgs, message{Role: "user", Content: req.Prompt})
	return msgs
}

func defaultMaxTokens(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}

func trimLocalModelID(id string) string {
	for _, prefix := range []string{"ollama/", "lmstudio/"} {
		if strings.HasPrefix(id, prefix) {
			return strings.TrimPrefix(id, prefix)
		}
	}
	return id
}

func joinTextBlocks(blocks []textBlock) string {
	var parts []string
	for _, b := range blocks {
		if text := strings.TrimSpace(b.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

type textBlock struct{ Text string }

func joinGeminiParts(parts []struct {
	Text string `json:"text"`
}) string {
	texts := make([]textBlock, 0, len(parts))
	for _, p := range parts {
		texts = append(texts, textBlock{Text: p.Text})
	}
	return joinTextBlocks(texts)
}

func joinAnthropicBlocks(blocks []struct {
	Type string `json:"type"`
	Text string `json:"text"`
}) string {
	texts := make([]textBlock, 0, len(blocks))
	for _, b := range blocks {
		texts = append(texts, textBlock{Text: b.Text})
	}
	return joinTextBlocks(texts)
}

func joinCohereBlocks(blocks []struct {
	Type string `json:"type"`
	Text string `json:"text"`
}) string {
	texts := make([]textBlock, 0, len(blocks))
	for _, b := range blocks {
		texts = append(texts, textBlock{Text: b.Text})
	}
	return joinTextBlocks(texts)
}

func httpStatusError(headID string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("http exec %s: status %d — %s", headID, resp.StatusCode, string(b))
}

func defaultModelFor(providerID string) string {
	type modelSpec struct {
		fallback string
		envs     []string
	}
	specs := map[string]modelSpec{
		"anthropic":  {fallback: "claude-sonnet-4-20250514", envs: []string{"ANTHROPIC_MODEL", "HYDRA_MODEL_ANTHROPIC"}},
		"openai":     {fallback: "gpt-4o", envs: []string{"OPENAI_MODEL", "HYDRA_MODEL_OPENAI"}},
		"google":     {fallback: "gemini-2.5-flash", envs: []string{"GEMINI_MODEL", "GOOGLE_MODEL", "HYDRA_MODEL_GOOGLE"}},
		"xai":        {fallback: "grok-3-latest", envs: []string{"XAI_MODEL", "HYDRA_MODEL_XAI"}},
		"groq":       {fallback: "llama-3.3-70b-versatile", envs: []string{"GROQ_MODEL", "HYDRA_MODEL_GROQ"}},
		"together":   {fallback: "meta-llama/Llama-3.3-70B-Instruct-Turbo", envs: []string{"TOGETHER_MODEL", "HYDRA_MODEL_TOGETHER"}},
		"fireworks":  {fallback: "accounts/fireworks/models/llama-v3p3-70b-instruct", envs: []string{"FIREWORKS_MODEL", "HYDRA_MODEL_FIREWORKS"}},
		"mistral":    {fallback: "mistral-large-latest", envs: []string{"MISTRAL_MODEL", "HYDRA_MODEL_MISTRAL"}},
		"deepseek":   {fallback: "deepseek-chat", envs: []string{"DEEPSEEK_MODEL", "HYDRA_MODEL_DEEPSEEK"}},
		"bedrock":    {fallback: "anthropic.claude-3-5-sonnet-20241022-v2:0", envs: []string{"BEDROCK_MODEL_ID", "AWS_BEDROCK_MODEL_ID", "HYDRA_MODEL_BEDROCK"}},
		"perplexity": {fallback: "sonar-pro", envs: []string{"PERPLEXITY_MODEL", "HYDRA_MODEL_PERPLEXITY"}},
		"cohere":     {fallback: "command-a-plus-05-2026", envs: []string{"COHERE_MODEL", "HYDRA_MODEL_COHERE"}},
		"replicate":  {fallback: "", envs: []string{"REPLICATE_MODEL", "HYDRA_MODEL_REPLICATE"}},
	}
	spec, ok := specs[providerID]
	if !ok {
		return ""
	}
	if v := firstEnv(spec.envs...); v != "" {
		return v
	}
	return spec.fallback
}

func apiKeyFor(providerID string) string {
	envs := map[string][]string{
		"anthropic": {"ANTHROPIC_API_KEY"},
		"openai":    {"OPENAI_API_KEY"},
		"google":    {"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		"xai":       {"XAI_API_KEY"},
		"groq":      {"GROQ_API_KEY"},
		"together":  {"TOGETHER_API_KEY"},
		"fireworks": {"FIREWORKS_API_KEY"},
		"mistral":   {"MISTRAL_API_KEY"},
		"deepseek":  {"DEEPSEEK_API_KEY"},
		"azure":     {"AZURE_OPENAI_API_KEY"},
		"perplexity": {
			"PERPLEXITY_API_KEY",
		},
		"cohere":    {"COHERE_API_KEY"},
		"replicate": {"REPLICATE_API_TOKEN"},
	}
	return firstEnv(envs[providerID]...)
}

func azureEndpoint() string   { return firstEnv("AZURE_OPENAI_ENDPOINT") }
func azureDeployment() string { return firstEnv("AZURE_OPENAI_DEPLOYMENT") }

func azureAPIVersion() string {
	return firstNonEmpty(firstEnv("AZURE_OPENAI_API_VERSION"), "2024-10-21")
}

func bedrockRegion() string { return firstEnv("AWS_REGION", "AWS_DEFAULT_REGION") }

func awsAccessKeyID() string     { return firstEnv("AWS_ACCESS_KEY_ID") }
func awsSecretAccessKey() string { return firstEnv("AWS_SECRET_ACCESS_KEY") }
func awsSessionToken() string    { return firstEnv("AWS_SESSION_TOKEN") }

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func signAWSRequest(req *http.Request, payload []byte, region, service string) error {
	accessKey := awsAccessKeyID()
	secretKey := awsSecretAccessKey()
	if accessKey == "" || secretKey == "" {
		return errors.New("missing AWS credentials")
	}

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	if token := awsSessionToken(); token != "" {
		req.Header.Set("X-Amz-Security-Token", token)
	}

	payloadHash := sha256Hex(payload)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQuery := req.URL.Query().Encode()
	canonicalHeaders, signedHeaders := canonicalHeaders(req)
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	credentialScope := strings.Join([]string{dateStamp, region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := awsSigningKey(secretKey, dateStamp, region, service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	auth := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey,
		credentialScope,
		signedHeaders,
		signature,
	)
	req.Header.Set("Authorization", auth)
	return nil
}

func canonicalHeaders(req *http.Request) (string, string) {
	keys := []string{"content-type", "host", "x-amz-content-sha256", "x-amz-date"}
	if req.Header.Get("X-Amz-Security-Token") != "" {
		keys = append(keys, "x-amz-security-token")
	}

	var b strings.Builder
	for _, key := range keys {
		switch key {
		case "host":
			b.WriteString("host:" + req.URL.Host + "\n")
		default:
			b.WriteString(key + ":" + strings.TrimSpace(req.Header.Get(http.CanonicalHeaderKey(key))) + "\n")
		}
	}
	return b.String(), strings.Join(keys, ";")
}

func awsSigningKey(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func stringifyAny(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			s := stringifyAny(item)
			if s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n")
	default:
		raw, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		return string(raw)
	}
}
